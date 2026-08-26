package keeper

import (
	"context"
	"testing"
	"time"

	"cosmossdk.io/log"
	"cosmossdk.io/math"
	"cosmossdk.io/store"
	"cosmossdk.io/store/metrics"
	storetypes "cosmossdk.io/store/types"
	tmproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/atoshi-chain/atoshi/v20/x/energy/types"
)

// fakeBank returns whatever balance was last set, with no real coin movement.
//
// onSend is an optional hook that mirrors what bank.AppendSendRestriction
// would do in production: fire after every base-denom send so that the
// energy keeper can refresh the affected accounts' snapshots. Tests that
// need to exercise the hook interaction (e.g. audit Issue-8) wire it
// after constructing the keeper.
type fakeBank struct {
	balances map[string]math.Int
	denom    string
	onSend   func(from, to sdk.AccAddress, amt sdk.Coins)
}

func newFakeBank(denom string) *fakeBank {
	return &fakeBank{balances: map[string]math.Int{}, denom: denom}
}

func (b *fakeBank) GetBalance(_ context.Context, addr sdk.AccAddress, denom string) sdk.Coin {
	v, ok := b.balances[addr.String()]
	if !ok {
		return sdk.NewCoin(denom, math.ZeroInt())
	}
	return sdk.NewCoin(denom, v)
}

func (b *fakeBank) SendCoinsFromAccountToModule(_ context.Context, sender sdk.AccAddress, recipientModule string, amt sdk.Coins) error {
	// Order matches Evmos cosmos-sdk@v0.50.9-evmos/x/bank/keeper/send.go:
	//     1. subUnlockedCoins(from)        ← FIRST: subtract from sender
	//     2. sendRestriction.Apply(...)    ← THEN: hook fires
	//     3. addCoins(to)                  ← LAST: credit recipient
	// (Note: upstream Cosmos SDK v0.50 has the OPPOSITE order — hook then
	// subtract — but the Evmos fork that this chain runs on flipped it.
	// This fake must mirror Evmos so unit tests reflect production state.)
	cur := b.balances[sender.String()]
	if cur.IsNil() {
		cur = math.ZeroInt()
	}
	b.balances[sender.String()] = cur.Sub(amt.AmountOf(b.denom)) // 1. sub first
	if b.onSend != nil {
		b.onSend(sender, sdk.AccAddress([]byte("module/"+recipientModule)), amt) // 2. hook
	}
	return nil
}

func (b *fakeBank) SendCoinsFromModuleToAccount(_ context.Context, senderModule string, recipient sdk.AccAddress, amt sdk.Coins) error {
	// Same Evmos order: sub from module's bank (we don't track module balances
	// here, so this step is a no-op for the fake), then hook, then add to
	// recipient.
	if b.onSend != nil {
		b.onSend(sdk.AccAddress([]byte("module/"+senderModule)), recipient, amt)
	}
	cur := b.balances[recipient.String()]
	if cur.IsNil() {
		cur = math.ZeroInt()
	}
	b.balances[recipient.String()] = cur.Add(amt.AmountOf(b.denom))
	return nil
}

// fakeAccountKeeper satisfies the slice of methods Settle/keeper need.
type fakeAccountKeeper struct{}

func (fakeAccountKeeper) GetModuleAddress(name string) sdk.AccAddress {
	return sdk.AccAddress([]byte("module/" + name))
}
func (fakeAccountKeeper) GetModuleAccount(_ context.Context, _ string) sdk.ModuleAccountI {
	// Nil is fine for these unit tests — only EnsureLockedPoolExists uses it.
	return nil
}

// newKeeperForTest builds an in-memory keeper. Returns the keeper, an SDK
// context with controllable BlockTime, and the fake bank for asserting.
func newKeeperForTest(t *testing.T) (Keeper, sdk.Context, *fakeBank) {
	t.Helper()

	storeKey := storetypes.NewKVStoreKey(types.StoreKey)
	db := dbm.NewMemDB()
	cms := store.NewCommitMultiStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	cms.MountStoreWithDB(storeKey, storetypes.StoreTypeIAVL, db)
	require.NoError(t, cms.LoadLatestVersion())

	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)

	bank := newFakeBank("liao")
	k := NewKeeper(cdc, storeKey, fakeAccountKeeper{}, bank, nil, nil,
		sdk.AccAddress([]byte("authority")).String(), "liao")

	header := tmproto.Header{Time: time.Unix(1_700_000_000, 0)}
	ctx := sdk.NewContext(cms, header, false, log.NewNopLogger())
	require.NoError(t, k.SetParams(ctx, types.DefaultParams()))
	return k, ctx, bank
}

// addr produces a deterministic test address.
func addr(label string) sdk.AccAddress { return sdk.AccAddress([]byte(label)) }

func TestSettle_FirstTouchInitializesSnapshot(t *testing.T) {
	k, ctx, bank := newKeeperForTest(t)
	a := addr("alice___________________")
	bank.balances[a.String()] = math.NewIntWithDecimal(30_000, 18)

	got := k.Settle(ctx, a)
	require.Equal(t, ctx.BlockTime().Unix(), got.LastUpdatedTime)
	require.True(t, got.LastBalanceSnapshot.Equal(math.NewIntWithDecimal(30_000, 18)))
	require.EqualValues(t, 0, got.TxEnergyAccrued, "no time has passed yet")
}

func TestSettle_AccruesOver12HoursAtThreshold(t *testing.T) {
	k, ctx, bank := newKeeperForTest(t)
	a := addr("alice___________________")
	bank.balances[a.String()] = math.NewIntWithDecimal(30_000, 18)
	k.Settle(ctx, a) // first touch
	// fast-forward 12h
	ctx = ctx.WithBlockTime(ctx.BlockTime().Add(12 * time.Hour))
	got := k.Settle(ctx, a)
	// 12h of 24h ≈ half capacity = 25,000
	require.InDelta(t, 25_000, float64(got.TxEnergyAccrued), 100)
}

func TestSettle_CapsAtFullCapacityAfter24Hours(t *testing.T) {
	k, ctx, bank := newKeeperForTest(t)
	a := addr("alice___________________")
	bank.balances[a.String()] = math.NewIntWithDecimal(30_000, 18)
	k.Settle(ctx, a)
	ctx = ctx.WithBlockTime(ctx.BlockTime().Add(48 * time.Hour))
	got := k.Settle(ctx, a)
	require.EqualValues(t, 50_000, got.TxEnergyAccrued)
}

func TestConsume_FreeForSubsidizedMsg(t *testing.T) {
	k, ctx, _ := newKeeperForTest(t)
	a := addr("alice___________________")
	res, err := k.Consume(ctx, a, 21_000, false, []string{"/atoshi.oracle.v1.MsgReportPrice"})
	require.NoError(t, err)
	require.True(t, res.Free)
	require.EqualValues(t, 0, res.EnergyDeducted)
	require.EqualValues(t, 0, res.ShortfallGas)
}

func TestConsume_DisabledFallsThroughAsShortfall(t *testing.T) {
	k, ctx, _ := newKeeperForTest(t)
	p := k.GetParams(ctx)
	p.EnergyEnabled = false
	require.NoError(t, k.SetParams(ctx, p))

	a := addr("alice___________________")
	res, err := k.Consume(ctx, a, 21_000, false, []string{"/cosmos.bank.v1beta1.MsgSend"})
	require.NoError(t, err)
	require.False(t, res.Free)
	require.EqualValues(t, 21_000, res.ShortfallGas)
	require.EqualValues(t, 0, res.EnergyDeducted)
}

func TestConsume_DrawsFromAccrued(t *testing.T) {
	k, ctx, bank := newKeeperForTest(t)
	a := addr("alice___________________")
	bank.balances[a.String()] = math.NewIntWithDecimal(30_000, 18)
	k.Settle(ctx, a)
	ctx = ctx.WithBlockTime(ctx.BlockTime().Add(24 * time.Hour))

	res, err := k.Consume(ctx, a, 21_000, false, []string{"/cosmos.bank.v1beta1.MsgSend"})
	require.NoError(t, err)
	require.EqualValues(t, 21_000, res.EnergyDeducted)
	require.EqualValues(t, 0, res.ShortfallGas)

	got := k.GetEnergyAccount(ctx, a)
	require.EqualValues(t, 50_000-21_000, got.TxEnergyAccrued)
}

func TestConsume_ShortfallWhenInsufficient(t *testing.T) {
	k, ctx, bank := newKeeperForTest(t)
	a := addr("alice___________________")
	// Below threshold: capacity 0
	bank.balances[a.String()] = math.NewIntWithDecimal(10_000, 18)
	k.Settle(ctx, a)
	ctx = ctx.WithBlockTime(ctx.BlockTime().Add(24 * time.Hour))

	res, err := k.Consume(ctx, a, 21_000, false, []string{"/cosmos.bank.v1beta1.MsgSend"})
	require.NoError(t, err)
	require.EqualValues(t, 0, res.EnergyDeducted)
	require.EqualValues(t, 21_000, res.ShortfallGas)
}

// Audit Issue 6 regression: cap-down on a balance drop must NOT cut
// into DelegatedOut. If TxEnergyAccrued is forced below DelegatedOut,
// ownAvail (= TxEnergyAccrued − DelegatedOut, clamped at 0) silently
// loses the delegator's bookkeeping invariant — already-honored
// delegations cannot be undelegated symmetrically.
//
// Setup: balance 60k → cap 100k, fill to 80k accrued, delegate out 70k
// (DelegatedOut=70k, TxEnergyAccrued=80k → ownAvail=10k). Then sell
// stake down to 25k → new cap = 25k.
//
// Pre-fix: TxEnergyAccrued would be slammed to 25k, far under
// DelegatedOut=70k. ownAvail clamps to 0; even worse, the next
// undelegation would underflow DelegatedOut bookkeeping.
//
// Post-fix: TxEnergyAccrued is floored at DelegatedOut=70k. The
// holder's own 10k is fully cut (cap-down's intent), but the 70k
// promised to delegatees is preserved.
func TestApplyBalanceChange_CapDownFloorsAtDelegatedOut(t *testing.T) {
	k, ctx, bank := newKeeperForTest(t)
	a := addr("alice___________________")

	bank.balances[a.String()] = math.NewIntWithDecimal(60_000, 18)
	acct := k.Settle(ctx, a)
	acct.TxEnergyAccrued = 80_000
	acct.DelegatedOut = 70_000
	k.SetEnergyAccount(ctx, acct)

	bank.balances[a.String()] = math.NewIntWithDecimal(25_000, 18)
	k.ApplyBalanceChange(ctx, a, math.NewIntWithDecimal(25_000, 18))

	got := k.GetEnergyAccount(ctx, a)
	require.EqualValues(t, 70_000, got.TxEnergyAccrued,
		"cap-down must floor at DelegatedOut so delegated commitments survive")
	require.EqualValues(t, 70_000, got.DelegatedOut,
		"DelegatedOut itself must not be touched by cap-down")
}

// Audit Issue 6 companion: when newTxCap is ABOVE DelegatedOut, the
// floor must not interfere — the holder's own excess still gets cut
// to newTxCap normally.
func TestApplyBalanceChange_CapDownAboveDelegatedOutCutsNormally(t *testing.T) {
	k, ctx, bank := newKeeperForTest(t)
	a := addr("alice___________________")

	bank.balances[a.String()] = math.NewIntWithDecimal(60_000, 18)
	acct := k.Settle(ctx, a)
	acct.TxEnergyAccrued = 80_000
	acct.DelegatedOut = 20_000
	k.SetEnergyAccount(ctx, acct)

	// Drop to 30k (newTxCap = 50k). 50k > DelegatedOut=20k, so normal
	// cap-down applies.
	bank.balances[a.String()] = math.NewIntWithDecimal(30_000, 18)
	k.ApplyBalanceChange(ctx, a, math.NewIntWithDecimal(30_000, 18))

	got := k.GetEnergyAccount(ctx, a)
	require.EqualValues(t, 50_000, got.TxEnergyAccrued,
		"floor must not raise the cap above newTxCap when DelegatedOut < newTxCap")
}

// Audit Issue-7 (round2, round1-issue6) end-to-end verification: the
// round2 audit re-flagged the ownAvail / cap-down interaction in
// Consume() with the same root cause as round1-issue6 (fixed at
// commit 8000067 — floor cap-down at DelegatedOut). The round2 wording
// is more end-to-end: "user transfers out ATOS → cap drops →
// TxEnergyAccrued cap-downs → ownAvail computation goes wrong → DoS".
//
// This test mirrors that exact scenario through the real SendRestriction
// hook path:
//  1. Alice holds 60k ATOS (cap = 100k), accrued = 100k, DelegatedOut
//     = 70k (committed to a delegatee already). ownAvail = 30k.
//  2. Alice transfers half her ATOS to Bob. Post-send balance = 30k
//     ATOS → new cap = 50k.
//  3. SendRestriction hook fires ApplyBalanceChange(alice, 30k_eligible)
//     → newTxCap=50k < DelegatedOut=70k → with the fix, TxEnergyAccrued
//     is floored at DelegatedOut=70k (NOT slammed to 50k).
//  4. Consume(alice, 20k, ...) succeeds: ownAvail = 70k - 70k = 0,
//     delegated-in-usable is unused here, shortfall = 20k. No
//     invariant violation, no DoS — the user lost own-energy ceiling
//     (correct, they sold half their ATOS) but did NOT lose the
//     delegated commitment.
//
// Pre-fix (round1 audit state): step 3 would have set TxEnergyAccrued
// = 50k. Step 4's ownAvail = max(0, 50k - 70k) = 0 (clamped), AND
// the next undelegation would under-credit because TxEnergyAccrued
// (50k) < DelegatedOut (70k) — invariant breach. The delegator's
// bookkeeping would be inconsistent.
func TestConsume_AfterBalanceDrop_PreservesDelegatedCommitment(t *testing.T) {
	k, ctx, bank := newKeeperForTest(t)
	alice := addr("alice___________________")
	bob := addr("bob_____________________")

	bank.balances[alice.String()] = math.NewIntWithDecimal(60_000, 18)
	bank.balances[bob.String()] = math.ZeroInt()

	// Wire SendRestriction so a bank send fires the real cap-down path.
	bank.onSend = func(from, to sdk.AccAddress, amt sdk.Coins) {
		_, _ = k.SendRestriction(ctx, from, to, amt)
	}

	a := k.Settle(ctx, alice)
	a.TxEnergyAccrued = 100_000
	a.DelegatedOut = 70_000
	k.SetEnergyAccount(ctx, a)

	// Alice sells half her ATOS: 60k → 30k.
	// Match Evmos's actual SendCoins ordering:
	//   1. subUnlockedCoins(from)  ← bank already reflects the loss
	//   2. SendRestriction.Apply   ← hook fires here
	//   3. addCoins(to)            ← bob's bank goes up AFTER the hook
	// fromBefore inside the hook therefore reads post-sub alice (30k)
	// and pre-add bob (0).  Pre-fix this test passed only because the
	// hook compensated by subtracting `moved` again — see fix in
	// x/energy/keeper/send_restriction.go (the +moved on the to-side
	// projection is intentional and correct because the hook fires
	// before addCoins).
	sold := math.NewIntWithDecimal(30_000, 18)
	bank.balances[alice.String()] = math.NewIntWithDecimal(30_000, 18) // 1. sub from alice
	_, err := k.SendRestriction(ctx, alice, bob, sdk.NewCoins(sdk.NewCoin("liao", sold)))
	require.NoError(t, err)
	bank.balances[bob.String()] = sold // 3. add to bob (post-hook)

	post := k.GetEnergyAccount(ctx, alice)
	require.EqualValues(t, 70_000, post.TxEnergyAccrued,
		"audit Issue-7: cap-down must floor at DelegatedOut so delegated commitment survives a balance drop")
	require.EqualValues(t, 70_000, post.DelegatedOut,
		"DelegatedOut itself must not be touched by cap-down")
	require.True(t, post.TxEnergyAccrued >= post.DelegatedOut,
		"audit Issue-7 invariant: TxEnergyAccrued ≥ DelegatedOut after balance change")

	// Consume must now correctly classify gas as shortfall (no own avail,
	// no delegated-in to draw on). DoS scenario from the audit would have
	// thrown ErrInsufficientEnergy or under-counted shortfall; we expect
	// a clean shortfall path.
	res, err := k.Consume(ctx, alice, 20_000, false,
		[]string{"/cosmos.bank.v1beta1.MsgSend"})
	require.NoError(t, err, "Consume must not error after balance drop")
	require.EqualValues(t, 0, res.OwnDeducted,
		"ownAvail correctly collapsed to 0 (TxEnergyAccrued == DelegatedOut)")
	require.EqualValues(t, 0, res.DelegatedDeducted,
		"no inbound delegations on Alice's side")
	require.EqualValues(t, 20_000, res.ShortfallGas,
		"all 20k must fall through to shortfall; pre-fix this could have produced inconsistent accounting")
}
