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

	// Duration policy:
	//   1. Client-supplied 0 → substitute the protocol default (prefer
	//      the governance-set params value; fall back to the compiled
	//      constant so pre-upgrade state without this field still works).
	//   2. Enforce the governance-set max cap (skip when unset / 0,
	//      meaning "no cap" for backward compat with pre-upgrade state).
	params := s.GetParams(ctx)
	duration := msg.DurationSeconds
	if duration == 0 {
		if params.DefaultDelegationDurationSeconds > 0 {
			duration = params.DefaultDelegationDurationSeconds
		} else {
			duration = types.DefaultDelegationDurationSeconds
		}
	}
	if params.MaxDelegationDurationSeconds > 0 &&
		duration > params.MaxDelegationDurationSeconds {
		return nil, fmt.Errorf(
			"duration_seconds %d exceeds max_delegation_duration_seconds %d",
			duration, params.MaxDelegationDurationSeconds)
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
