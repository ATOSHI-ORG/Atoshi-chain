package keeper_test

import (
	"testing"

	"cosmossdk.io/math"
	"github.com/stretchr/testify/require"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"

	"github.com/atoshi-chain/atoshi/v20/testutil/integration/evmos/network"
	atoshitypes "github.com/atoshi-chain/atoshi/v20/types"
	"github.com/atoshi-chain/atoshi/v20/x/tokenomics/keeper"
	tokenomicstypes "github.com/atoshi-chain/atoshi/v20/x/tokenomics/types"
)

// fundSlashPool puts coins in slash_pool the way a slash does: straight into the
// module account, without minting.
func fundSlashPool(t *testing.T, nw *network.UnitTestNetwork, ctx sdk.Context, amount math.Int) {
	t.Helper()
	coins := sdk.NewCoins(sdk.NewCoin(atoshitypes.BaseDenom, amount))
	funder := sdk.AccAddress([]byte("slash-pool-funder---"))
	require.NoError(t, nw.FundAccount(funder, coins))
	require.NoError(t, nw.App.BankKeeper.SendCoinsFromAccountToModule(
		ctx, funder, tokenomicstypes.SlashPoolName, coins))
}

func slashPoolAmount(nw *network.UnitTestNetwork, ctx sdk.Context) math.Int {
	addr := nw.App.AccountKeeper.GetModuleAddress(tokenomicstypes.SlashPoolName)
	return nw.App.BankKeeper.GetBalance(ctx, addr, atoshitypes.BaseDenom).Amount
}

func govAuthority() string {
	return authtypes.NewModuleAddress(govtypes.ModuleName).String()
}

// TestSpendSlashPool_PaysTheRecipient is the happy path: this is how the
// 2026-10-18 delegators would be made whole once the pool has been filled by
// future slashes.
func TestSpendSlashPool_PaysTheRecipient(t *testing.T) {
	nw := network.NewUnitTestNetwork()
	ctx := nw.GetContext()
	srv := keeper.NewMsgServerImpl(nw.App.TokenomicsKeeper)

	fundSlashPool(t, nw, ctx, math.NewInt(1_000))
	recipient := sdk.AccAddress([]byte("compensated-user----"))
	before := nw.App.BankKeeper.GetBalance(ctx, recipient, atoshitypes.BaseDenom).Amount

	pay := sdk.NewCoins(sdk.NewCoin(atoshitypes.BaseDenom, math.NewInt(400)))
	_, err := srv.SpendSlashPool(ctx, &tokenomicstypes.MsgSpendSlashPool{
		Authority: govAuthority(),
		Recipient: recipient.String(),
		Amount:    pay,
	})
	require.NoError(t, err)

	require.Equal(t, before.AddRaw(400).String(),
		nw.App.BankKeeper.GetBalance(ctx, recipient, atoshitypes.BaseDenom).Amount.String())
	require.Equal(t, "600", slashPoolAmount(nw, ctx).String(),
		"the pool must fall by exactly what was paid")
}

// TestSpendSlashPool_RejectsNonGovernance is the containment check. slash_pool
// has no Burner and no Minter, so this handler is the only way anything leaves
// it -- which makes the authority check the whole access control story.
func TestSpendSlashPool_RejectsNonGovernance(t *testing.T) {
	nw := network.NewUnitTestNetwork()
	ctx := nw.GetContext()
	srv := keeper.NewMsgServerImpl(nw.App.TokenomicsKeeper)

	fundSlashPool(t, nw, ctx, math.NewInt(1_000))

	_, err := srv.SpendSlashPool(ctx, &tokenomicstypes.MsgSpendSlashPool{
		Authority: sdk.AccAddress([]byte("not-the-gov-module--")).String(),
		Recipient: sdk.AccAddress([]byte("would-be-thief------")).String(),
		Amount:    sdk.NewCoins(sdk.NewCoin(atoshitypes.BaseDenom, math.NewInt(1_000))),
	})
	require.ErrorContains(t, err, "unauthorized")
	require.Equal(t, "1000", slashPoolAmount(nw, ctx).String(), "the pool must be untouched")
}

// TestSpendSlashPool_RejectsOverdraft fails the proposal rather than the bank,
// so the error names both figures. A compensation proposal that asks for more
// than was ever confiscated is a drafting mistake worth seeing in full.
func TestSpendSlashPool_RejectsOverdraft(t *testing.T) {
	nw := network.NewUnitTestNetwork()
	ctx := nw.GetContext()
	srv := keeper.NewMsgServerImpl(nw.App.TokenomicsKeeper)

	fundSlashPool(t, nw, ctx, math.NewInt(100))

	_, err := srv.SpendSlashPool(ctx, &tokenomicstypes.MsgSpendSlashPool{
		Authority: govAuthority(),
		Recipient: sdk.AccAddress([]byte("compensated-user----")).String(),
		Amount:    sdk.NewCoins(sdk.NewCoin(atoshitypes.BaseDenom, math.NewInt(101))),
	})
	require.ErrorContains(t, err, "slash pool holds")
	require.Equal(t, "100", slashPoolAmount(nw, ctx).String())
}

// TestSpendSlashPool_RejectsBlockedRecipient catches a proposal naming a module
// account at submission instead of after a week of voting.
func TestSpendSlashPool_RejectsBlockedRecipient(t *testing.T) {
	nw := network.NewUnitTestNetwork()
	ctx := nw.GetContext()
	srv := keeper.NewMsgServerImpl(nw.App.TokenomicsKeeper)

	fundSlashPool(t, nw, ctx, math.NewInt(1_000))

	_, err := srv.SpendSlashPool(ctx, &tokenomicstypes.MsgSpendSlashPool{
		Authority: govAuthority(),
		Recipient: nw.App.AccountKeeper.GetModuleAddress(tokenomicstypes.MigrationPoolName).String(),
		Amount:    sdk.NewCoins(sdk.NewCoin(atoshitypes.BaseDenom, math.NewInt(1))),
	})
	require.ErrorContains(t, err, "not allowed to receive funds")
	require.Equal(t, "1000", slashPoolAmount(nw, ctx).String())
}
