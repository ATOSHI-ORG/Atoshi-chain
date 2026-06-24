package ante

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

	"github.com/atoshi-chain/atoshi/v20/x/energy/keeper"
	"github.com/atoshi-chain/atoshi/v20/x/energy/types"
)

// shortfallBankStub is a minimal bank for the keeper constructor; we
// never exercise its methods from these tests (computeShortfallFee
// only needs params + denom).
type shortfallBankStub struct{}

func (shortfallBankStub) GetBalance(_ context.Context, _ sdk.AccAddress, denom string) sdk.Coin {
	return sdk.NewCoin(denom, math.ZeroInt())
}
func (shortfallBankStub) SendCoinsFromAccountToModule(_ context.Context, _ sdk.AccAddress, _ string, _ sdk.Coins) error {
	return nil
}
func (shortfallBankStub) SendCoinsFromModuleToAccount(_ context.Context, _ string, _ sdk.AccAddress, _ sdk.Coins) error {
	return nil
}

type shortfallAcctStub struct{}

func (shortfallAcctStub) GetModuleAddress(name string) sdk.AccAddress {
	return sdk.AccAddress([]byte("module/" + name))
}
func (shortfallAcctStub) GetModuleAccount(_ context.Context, _ string) sdk.ModuleAccountI {
	return nil
}

func newKeeperForShortfallTest(t *testing.T) (keeper.Keeper, sdk.Context) {
	t.Helper()
	storeKey := storetypes.NewKVStoreKey(types.StoreKey)
	db := dbm.NewMemDB()
	cms := store.NewCommitMultiStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	cms.MountStoreWithDB(storeKey, storetypes.StoreTypeIAVL, db)
	require.NoError(t, cms.LoadLatestVersion())

	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)

	k := keeper.NewKeeper(cdc, storeKey, shortfallAcctStub{}, shortfallBankStub{}, nil,
		sdk.AccAddress([]byte("authority")).String(), "aatos")

	header := tmproto.Header{Time: time.Unix(1_700_000_000, 0)}
	ctx := sdk.NewContext(cms, header, false, log.NewNopLogger())
	require.NoError(t, k.SetParams(ctx, types.DefaultParams()))
	return k, ctx
}

// Audit Question 6 (round2) regression: a user offering 1 aatos in
// fee with gasLimit = 200,000 must NOT slip through with chargeAtos = 0
// when there is genuine shortfall gas to bill.
//
// Pre-fix:
//   num = 1 × shortfallGas
//   amt = num / 200_000 → 0 (integer truncation when shortfallGas < gasLimit)
//   chargeAtos.IsZero() → ante early-returns to next() — full evasion.
//
// Post-fix: the InsufficientGasPrice floor kicks in. With default
// params (InsufficientGasPrice = 0.0021), and shortfallGas = 1000:
//   floor amount = ceil(0.0021 × 1000) = ceil(2.1) = 3 aatos
// So chargeAtos must be at least 3 aatos — never zero.
func TestComputeShortfallFee_FloorsAtInsufficientGasPriceOnDustOffer(t *testing.T) {
	k, ctx := newKeeperForShortfallTest(t)

	// User offers a 1-aatos fee against gas_limit = 200_000.
	fee := sdk.NewCoins(sdk.NewCoin("aatos", math.NewInt(1)))
	got := computeShortfallFee(k, ctx, 1000 /* shortfallGas */, fee, 200_000 /* gasLimit */)

	require.False(t, got.IsZero(),
		"audit Q6: dust offer (1 aatos / 200k gas) must NOT produce chargeAtos = 0; the InsufficientGasPrice floor must trigger")

	// Default InsufficientGasPrice = 0.0021. shortfallGas = 1000 →
	// floor = ceil(0.0021 × 1000) = ceil(2.1) = 3 aatos.
	require.EqualValues(t, int64(3), got.AmountOf("aatos").Int64(),
		"chargeAtos must equal ceil(InsufficientGasPrice × shortfallGas) when the user's offered rate falls below the floor")
}

// Audit Question 6 (round2) companion: a healthy fee (above the
// InsufficientGasPrice floor) gets pro-rated as before — the floor
// does not penalize legitimate offers.
func TestComputeShortfallFee_ProRatesNormalOffer(t *testing.T) {
	k, ctx := newKeeperForShortfallTest(t)

	// 1 aatos/gas × 200_000 = 200_000 aatos total fee. Well above
	// the 0.0021 InsufficientGasPrice floor.
	fee := sdk.NewCoins(sdk.NewCoin("aatos", math.NewInt(200_000)))
	got := computeShortfallFee(k, ctx, 1000, fee, 200_000)

	// Pro-rated: 200_000 × 1000 / 200_000 = 1000 aatos.
	require.EqualValues(t, int64(1000), got.AmountOf("aatos").Int64(),
		"normal offer must pro-rate exactly: offered × shortfallGas / gasLimit")
}

// Audit Question 6 (round2): the pre-existing zero-offer branch
// (user offers no fee at all) is preserved by the unified
// computation — InsufficientGasPrice still floors. Same outcome as
// before, just routed through one code path now.
func TestComputeShortfallFee_ZeroOfferStillFloors(t *testing.T) {
	k, ctx := newKeeperForShortfallTest(t)

	got := computeShortfallFee(k, ctx, 1000, sdk.NewCoins(), 200_000)

	// ceil(0.0021 × 1000) = 3.
	require.EqualValues(t, int64(3), got.AmountOf("aatos").Int64(),
		"zero-offer floor unchanged after the unified rewrite")
}

// shortfallGas == 0 short-circuits regardless of offer. Energy
// covered all gas; nothing to charge.
func TestComputeShortfallFee_ZeroShortfallChargesNothing(t *testing.T) {
	k, ctx := newKeeperForShortfallTest(t)
	fee := sdk.NewCoins(sdk.NewCoin("aatos", math.NewInt(1)))
	got := computeShortfallFee(k, ctx, 0, fee, 200_000)
	require.True(t, got.IsZero(), "shortfallGas=0 must return empty coins")
}
