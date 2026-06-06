package keeper

import (
	"context"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/atoshi-chain/atoshi/v20/x/oracle/types"
)

type msgServer struct {
	Keeper
}

// NewMsgServerImpl returns an implementation of the MsgServer interface.
func NewMsgServerImpl(keeper Keeper) types.MsgServer {
	return &msgServer{Keeper: keeper}
}

var _ types.MsgServer = msgServer{}

func (k msgServer) ReportPrice(goCtx context.Context, msg *types.MsgReportPrice) (*types.MsgReportPriceResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	params := k.GetParams(ctx)

	if !params.IsAllowedFeeder(msg.Feeder) {
		return nil, types.ErrUnauthorizedFeeder
	}

	priceData := types.PriceData{
		Price:     msg.Price,
		Volume24h: msg.Volume24h,
		Timestamp: ctx.BlockTime().Unix(),
		Feeder:    msg.Feeder,
		Source:    msg.Source,
	}

	if err := k.SetCurrentPrice(ctx, priceData); err != nil {
		return nil, err
	}

	if err := k.AppendPriceHistory(ctx, priceData); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			types.EventTypeReportPrice,
			sdk.NewAttribute(types.AttributeKeyPrice, msg.Price.String()),
			sdk.NewAttribute(types.AttributeKeyVolume, msg.Volume24h.String()),
			sdk.NewAttribute(types.AttributeKeyFeeder, msg.Feeder),
			sdk.NewAttribute(types.AttributeKeySource, msg.Source),
			sdk.NewAttribute(types.AttributeKeyTimestamp, fmt.Sprintf("%d", ctx.BlockTime().Unix())),
		),
	)

	k.Logger(ctx).Info(
		"price reported",
		"price", msg.Price.String(),
		"volume", msg.Volume24h.String(),
		"feeder", msg.Feeder,
		"source", msg.Source,
	)

	return &types.MsgReportPriceResponse{}, nil
}

func (k msgServer) UpdateParams(goCtx context.Context, msg *types.MsgUpdateParams) (*types.MsgUpdateParamsResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	if k.GetAuthority() != msg.Authority {
		return nil, fmt.Errorf("unauthorized: expected %s, got %s", k.GetAuthority(), msg.Authority)
	}

	if err := k.SetParams(ctx, msg.Params); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(types.EventTypeUpdateParams),
	)

	return &types.MsgUpdateParamsResponse{}, nil
}
