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
type fakeBank struct {
	balances map[string]math.Int
	denom    string
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

func (b *fakeBank) SendCoinsFromAccountToModule(_ context.Context, sender sdk.AccAddress, _ string, amt sdk.Coins) error {
	cur := b.balances[sender.String()]
	if cur.IsNil() {
		cur = math.ZeroInt()
	}
	b.balances[sender.String()] = cur.Sub(amt.AmountOf(b.denom))
	return nil
}

func (b *fakeBank) SendCoinsFromModuleToAccount(_ context.Context, _ string, recipient sdk.AccAddress, amt sdk.Coins) error {
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

	bank := newFakeBank("aatos")
	k := NewKeeper(cdc, storeKey, fakeAccountKeeper{}, bank,
		sdk.AccAddress([]byte("authority")).String(), "aatos")

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

func TestOnBalanceChange_CapsDownAccruedOnSell(t *testing.T) {
	k, ctx, bank := newKeeperForTest(t)
	a := addr("alice___________________")
	// Start at 60,000 ATOS (capacity 100k).
	bank.balances[a.String()] = math.NewIntWithDecimal(60_000, 18)
	k.Settle(ctx, a)
	ctx = ctx.WithBlockTime(ctx.BlockTime().Add(48 * time.Hour))
	full := k.Settle(ctx, a)
	require.EqualValues(t, 100_000, full.TxEnergyAccrued)

	// Sell 30,000: balance drops to 30,000, capacity 50k.
	bank.balances[a.String()] = math.NewIntWithDecimal(30_000, 18)
	k.OnBalanceChange(ctx, a)
	got := k.GetEnergyAccount(ctx, a)
	require.EqualValues(t, 50_000, got.TxEnergyAccrued, "should be capped to new capacity")
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
