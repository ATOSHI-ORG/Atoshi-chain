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

	// TotalDistributed counts ATOX emitted, and nothing else. It must never feed
	// GetCirculatingSupply, which is an ATOS figure and the basis for tier release
	// quotas: adding ATOX amounts there would inflate circulating supply and so
	// inflate every future release.
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

// secondsPerDay is the UTC day length used to bucket samples. Deliberately a
// plain constant and not a parameter: the release rule is specified in calendar
// days, so a configurable "day" would silently rescale the 30-day streak.
const secondsPerDay = 86400

// EndBlocker samples the oracle and settles the streak at UTC day boundaries.
//
// # The rule
//
// The feeder takes params.DailySamples readings at random instants during each
// UTC day. If ANY of them clears both the price and volume thresholds, the day
// counts towards the streak; otherwise the streak resets to zero. Once the
// streak reaches params.ConsecutiveDaysRequired, a release fires.
//
// # Why the feeder picks the instants
//
// The chain records outcomes, it does not choose when to look. A chain-chosen
// instant is a publicly known block height, so anyone wanting the release to
// fire would know exactly when the price has to hold and could arrange for it
// to hold only then. Randomising off-chain makes that unprofitable, at the cost
// of trusting the feeder about *when* -- which the sample cap and the spacing
// floor below bound.
//
// # Why the sample count is capped
//
// Under ANY-of-N semantics more samples is strictly more permissive: each one is
// another chance for the threshold to be met. An unbounded feeder reporting
// every block would turn "the price held on three random checks" into "the price
// touched the threshold at least once today". So only the first DailySamples
// readings of each day are consumed, and PriceCheckEpochBlocks enforces a
// minimum gap between them so they cannot all land in the same few seconds.
//
// # What changed from the previous behaviour
//
// This used to evaluate once every PriceCheckEpochBlocks against whatever the
// latest report happened to be. That is a different rule -- it samples at a
// fixed, publicly known height -- and it had no concept of a day at all.
func (k Keeper) EndBlocker(ctx sdk.Context) error {
	params := k.GetParams(ctx)
	state := k.GetReleaseState(ctx)

	now := ctx.BlockTime().Unix()
	if now <= 0 {
		// Genesis blocks can carry a zero time; bucketing by it would put every
		// sample in day 0 and settle the day at the first real block.
		return nil
	}
	today := now / secondsPerDay

	// ----- settle the previous day, if it has rolled over -----
	if state.CurrentSampleDay != 0 && today > state.CurrentSampleDay {
		// A gap of more than one day means no sample landed on those days. That
		// is a failed streak, not a paused one: the rule asks for CONSECUTIVE
		// days, and a day nobody measured is a day that did not qualify.
		//
		// Note this differs from the staleness handling below, which pauses
		// rather than resets. The distinction: a stale reading within a day is
		// "no signal yet, the day is not over"; a whole day with no qualifying
		// sample is a definite negative.
		if state.DayQualified {
			state.ConsecutiveDays++
		} else {
			state.ConsecutiveDays = 0
		}

		if state.ConsecutiveDays >= params.ConsecutiveDaysRequired {
			if err := k.TriggerRelease(ctx, &state, params); err != nil {
				return err
			}
			state.ConsecutiveDays = 0
			state.CurrentTier++
		}

		ctx.EventManager().EmitEvent(sdk.NewEvent(
			tokenomicstypes.EventTypeDailyCheck,
			sdk.NewAttribute(tokenomicstypes.AttributeKeyDay, fmt.Sprintf("%d", state.CurrentSampleDay)),
			sdk.NewAttribute(tokenomicstypes.AttributeKeyQualified, fmt.Sprintf("%t", state.DayQualified)),
			sdk.NewAttribute(tokenomicstypes.AttributeKeySamples, fmt.Sprintf("%d", state.SamplesToday)),
			sdk.NewAttribute(tokenomicstypes.AttributeKeyConsecutiveDays, fmt.Sprintf("%d", state.ConsecutiveDays)),
		))

		state.DayQualified = false
		state.SamplesToday = 0
	}
	state.CurrentSampleDay = today

	// ----- take a sample, if today's quota allows -----
	dailySamples := params.DailySamples
	if dailySamples <= 0 {
		dailySamples = tokenomicstypes.DefaultDailySamples
	}
	if int64(state.SamplesToday) >= dailySamples {
		return k.SetReleaseState(ctx, state)
	}

	// Spacing floor: keeps the day's samples from clustering.
	if state.LastSampleBlock > 0 &&
		ctx.BlockHeight()-state.LastSampleBlock < params.PriceCheckEpochBlocks {
		return k.SetReleaseState(ctx, state)
	}

	priceData, err := k.oracleKeeper.GetCurrentPrice(ctx)
	if err != nil {
		// No price at all (nobody has ever reported). Not a sample, and not a
		// failure -- the day can still qualify if a feeder shows up later.
		return k.SetReleaseState(ctx, state)
	}

	// A report is consumed as a sample exactly once. Without this the same
	// reading would be re-counted on every subsequent block until a new report
	// arrived, spending the whole day's quota on one price.
	if priceData.Timestamp <= state.LastSampledPriceTime {
		return k.SetReleaseState(ctx, state)
	}

	// Audit Issue 3: reject stale oracle data. The tier engine used to consume
	// whatever GetCurrentPrice returned even if no feeder had reported in days,
	// so an absent or malicious feeder could leave a high price in the store and
	// let the streak climb on a reading nobody stood behind.
	//
	// Staleness pauses rather than resets: it means "no signal", not "negative
	// signal", so a brief feeder outage does not consume a sample or kill a
	// legitimate streak. The day-rollover branch above is what turns a genuinely
	// missed day into a reset.
	oracleParams := k.oracleKeeper.GetParams(ctx)
	if priceData.Timestamp == 0 ||
		(oracleParams.MaxPriceAgeSeconds > 0 &&
			uint64(now-priceData.Timestamp) > oracleParams.MaxPriceAgeSeconds) {
		k.Logger(ctx).Info("oracle price stale; not sampling",
			"price_timestamp", priceData.Timestamp,
			"now", now,
			"max_age", oracleParams.MaxPriceAgeSeconds)
		return k.SetReleaseState(ctx, state)
	}

	requiredPrice := params.PriceBase.Mul(params.TierMultiplier.Power(uint64(state.CurrentTier)))
	requiredVolume := params.VolumeBase.Mul(params.TierMultiplier.Power(uint64(state.CurrentTier)))
	sampleOk := priceData.Price.GTE(requiredPrice) && priceData.Volume24h.GTE(requiredVolume)

	state.SamplesToday++
	state.LastSampledPriceTime = priceData.Timestamp
	state.LastSampleBlock = ctx.BlockHeight()
	state.LastCheckBlock = ctx.BlockHeight()
	state.LastCheckTimeUnix = now
	if sampleOk {
		// ANY-of-N: one qualifying sample settles the day. Later samples cannot
		// take it back, which is the whole point of the rule -- the price only
		// has to clear the bar once per day, not hold it all day.
		state.DayQualified = true
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		tokenomicstypes.EventTypeDailySample,
		sdk.NewAttribute(tokenomicstypes.AttributeKeyDay, fmt.Sprintf("%d", today)),
		sdk.NewAttribute(tokenomicstypes.AttributeKeySamples, fmt.Sprintf("%d", state.SamplesToday)),
		sdk.NewAttribute(tokenomicstypes.AttributeKeyPrice, priceData.Price.String()),
		sdk.NewAttribute(tokenomicstypes.AttributeKeyVolume, priceData.Volume24h.String()),
		sdk.NewAttribute(tokenomicstypes.AttributeKeySampleOk, fmt.Sprintf("%t", sampleOk)),
	))

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

	// Record only. The ATOS behind this authorisation does not move here: it is
	// released by x/bridgeadapter once Ethereum confirms, by Hyperlane receipt,
	// that the matching ERC20 has landed in the bridge vault.
	//
	// Releasing here instead would let holders convert ATOX into ATOS that
	// nothing backs, for as long as the Ethereum side lagged or failed — which is
	// the exact failure the receipt round-trip exists to prevent. The authorised
	// figure is capped to the miner pool's balance so it can never authorise more
	// ATOS than exists to release.
	actualMinerRelease, err := k.authorizeMinerRelease(ctx, minerTarget)
	if err != nil {
		return err
	}
	actualProjectRelease := projectTarget
	if actualMinerRelease.LT(minerTarget) {
		actualProjectRelease = actualProjectRelease.Add(minerTarget.Sub(actualMinerRelease))
	}

	// ProjectClaimable is deliberately NOT credited here. Per the design doc
	// §3.4 step 1 only books the authorisation; ProjectClaimable is set in step 4
	// from the Ethereum receipt (x/bridgeadapter applyReceipt). Crediting it in
	// both places double-counts every release -- invisible today only because the
	// forward dispatch did not exist, so no receipt ever arrived.
	//
	// KNOWN GAP, needs review: the capacity cap below still measures headroom
	// against ProjectClaimable, which now lags this authorisation by one round
	// trip. Between step 1 and step 4 the cap cannot see the in-flight
	// authorisation, so consecutive tier releases could over-authorise by up to
	// the in-flight amount. Bounded by one release (release_percentage_bps of
	// circulating supply), and the ATOX side is still protected by the
	// ERC20-lands-first invariant, but the accounting wants a pending-authorisation
	// counter rather than this proxy.
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

	state.TotalMinerReleased = state.TotalMinerReleased.Add(actualMinerRelease)
	state.TotalProjectReleased = state.TotalProjectReleased.Add(actualProjectRelease)

	// Step 1 of the round trip: tell Ethereum the new cumulative targets. Failure
	// is logged, not propagated: the amounts are cumulative, so the next release
	// carries the full target and repairs a missed dispatch, whereas returning an
	// error here would abort the EndBlocker and stall the chain on a bridge that
	// is merely unconfigured or paused.
	if k.tierDispatcher != nil {
		if _, err := k.tierDispatcher.DispatchTierRelease(
			ctx, state.TotalMinerReleased, state.TotalProjectReleased,
		); err != nil {
			k.Logger(ctx).Error("tier release dispatch failed; next release will resend the cumulative target",
				"error", err,
				"cumulative_miner", state.TotalMinerReleased.String(),
				"cumulative_project", state.TotalProjectReleased.String(),
			)
		}
	}

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

// authorizeMinerRelease records how much of the miner pool a tier judgment has
// authorised for release, without moving any ATOS.
//
// Under the ATOX model the miner share is not owed to specific validators — it
// backs every ATOX holder pro rata, and x/atox's index apportions it — so there
// is no per-validator accounting to do here. What remains is to cap the
// authorisation at the pool's actual balance, so a mis-specified release
// percentage can never authorise more ATOS than exists.
//
// x/bridgeadapter does the moving, when Ethereum confirms the matching ERC20.
func (k Keeper) authorizeMinerRelease(ctx sdk.Context, target math.Int) (math.Int, error) {
	if !target.IsPositive() {
		return math.ZeroInt(), nil
	}

	poolAddr := k.accountKeeper.GetModuleAddress(tokenomicstypes.MinerPoolName)
	available := k.bankKeeper.GetBalance(ctx, poolAddr, k.baseDenom()).Amount
	if !available.IsPositive() {
		return math.ZeroInt(), nil
	}

	if target.GT(available) {
		return available, nil
	}
	return target, nil
}

// AuthorizedReleases returns the cumulative miner and project shares that tier
// judgments have authorised, in ATOS. x/bridgeadapter reads these to reject a
// receipt claiming more than the chain ever authorised.
func (k Keeper) AuthorizedReleases(ctx sdk.Context) (miner, project math.Int) {
	state := k.GetReleaseState(ctx)
	return state.TotalMinerReleased, state.TotalProjectReleased
}

// MinerPoolName is the module account holding the ATOS that backs ATOX.
func (k Keeper) MinerPoolName() string { return tokenomicstypes.MinerPoolName }

// GetCirculatingSupply is the ATOS actually in circulation: the migration pool
// plus everything tier releases have authorised.
//
// Block rewards contribute nothing — they are ATOX, and ATOX is not ATOS. The
// old immediate-reward term is gone with the field it read.
func (k Keeper) GetCirculatingSupply(ctx sdk.Context) math.Int {
	params := k.GetParams(ctx)
	state := k.GetReleaseState(ctx)
	return params.MigrationPoolTotal.
		Add(state.TotalMinerReleased).
		Add(state.TotalProjectReleased)
}

// MigrationPoolName is the module account the asset bridge locks ATOS into and
// releases from. It is the bridge's counterparty: neither ATOS nor the ERC20 can
// be minted, so outbound locks here and inbound pays out of the same balance.
func (k Keeper) MigrationPoolName() string { return tokenomicstypes.MigrationPoolName }

// MigrationPoolBalance is the pool's live balance, which bounds both the daily
// outbound cap and what an inbound transfer can be paid from.
func (k Keeper) MigrationPoolBalance(ctx sdk.Context) math.Int {
	addr := k.accountKeeper.GetModuleAddress(tokenomicstypes.MigrationPoolName)
	return k.bankKeeper.GetBalance(ctx, addr, k.baseDenom()).Amount
}

// MigrationPoolTotal is the pool's configured size, the denominator for the
// bridge's crisis-mode floor.
func (k Keeper) MigrationPoolTotal(ctx sdk.Context) math.Int {
	return k.GetParams(ctx).MigrationPoolTotal
}

// BaseDenom is the ATOS denom.
func (k Keeper) BaseDenom() string { return k.baseDenom() }
