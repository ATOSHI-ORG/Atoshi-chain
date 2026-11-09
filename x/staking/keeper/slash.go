package keeper

import (
	"context"
	"errors"
	"fmt"

	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/staking/types"

	tokenomicstypes "github.com/atoshi-chain/atoshi/v20/x/tokenomics/types"
)

// Event emitted when a penalty is confiscated. Named apart from x/slashing's
// "slash" event on purpose: that one reports what was decided, this one reports
// what was taken and where it went.
const (
	EventTypeSlashConfiscated = "slash_confiscated"

	AttributeKeyValidator  = "validator"
	AttributeKeyPenalty    = "penalty"
	AttributeKeyAmount     = "amount"
	AttributeKeySourcePool = "source_pool"
)

// Slash confiscates a penalty from the validator's own stake and moves it to
// slash_pool. It replaces the SDK implementation, which does neither.
//
// # Why delegators stop paying for it
//
// The SDK slashes by lowering validator.Tokens and leaving DelegatorShares
// alone (x/staking/keeper/slash.go, RemoveValidatorTokens). Shares are a claim
// on a pool that just shrank, so every delegator loses value without any of
// their records being touched. On 2026-10-18 two downtime slashes at 1% took
// 41,965,283.22 ATOS from 1,841 delegators who had done nothing; the support
// tickets said "my staked balance dropped" and the chain state was, strictly,
// correct.
//
// This implementation uses Unbond instead. Unbond removes shares and tokens
// together through RemoveValidatorTokensAndShares, so the tokens-per-share
// ratio is unchanged and every other delegation is worth exactly what it was
// worth a block earlier. That is the property to test for; it is also why the
// penalty is capped at what the validator itself has staked. A validator with
// too little self-stake to cover the penalty pays everything it has and no
// more -- the alternative is reaching into delegations, which is the behaviour
// being removed.
//
// The cap is not the real deterrent. ValidatorMinSelfDelegation is 1e26 (100M
// ATOS), the ante handler holds every validator above it, and a 0.01% downtime
// penalty against that floor is 10,000 ATOS -- three orders of magnitude inside
// the cap. The cap exists for the degenerate case, not the normal one.
//
// # Why the proceeds are confiscated rather than burned
//
// Burning destroyed supply that is pegged 100:1 against a fixed ERC20 total, so
// returning it meant minting against that peg, which is the one thing the
// tokenomics module is built to refuse. Moving it into a module account with no
// Minter permission and no spend path except MsgSpendSlashPool keeps supply and
// peg intact, and makes "confiscated so far" and "returned so far" two balances
// anyone can read instead of a burn nobody can reverse.
//
// # What is deliberately not reproduced from the SDK
//
// Unbonding delegations and redelegations are not touched. The SDK walks them
// so that stake which left the validator after the infraction height still pays
// its share; here only the validator pays, and the validator's own stake is
// held above the floor by the ante handler for as long as it is active. Unbond
// also jails a validator whose self-delegation falls below MinSelfDelegation,
// which is a sharper consequence than the dilution it replaces.
//
// Returns the amount confiscated, which is what the slashing module logs.
func (k Keeper) Slash(
	ctx context.Context,
	consAddr sdk.ConsAddress,
	infractionHeight, power int64,
	slashFactor math.LegacyDec,
) (math.Int, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	logger := k.Logger(ctx)

	if slashFactor.IsNegative() {
		return math.ZeroInt(), fmt.Errorf("attempted to slash with a negative slash factor: %v", slashFactor)
	}
	if infractionHeight > sdkCtx.BlockHeight() {
		return math.ZeroInt(), fmt.Errorf(
			"impossible attempt to slash future infraction at height %d but we are at height %d",
			infractionHeight, sdkCtx.BlockHeight())
	}

	validator, err := k.GetValidatorByConsAddr(ctx, consAddr)
	if errors.Is(err, types.ErrNoValidatorFound) {
		// Matches the SDK: an overslashed validator is removed, and a later
		// evidence item for it is not an error. Logged at error level there and
		// here, because it is still worth knowing about.
		logger.Error(
			"WARNING: ignored attempt to slash a nonexistent validator; we recommend you investigate immediately",
			"cons_addr", consAddr.String(),
		)
		return math.ZeroInt(), nil
	} else if err != nil {
		return math.ZeroInt(), err
	}
	if validator.IsUnbonded() {
		return math.ZeroInt(), fmt.Errorf("should not be slashing unbonded validator: %s", validator.GetOperator())
	}

	valAddr, err := k.ValidatorAddressCodec().StringToBytes(validator.GetOperator())
	if err != nil {
		return math.ZeroInt(), err
	}
	// On this chain an operator address and its account address share the same
	// 20 bytes, so the self-delegation is keyed by the operator bytes read as an
	// account. Same assumption the SDK's own self-delegation check makes.
	selfDelegator := sdk.AccAddress(valAddr)

	// Read the status before unbonding. Unbond moves no coins between the bonded
	// and not-bonded pools -- its caller normally does that -- so the penalty has
	// to be taken out of whichever pool the stake is sitting in now. A validator
	// jailed by the Unbond below is still bonded until EndBlock moves it.
	sourcePool := types.BondedPoolName
	if validator.IsUnbonding() {
		sourcePool = types.NotBondedPoolName
	}

	// Same figure the SDK would have destroyed: the stake behind the validator's
	// consensus power at the infraction height, times the factor.
	penalty := math.LegacyNewDecFromInt(k.TokensFromConsensusPower(ctx, power)).Mul(slashFactor).TruncateInt()

	delegation, err := k.GetDelegation(ctx, selfDelegator, valAddr)
	if errors.Is(err, types.ErrNoDelegation) {
		// Possible only outside the ante handler's reach -- a genesis validator
		// with no self-delegation. Nothing to confiscate, and reaching past it
		// into delegations is the behaviour this function exists to remove.
		logger.Error(
			"validator has no self-delegation to slash; penalty not applied",
			"validator", validator.GetOperator(),
			"penalty", penalty.String(),
		)
		return math.ZeroInt(), nil
	} else if err != nil {
		return math.ZeroInt(), err
	}

	selfStake := validator.TokensFromShares(delegation.Shares).TruncateInt()
	confiscate := math.MinInt(penalty, selfStake)
	if !confiscate.IsPositive() {
		logger.Info(
			"no validator slashing because slash amount is zero",
			"validator", validator.GetOperator(),
			"slash_factor", slashFactor.String(),
		)
		return math.ZeroInt(), nil
	}
	if confiscate.LT(penalty) {
		// Not an error, but it means the penalty was blunted. Worth an entry
		// because it should not happen while the self-delegation floor holds.
		logger.Error(
			"penalty exceeds validator self-stake; confiscating the whole self-stake",
			"validator", validator.GetOperator(),
			"penalty", penalty.String(),
			"self_stake", selfStake.String(),
		)
	}

	shares, err := k.ValidateUnbondAmount(ctx, selfDelegator, valAddr, confiscate)
	if err != nil {
		return math.ZeroInt(), err
	}
	// Unbond handles the rest: it withdraws the validator's pending rewards
	// through BeforeDelegationSharesModified, removes shares and tokens in the
	// same ratio, jails the validator if this drops it below MinSelfDelegation,
	// and deletes the delegation if nothing is left.
	confiscated, err := k.Unbond(ctx, selfDelegator, valAddr, shares)
	if err != nil {
		return math.ZeroInt(), err
	}
	if !confiscated.IsPositive() {
		return math.ZeroInt(), nil
	}

	bondDenom, err := k.BondDenom(ctx)
	if err != nil {
		return math.ZeroInt(), err
	}
	coins := sdk.NewCoins(sdk.NewCoin(bondDenom, confiscated))
	if err := k.bk.SendCoinsFromModuleToModule(
		ctx, sourcePool, tokenomicstypes.SlashPoolName, coins,
	); err != nil {
		// The shares are already gone at this point. Returning the error unwinds
		// the whole message -- the slashing module's caller is in BeginBlock, so
		// the alternative is stake removed from a validator with no matching
		// credit in the pool, which would leave the staking pools short.
		return math.ZeroInt(), err
	}

	logger.Info(
		"validator self-stake confiscated to slash pool",
		"validator", validator.GetOperator(),
		"slash_factor", slashFactor.String(),
		"confiscated", confiscated.String(),
		"source_pool", sourcePool,
	)
	// Distinct from x/slashing's own "slash" event, which still fires and still
	// reports the reason and the factor. This one answers the question that event
	// cannot: how much actually left the validator, and where it went. They will
	// disagree whenever the cap above bites, and that disagreement is the signal.
	sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
		EventTypeSlashConfiscated,
		sdk.NewAttribute(AttributeKeyValidator, validator.GetOperator()),
		sdk.NewAttribute(AttributeKeyPenalty, penalty.String()),
		sdk.NewAttribute(AttributeKeyAmount, confiscated.String()),
		sdk.NewAttribute(AttributeKeySourcePool, sourcePool),
	))

	return confiscated, nil
}

// SlashWithInfractionReason is the entry point x/evidence uses for double signs,
// and x/slashing for downtime.
//
// It has to be overridden alongside Slash and not merely inherited: the embedded
// SDK keeper's copy calls the SDK's Slash, not this one, because Go method sets
// do not dispatch virtually. Overriding only Slash would leave every double-sign
// penalty on the old path, diluting delegators exactly as before, and nothing
// about the resulting behaviour would look like a missing override.
func (k Keeper) SlashWithInfractionReason(
	ctx context.Context,
	consAddr sdk.ConsAddress,
	infractionHeight, power int64,
	slashFactor math.LegacyDec,
	_ types.Infraction,
) (math.Int, error) {
	return k.Slash(ctx, consAddr, infractionHeight, power, slashFactor)
}
