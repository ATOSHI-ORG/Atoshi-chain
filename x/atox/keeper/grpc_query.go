package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/atoshi-chain/atoshi/v20/x/atox/types"
)

type Querier struct {
	Keeper
}

func NewQuerier(k Keeper) types.QueryServer {
	return Querier{Keeper: k}
}

var _ types.QueryServer = Querier{}

func (q Querier) Params(goCtx context.Context, _ *types.QueryParamsRequest) (*types.QueryParamsResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	return &types.QueryParamsResponse{Params: q.GetParams(ctx)}, nil
}

func (q Querier) GlobalState(goCtx context.Context, _ *types.QueryGlobalStateRequest) (*types.QueryGlobalStateResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	return &types.QueryGlobalStateResponse{
		State:      q.GetGlobalState(ctx),
		AtoxSupply: q.AtoxSupply(ctx),
	}, nil
}

// Account is what wallets read to show a holder's position. `claimable` is the
// figure to display as convertible-to-ATOS: `pending` alone understates it,
// since the span since the last settlement has real value that just has not been
// booked yet.
func (q Querier) Account(goCtx context.Context, req *types.QueryAccountRequest) (*types.QueryAccountResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}
	addr, err := sdk.AccAddressFromBech32(req.Address)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	pending, unsettled := q.Claimable(ctx, addr)

	return &types.QueryAccountResponse{
		Account:     q.GetAtoxAccount(ctx, addr),
		AtoxBalance: q.AtoxBalance(ctx, addr),
		Unsettled:   unsettled,
		Claimable:   pending.Add(unsettled),
	}, nil
}

// ExchangePool exposes the pool balance next to what is owed so solvency can be
// checked from outside against the real bank balance, rather than by trusting the
// module's own running totals.
func (q Querier) ExchangePool(goCtx context.Context, _ *types.QueryExchangePoolRequest) (*types.QueryExchangePoolResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	return &types.QueryExchangePoolResponse{
		Address:     q.ExchangePoolAddress().String(),
		Balance:     q.ExchangePoolBalance(ctx),
		Outstanding: q.GetGlobalState(ctx).TotalPending,
	}, nil
}
