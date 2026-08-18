package keeper

import (
	"fmt"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	tokenomicstypes "github.com/atoshi-chain/atoshi/v20/x/tokenomics/types"
)

// BeginBlocker handles per-block miner reward distribution.
// 20% is immediate (counted into circulating supply tracking), 80% is locked to validators by voting power.
func (k Keeper) BeginBlocker(ctx sdk.Context) error {
	params := k.GetParams(ctx)
	blockRewardState := k.GetBlockRewardState(ctx)
	releaseState := k.GetReleaseState(ctx)

	currentReward := k.GetCurrentBlockReward(ctx)
	if currentReward.IsZero() {
		return nil
	}

	// Ensure we do not exceed the miner pool.
	if blockRewardState.TotalDistributed.Add(currentReward).GT(params.MinerPoolTotal) {
		remaining := params.MinerPoolTotal.Sub(blockRewardState.TotalDistributed)
		if !remaining.IsPositive() {
			return nil
		}
		currentReward = remaining
	}

	// Audit Issue-18 (round2): check totalBonded BEFORE any state
	// mutation. The pre-fix code transferred the immediate share to
	// the fee collector and updated releaseState IN MEMORY, then
	// looked up totalBonded; if it was zero (genesis bootstrap before
	// any validator bonds, or a degenerate scenario where all
	// validators got unbonded), the function returned at line 54
	// without calling SetBlockRewardState/SetReleaseState. Effect:
	//   - bank moved liao from MinerPool to FeeCollector (committed),
	//   - releaseState.TotalImmediateDistributed never persisted,
	//   - blockRewardState.TotalDistributed never persisted.
	// Next block, currentReward repeats the SAME amount (TotalDistributed
	// hasn't advanced), bank gets another chunk of coins, and the
	// tokenomics accounting silently drifts further from bank reality.
	// Over enough blocks the immediate share would over-distribute
	// without the supply cap (MinerPoolTotal) catching up.
	//
	// Moving the check up front means: if no validator can receive
	// locked rewards, we skip the whole block — no bank send, no
	// releaseState bump, no drift. This is a safe no-op for chains
	// with no bonded validators (which shouldn't be producing blocks
	// at all under normal CometBFT consensus, but the audit's concern
	// is the genesis bootstrap window where the first block may be
	// processed before any validator bond is recorded).
	totalBonded, err := k.stakingKeeper.TotalBondedTokens(ctx)
	if err != nil {
		return err
	}
	if totalBonded.IsZero() {
		return nil
	}

	immediate := currentReward.MulRaw(int64(params.ImmediateRewardBps)).QuoRaw(10000)
	locked := currentReward.Sub(immediate)

	if immediate.IsPositive() {
		coin := sdk.NewCoin(k.baseDenom(), immediate)
		if err := k.bankKeeper.SendCoinsFromModuleToModule(ctx, tokenomicstypes.MinerPoolName, k.feeCollectorName, sdk.NewCoins(coin)); err != nil {
			return err
		}
	}

	// Track immediate rewards globally as circulating supply.
	releaseState.TotalImmediateDistributed = releaseState.TotalImmediateDistributed.Add(immediate)
	releaseState.TotalMinerLocked = releaseState.TotalMinerLocked.Add(locked)

	// Audit Recommendation-2 (round2): a block handler must NOT panic
	// on a recoverable failure. cosmos-sdk runs BeginBlocker /
	// EndBlocker as part of every block's FinalizeBlock; a panic here
	// propagates up to baseapp and halts consensus across every node
	// hitting the same condition. The prior code panicked on
	// SetMinerLockedBalance errors — serialization issues, store
	// problems, schema mismatches — any of which would freeze the
	// chain until a coordinated binary fix and migration. We capture
	// the error into a shadowed variable and break out of the
	// validator iterator instead, returning it from BeginBlocker so
	// the FinalizeBlock pipeline can record and surface it as a
	// regular block error.
	remainingLocked := locked
	var setErr error
	err = k.stakingKeeper.IterateBondedValidatorsByPower(ctx, func(_ int64, validator stakingtypes.ValidatorI) bool {
		valPower := validator.GetTokens()
		if !valPower.IsPositive() {
			return false
		}

		share := locked.Mul(valPower).Quo(totalBonded)
		if share.GT(remainingLocked) {
			share = remainingLocked
		}
		if share.IsPositive() {
			bal := k.GetMinerLockedBalance(ctx, validator.GetOperator())
			bal.LockedAccrued = bal.LockedAccrued.Add(share)
			if err := k.SetMinerLockedBalance(ctx, bal); err != nil {
				setErr = fmt.Errorf("set miner locked balance %q: %w", validator.GetOperator(), err)
				return true // stop the iterator
			}
			remainingLocked = remainingLocked.Sub(share)
		}
		return false
	})
	if err != nil {
		return err
	}
	if setErr != nil {
		return setErr
	}

	blockRewardState.TotalDistributed = blockRewardState.TotalDistributed.Add(currentReward)
	blockRewardState.CurrentPeriod = uint64(ctx.BlockHeight() / params.HalvingIntervalBlocks)

	if err := k.SetBlockRewardState(ctx, blockRewardState); err != nil {
		return err
	}
	if err := k.SetReleaseState(ctx, releaseState); err != nil {
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

	actualMinerRelease, err := k.ReleaseMinerLockedRewards(ctx, minerTarget)
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

// ReleaseMinerLockedRewards distributes newly unlocked miner rewards
// proportionally to existing locked balances.
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
