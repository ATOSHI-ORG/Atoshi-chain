package keeper_test

import (
	"context"
	"fmt"
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
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/stretchr/testify/require"

	"github.com/atoshi-chain/atoshi/v20/x/atox/keeper"
	"github.com/atoshi-chain/atoshi/v20/x/atox/types"
)

const (
	atosDenom = "liao"
	atoxDenom = "aatox"
)

// ---------- test doubles ----------

// fakeBank mirrors the Evmos bank send ordering so the hook sees production
// state: subtract from the sender, fire the restriction, then credit the
// receiver. Getting this order wrong in the fake would hide exactly the class of
// bug the hook is written to avoid.
type fakeBank struct {
	balances map[string]sdk.Coins
	supply   sdk.Coins
	modAddrs map[string]sdk.AccAddress
	hook     func(ctx context.Context, from, to sdk.AccAddress, amt sdk.Coins) (sdk.AccAddress, error)
}

func newFakeBank(modAddrs map[string]sdk.AccAddress) *fakeBank {
	return &fakeBank{balances: map[string]sdk.Coins{}, modAddrs: modAddrs}
}

func (b *fakeBank) GetBalance(_ context.Context, addr sdk.AccAddress, denom string) sdk.Coin {
	return sdk.NewCoin(denom, b.balances[addr.String()].AmountOf(denom))
}

func (b *fakeBank) GetSupply(_ context.Context, denom string) sdk.Coin {
	return sdk.NewCoin(denom, b.supply.AmountOf(denom))
}

func (b *fakeBank) MintCoins(_ context.Context, module string, amt sdk.Coins) error {
	addr := b.modAddrs[module]
	b.balances[addr.String()] = b.balances[addr.String()].Add(amt...)
	b.supply = b.supply.Add(amt...)
	return nil
}

func (b *fakeBank) send(ctx context.Context, from, to sdk.AccAddress, amt sdk.Coins) error {
	cur := b.balances[from.String()]
	if !cur.IsAllGTE(amt) {
		return fmt.Errorf("insufficient funds: %s has %s, needs %s", from, cur, amt)
	}
	b.balances[from.String()] = cur.Sub(amt...) // 1. debit sender
	if b.hook != nil {
		if _, err := b.hook(ctx, from, to, amt); err != nil { // 2. restriction
			return err
		}
	}
	b.balances[to.String()] = b.balances[to.String()].Add(amt...) // 3. credit receiver
	return nil
}

func (b *fakeBank) SendCoinsFromModuleToAccount(ctx context.Context, module string, to sdk.AccAddress, amt sdk.Coins) error {
	return b.send(ctx, b.modAddrs[module], to, amt)
}

func (b *fakeBank) SendCoinsFromModuleToModule(ctx context.Context, from, to string, amt sdk.Coins) error {
	return b.send(ctx, b.modAddrs[from], b.modAddrs[to], amt)
}

// SendCoins is the account-to-account path used by the transfer tests.
func (b *fakeBank) SendCoins(ctx context.Context, from, to sdk.AccAddress, amt sdk.Coins) error {
	return b.send(ctx, from, to, amt)
}

type fakeAccountKeeper struct {
	modAddrs map[string]sdk.AccAddress
	isModule map[string]bool
}

func (a fakeAccountKeeper) GetAccount(_ context.Context, addr sdk.AccAddress) sdk.AccountI {
	if a.isModule[addr.String()] {
		return authtypes.NewEmptyModuleAccount("stub")
	}
	return nil
}

func (a fakeAccountKeeper) GetModuleAddress(name string) sdk.AccAddress {
	return a.modAddrs[name]
}

func (a fakeAccountKeeper) GetModuleAccount(_ context.Context, name string) sdk.ModuleAccountI {
	return authtypes.NewEmptyModuleAccount(name)
}

// sourceModule funds the exchange pool in tests, standing in for the tokenomics
// pool that tier releases draw from.
const sourceModule = "test_source"

func setup(t *testing.T) (keeper.Keeper, sdk.Context, *fakeBank) {
	t.Helper()

	storeKey := storetypes.NewKVStoreKey(types.StoreKey)
	db := dbm.NewMemDB()
	cms := store.NewCommitMultiStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	cms.MountStoreWithDB(storeKey, storetypes.StoreTypeIAVL, db)
	require.NoError(t, cms.LoadLatestVersion())

	modAddrs := map[string]sdk.AccAddress{
		types.ModuleName:       authtypes.NewModuleAddress(types.ModuleName),
		types.ExchangePoolName: authtypes.NewModuleAddress(types.ExchangePoolName),
		sourceModule:           authtypes.NewModuleAddress(sourceModule),
	}
	isModule := map[string]bool{}
	for _, a := range modAddrs {
		isModule[a.String()] = true
	}

	bank := newFakeBank(modAddrs)
	ak := fakeAccountKeeper{modAddrs: modAddrs, isModule: isModule}

	registry := codectypes.NewInterfaceRegistry()
	k := keeper.NewKeeper(
		codec.NewProtoCodec(registry), storeKey, ak, bank,
		authtypes.NewModuleAddress("gov").String(), atosDenom, atoxDenom,
	)
	bank.hook = k.SendRestriction

	ctx := sdk.NewContext(cms, tmproto.Header{Time: time.Unix(1_700_000_000, 0)}, false, log.NewNopLogger())
	require.NoError(t, k.SetParams(ctx, types.DefaultParams()))

	// Give the source module enough ATOS to fund every release in these tests.
	require.NoError(t, bank.MintCoins(ctx, sourceModule,
		sdk.NewCoins(sdk.NewCoin(atosDenom, math.NewIntWithDecimal(1, 32)))))

	return k, ctx, bank
}

func acc(name string) sdk.AccAddress {
	return sdk.AccAddress([]byte(fmt.Sprintf("%-20s", name)[:20]))
}

// ---------- tests ----------

func TestMintAtox_RespectsSupplyCap(t *testing.T) {
	k, ctx, _ := setup(t)
	cap := k.GetParams(ctx).SupplyCap

	require.NoError(t, k.MintAtox(ctx, acc("alice"), cap))
	require.Equal(t, cap.String(), k.AtoxSupply(ctx).String())

	require.ErrorIs(t, k.MintAtox(ctx, acc("bob"), math.NewInt(1)), types.ErrSupplyCapReached)
}

// TestMintAtox_SettlesBeforeCrediting is the guard on the zero-default index: a
// first-time recipient must not be paid for index history that accrued before
// they held any ATOX.
func TestMintAtox_SettlesBeforeCrediting(t *testing.T) {
	k, ctx, _ := setup(t)

	// Index advances while nobody holds ATOX.
	require.NoError(t, k.AddToExchangePool(ctx, sourceModule, math.NewIntWithDecimal(1, 28)))
	require.True(t, k.GlobalIndex(ctx).IsPositive())

	// Now mint to a brand-new account.
	alice := acc("alice")
	require.NoError(t, k.MintAtox(ctx, alice, math.NewIntWithDecimal(1, 28)))

	pending, unsettled := k.Claimable(ctx, alice)
	require.True(t, pending.IsZero(), "must not be credited for pre-holding history, got %s", pending)
	require.True(t, unsettled.IsZero(), "index must be pinned to now, got %s", unsettled)

	// A release AFTER minting is credited normally.
	require.NoError(t, k.AddToExchangePool(ctx, sourceModule, math.NewIntWithDecimal(1, 28)))
	_, unsettled = k.Claimable(ctx, alice)
	require.True(t, unsettled.IsPositive())
}

func TestAddToExchangePool_MovesCoinsAndIndexTogether(t *testing.T) {
	k, ctx, _ := setup(t)
	amount := math.NewIntWithDecimal(1, 28) // 10 billion ATOS

	require.NoError(t, k.AddToExchangePool(ctx, sourceModule, amount))

	require.Equal(t, amount.String(), k.ExchangePoolBalance(ctx).String())
	gs := k.GetGlobalState(ctx)
	require.Equal(t, amount.String(), gs.TotalReleasedToPool.String())
	require.Equal(t, "0.010000000000000000", gs.GlobalIndex.String())
}

func TestAddToExchangePool_RejectsBadInput(t *testing.T) {
	k, ctx, _ := setup(t)
	require.ErrorIs(t, k.AddToExchangePool(ctx, sourceModule, math.ZeroInt()), types.ErrInvalidAmount)
	require.ErrorIs(t, k.AddToExchangePool(ctx, sourceModule, math.NewInt(-1)), types.ErrInvalidAmount)

	p := k.GetParams(ctx)
	p.Enabled = false
	require.NoError(t, k.SetParams(ctx, p))
	require.ErrorIs(t, k.AddToExchangePool(ctx, sourceModule, math.NewInt(1)), types.ErrAtoxDisabled)
}

func TestClaim_PaysOutAndZeroesPending(t *testing.T) {
	k, ctx, bank := setup(t)
	alice := acc("alice")

	require.NoError(t, k.MintAtox(ctx, alice, math.NewIntWithDecimal(1, 30))) // whole cap
	require.NoError(t, k.AddToExchangePool(ctx, sourceModule, math.NewIntWithDecimal(1, 28)))

	paid, err := k.PayoutPending(ctx, alice, math.ZeroInt(), types.TriggerClaim)
	require.NoError(t, err)
	require.Equal(t, math.NewIntWithDecimal(1, 28).String(), paid.String(),
		"holding the entire cap should claim the entire release")

	require.Equal(t, paid.String(), bank.GetBalance(ctx, alice, atosDenom).Amount.String())
	require.True(t, k.GetAtoxAccount(ctx, alice).Pending.IsZero())
	require.True(t, k.GetGlobalState(ctx).TotalPending.IsZero())
	require.Equal(t, paid.String(), k.GetGlobalState(ctx).TotalPaidOut.String())
	require.True(t, k.ExchangePoolBalance(ctx).IsZero())
}

func TestPayoutPending_HonoursMinPayout(t *testing.T) {
	k, ctx, _ := setup(t)
	alice := acc("alice")

	require.NoError(t, k.MintAtox(ctx, alice, math.NewIntWithDecimal(1, 18))) // 1 ATOX
	require.NoError(t, k.AddToExchangePool(ctx, sourceModule, math.NewIntWithDecimal(1, 18)))

	// 1 ATOX out of a 1e30 cap earns 1e-12 of the release: well under the dust bar.
	min := math.NewIntWithDecimal(1, 15)
	paid, err := k.PayoutPending(ctx, alice, min, types.TriggerSweep)
	require.NoError(t, err)
	require.True(t, paid.IsZero(), "below min_auto_payout must not transfer")

	// Settlement still happened, so an explicit claim gets it.
	require.True(t, k.GetAtoxAccount(ctx, alice).Pending.IsPositive(),
		"the debt must be booked even when the transfer is skipped")
	paid, err = k.PayoutPending(ctx, alice, math.ZeroInt(), types.TriggerClaim)
	require.NoError(t, err)
	require.True(t, paid.IsPositive())
}

// TestModuleAccountsNeverAccrue covers the in-flight problem: ATOX passes through
// the atox module account between MintCoins and the transfer out, and would later
// pass through fee_collector and distribution. If those accrued, the module would
// book claims against coins in transit and drain ATOS from real holders.
func TestModuleAccountsNeverAccrue(t *testing.T) {
	k, ctx, _ := setup(t)

	require.NoError(t, k.AddToExchangePool(ctx, sourceModule, math.NewIntWithDecimal(1, 28)))
	// Minting routes ATOX through the module account while the index is positive.
	require.NoError(t, k.MintAtox(ctx, acc("alice"), math.NewIntWithDecimal(1, 28)))

	for _, name := range []string{types.ModuleName, types.ExchangePoolName, sourceModule} {
		addr := k.ExchangePoolAddress()
		if name != types.ExchangePoolName {
			addr = authtypes.NewModuleAddress(name)
		}
		pending, unsettled := k.Claimable(ctx, addr)
		require.True(t, pending.IsZero(), "%s accrued pending %s", name, pending)
		require.True(t, unsettled.IsZero(), "%s accrued unsettled %s", name, unsettled)
	}
}

// TestSolvency_NinetyTransfersCannotOutEarnStayingPut is the adversarial test.
//
// A fixed pot of ATOX is passed through 90 accounts with a release between each
// hop, every transfer going through the real bank ordering and the real hook. The
// total ATOS extracted must not exceed what one holder sitting still through all
// 90 releases would have received.
//
// Without settling the RECEIVER, each new holder would inherit index 0 and their
// next settlement would pay them the whole index history rather than just their
// own span, so the total would blow past the baseline — the ~20x over-issuance
// this design exists to prevent.
func TestSolvency_NinetyTransfersCannotOutEarnStayingPut(t *testing.T) {
	k, ctx, bank := setup(t)

	const hops = 90
	pot := k.GetParams(ctx).SupplyCap.QuoRaw(100) // 1% of the cap changes hands
	perRelease := math.NewIntWithDecimal(1, 26)   // 100m ATOS per release

	// Baseline: one holder, same pot, all 90 releases, never moves.
	baseline := func() math.Int {
		bk, bctx, _ := setup(t)
		holder := acc("stationary")
		require.NoError(t, bk.MintAtox(bctx, holder, pot))
		for i := 0; i < hops; i++ {
			require.NoError(t, bk.AddToExchangePool(bctx, sourceModule, perRelease))
		}
		paid, err := bk.PayoutPending(bctx, holder, math.ZeroInt(), types.TriggerClaim)
		require.NoError(t, err)
		return paid
	}()
	require.True(t, baseline.IsPositive())

	// Adversarial: the pot moves after every release.
	holder := acc("hop0")
	require.NoError(t, k.MintAtox(ctx, holder, pot))

	for i := 1; i <= hops; i++ {
		require.NoError(t, k.AddToExchangePool(ctx, sourceModule, perRelease))
		next := acc(fmt.Sprintf("hop%d", i))
		require.NoError(t, bank.SendCoins(ctx, holder, next,
			sdk.NewCoins(sdk.NewCoin(atoxDenom, pot))))
		holder = next
	}

	// Sum what every participant can extract.
	totalExtracted := math.ZeroInt()
	for i := 0; i <= hops; i++ {
		pending, unsettled := k.Claimable(ctx, acc(fmt.Sprintf("hop%d", i)))
		totalExtracted = totalExtracted.Add(pending).Add(unsettled)
	}

	require.True(t, totalExtracted.LTE(baseline),
		"90 hops extracted %s, a stationary holder gets %s", totalExtracted, baseline)

	// And it must still be close to the baseline: holders should not LOSE their
	// entitlement by transacting, only be unable to duplicate it. Truncation
	// costs at most 1 liao per settlement, and there are two per hop.
	require.True(t, baseline.Sub(totalExtracted).LTE(math.NewInt(2*hops)),
		"transfers should not destroy entitlement: baseline %s vs extracted %s",
		baseline, totalExtracted)

	// The pool must be able to cover everything booked.
	released := k.GetGlobalState(ctx).TotalReleasedToPool
	require.True(t, totalExtracted.LTE(released),
		"extracted %s exceeds released %s", totalExtracted, released)
}

// TestTransfer_SplitsSpanBetweenSenderAndReceiver pins the mechanism at a size
// where the arithmetic is checkable by hand.
func TestTransfer_SplitsSpanBetweenSenderAndReceiver(t *testing.T) {
	k, ctx, bank := setup(t)
	alice, bob := acc("alice"), acc("bob")

	pot := k.GetParams(ctx).SupplyCap // hold the whole cap so index == payout rate
	require.NoError(t, k.MintAtox(ctx, alice, pot))

	// Release 1 accrues entirely to alice.
	rel := math.NewIntWithDecimal(1, 28)
	require.NoError(t, k.AddToExchangePool(ctx, sourceModule, rel))

	require.NoError(t, bank.SendCoins(ctx, alice, bob, sdk.NewCoins(sdk.NewCoin(atoxDenom, pot))))

	// Release 2 accrues entirely to bob.
	require.NoError(t, k.AddToExchangePool(ctx, sourceModule, rel))

	aP, aU := k.Claimable(ctx, alice)
	bP, bU := k.Claimable(ctx, bob)

	require.Equal(t, rel.String(), aP.Add(aU).String(), "alice gets exactly release 1")
	require.Equal(t, rel.String(), bP.Add(bU).String(), "bob gets exactly release 2")
	require.True(t, aU.IsZero(), "alice holds no ATOX, so nothing further accrues")
}

func TestSelfTransfer_IsNeutral(t *testing.T) {
	k, ctx, bank := setup(t)
	alice := acc("alice")

	pot := k.GetParams(ctx).SupplyCap
	require.NoError(t, k.MintAtox(ctx, alice, pot))
	rel := math.NewIntWithDecimal(1, 28)
	require.NoError(t, k.AddToExchangePool(ctx, sourceModule, rel))

	before, beforeU := k.Claimable(ctx, alice)
	require.NoError(t, bank.SendCoins(ctx, alice, alice, sdk.NewCoins(sdk.NewCoin(atoxDenom, pot))))
	after, afterU := k.Claimable(ctx, alice)

	require.Equal(t, before.Add(beforeU).String(), after.Add(afterU).String())
	require.Equal(t, pot.String(), k.AtoxBalance(ctx, alice).String())
}

func TestEndBlocker_SweepCoversEveryAccountAndCyclesCursor(t *testing.T) {
	k, ctx, bank := setup(t)

	// 10 holders, one tenth of the cap each, so every one clears the dust bar.
	const holders = 10
	share := k.GetParams(ctx).SupplyCap.QuoRaw(holders)
	for i := 0; i < holders; i++ {
		require.NoError(t, k.MintAtox(ctx, acc(fmt.Sprintf("holder%d", i)), share))
	}
	require.NoError(t, k.AddToExchangePool(ctx, sourceModule, math.NewIntWithDecimal(1, 28)))

	// Three accounts per block: four blocks to cover ten and wrap around.
	p := k.GetParams(ctx)
	p.AutoSettlePerBlock = 3
	require.NoError(t, k.SetParams(ctx, p))

	for block := 0; block < 4; block++ {
		require.NoError(t, k.EndBlocker(ctx))
	}

	for i := 0; i < holders; i++ {
		addr := acc(fmt.Sprintf("holder%d", i))
		require.True(t, bank.GetBalance(ctx, addr, atosDenom).Amount.IsPositive(),
			"holder%d was never reached by the sweep", i)
	}
	require.Empty(t, k.GetScanCursor(ctx), "cursor should reset after a full pass")

	gs := k.GetGlobalState(ctx)
	require.True(t, gs.TotalPaidOut.LTE(gs.TotalReleasedToPool))
}

func TestEndBlocker_CursorAdvancesAndDoesNotRepeat(t *testing.T) {
	k, ctx, _ := setup(t)

	const holders = 6
	share := k.GetParams(ctx).SupplyCap.QuoRaw(holders)
	for i := 0; i < holders; i++ {
		require.NoError(t, k.MintAtox(ctx, acc(fmt.Sprintf("holder%d", i)), share))
	}
	require.NoError(t, k.AddToExchangePool(ctx, sourceModule, math.NewIntWithDecimal(1, 28)))

	p := k.GetParams(ctx)
	p.AutoSettlePerBlock = 2
	require.NoError(t, k.SetParams(ctx, p))

	require.NoError(t, k.EndBlocker(ctx))
	first := k.GetScanCursor(ctx)
	require.NotEmpty(t, first, "a partial pass must leave a cursor")

	require.NoError(t, k.EndBlocker(ctx))
	second := k.GetScanCursor(ctx)
	require.NotEqual(t, string(first), string(second), "cursor must advance, not re-settle the same account")
}

func TestEndBlocker_DisabledOrZeroSweepIsNoop(t *testing.T) {
	k, ctx, bank := setup(t)
	alice := acc("alice")
	require.NoError(t, k.MintAtox(ctx, alice, k.GetParams(ctx).SupplyCap))
	require.NoError(t, k.AddToExchangePool(ctx, sourceModule, math.NewIntWithDecimal(1, 28)))

	p := k.GetParams(ctx)
	p.AutoSettlePerBlock = 0
	require.NoError(t, k.SetParams(ctx, p))
	require.NoError(t, k.EndBlocker(ctx))
	require.True(t, bank.GetBalance(ctx, alice, atosDenom).Amount.IsZero())

	p.AutoSettlePerBlock = 50
	p.Enabled = false
	require.NoError(t, k.SetParams(ctx, p))
	require.NoError(t, k.EndBlocker(ctx))
	require.True(t, bank.GetBalance(ctx, alice, atosDenom).Amount.IsZero())
}

// TestSolvency_PoolAlwaysCoversBooks checks the invariant the auditor should
// verify continuously: settled-but-unpaid obligations never exceed the pool.
func TestSolvency_PoolAlwaysCoversBooks(t *testing.T) {
	k, ctx, bank := setup(t)

	holders := []sdk.AccAddress{acc("a"), acc("b"), acc("c")}
	share := k.GetParams(ctx).SupplyCap.QuoRaw(4) // 3/4 of cap minted in total
	for _, h := range holders {
		require.NoError(t, k.MintAtox(ctx, h, share))
	}

	for round := 0; round < 5; round++ {
		require.NoError(t, k.AddToExchangePool(ctx, sourceModule, math.NewIntWithDecimal(1, 27)))
		require.NoError(t, bank.SendCoins(ctx, holders[0], holders[1],
			sdk.NewCoins(sdk.NewCoin(atoxDenom, share.QuoRaw(10)))))
		require.NoError(t, k.EndBlocker(ctx))

		gs := k.GetGlobalState(ctx)
		require.True(t, k.ExchangePoolBalance(ctx).GTE(gs.TotalPending),
			"round %d: pool %s cannot cover pending %s",
			round, k.ExchangePoolBalance(ctx), gs.TotalPending)
		require.True(t, gs.TotalPending.Add(gs.TotalPaidOut).LTE(gs.TotalReleasedToPool),
			"round %d: booked exceeds released", round)
	}
}
