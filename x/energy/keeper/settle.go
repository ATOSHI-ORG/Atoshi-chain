package keeper

import (
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	atoshitypes "github.com/atoshi-chain/atoshi/v20/types"

	"github.com/atoshi-chain/atoshi/v20/x/energy/types"
)

// Settle brings the account's stored energy state up to the current block
// time. It MUST be called before any read/write that depends on accrued
// energy. The function is idempotent (settling twice in the same block
// is a no-op) and never refunds energy.
//
// Algorithm (simplified):
//  1. elapsed = now - last_updated_time
//  2. Refill TxEnergyAccrued at the rate implied by `last_balance_snapshot`
//     up to TxEnergyCapacity(last_balance_snapshot).
//  3. Refill DeployEnergyAccrued via deployAddOverElapsed
//     (multiplies before dividing for sub-second precision) up to
//     DeployEnergyCapacity (constant).
//  4. last_updated_time := now.
//
// We deliberately do NOT touch last_balance_snapshot here. Balance changes
// are recorded by OnBalanceChange (called from the bank send hook), which
// first calls Settle (closing the previous epoch with the OLD balance),
// then writes the new balance.
func (k Keeper) Settle(ctx sdk.Context, addr sdk.AccAddress) types.EnergyAccount {
	acct := k.GetEnergyAccount(ctx, addr)
	now := ctx.BlockTime().Unix()
	if acct.LastUpdatedTime == 0 {
		// First touch: initialize the snapshot to current eligible balance.
		//
		// The staked term is cached alongside it. This is the path that covers
		// the genesis validators: the staking hooks deliberately no-op at
		// height 0, so the first time anything touches such an account this is
		// where its 100M self-delegation gets recorded. Without the cache write
		// the very next transfer would read staked_snapshot as zero and drop it
		// back out of the snapshot.
		acct.LastBalanceSnapshot = k.EligibleBalance(ctx, addr)
		acct.StakedSnapshot = k.stakedAtos(ctx, addr)
		acct.LastUpdatedTime = now
		k.SetEnergyAccount(ctx, acct)
		return acct
	}
	if now <= acct.LastUpdatedTime {
		return acct
	}

	params := k.GetParams(ctx)
	// The `now <= acct.LastUpdatedTime` guard above makes the subtraction
	// positive, so the conversion cannot wrap. Worth stating: if it could, a
	// timestamp in the future would turn into a near-infinite `elapsed` and mint
	// the account a full energy bar every block.
	elapsed := uint64(now - acct.LastUpdatedTime) //nolint:gosec // G115: guarded above

	// --- TxEnergy refill ---
	// Compute (capacity * elapsed / window) to avoid sub-second
	// integer-division underflow (50000 / 86400 == 0 in uint64).
	txCap := types.TxEnergyCapacity(acct.LastBalanceSnapshot, params)
	if txCap > 0 && acct.TxEnergyAccrued < txCap && params.TxEnergyMaxAccrueWindow > 0 {
		add := saturatingMul(elapsed, txCap) / uint64(params.TxEnergyMaxAccrueWindow)
		newAccrued := saturatingAdd(acct.TxEnergyAccrued, add)
		if newAccrued > txCap {
			newAccrued = txCap
		}
		acct.TxEnergyAccrued = newAccrued
	}

	// --- DeployEnergy refill ---
	// Same rationale as TxEnergy: a per-second rate rounds to 0
	// below ~5x threshold. Compute as (units * cap * elapsed) / (days
	// * 86400) to keep precision; the saturatingMul calls inside
	// deployAddOverElapsed guard against uint64 overflow on
	// pathologically retuned params.
	if acct.DeployEnergyAccrued < params.DeployEnergyCapacity {
		add := deployAddOverElapsed(acct.LastBalanceSnapshot, elapsed, params)
		if add > 0 {
			newAccrued := saturatingAdd(acct.DeployEnergyAccrued, add)
			if newAccrued > params.DeployEnergyCapacity {
				newAccrued = params.DeployEnergyCapacity
			}
			acct.DeployEnergyAccrued = newAccrued
		}
	}

	acct.LastUpdatedTime = now
	k.SetEnergyAccount(ctx, acct)
	return acct
}

// ApplyBalanceChange is the snapshot-update primitive used by
// SendRestriction. It accepts the post-change
// eligible balance explicitly rather than reading it from bank, so the
// bank send restriction (which runs before the bank write) can pass the
// projected post-send amount and still produce a correct snapshot.
//
// A negative `newEligible` is clamped to zero — this lets callers freely
// subtract amounts without worrying about underflow when an account is
// drained to zero in the same tx.
func (k Keeper) ApplyBalanceChange(ctx sdk.Context, addr sdk.AccAddress, newEligible math.Int) {
	if newEligible.IsNil() || newEligible.IsNegative() {
		newEligible = math.ZeroInt()
	}

	acct := k.Settle(ctx, addr)

	acct.LastBalanceSnapshot = newEligible
	acct.LastUpdatedTime = ctx.BlockTime().Unix()

	params := k.GetParams(ctx)
	newTxCap := types.TxEnergyCapacity(newEligible, params)
	// Audit Issue 6: cap-down must NOT cut into DelegatedOut.
	//
	// ownAvail in consume.go is TxEnergyAccrued − DelegatedOut. DelegatedOut
	// is energy already promised to other accounts: the delegatee's
	// DelegatedInUsable was minted at delegation time and may have been
	// spent. If a holder partially sells stake and the new cap drops
	// below DelegatedOut, setting TxEnergyAccrued = newTxCap collapses the
	// gap and would let the holder later under-credit undelegation
	// (delegation.go subtracts DelegatedOut symmetrically with
	// TxEnergyAccrued). Floor the cap-down at DelegatedOut. The economic
	// intent of cap-down (deny a giant buffer to a now-poorer account) is
	// preserved — only the holder's OWN unused portion is cut. The
	// delegated portion can still be reclaimed via undelegation, which
	// shrinks BOTH counters in lockstep.
	effectiveCap := newTxCap
	if effectiveCap < acct.DelegatedOut {
		effectiveCap = acct.DelegatedOut
	}
	if acct.TxEnergyAccrued > effectiveCap {
		acct.TxEnergyAccrued = effectiveCap
	}
	if acct.DeployEnergyAccrued > params.DeployEnergyCapacity {
		acct.DeployEnergyAccrued = params.DeployEnergyCapacity
	}

	k.SetEnergyAccount(ctx, acct)
}

// ApplyStakedChange is ApplyBalanceChange for the staking hooks: it recomputes
// the snapshot around a new staked total and caches that total on the account.
//
// It exists as its own primitive rather than "EligibleBalance, then
// ApplyBalanceChange, then write the cache" because that sequence reads the
// delegations twice and writes the account three times. Redelegation fires the
// hook for both the source and the destination validator, and inside the EVM
// staking precompile the whole call runs against a fixed gas budget -- the
// naive version ran it out ("VmError: out of gas" across
// precompiles/staking's redelegation suite). One delegation read, one
// read-modify-write.
func (k Keeper) ApplyStakedChange(ctx sdk.Context, addr sdk.AccAddress, staked math.Int) {
	if staked.IsNil() || staked.IsNegative() {
		staked = math.ZeroInt()
	}

	acct := k.Settle(ctx, addr)

	newEligible := k.bankKeeper.GetBalance(ctx, addr, k.BaseDenom()).Amount.
		Add(k.bankKeeper.GetBalance(ctx, addr, atoshitypes.AtoxBaseDenom).Amount).
		Add(staked)
	if !acct.LockedAtos.IsNil() && acct.LockedAtos.IsPositive() {
		newEligible = newEligible.Add(acct.LockedAtos)
	}

	acct.LastBalanceSnapshot = newEligible
	acct.StakedSnapshot = staked
	acct.LastUpdatedTime = ctx.BlockTime().Unix()

	// Same cap-down rule as ApplyBalanceChange, and for the same reason
	// (audit Issue 6): never cut into DelegatedOut.
	params := k.GetParams(ctx)
	effectiveCap := types.TxEnergyCapacity(newEligible, params)
	if effectiveCap < acct.DelegatedOut {
		effectiveCap = acct.DelegatedOut
	}
	if acct.TxEnergyAccrued > effectiveCap {
		acct.TxEnergyAccrued = effectiveCap
	}
	if acct.DeployEnergyAccrued > params.DeployEnergyCapacity {
		acct.DeployEnergyAccrued = params.DeployEnergyCapacity
	}

	k.SetEnergyAccount(ctx, acct)
}

// SimulateSettle returns the settled account without persisting it.
// Used by Query.Account so external callers see live numbers without
// having to broadcast a tx first.
func (k Keeper) SimulateSettle(ctx sdk.Context, addr sdk.AccAddress) types.EnergyAccount {
	acct := k.GetEnergyAccount(ctx, addr)
	now := ctx.BlockTime().Unix()
	if acct.LastUpdatedTime == 0 || now <= acct.LastUpdatedTime {
		return acct
	}
	params := k.GetParams(ctx)
	// Same guard as Settle: the early return above covers now <= LastUpdatedTime.
	elapsed := uint64(now - acct.LastUpdatedTime) //nolint:gosec // G115: guarded above

	txCap := types.TxEnergyCapacity(acct.LastBalanceSnapshot, params)
	if txCap > 0 && acct.TxEnergyAccrued < txCap && params.TxEnergyMaxAccrueWindow > 0 {
		add := saturatingMul(elapsed, txCap) / uint64(params.TxEnergyMaxAccrueWindow)
		acct.TxEnergyAccrued = minU64(saturatingAdd(acct.TxEnergyAccrued, add), txCap)
	}
	if acct.DeployEnergyAccrued < params.DeployEnergyCapacity {
		add := deployAddOverElapsed(acct.LastBalanceSnapshot, elapsed, params)
		if add > 0 {
			acct.DeployEnergyAccrued = minU64(saturatingAdd(acct.DeployEnergyAccrued, add), params.DeployEnergyCapacity)
		}
	}
	return acct
}

// CurrentTxCapacity returns the TxEnergy ceiling for the addr right now.
// Used by Query.Account; does not mutate state.
func (k Keeper) CurrentTxCapacity(ctx sdk.Context, addr sdk.AccAddress) uint64 {
	return types.TxEnergyCapacity(k.EligibleBalance(ctx, addr), k.GetParams(ctx))
}

// deployAddOverElapsed returns the DeployEnergy delta over `elapsed`
// seconds for an account snapshotted at `eligibleBalance`. Multiplies
// before dividing to keep precision when the per-second rate would
// truncate to 0 (e.g. exactly 1M ATOS holding).
func deployAddOverElapsed(eligibleBalance math.Int, elapsed uint64, p types.Params) uint64 {
	if eligibleBalance.IsNil() || !eligibleBalance.IsPositive() ||
		p.DeployHoldingThreshold.IsNil() || !p.DeployHoldingThreshold.IsPositive() {
		return 0
	}
	units := eligibleBalance.Quo(p.DeployHoldingThreshold)
	if units.IsZero() || !units.IsUint64() {
		if !units.IsZero() {
			return maxU64
		}
		return 0
	}
	denom := uint64(p.DeployRecoverDays) * 86_400
	if denom == 0 {
		return 0
	}
	// (units * capacity * elapsed) / denom
	num := saturatingMul(saturatingMul(units.Uint64(), p.DeployEnergyCapacity), elapsed)
	return num / denom
}

// ----- helpers (saturating arithmetic) -----

const maxU64 = ^uint64(0)

func saturatingAdd(a, b uint64) uint64 {
	if a > maxU64-b {
		return maxU64
	}
	return a + b
}

func saturatingMul(a, b uint64) uint64 {
	if a == 0 || b == 0 {
		return 0
	}
	if a > maxU64/b {
		return maxU64
	}
	return a * b
}

func minU64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

// safeIntFromUint guards against negative or overflowing math.Int → uint64.
func safeIntToUint64(i math.Int) uint64 {
	if i.IsNil() || !i.IsPositive() {
		return 0
	}
	if !i.IsUint64() {
		return maxU64
	}
	return i.Uint64()
}
