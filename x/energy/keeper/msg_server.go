package keeper

import (
	"context"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/atoshi-chain/atoshi/v20/x/energy/types"
)

type msgServer struct{ Keeper }

func NewMsgServerImpl(k Keeper) types.MsgServer { return &msgServer{Keeper: k} }

func (s msgServer) DelegateEnergy(goCtx context.Context, msg *types.MsgDelegateEnergy) (*types.MsgDelegateEnergyResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	delegator, err := sdk.AccAddressFromBech32(msg.Delegator)
	if err != nil {
		return nil, err
	}
	delegatee, err := sdk.AccAddressFromBech32(msg.Delegatee)
	if err != nil {
		return nil, err
	}

	// Apply the protocol default when the client did not specify a
	// duration. Protobuf int64 zero-value is the natural "field not
	// set" signal — wallets that don't expose a duration picker can
	// simply omit the field. ValidateBasic above accepts 0 and
	// rejects negatives; the keeper's Delegate still requires > 0,
	// so any 0 reaching it would error — we normalize here once.
	duration := msg.DurationSeconds
	if duration == 0 {
		duration = types.DefaultDelegationDurationSeconds
	}

	id, locked, err := s.Delegate(ctx, delegator, delegatee, msg.Amount, duration)
	if err != nil {
		return nil, err
	}
	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeEnergyDelegated,
		sdk.NewAttribute(types.AttributeKeyDelegationID, fmt.Sprintf("%d", id)),
		sdk.NewAttribute(types.AttributeKeyDelegator, msg.Delegator),
		sdk.NewAttribute(types.AttributeKeyDelegatee, msg.Delegatee),
		sdk.NewAttribute(types.AttributeKeyAmount, fmt.Sprintf("%d", msg.Amount)),
		sdk.NewAttribute(types.AttributeKeyLockedATOS, locked.String()),
	))
	return &types.MsgDelegateEnergyResponse{DelegationId: id, LockedAtos: locked}, nil
}

func (s msgServer) UndelegateEnergy(goCtx context.Context, msg *types.MsgUndelegateEnergy) (*types.MsgUndelegateEnergyResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	delegator, err := sdk.AccAddressFromBech32(msg.Delegator)
	if err != nil {
		return nil, err
	}
	if err := s.Undelegate(ctx, delegator, msg.DelegationId); err != nil {
		return nil, err
	}
	return &types.MsgUndelegateEnergyResponse{}, nil
}

func (s msgServer) UpdateParams(goCtx context.Context, msg *types.MsgUpdateParams) (*types.MsgUpdateParamsResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	if s.GetAuthority() != msg.Authority {
		return nil, fmt.Errorf("unauthorized: expected %s, got %s", s.GetAuthority(), msg.Authority)
	}
	if err := s.SetParams(ctx, msg.Params); err != nil {
		return nil, err
	}
	ctx.EventManager().EmitEvent(sdk.NewEvent(types.EventTypeUpdateParams))
	return &types.MsgUpdateParamsResponse{}, nil
}