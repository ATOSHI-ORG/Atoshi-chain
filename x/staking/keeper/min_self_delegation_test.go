package keeper_test

import (
	"testing"

	"cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"

	"github.com/atoshi-chain/atoshi/v20/testutil"
	"github.com/atoshi-chain/atoshi/v20/testutil/integration/evmos/network"
	utiltx "github.com/atoshi-chain/atoshi/v20/testutil/tx"
	evmostypes "github.com/atoshi-chain/atoshi/v20/types"
	"github.com/atoshi-chain/atoshi/v20/x/staking/keeper"
)

// The chain-wide self-stake floor used to be enforced only by an AnteHandler
// decorator, which EVM txs never pass through: the staking precompile reached
// this msg server directly and created validators with 1 wei of self-stake.
// The msg server now enforces it for every caller.
func TestMinSelfDelegationFloorInMsgServer(t *testing.T) {
	floor := math.NewInt(1e18).MulRaw(4)

	setup := func(t *testing.T) (*network.UnitTestNetwork, sdk.Context, sdk.AccAddress) {
		nw := network.NewUnitTestNetwork()
		ctx := nw.GetContext()
		params := nw.App.TokenomicsKeeper.GetParams(ctx)
		params.ValidatorMinSelfDelegation = floor
		require.NoError(t, nw.App.TokenomicsKeeper.SetParams(ctx, params))

		valAddr, _ := utiltx.NewAccAddressAndKey()
		require.NoError(t, testutil.FundAccountWithBaseDenom(ctx, nw.App.BankKeeper, valAddr, floor.MulRaw(2).Int64()))
		return nw, ctx, valAddr
	}

	create := func(nw *network.UnitTestNetwork, ctx sdk.Context, valAddr sdk.AccAddress, selfBond, minSelf math.Int) error {
		msg, err := types.NewMsgCreateValidator(
			sdk.ValAddress(valAddr).String(),
			ed25519.GenPrivKey().PubKey(),
			sdk.NewCoin(evmostypes.BaseDenom, selfBond),
			types.NewDescription("T", "E", "S", "T", "Z"),
			types.NewCommissionRates(math.LegacyNewDecWithPrec(5, 2), math.LegacyNewDecWithPrec(2, 1), math.LegacyNewDecWithPrec(5, 2)),
			minSelf,
		)
		require.NoError(t, err)
		_, err = keeper.NewMsgServerImpl(&nw.App.StakingKeeper).CreateValidator(ctx, msg)
		return err
	}

	t.Run("declared minimum below the floor is rejected", func(t *testing.T) {
		nw, ctx, valAddr := setup(t)
		require.ErrorContains(t, create(nw, ctx, valAddr, floor, math.OneInt()), "min_self_delegation must be at least")
	})

	t.Run("self-bond below the floor is rejected", func(t *testing.T) {
		nw, ctx, valAddr := setup(t)
		require.ErrorContains(t, create(nw, ctx, valAddr, math.OneInt(), floor), "must self-delegate at least")
	})

	t.Run("meeting the floor succeeds, and it cannot be edited below it", func(t *testing.T) {
		nw, ctx, valAddr := setup(t)
		require.NoError(t, create(nw, ctx, valAddr, floor, floor))

		lower := math.OneInt()
		edit := types.NewMsgEditValidator(sdk.ValAddress(valAddr).String(), types.NewDescription("T", "", "", "", ""), nil, &lower)
		_, err := keeper.NewMsgServerImpl(&nw.App.StakingKeeper).EditValidator(ctx, edit)
		require.ErrorContains(t, err, "min_self_delegation must be at least")
	})
}
