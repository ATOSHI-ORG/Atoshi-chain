package keeper

import (
	"context"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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

// Limits resolves the caps in force right now and how much of each is left.
//
// Both the ceiling and the remaining figure are returned. A client that can only
// read params sees the ceiling, which looks the same on an untouched day as on a
// nearly exhausted one -- the usual reason a working rate limit gets reported as
// broken.
func (q Querier) Limits(goCtx context.Context, req *types.QueryLimitsRequest) (*types.QueryLimitsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}
	ctx := sdk.UnwrapSDKContext(goCtx)

	l := q.Keeper.Limits(ctx)
	rl := q.GetRateLimitState(ctx)

	sub := func(cap, used math.Int) math.Int {
		if cap.IsNil() || !cap.IsPositive() {
			return math.ZeroInt()
		}
		if used.IsNil() {
			used = math.ZeroInt()
		}
		r := cap.Sub(used)
		if r.IsNegative() {
			return math.ZeroInt()
		}
		return r
	}

	return &types.QueryLimitsResponse{
		OutboundGlobalCap:       l.Global,
		OutboundGlobalRemaining: sub(l.Global, rl.Used),
		OutboundLargeBudget:     l.LargeBudget,
		OutboundLargeRemaining:  sub(l.LargeBudget, rl.UsedLarge),
		PerAddressCap:           l.PerAddress,
		SmallTransferThreshold:  l.SmallThreshold,
		MinTransferOut:          l.MinTransfer,
		CrisisMode:              l.CrisisMode,
		InboundCap:              l.Inbound,
		InboundRemaining:        sub(l.Inbound, rl.UsedInbound),
		ResetsAtUnix:            (types.DayOf(ctx.BlockTime().Unix()) + 1) * types.SecondsPerDay,
		MigrationPoolBalance:    q.tokenomicsKeeper.MigrationPoolBalance(ctx),
		// Reported even when nothing is enforced: the figures above stay
		// meaningful (they are what the limits would be), and a UI needs this to
		// avoid drawing a quota bar for a cap nobody is applying.
		RateLimitsDisabled: l.Disabled,
	}, nil
}

// RateLimitState returns the raw daily counters.
func (q Querier) RateLimitState(goCtx context.Context, req *types.QueryRateLimitStateRequest) (*types.QueryRateLimitStateResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}
	ctx := sdk.UnwrapSDKContext(goCtx)
	return &types.QueryRateLimitStateResponse{State: q.GetRateLimitState(ctx)}, nil
}

// AddressUsage is one address's outbound usage today and what it has left.
func (q Querier) AddressUsage(goCtx context.Context, req *types.QueryAddressUsageRequest) (*types.QueryAddressUsageResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}
	if _, err := sdk.AccAddressFromBech32(req.Address); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	ctx := sdk.UnwrapSDKContext(goCtx)

	used := q.GetAddressUsage(ctx, req.Address)
	cap := q.Keeper.Limits(ctx).PerAddress

	remaining := math.ZeroInt()
	if cap.IsPositive() && cap.GT(used) {
		remaining = cap.Sub(used)
	}
	return &types.QueryAddressUsageResponse{Used: used, Remaining: remaining}, nil
}
