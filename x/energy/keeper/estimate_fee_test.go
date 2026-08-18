package keeper_test

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

// stubFeemarket returns a fixed min_gas_price for the EstimateFee test.
// Production wires `app/app.go:feemarketKeeperShim` which reads
// feemarket.GetParams(ctx).MinGasPrice — this stub stands in for that
// shim so the test doesn't pull in x/feemarket's full keeper.
type stubFeemarket struct{ price math.LegacyDec }

func (s stubFeemarket) GetMinGasPrice(_ sdk.Context) math.LegacyDec { return s.price }

// stubAccountKeeper / stubBank are intentionally minimal — EstimateFee
// only invokes EstimateConsume which reads from the energy store; the
// account / bank keepers aren't called on this path with an existing
// account that has no own energy.
type estFeeAcct struct{}

func (estFeeAcct) GetModuleAddress(name string) sdk.AccAddress {
	return sdk.AccAddress([]byte("module/" + name))
}
func (estFeeAcct) GetModuleAccount(_ context.Context, _ string) sdk.ModuleAccountI { return nil }

type estFeeBank struct{}

func (estFeeBank) GetBalance(_ context.Context, _ sdk.AccAddress, denom string) sdk.Coin {
	return sdk.NewCoin(denom, math.ZeroInt())
}
func (estFeeBank) SendCoinsFromAccountToModule(_ context.Context, _ sdk.AccAddress, _ string, _ sdk.Coins) error {
	return nil
}
func (estFeeBank) SendCoinsFromModuleToAccount(_ context.Context, _ string, _ sdk.AccAddress, _ sdk.Coins) error {
	return nil
}

func newEstFeeKeeper(t *testing.T, fk types.FeemarketKeeper) (keeper.Keeper, sdk.Context) {
	t.Helper()
	storeKey := storetypes.NewKVStoreKey(types.StoreKey)
	db := dbm.NewMemDB()
	cms := store.NewCommitMultiStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	cms.MountStoreWithDB(storeKey, storetypes.StoreTypeIAVL, db)
	require.NoError(t, cms.LoadLatestVersion())

	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)

	k := keeper.NewKeeper(cdc, storeKey, estFeeAcct{}, estFeeBank{}, nil, fk,
		sdk.AccAddress([]byte("authority")).String(), "liao")

	header := tmproto.Header{Time: time.Unix(1_700_000_000, 0)}
	ctx := sdk.NewContext(cms, header, false, log.NewNopLogger()).
		WithGasMeter(storetypes.NewGasMeter(1_000_000))
	require.NoError(t, k.SetParams(ctx, types.DefaultParams()))
	return k, ctx
}

// Production scenario: chain genesis sets feemarket.min_gas_price = 1 gwei
// (10^9 liao/gas). A typical 300k-gas MsgSend with zero accrued energy
// has shortfall_gas = 300_000, so estimate_fee should return
//
//   atos_fee = 1e9 × 300_000 = 3e14 liao = 0.0003 ATOS
//
// Pre-fix (round-3 only) this returned 0.0021 × 300_000 = 630 liao
// (≈6.3e-16 ATOS) — off by 10^12, wallet UI displayed "0.000000..." and
// looked broken.
func TestEstimateFee_UsesFeemarketMinGasPrice(t *testing.T) {
	gwei := math.LegacyNewDec(1_000_000_000) // 1 gwei in liao/gas
	k, ctx := newEstFeeKeeper(t, stubFeemarket{price: gwei})

	signer := sdk.AccAddress([]byte("alice___________________"))
	// no energy seeded → all 300_000 gas falls into shortfall
	q := keeper.NewQueryServerImpl(k)
	resp, err := q.EstimateFee(sdk.WrapSDKContext(ctx), &types.QueryEstimateFeeRequest{
		Signer:   signer.String(),
		GasLimit: 300_000,
		IsDeploy: false,
	})
	require.NoError(t, err)
	require.EqualValues(t, 300_000, resp.ShortfallGas)

	expected := math.LegacyNewDec(300_000_000_000_000) // 0.0003 ATOS = 3e14 liao
	require.True(t, resp.AtosFee.Equal(expected),
		"expected atos_fee = %s liao, got %s", expected, resp.AtosFee)
}

// Belt-and-suspenders: if production ever forgets to wire feemarket
// (or feemarket returns zero), EstimateFee falls back to the
// InsufficientGasPrice floor so callers don't see atos_fee = 0 on a
// non-zero shortfall.
func TestEstimateFee_FallbackToInsufficientGasPrice(t *testing.T) {
	k, ctx := newEstFeeKeeper(t, nil) // no feemarket keeper

	signer := sdk.AccAddress([]byte("alice___________________"))
	q := keeper.NewQueryServerImpl(k)
	resp, err := q.EstimateFee(sdk.WrapSDKContext(ctx), &types.QueryEstimateFeeRequest{
		Signer:   signer.String(),
		GasLimit: 300_000,
	})
	require.NoError(t, err)
	require.EqualValues(t, 300_000, resp.ShortfallGas)
	// DefaultParams.InsufficientGasPrice = 0.0021 liao/gas
	// → 0.0021 × 300_000 = 630
	require.True(t, resp.AtosFee.Equal(math.LegacyNewDec(630)),
		"fallback path: expected 630 liao, got %s", resp.AtosFee)
}

// Zero shortfall (energy fully covered the gas) should return atos_fee=0
// regardless of which gas price source is wired.
func TestEstimateFee_ZeroShortfall_ZeroFee(t *testing.T) {
	gwei := math.LegacyNewDec(1_000_000_000)
	k, ctx := newEstFeeKeeper(t, stubFeemarket{price: gwei})

	signer := sdk.AccAddress([]byte("alice___________________"))
	// seed enough TxEnergyAccrued to fully cover a tiny gas budget
	acct := types.EnergyAccount{
		Address:             signer.String(),
		TxEnergyAccrued:     1_000,
		LastBalanceSnapshot: math.NewInt(0),
		LastUpdatedTime:     ctx.BlockTime().Unix(),
	}
	k.SetEnergyAccount(ctx, acct)

	q := keeper.NewQueryServerImpl(k)
	resp, err := q.EstimateFee(sdk.WrapSDKContext(ctx), &types.QueryEstimateFeeRequest{
		Signer:   signer.String(),
		GasLimit: 500,
	})
	require.NoError(t, err)
	require.EqualValues(t, 0, resp.ShortfallGas)
	require.True(t, resp.AtosFee.IsZero(), "expected zero fee for zero shortfall, got %s", resp.AtosFee)
}
