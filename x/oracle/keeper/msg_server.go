package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/atoshi-chain/atoshi/v20/x/oracle/types"
)

// withinDeviation returns true if |newPrice − prev| / prev × 10000 ≤ maxBps.
// Both inputs are 18-decimal LegacyDec; the comparison is exact.
func withinDeviation(prev, next math.LegacyDec, maxBps uint32) bool {
	if !prev.IsPositive() {
		// No baseline → no deviation cap can apply.
		return true
	}
	diff := next.Sub(prev).Abs()
	// diff_bps = diff / prev * 10000
	bps := diff.MulInt64(10000).Quo(prev)
	cap := math.LegacyNewDec(int64(maxBps))
	return bps.LTE(cap)
}

// hasEnoughFreshFeeders reports whether the recent price history
// contains reports from at least params.MinValidReports DISTINCT
// allow-listed feeders within MaxPriceAgeSeconds. Used to gate
// promotion to current-price when MinValidReports > 1.
func (k Keeper) hasEnoughFreshFeeders(ctx sdk.Context, params types.Params) bool {
	if params.MinValidReports <= 1 {
		return true
	}
	now := ctx.BlockTime().Unix()
	cutoff := now - int64(params.MaxPriceAgeSeconds)
	if params.MaxPriceAgeSeconds == 0 {
		// No staleness rule defined; degrade to "any history counts".
		cutoff = 0
	}
	history := k.GetPricesSince(ctx, cutoff)
	seen := make(map[string]struct{}, len(history))
	for _, pd := range history {
		seen[pd.Feeder] = struct{}{}
	}
	return uint32(len(seen)) >= params.MinValidReports
}

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

	// Audit Issue 4 (a): enforce MaxPriceDeviationBps against the
	// current on-chain price. Previously the parameter was defined but
	// not consulted — a single feeder could report any value, including
	// one 1000x off-market, and it would be accepted. Now we reject
	// reports whose price moves more than the allowed bps from the
	// existing current price. If there is no current price yet (first
	// report on a freshly-started chain), skip the check.
	if params.MaxPriceDeviationBps > 0 {
		if cur, err := k.GetCurrentPrice(ctx); err == nil &&
			!cur.Price.IsNil() && cur.Price.IsPositive() {
			if !withinDeviation(cur.Price, msg.Price, params.MaxPriceDeviationBps) {
				return nil, types.ErrPriceDeviationTooHigh.Wrapf(
					"prev=%s new=%s allowed_bps=%d",
					cur.Price, msg.Price, params.MaxPriceDeviationBps)
			}
		}
	}

	priceData := types.PriceData{
		Price:     msg.Price,
		Volume24h: msg.Volume24h,
		Timestamp: ctx.BlockTime().Unix(),
		Feeder:    msg.Feeder,
		Source:    msg.Source,
	}

	// Always persist the report to history so downstream consensus
	// computations (TWAP, MinValidReports check) have data to read.
	if err := k.AppendPriceHistory(ctx, priceData); err != nil {
		return nil, err
	}

	// Audit Issue 4 (b): only promote to "current price" once we have
	// at least MinValidReports distinct feeders reporting within the
	// MaxPriceAgeSeconds window. Until then, the chain has no
	// authoritative single price — current remains at the previous
	// value (which downstream code already cross-checks against
	// MaxPriceAgeSeconds via the Issue-3 fix).
	if params.MinValidReports <= 1 || k.hasEnoughFreshFeeders(ctx, params) {
		if err := k.SetCurrentPrice(ctx, priceData); err != nil {
			return nil, err
		}
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
