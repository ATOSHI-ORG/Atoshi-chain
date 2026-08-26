package app_test

import (
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/stretchr/testify/require"

	"github.com/atoshi-chain/atoshi/v20/app"
	atoshitypes "github.com/atoshi-chain/atoshi/v20/types"
	"github.com/atoshi-chain/atoshi/v20/utils"
	atoxtypes "github.com/atoshi-chain/atoshi/v20/x/atox/types"
	tokenomicstypes "github.com/atoshi-chain/atoshi/v20/x/tokenomics/types"
)

func setupApp(t *testing.T) (*app.Atoshi, sdk.Context) {
	t.Helper()
	a := app.Setup(false, nil, utils.MainnetChainID+"-1")
	return a, a.BaseApp.NewContext(false)
}

// TestAtoxWiring_ModuleAccountsExist fails loudly if maccPerms was not updated:
// the exchange pool must exist to hold ATOS, and the module account needs Burner
// for the transfer-fee burn.
func TestAtoxWiring_ModuleAccountsExist(t *testing.T) {
	a, ctx := setupApp(t)

	require.NotNil(t, a.AccountKeeper.GetModuleAccount(ctx, atoxtypes.ModuleName))
	require.NotNil(t, a.AccountKeeper.GetModuleAccount(ctx, atoxtypes.ExchangePoolName))

	perms := a.AccountKeeper.GetModulePermissions()[atoxtypes.ModuleName]
	require.NotNil(t, perms, "atox module account is not registered in maccPerms")
	require.True(t, perms.HasPermission(authtypes.Minter), "atox needs Minter to emit block rewards")
	require.True(t, perms.HasPermission(authtypes.Burner), "atox needs Burner to burn the transfer fee")
}

// TestAtoxWiring_EndToEnd walks the whole design against a real app: mine ATOX,
// transfer it and see the fee burn, fund the exchange pool as a tier release
// would, then let the EndBlocker sweep convert to ATOS with no user action.
func TestAtoxWiring_EndToEnd(t *testing.T) {
	a, ctx := setupApp(t)
	k := a.AtoxKeeper

	alice := sdk.AccAddress([]byte("alice---------------"))
	bob := sdk.AccAddress([]byte("bob-----------------"))

	// --- mine ATOX (what tokenomics block rewards will do) ---
	mined := math.NewIntWithDecimal(1, 30) // the whole cap, so index == payout rate
	require.NoError(t, k.MintAtox(ctx, alice, mined))
	require.Equal(t, mined.String(), a.BankKeeper.GetBalance(ctx, alice, atoshitypes.AtoxBaseDenom).Amount.String())

	// --- transfer through the real bank: fee charged on top and burned ---
	send := atoxtypes.MaxSendableWithFee(mined, k.GetParams(ctx).TransferFeeBps)
	supplyBefore := a.BankKeeper.GetSupply(ctx, atoshitypes.AtoxBaseDenom).Amount
	require.NoError(t, a.BankKeeper.SendCoins(ctx, alice, bob,
		sdk.NewCoins(sdk.NewCoin(atoshitypes.AtoxBaseDenom, send))))

	require.Equal(t, send.String(), a.BankKeeper.GetBalance(ctx, bob, atoshitypes.AtoxBaseDenom).Amount.String())
	burned := k.GetGlobalState(ctx).TotalFeeBurned
	require.True(t, burned.IsPositive(), "transfer fee must be charged through the real bank path")
	require.Equal(t, supplyBefore.Sub(burned).String(),
		a.BankKeeper.GetSupply(ctx, atoshitypes.AtoxBaseDenom).Amount.String(),
		"the fee must actually leave supply, which is what frees mining headroom")

	// --- fund the exchange pool the way a tier release will ---
	release := math.NewIntWithDecimal(1, 28) // 10 billion ATOS
	require.NoError(t, a.BankKeeper.MintCoins(ctx, tokenomicstypes.ModuleName,
		sdk.NewCoins(sdk.NewCoin(atoshitypes.BaseDenom, release))))
	require.NoError(t, k.AddToExchangePool(ctx, tokenomicstypes.ModuleName, release))
	require.Equal(t, release.String(), k.ExchangePoolBalance(ctx).String())

	// --- the sweep converts with no user transaction ---
	atosBefore := a.BankKeeper.GetBalance(ctx, bob, atoshitypes.BaseDenom).Amount
	require.NoError(t, k.EndBlocker(ctx))
	atosAfter := a.BankKeeper.GetBalance(ctx, bob, atoshitypes.BaseDenom).Amount
	require.True(t, atosAfter.GT(atosBefore),
		"the sweep must convert ATOX to ATOS without the holder doing anything")

	// Solvency holds against the real bank balance, not just the counters.
	gs := k.GetGlobalState(ctx)
	require.True(t, k.ExchangePoolBalance(ctx).GTE(gs.TotalPending))
	require.True(t, gs.TotalPending.Add(gs.TotalPaidOut).LTE(gs.TotalReleasedToPool))
}

// TestAtox_CannotPayGas is the regression guard on the rule that ATOS is the only
// fee token. It is enforced by app/ante/cosmos/min_price.go rather than anything
// in x/atox, so nothing in the atox module's own tests would catch a regression.
func TestAtox_CannotPayGas(t *testing.T) {
	a, ctx := setupApp(t)

	base, err := sdk.GetBaseDenom()
	require.NoError(t, err)
	require.Equal(t, atoshitypes.BaseDenom, base,
		"the fee check compares against sdk.GetBaseDenom, so it must be ATOS")
	require.NotEqual(t, atoshitypes.AtoxBaseDenom, base, "ATOX must never be the fee denom")

	// ATOX is also not the staking bond denom, so it cannot be used to gain
	// consensus power either.
	bondDenom, err := a.StakingKeeper.BondDenom(ctx)
	require.NoError(t, err)
	require.Equal(t, atoshitypes.BaseDenom, bondDenom)
	require.NotEqual(t, atoshitypes.AtoxBaseDenom, bondDenom)
}

// TestAtoxWiring_HookOrderIsIndependentOfEnergy — both x/energy and x/atox append
// a send restriction. bank chains them, so each must ignore the other's denom;
// otherwise an ATOS transfer would pay ATOX fees or vice versa.
func TestAtoxWiring_HookOrderIsIndependentOfEnergy(t *testing.T) {
	a, ctx := setupApp(t)

	alice := sdk.AccAddress([]byte("alice2--------------"))
	bob := sdk.AccAddress([]byte("bob2----------------"))

	// Fund alice with ATOS only.
	atos := math.NewIntWithDecimal(1, 24)
	require.NoError(t, a.BankKeeper.MintCoins(ctx, tokenomicstypes.ModuleName,
		sdk.NewCoins(sdk.NewCoin(atoshitypes.BaseDenom, atos))))
	require.NoError(t, a.BankKeeper.SendCoinsFromModuleToAccount(ctx, tokenomicstypes.ModuleName,
		alice, sdk.NewCoins(sdk.NewCoin(atoshitypes.BaseDenom, atos))))

	// A pure ATOS transfer must not touch atox state or burn anything.
	require.NoError(t, a.BankKeeper.SendCoins(ctx, alice, bob,
		sdk.NewCoins(sdk.NewCoin(atoshitypes.BaseDenom, atos))))

	require.Equal(t, atos.String(), a.BankKeeper.GetBalance(ctx, bob, atoshitypes.BaseDenom).Amount.String(),
		"an ATOS transfer must arrive whole; no ATOX fee applies")
	require.True(t, a.AtoxKeeper.GetGlobalState(ctx).TotalFeeBurned.IsZero())
}

// TestAtoxWiring_GenesisRoundTrip guards the module's place in InitGenesis order:
// it must come after tokenomics, whose init creates the module accounts the atox
// genesis check requires.
func TestAtoxWiring_GenesisRoundTrip(t *testing.T) {
	a, ctx := setupApp(t)

	exported := a.AtoxKeeper.ExportGenesis(ctx)
	require.NoError(t, exported.Validate())
	require.Equal(t, atoxtypes.DefaultParams().SupplyCap.String(), exported.Params.SupplyCap.String())
	require.True(t, exported.GlobalState.GlobalIndex.IsZero(), "fresh chain starts with a zero index")

	// The exchange pool starts empty: ATOS only enters it against ERC20 already
	// confirmed on Ethereum, so pre-funding would create unbacked claims.
	require.True(t, a.AtoxKeeper.ExchangePoolBalance(ctx).IsZero())
	require.True(t, a.AtoxKeeper.AtoxSupply(ctx).IsZero(), "ATOX has no pre-mine")
}
