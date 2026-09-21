package keeper

import (
	"context"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/atoshi-chain/atoshi/v20/x/energy/types"
)

type queryServer struct{ Keeper }

func NewQueryServerImpl(k Keeper) types.QueryServer { return &queryServer{Keeper: k} }

func (q queryServer) Params(goCtx context.Context, _ *types.QueryParamsRequest) (*types.QueryParamsResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	return &types.QueryParamsResponse{Params: q.GetParams(ctx)}, nil
}

func (q queryServer) Account(goCtx context.Context, req *types.QueryAccountRequest) (*types.QueryAccountResponse, error) {
	if req == nil || req.Address == "" {
		return nil, status.Error(codes.InvalidArgument, "address required")
	}
	ctx := sdk.UnwrapSDKContext(goCtx)
	addr, err := sdk.AccAddressFromBech32(req.Address)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "bad address: %v", err)
	}
	settled := q.SimulateSettle(ctx, addr)
	params := q.GetParams(ctx)
	return &types.QueryAccountResponse{
		Settled:              settled,
		TxEnergyCapacity:     types.TxEnergyCapacity(settled.LastBalanceSnapshot, params),
		DeployEnergyCapacity: params.DeployEnergyCapacity,
	}, nil
}

func (q queryServer) Delegations(goCtx context.Context, req *types.QueryDelegationsRequest) (*types.QueryDelegationsResponse, error) {
	if req == nil || req.Address == "" {
		return nil, status.Error(codes.InvalidArgument, "address required")
	}
	ctx := sdk.UnwrapSDKContext(goCtx)
	addr, err := sdk.AccAddressFromBech32(req.Address)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "bad address: %v", err)
	}
	resp := &types.QueryDelegationsResponse{}
	if req.Direction == "" || req.Direction == "all" || req.Direction == "out" {
		q.IterateDelegationsByDelegator(ctx, addr.String(), func(d types.EnergyDelegation) bool {
			resp.Outbound = append(resp.Outbound, d)
			return false
		})
	}
	if req.Direction == "" || req.Direction == "all" || req.Direction == "in" {
		q.IterateDelegationsByDelegatee(ctx, addr.String(), func(d types.EnergyDelegation) bool {
			resp.Inbound = append(resp.Inbound, d)
			return false
		})
	}
	return resp, nil
}

func (q queryServer) EstimateFee(goCtx context.Context, req *types.QueryEstimateFeeRequest) (*types.QueryEstimateFeeResponse, error) {
	if req == nil || req.Signer == "" {
		return nil, status.Error(codes.InvalidArgument, "signer required")
	}
	ctx := sdk.UnwrapSDKContext(goCtx)
	signer, err := sdk.AccAddressFromBech32(req.Signer)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "bad signer: %v", err)
	}
	r := q.EstimateConsume(ctx, signer, req.GasLimit, req.IsDeploy, nil)

	// Pick the gas price that real txs will actually pay for the
	// shortfall portion. The AnteHandler's `computeShortfallFee`
	// charges `max(offeredPerGas, InsufficientGasPrice) × shortfallGas`,
	// where offeredPerGas comes from the tx's declared fee. Wallets
	// broadcast txs offering `gasLimit × feemarket.min_gas_price`
	// (currently 1 gwei = 10^9 liao/gas), so the realistic estimate
	// is `min_gas_price × shortfallGas` — NOT
	// `InsufficientGasPrice × shortfallGas` (= 0.0021 × N ≈ 567 liao
	// for a typical transfer, which displays as ~5.67e-16 ATOS and
	// looks broken in the wallet UI).
	//
	// Fall back to InsufficientGasPrice only if the feemarket keeper
	// isn't wired or returns a non-positive rate (defensive — should
	// never happen in production with a genesis-set min_gas_price).
	fee := math.LegacyZeroDec()
	if r.ShortfallGas > 0 {
		gasPrice := math.LegacyZeroDec()
		if q.feemarketKeeper != nil {
			gasPrice = q.feemarketKeeper.GetMinGasPrice(ctx)
		}
		params := q.GetParams(ctx)
		if !gasPrice.IsPositive() && !params.InsufficientGasPrice.IsNil() && params.InsufficientGasPrice.IsPositive() {
			gasPrice = params.InsufficientGasPrice
		}
		if gasPrice.IsPositive() {
			fee = gasPrice.MulInt64(int64(r.ShortfallGas)).Ceil()
		}
	}
	return &types.QueryEstimateFeeResponse{
		EnergyUsed:   r.EnergyDeducted + r.DeployEnergyUsed,
		ShortfallGas: r.ShortfallGas,
		AtosFee:      fee,
		Free:         r.Free,
	}, nil
}
