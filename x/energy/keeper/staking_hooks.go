package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"cosmossdk.io/math"
)

// Hooks makes x/energy observe x/staking.
//
// Why this exists: EligibleBalance counts staked ATOS (keeper.go), but the
// stored last_balance_snapshot is only ever refreshed by SendRestriction,
// and delegating never reaches SendRestriction at all -- the Evmos SDK's
// BaseKeeper.DelegateCoins writes through setBalance/addCoins directly and
// never calls the bank send-restriction chain.
//
// The consequence, observed on testnet account
// atoshi14dpp92vlaxlpp4ctve0082zqhgys9ydv5gz5nd on 2026-09-22: the holder
// staked 7,500 ATOS, which should have pushed eligibility from 22,518 to
// 30,018 and over the 30,000 threshold for the first 50,000 energy. Instead
// the next fee deduction ran SendRestriction, which rewrote the snapshot
// from bank balances alone, and the staked term vanished. Capacity stayed 0.
//
// These hooks refresh the snapshot from the full EligibleBalance whenever a
// delegation changes, and cache the staked term on the account so that
// SendRestriction can preserve it without paying for the delegation reads
// itself. Asking x/staking on the transfer path is what the original code was
// avoiding, with good reason: it pushes a plain MsgSend to 200_061 gas, past
// the 200_000 default. These hooks only fire on delegation writes, so they
// carry that cost where it is affordable.
type Hooks struct {
	k Keeper
}

var _ stakingtypes.StakingHooks = Hooks{}

// Hooks returns the staking hooks wired in app.go.
func (k Keeper) Hooks() Hooks {
	return Hooks{k: k}
}

// refresh recomputes and stores the delegator's eligibility snapshot.
//
// Skipped at height 0: genutil delivers the gentxs (and therefore the genesis
// self-delegations) before x/energy's own InitGenesis has written params, and
// an account created here would be built against DefaultParams. Genesis
// accounts do not need it -- Settle's first-touch path reads EligibleBalance
// in full, staked ATOS included.
func (h Hooks) refresh(ctx context.Context, delAddr sdk.AccAddress) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	if sdkCtx.BlockHeight() == 0 {
		return nil
	}
	if !h.k.GetParams(sdkCtx).EnergyEnabled {
		return nil
	}

	// Read the delegations once and hand the total to a single
	// read-modify-write. Doing this as EligibleBalance + ApplyBalanceChange +
	// a separate cache write reads x/staking twice and writes the account
	// three times, which redelegation through the EVM staking precompile
	// cannot afford -- it fires this hook for both validators inside one
	// fixed gas budget and ran out.
	h.k.ApplyStakedChange(sdkCtx, delAddr, h.k.stakedAtos(sdkCtx, delAddr))
	return nil
}

// AfterDelegationModified covers delegate, redelegate and partial undelegate:
// all three leave a delegation record behind and land here once shares are
// final.
func (h Hooks) AfterDelegationModified(ctx context.Context, delAddr sdk.AccAddress, _ sdk.ValAddress) error {
	return h.refresh(ctx, delAddr)
}

// BeforeDelegationRemoved covers the full undelegate, where the delegation
// record is deleted rather than modified so AfterDelegationModified never
// fires.
//
// It runs before the removal, so EligibleBalance here still sees the shares
// as bonded. That is not a staleness bug: undelegating moves the tokens from
// bonded to unbonding and stakedAtos sums both, so the eligibility total is
// unchanged by this transition either way. The call is here so the snapshot
// still picks up anything else that drifted, and so the account exists when
// the unbonding later completes.
func (h Hooks) BeforeDelegationRemoved(ctx context.Context, delAddr sdk.AccAddress, _ sdk.ValAddress) error {
	return h.refresh(ctx, delAddr)
}

// The rest of the interface is not interesting to energy eligibility.
//
// BeforeValidatorSlashed is deliberately a no-op: it names a validator, not
// its delegators, and enumerating them to refresh each snapshot would put an
// unbounded loop in the slashing path. A slashed delegator's snapshot stays
// slightly high until its next transfer or delegation, at which point
// SendRestriction's absolute recompute corrects it.

func (h Hooks) AfterValidatorCreated(context.Context, sdk.ValAddress) error { return nil }
func (h Hooks) BeforeValidatorModified(context.Context, sdk.ValAddress) error {
	return nil
}
func (h Hooks) AfterValidatorRemoved(context.Context, sdk.ConsAddress, sdk.ValAddress) error {
	return nil
}
func (h Hooks) AfterValidatorBonded(context.Context, sdk.ConsAddress, sdk.ValAddress) error {
	return nil
}
func (h Hooks) AfterValidatorBeginUnbonding(context.Context, sdk.ConsAddress, sdk.ValAddress) error {
	return nil
}
func (h Hooks) BeforeDelegationCreated(context.Context, sdk.AccAddress, sdk.ValAddress) error {
	return nil
}
func (h Hooks) BeforeDelegationSharesModified(context.Context, sdk.AccAddress, sdk.ValAddress) error {
	return nil
}
func (h Hooks) BeforeValidatorSlashed(context.Context, sdk.ValAddress, math.LegacyDec) error {
	return nil
}
func (h Hooks) AfterUnbondingInitiated(context.Context, uint64) error { return nil }
