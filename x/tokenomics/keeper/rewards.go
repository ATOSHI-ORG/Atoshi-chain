package keeper

import (
	"fmt"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	tokenomicstypes "github.com/atoshi-chain/atoshi/v20/x/tokenomics/types"
)

// BeginBlocker emits the per-block mining reward.
//
// The reward is ATOX, not ATOS. It is minted into the fee collector so
// x/distribution splits it across the active validator set and their delegators
// by commission, on exactly the path transaction fees take — which means
// delegators receive mining rewards without any new distribution logic here.
// The consequence for clients is that rewards are now multi-denom: ATOS from
// fees, ATOX from mining, withdrawn together.
//
// The ATOS that backs ATOX stays in the miner pool and only moves into the
// x/atox conversion pool on a tier release, once the matching ERC20 has landed
// in the Ethereum bridge vault. Nothing here spends ATOS.
func (k Keeper) BeginBlocker(ctx sdk.Context) error {
	if k.atoxKeeper == nil {
		return nil
	}

	params := k.GetParams(ctx)
	blockRewardState := k.GetBlockRewardState(ctx)

	currentReward := k.GetCurrentBlockReward(ctx)
	if currentReward.IsZero() {
		return nil
	}

	// Clamp against the ATOX cap held by x/atox, which is the authoritative
	// ceiling and is immutable there. Clamping rather than letting MintAtox
	// reject matters: an error here propagates into FinalizeBlock, so once the
	// cap were reached every block on every node would fail and the chain would
	// halt. Emission must simply stop.
	supply := k.atoxKeeper.AtoxSupply(ctx)
	cap := k.atoxKeeper.AtoxSupplyCap(ctx)
	if remaining := cap.Sub(supply); remaining.LT(currentReward) {
		if !remaining.IsPositive() {
			return nil
		}
		currentReward = remaining
	}

	// Audit Issue-18 (round2): check totalBonded BEFORE any state mutation. With
	// nothing bonded, x/distribution has no validator to allocate to and would
	// sweep the whole reward into the community pool — a module account, which
	// never accrues a conversion claim, so the ATOX would be stranded there. This
	// is reachable during the genesis bootstrap window before the first validator
	// bonds.
	totalBonded, err := k.stakingKeeper.TotalBondedTokens(ctx)
	if err != nil {
		return err
	}
	if totalBonded.IsZero() {
		return nil
	}

	if err := k.atoxKeeper.MintAtoxToModule(ctx, k.feeCollectorName, currentReward); err != nil {
		return err
	}

	// TotalDistributed counts ATOX emitted. ReleaseState.TotalImmediateDistributed
	// is deliberately NOT touched: it feeds GetCirculatingSupply, which is an ATOS
	// figure and the basis for tier release quotas. Adding ATOX amounts to it
	// would inflate circulating supply and so inflate every future release.
	blockRewardState.TotalDistributed = blockRewardState.TotalDistributed.Add(currentReward)
	blockRewardState.CurrentPeriod = uint64(ctx.BlockHeight() / params.HalvingIntervalBlocks)
	if err := k.SetBlockRewardState(ctx, blockRewardState); err != nil {
		return err
	}

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			tokenomicstypes.EventTypeDistributeBlockReward,
			sdk.NewAttribute(tokenomicstypes.AttributeKeyAmount, currentReward.String()),
		),
	)

	return nil
}

// EndBlocker performs daily price checks and triggers release windows.
func (k Keeper) EndBlocker(ctx sdk.Context) error {
	params := k.GetParams(ctx)
	state := k.GetReleaseState(ctx)

	// Only evaluate once per configured epoch.
	if state.LastCheckBlock > 0 && ctx.BlockHeight()-state.LastCheckBlock < params.PriceCheckEpochBlocks {
		return nil
	}

	priceData, err := k.oracleKeeper.GetCurrentPrice(ctx)
	if err != nil {
		k.Logger(ctx).Error("failed to get oracle price", "err", err)
		state.LastCheckBlock = ctx.BlockHeight()
		return k.SetReleaseState(ctx, state)
	}

	// Audit Issue 3: reject stale oracle data. Previously the tier
	// engine consumed whatever GetCurrentPrice returned, even if no
	// feeder had reported in days. A malicious or absent feeder could
	// have left a high-tier price persistently in the store, causing
	// ConsecutiveDays to keep climbing and eventually trigger an
	// undeserved miner/project release. Cross-check the price age
	// against oracle.params.MaxPriceAgeSeconds; if stale, pause the
	// streak (do NOT increment, do NOT reset — we treat staleness as
	// "no signal" rather than "negative signal", so a brief feeder
	// outage doesn't kill a legitimate ongoing streak).
	oracleParams := k.oracleKeeper.GetParams(ctx)
	now := ctx.BlockTime().Unix()
	if priceData.Timestamp == 0 ||
		(oracleParams.MaxPriceAgeSeconds > 0 &&
			uint64(now-priceData.Timestamp) > oracleParams.MaxPriceAgeSeconds) {
		k.Logger(ctx).Info("oracle price stale; skipping tier check",
			"price_timestamp", priceData.Timestamp,
			"now", now,
			"max_age", oracleParams.MaxPriceAgeSeconds)
		state.LastCheckBlock = ctx.BlockHeight()
		state.LastCheckTimeUnix = now
		return k.SetReleaseState(ctx, state)
	}

	requiredPrice := params.PriceBase.Mul(params.TierMultiplier.Power(uint64(state.CurrentTier)))
	requiredVolume := params.VolumeBase.Mul(params.TierMultiplier.Power(uint64(state.CurrentTier)))

	if priceData.Price.GTE(requiredPrice) && priceData.Volume24h.GTE(requiredVolume) {
		state.ConsecutiveDays++
	} else {
		state.ConsecutiveDays = 0
	}

	state.LastCheckBlock = ctx.BlockHeight()
	state.LastCheckTimeUnix = ctx.BlockTime().Unix()

	if state.ConsecutiveDays >= params.ConsecutiveDaysRequired {
		if err := k.TriggerRelease(ctx, &state, params); err != nil {
			return err
		}
		state.ConsecutiveDays = 0
		state.CurrentTier++
	}

	return k.SetReleaseState(ctx, state)
}

// GetCurrentBlockReward returns the current block reward after halvings.
func (k Keeper) GetCurrentBlockReward(ctx sdk.Context) math.Int {
	params := k.GetParams(ctx)
	if params.HalvingIntervalBlocks <= 0 {
		return math.ZeroInt()
	}
	period := ctx.BlockHeight() / params.HalvingIntervalBlocks
	reward := params.InitialBlockReward
	for i := int64(0); i < period; i++ {
		reward = reward.QuoRaw(2)
		if reward.IsZero() {
			break
		}
	}
	return reward
}

// TriggerRelease unlocks a new tranche from miner locked pool and project pool.
func (k Keeper) TriggerRelease(ctx sdk.Context, state *tokenomicstypes.ReleaseState, params tokenomicstypes.Params) error {
	circulating := k.GetCirculatingSupply(ctx)
	if !circulating.IsPositive() {
		return nil
	}

	releaseQuota := circulating.MulRaw(int64(params.ReleasePercentageBps)).QuoRaw(10000)
	minerTarget := releaseQuota.MulRaw(int64(params.MinerReleaseShareBps)).QuoRaw(10000)
	projectTarget := releaseQuota.Sub(minerTarget)

	actualMinerRelease, err := k.releaseMinerShareToExchangePool(ctx, minerTarget)
	if err != nil {
		return err
	}
	actualProjectRelease := projectTarget
	if actualMinerRelease.LT(minerTarget) {
		actualProjectRelease = actualProjectRelease.Add(minerTarget.Sub(actualMinerRelease))
	}

	projectClaimable := k.GetProjectClaimable(ctx)
	projectPoolAddr := k.accountKeeper.GetModuleAddress(tokenomicstypes.ProjectPoolName)
	projectPoolBalance := k.bankKeeper.GetBalance(ctx, projectPoolAddr, k.baseDenom()).Amount
	remainingProjectCapacity := projectPoolBalance.Sub(projectClaimable)
	if remainingProjectCapacity.IsNegative() {
		remainingProjectCapacity = math.ZeroInt()
	}
	if actualProjectRelease.GT(remainingProjectCapacity) {
		actualProjectRelease = remainingProjectCapacity
	}
	k.SetProjectClaimable(ctx, projectClaimable.Add(actualProjectRelease))

	state.TotalMinerReleased = state.TotalMinerReleased.Add(actualMinerRelease)
	state.TotalProjectReleased = state.TotalProjectReleased.Add(actualProjectRelease)

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			tokenomicstypes.EventTypeReleaseTriggered,
			sdk.NewAttribute(tokenomicstypes.AttributeKeyTier, fmt.Sprintf("%d", state.CurrentTier)),
			sdk.NewAttribute(tokenomicstypes.AttributeKeyConsecutiveDays, fmt.Sprintf("%d", state.ConsecutiveDays)),
			sdk.NewAttribute("miner_release", actualMinerRelease.String()),
			sdk.NewAttribute("project_release", actualProjectRelease.String()),
		),
	)

	return nil
}

// releaseMinerShareToExchangePool moves the miner share of a tier release into
// the x/atox conversion pool, which is what raises the ATOX -> ATOS rate.
//
// This replaces the old per-validator locked-balance accounting. Under the ATOX
// model the miner share is not owed to specific validators: it backs every ATOX
// holder pro rata, and x/atox's index is what apportions it. Moving the ATOS and
// advancing the index happen in one call there, so the pool balance and what the
// index promises cannot diverge.
//
// The transfer is capped by the miner pool's actual balance rather than trusted
// from the quota, so a mis-specified release percentage can never overdraw it.
func (k Keeper) releaseMinerShareToExchangePool(ctx sdk.Context, target math.Int) (math.Int, error) {
	if !target.IsPositive() || k.atoxKeeper == nil {
		return math.ZeroInt(), nil
	}

	poolAddr := k.accountKeeper.GetModuleAddress(tokenomicstypes.MinerPoolName)
	available := k.bankKeeper.GetBalance(ctx, poolAddr, k.baseDenom()).Amount
	if !available.IsPositive() {
		return math.ZeroInt(), nil
	}

	amount := target
	if amount.GT(available) {
		amount = available
	}

	if err := k.atoxKeeper.AddToExchangePool(ctx, tokenomicstypes.MinerPoolName, amount); err != nil {
		return math.ZeroInt(), err
	}
	return amount, nil
}

// ReleaseMinerLockedRewards distributes newly unlocked miner rewards
// proportionally to existing locked balances.
//
// Deprecated: unused since block rewards became ATOX. The locked-balance table is
// no longer fed by BeginBlocker, so this always finds nothing to release. Kept
// only until the MinerLockedBalance machinery is removed wholesale.
//
// Audit Recommendation-2 (round2): return any SetMinerLockedBalance
// error to the caller instead of panicking. The caller (TriggerRelease
// → EndBlocker) already propagates errors up the FinalizeBlock chain,
// so a failure here becomes a regular block error rather than a chain
// halt.
func (k Keeper) ReleaseMinerLockedRewards(ctx sdk.Context, target math.Int) (math.Int, error) {
	if !target.IsPositive() {
		return math.ZeroInt(), nil
	}

	totalLockedRemaining := math.ZeroInt()
	var balances []tokenomicstypes.MinerLockedBalance
	k.IterateMinerLockedBalances(ctx, func(balance tokenomicstypes.MinerLockedBalance) bool {
		remaining := balance.LockedAccrued.Sub(balance.LockedClaimed).Sub(balance.LockedClaimable)
		if remaining.IsPositive() {
			totalLockedRemaining = totalLockedRemaining.Add(remaining)
			balances = append(balances, balance)
		}
		return false
	})

	if !totalLockedRemaining.IsPositive() {
		return math.ZeroInt(), nil
	}

	actual := target
	if target.GT(totalLockedRemaining) {
		actual = totalLockedRemaining
	}

	remainingToAssign := actual
	for i, bal := range balances {
		remaining := bal.LockedAccrued.Sub(bal.LockedClaimed).Sub(bal.LockedClaimable)
		share := actual.Mul(remaining).Quo(totalLockedRemaining)
		if i == len(balances)-1 {
			share = remainingToAssign
		}
		if share.GT(remaining) {
			share = remaining
		}
		if share.IsPositive() {
			bal.LockedClaimable = bal.LockedClaimable.Add(share)
			if err := k.SetMinerLockedBalance(ctx, bal); err != nil {
				return math.ZeroInt(), fmt.Errorf("set miner locked balance %q: %w", bal.ValidatorAddress, err)
			}
			remainingToAssign = remainingToAssign.Sub(share)
		}
	}

	return actual.Sub(remainingToAssign), nil
}

// GetCirculatingSupply = migration pool total + immediate miner rewards + unlocked miner rewards + unlocked project rewards.
func (k Keeper) GetCirculatingSupply(ctx sdk.Context) math.Int {
	params := k.GetParams(ctx)
	state := k.GetReleaseState(ctx)
	return params.MigrationPoolTotal.
		Add(state.TotalImmediateDistributed).
		Add(state.TotalMinerReleased).
		Add(state.TotalProjectReleased)
}
