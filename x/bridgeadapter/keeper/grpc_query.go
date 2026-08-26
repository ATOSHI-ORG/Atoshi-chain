package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/atoshi-chain/atoshi/v20/x/bridgeadapter/types"
)

type Querier struct {
	Keeper
}

func NewQuerier(k Keeper) types.QueryServer { return Querier{Keeper: k} }

var _ types.QueryServer = Querier{}

func (q Querier) Params(goCtx context.Context, _ *types.QueryParamsRequest) (*types.QueryParamsResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	return &types.QueryParamsResponse{Params: q.GetParams(ctx)}, nil
}

func (q Querier) ReceiptState(goCtx context.Context, _ *types.QueryReceiptStateRequest) (*types.QueryReceiptStateResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	bridge, project := q.PendingConfirmation(ctx)
	return &types.QueryReceiptStateResponse{
		State:          q.GetReceiptState(ctx),
		PendingBridge:  bridge,
		PendingProject: project,
	}, nil
}
