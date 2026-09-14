package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/stretchr/testify/require"

	erc20precompile "github.com/atoshi-chain/atoshi/v20/precompiles/erc20"
	"github.com/atoshi-chain/atoshi/v20/testutil/integration/evmos/network"
	atoshitypes "github.com/atoshi-chain/atoshi/v20/types"
)

// TestTransferFee_ERC20PathChargesTheSameFee pins that ATOX pays the transfer
// fee whichever Msg server moves it.
//
// There are two, and they used not to be the same object:
//
//	the router's    registered by the bank AppModule -- our fee-inclusive
//	                wrapper. A wallet's MsgSend and authz dispatch land here.
//	the precompile's  precompiles/erc20 built its own with
//	                bankkeeper.NewMsgServerImpl(bankKeeper) -- the stock one,
//	                no fee.
//
// ATOX is a registered token pair (0xc2b6…), so that second one made ERC20
// transfer() a fee-free route for ATOX, while transferFrom() -- which goes out
// through authz and therefore the router -- did pay. Measured, not theorised:
// before the fix this test showed the receiver getting the full 100.
//
// Both ends are asserted because the bug lived in the seam between them: the
// app must inject a fee-charging server, and x/erc20 must hand that server to
// the precompiles it builds.
func TestTransferFee_ERC20PathChargesTheSameFee(t *testing.T) {
	nw := network.NewUnitTestNetwork()
	ctx := nw.GetContext()
	k := nw.App.AtoxKeeper

	denom := atoshitypes.AtoxBaseDenom
	amount := atox(100)

	received := func(srv banktypes.MsgServer, label string) string {
		from, to := acc("from-"+label), acc("to-"+label)
		require.NoError(t, k.MintAtox(ctx, from, amount))
		_, err := srv.Send(ctx, banktypes.NewMsgSend(from, to, sdk.NewCoins(sdk.NewCoin(denom, amount))))
		require.NoError(t, err)
		return k.AtoxBalance(ctx, to).String()
	}

	// What the Msg router dispatches a wallet's MsgSend to.
	routed := nw.App.MsgServiceRouter().Handler(&banktypes.MsgSend{})
	require.NotNil(t, routed, "bank MsgSend must have a registered handler")
	from, to := acc("from-routed"), acc("to-routed")
	require.NoError(t, k.MintAtox(ctx, from, amount))
	_, err := routed(ctx, banktypes.NewMsgSend(from, to, sdk.NewCoins(sdk.NewCoin(denom, amount))))
	require.NoError(t, err)
	require.Equal(t, atox(90).String(), k.AtoxBalance(ctx, to).String(),
		"the routed path must charge the inclusive fee")

	// What the ERC20 token-pair precompiles transfer through.
	erc20Srv := nw.App.Erc20Keeper.BankMsgServer()
	require.NotNil(t, erc20Srv,
		"x/erc20 has no Msg server injected, so its precompiles fall back to the "+
			"stock one and ERC20 transfer() moves ATOX untaxed")
	require.Equal(t, atox(90).String(), received(erc20Srv, "erc20"),
		"ERC20 transfer() must charge the same fee as MsgSend")
}

// TestTransferFee_InstantiatedERC20PrecompileCarriesTheFeeServer closes the last
// seam: x/erc20 must actually hand its injected Msg server to the precompile it
// builds. Asserting only the keeper's field would still pass if
// InstantiateERC20Precompile dropped it on the floor.
func TestTransferFee_InstantiatedERC20PrecompileCarriesTheFeeServer(t *testing.T) {
	nw := network.NewUnitTestNetwork()
	ctx := nw.GetContext()

	pairs := nw.App.Erc20Keeper.GetTokenPairs(ctx)
	require.NotEmpty(t, pairs, "no token pairs registered; nothing to instantiate")

	// Prefer ATOX when the fixture registers it; otherwise any pair proves the
	// same thing -- the Msg server is wired per keeper, not per denom, so a pair
	// that carries it carries it for ATOX too. (The unit-test genesis registers
	// only the staking denom; the live chain also has aatox at 0xc2b6….)
	pair := pairs[0]
	for i := range pairs {
		if pairs[i].Denom == atoshitypes.AtoxBaseDenom {
			pair = pairs[i]
			break
		}
	}

	pc, err := nw.App.Erc20Keeper.InstantiateERC20Precompile(
		ctx, pair.GetERC20Contract(), false)
	require.NoError(t, err)

	p, ok := pc.(*erc20precompile.Precompile)
	require.True(t, ok)
	require.NotNil(t, p.BankMsgServer(),
		"the instantiated ERC20 precompile (%s) has no fee-charging Msg server, "+
			"so transfer() would move ATOX untaxed", pair.Denom)
}
