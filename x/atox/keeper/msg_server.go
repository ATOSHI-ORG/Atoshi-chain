package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/atoshi-chain/atoshi/v20/x/atox/types"
)

type msgServer struct {
	Keeper
}

func NewMsgServerImpl(k Keeper) types.MsgServer {
	return &msgServer{Keeper: k}
}

var _ types.MsgServer = msgServer{}

// ClaimAtos settles the sender and pays out the whole pending balance.
//
// Holders do not need this under normal operation — the EndBlocker sweep reaches
// every account — but it lets someone convert immediately rather than waiting
// for the sweep to come round. minPayout is zero here on purpose: the dust
// threshold exists to keep the automatic sweep from filling blocks, and should
// not stop a holder who explicitly asked to be paid.
func (k msgServer) ClaimAtos(goCtx context.Context, msg *types.MsgClaimAtos) (*types.MsgClaimAtosResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	if !k.GetParams(ctx).Enabled {
		return nil, types.ErrAtoxDisabled
	}

	claimer, err := sdk.AccAddressFromBech32(msg.Claimer)
	if err != nil {
		return nil, err
	}

	paid, err := k.PayoutPending(ctx, claimer, math.ZeroInt(), types.TriggerClaim)
	if err != nil {
		return nil, err
	}
	if !paid.IsPositive() {
		return nil, types.ErrNothingToClaim
	}

	return &types.MsgClaimAtosResponse{Amount: paid}, nil
}

func (k msgServer) UpdateParams(goCtx context.Context, msg *types.MsgUpdateParams) (*types.MsgUpdateParamsResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	if k.GetAuthority() != msg.Authority {
		return nil, fmt.Errorf("unauthorized: expected %s, got %s", k.GetAuthority(), msg.Authority)
	}

	// The supply cap is the index denominator. Lowering it below what has already
	// been minted would make sum(balances)*delta exceed the released amount and
	// break the solvency bound; raising it silently dilutes every existing
	// holder's claim. Neither is a parameter change, so both are rejected.
	if current := k.GetParams(ctx); !msg.Params.SupplyCap.Equal(current.SupplyCap) {
		return nil, fmt.Errorf(
			"supply_cap is immutable (it is the conversion index denominator): have %s, got %s",
			current.SupplyCap, msg.Params.SupplyCap)
	}

	if err := k.SetParams(ctx, msg.Params); err != nil {
		return nil, err
	}
	ctx.EventManager().EmitEvent(sdk.NewEvent(types.EventTypeUpdateParams))
	return &types.MsgUpdateParamsResponse{}, nil
}
