package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"

	tokenomicstypes "github.com/atoshi-chain/atoshi/v20/x/tokenomics/types"
)

type queryServer struct {
	Keeper
}

func NewQueryServerImpl(keeper Keeper) tokenomicstypes.QueryServer {
	return &queryServer{Keeper: keeper}
}

var _ tokenomicstypes.QueryServer = queryServer{}

func (q queryServer) Params(goCtx context.Context, _ *tokenomicstypes.QueryParamsRequest) (*tokenomicstypes.QueryParamsResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	return &tokenomicstypes.QueryParamsResponse{Params: q.GetParams(ctx)}, nil
}

func (q queryServer) MinerLockedBalance(goCtx context.Context, req *tokenomicstypes.QueryMinerLockedBalanceRequest) (*tokenomicstypes.QueryMinerLockedBalanceResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	return &tokenomicstypes.QueryMinerLockedBalanceResponse{Balance: q.GetMinerLockedBalance(ctx, req.ValidatorAddress)}, nil
}

func (q queryServer) ReleaseStatus(goCtx context.Context, _ *tokenomicstypes.QueryReleaseStatusRequest) (*tokenomicstypes.QueryReleaseStatusResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	return &tokenomicstypes.QueryReleaseStatusResponse{State: q.GetReleaseState(ctx)}, nil
}

func (q queryServer) CirculatingSupply(goCtx context.Context, _ *tokenomicstypes.QueryCirculatingSupplyRequest) (*tokenomicstypes.QueryCirculatingSupplyResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	return &tokenomicstypes.QueryCirculatingSupplyResponse{CirculatingSupply: q.GetCirculatingSupply(ctx)}, nil
}

func (q queryServer) BlockReward(goCtx context.Context, _ *tokenomicstypes.QueryBlockRewardRequest) (*tokenomicstypes.QueryBlockRewardResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	state := q.GetBlockRewardState(ctx)
	return &tokenomicstypes.QueryBlockRewardResponse{
		CurrentReward: q.GetCurrentBlockReward(ctx),
		Period:        state.CurrentPeriod,
	}, nil
}

func (q queryServer) ProjectClaimable(goCtx context.Context, _ *tokenomicstypes.QueryProjectClaimableRequest) (*tokenomicstypes.QueryProjectClaimableResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	return &tokenomicstypes.QueryProjectClaimableResponse{Claimable: q.GetProjectClaimable(ctx)}, nil
}
