package keeper

import (
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

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
//  3. Refill DeployEnergyAccrued at DeployRecoverPerSecond(last_balance_snapshot)
//     up to DeployEnergyCapacity (constant).
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
		acct.LastBalanceSnapshot = k.EligibleBalance(ctx, addr)
		acct.LastUpdatedTime = now
		k.SetEnergyAccount(ctx, acct)
		return acct
	}
	if now <= acct.LastUpdatedTime {
		return acct
	}

	params := k.GetParams(ctx)
	elapsed := uint64(now - acct.LastUpdatedTime)

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
	// Same rationale: DeployRecoverPerSecond rounds to 0 below ~5x
	// threshold. Recompute as (units * cap * elapsed / (days * 86400))
	// to keep precision.
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

// OnBalanceChange is called by the bank Send hook (and any other code
// that mutates an account's balance). It first settles using the OLD
// snapshot, then snaps the new eligible balance, then caps any over-
// capacity accrued energy down to the new ceiling.
//
// The cap-down step is deliberate: a holder that sells most of their
// stake should not retain a giant prefilled energy buffer. Otherwise
// rotating accounts could harvest energy with one wallet then move
// the ATOS to another wallet for free transactions everywhere.
func (k Keeper) OnBalanceChange(ctx sdk.Context, addr sdk.AccAddress) {
	// Step 1: close the old epoch.
	acct := k.Settle(ctx, addr)

	// Step 2: read fresh eligible balance.
	newEligible := k.EligibleBalance(ctx, addr)
	acct.LastBalanceSnapshot = newEligible
	acct.LastUpdatedTime = ctx.BlockTime().Unix()

	// Step 3: cap accrued to the new (possibly lower) capacity.
	params := k.GetParams(ctx)
	newTxCap := types.TxEnergyCapacity(newEligible, params)
	if acct.TxEnergyAccrued > newTxCap {
		acct.TxEnergyAccrued = newTxCap
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
	elapsed := uint64(now - acct.LastUpdatedTime)

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