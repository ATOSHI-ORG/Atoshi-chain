package keeper

import (
	"context"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/atoshi-chain/atoshi/v20/x/bridgeadapter/types"
)

type msgServer struct {
	Keeper
}

func NewMsgServerImpl(k Keeper) types.MsgServer {
	return &msgServer{Keeper: k}
}

var _ types.MsgServer = msgServer{}

// BridgeOut is the only way ATOS leaves for Ethereum. Everything the rate
// limiter and the peg require is enforced in the keeper, so this handler just
// decodes and delegates.
func (k msgServer) BridgeOut(goCtx context.Context, msg *types.MsgBridgeOut) (*types.MsgBridgeOutResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	sender, err := sdk.AccAddressFromBech32(msg.Sender)
	if err != nil {
		return nil, err
	}

	msgID, erc20Amount, err := k.ExecuteBridgeOut(ctx, sender, msg.Recipient, msg.Amount, msg.MaxFee)
	if err != nil {
		return nil, err
	}

	return &types.MsgBridgeOutResponse{
		MessageId:   msgID.Bytes(),
		Erc20Amount: erc20Amount,
	}, nil
}

func (k msgServer) UpdateParams(goCtx context.Context, msg *types.MsgUpdateParams) (*types.MsgUpdateParamsResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	if k.GetAuthority() != msg.Authority {
		return nil, fmt.Errorf("unauthorized: expected %s, got %s", k.GetAuthority(), msg.Authority)
	}

	current := k.GetParams(ctx)

	// The peg is not a tunable. Raising it would release more ATOS than the ERC20
	// behind a confirmed receipt covers; lowering it would strand ATOS that is
	// already backed. Either way it breaks the invariant the whole round trip
	// exists to maintain, so it is rejected outright rather than left to a
	// proposal author to get right.
	if msg.Params.AtosPerErc20 != current.AtosPerErc20 {
		return nil, fmt.Errorf(
			"atos_per_erc20 is immutable (it is the ATOS/ERC20 peg): have %d, got %d",
			current.AtosPerErc20, msg.Params.AtosPerErc20)
	}

	// Retargeting the vault mid-flight would make the cumulative totals refer to
	// a different contract's history, so a lower total from the new vault would
	// look like a backwards receipt and a higher one would release against
	// amounts the old vault reported. Changing it requires disabling first, which
	// forces the operator to reconcile deliberately.
	if current.Enabled && msg.Params.Enabled &&
		len(current.TierReleaseVault) == HexAddressLenLocal &&
		!current.VaultMatches(msg.Params.TierReleaseVault) {
		return nil, fmt.Errorf(
			"cannot retarget tier_release_vault while enabled: disable the adapter first")
	}

	if err := k.SetParams(ctx, msg.Params); err != nil {
		return nil, err
	}
	ctx.EventManager().EmitEvent(sdk.NewEvent(types.EventTypeUpdateParams))
	return &types.MsgUpdateParamsResponse{}, nil
}

// HexAddressLenLocal mirrors types.HexAddressLen for readability at the call
// site above.
const HexAddressLenLocal = types.HexAddressLen
