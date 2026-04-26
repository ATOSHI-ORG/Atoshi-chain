package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/atoshi-chain/atoshi/v20/x/oracle/types"
)

type queryServer struct {
	Keeper
}

// NewQueryServerImpl returns an implementation of the QueryServer interface.
func NewQueryServerImpl(keeper Keeper) types.QueryServer {
	return &queryServer{Keeper: keeper}
}

var _ types.QueryServer = queryServer{}

func (q queryServer) CurrentPrice(goCtx context.Context, _ *types.QueryCurrentPriceRequest) (*types.QueryCurrentPriceResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	price, err := q.GetCurrentPrice(ctx)
	if err != nil {
		return nil, err
	}
	return &types.QueryCurrentPriceResponse{PriceData: price}, nil
}

func (q queryServer) PriceHistory(goCtx context.Context, req *types.QueryPriceHistoryRequest) (*types.QueryPriceHistoryResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	limit := req.Limit
	if limit == 0 {
		limit = 100
	}
	prices := q.GetPriceHistory(ctx, limit)
	return &types.QueryPriceHistoryResponse{Prices: prices}, nil
}

func (q queryServer) TWAP(goCtx context.Context, req *types.QueryTWAPRequest) (*types.QueryTWAPResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	lookback := req.LookbackSeconds
	if lookback == 0 {
		params := q.GetParams(ctx)
		lookback = params.TWAPLookbackSeconds
	}
	twapPrice, avgVolume, err := q.CalculateTWAP(ctx, lookback)
	if err != nil {
		return nil, err
	}
	return &types.QueryTWAPResponse{TWAPPrice: twapPrice, AvgVolume: avgVolume}, nil
}

func (q queryServer) Params(goCtx context.Context, _ *types.QueryParamsRequest) (*types.QueryParamsResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	params := q.GetParams(ctx)
	return &types.QueryParamsResponse{Params: params}, nil
}
