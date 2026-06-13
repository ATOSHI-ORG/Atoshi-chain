package keeper

import (
	"fmt"
	"sort"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/atoshi-chain/atoshi/v20/x/energy/types"
)

// ConsumeResult describes how a tx's gas was paid: how much energy got
// burned, and how much remaining gas the caller still needs to charge
// in ATOS via the standard fee deduction. The AnteHandler is the only
// caller; tests also exercise this directly.
type ConsumeResult struct {
	EnergyDeducted    uint64 // tx_energy + delegated drawn down
	DeployEnergyUsed  uint64 // deploy_energy drawn down (only when isDeploy)
	ShortfallGas      uint64 // gas the standard fee path must cover
	Free              bool   // msg type was on the subsidized whitelist
}

// Consume draws energy from `signer` to cover `gasNeeded` units (typically
// tx.GetGas()). For deploy txs the deploy bucket is consulted first.
//
// Order of preference:
//  1. Subsidized whitelist → free = true, nothing consumed
//  2. Module disabled → free = false, all gas → ShortfallGas
//  3. is_deploy: drain DeployEnergyAccrued, then TxEnergyAccrued, then DelegatedInUsable
//  4. otherwise: drain TxEnergyAccrued, then DelegatedInUsable
//  5. Whatever isn't covered ends up in ShortfallGas.
//
// Returned ConsumeResult tells the AnteHandler to skip / partial / full
// fee deduction. State writes happen only when energy actually moves.
func (k Keeper) Consume(
	ctx sdk.Context,
	signer sdk.AccAddress,
	gasNeeded uint64,
	isDeploy bool,
	msgTypeUrls []string,
) (ConsumeResult, error) {
	params := k.GetParams(ctx)

	// Whitelisted msg types: free, no state mutation.
	if allSubsidized(params, msgTypeUrls) {
		return ConsumeResult{Free: true}, nil
	}
	if !params.EnergyEnabled {
		return ConsumeResult{ShortfallGas: gasNeeded}, nil
	}
	if gasNeeded == 0 {
		return ConsumeResult{}, nil
	}

	acct := k.Settle(ctx, signer)
	remaining := gasNeeded
	out := ConsumeResult{}

	// Deploy txs hit the deploy bucket first.
	if isDeploy && acct.DeployEnergyAccrued > 0 {
		take := minU64(remaining, acct.DeployEnergyAccrued)
		acct.DeployEnergyAccrued -= take
		out.DeployEnergyUsed = take
		remaining -= take
	}

	// Own TxEnergy (less anything currently delegated out — DelegatedOut
	// is bookkeeping; the actual lent energy has been added to the
	// delegatee's DelegatedInUsable, so subtracting here ensures we
	// don't double-spend).
	ownAvail := acct.TxEnergyAccrued
	if acct.DelegatedOut < ownAvail {
		ownAvail -= acct.DelegatedOut
	} else {
		ownAvail = 0
	}
	if remaining > 0 && ownAvail > 0 {
		take := minU64(remaining, ownAvail)
		acct.TxEnergyAccrued -= take
		out.EnergyDeducted += take
		remaining -= take
	}

	// Delegated-in pool. We do not attribute back to specific
	// EnergyDelegation records on the ante path — that would force a
	// linear scan every tx. Instead Consume just decrements the cached
	// DelegatedInUsable; a periodic reconciliation (or EndBlocker
	// sweep) attributes the burn proportionally to active inbound
	// delegations.
	if remaining > 0 && acct.DelegatedInUsable > 0 {
		take := minU64(remaining, acct.DelegatedInUsable)
		acct.DelegatedInUsable -= take
		out.EnergyDeducted += take
		remaining -= take
		if err := k.attributeDelegatedConsumption(ctx, signer, take); err != nil {
			return out, err
		}
	}

	if remaining > 0 {
		out.ShortfallGas = remaining
	}

	k.SetEnergyAccount(ctx, acct)

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeEnergyConsumed,
		sdk.NewAttribute(types.AttributeKeyAddress, signer.String()),
		sdk.NewAttribute(types.AttributeKeyEnergyUsed, fmt.Sprintf("%d", out.EnergyDeducted+out.DeployEnergyUsed)),
		sdk.NewAttribute(types.AttributeKeyShortfallGas, fmt.Sprintf("%d", out.ShortfallGas)),
	))

	return out, nil
}

// RefundEnergy returns previously-consumed TxEnergy to the signer's own
// bucket. Called by the PostHandler with `reserved - actually_used` so
// that pre-charging by gas_limit doesn't penalize users whose txs
// consumed less than they reserved.
//
// Refund only credits TxEnergyAccrued. We do not attempt to repay
// delegated-in energy (it is already accounted for in the pool) — the
// over-refund is functionally equivalent and avoids brittle bookkeeping.
func (k Keeper) RefundEnergy(ctx sdk.Context, signer sdk.AccAddress, amount uint64) {
	if amount == 0 {
		return
	}
	acct := k.GetEnergyAccount(ctx, signer)
	params := k.GetParams(ctx)
	cap := types.TxEnergyCapacity(acct.LastBalanceSnapshot, params)
	acct.TxEnergyAccrued = minU64(saturatingAdd(acct.TxEnergyAccrued, amount), cap)
	k.SetEnergyAccount(ctx, acct)
}

// EstimateConsume previews what Consume() would do without mutating state.
// Used by Query.EstimateFee and tests.
func (k Keeper) EstimateConsume(
	ctx sdk.Context, signer sdk.AccAddress, gasNeeded uint64, isDeploy bool, msgTypeUrls []string,
) ConsumeResult {
	params := k.GetParams(ctx)
	if allSubsidized(params, msgTypeUrls) {
		return ConsumeResult{Free: true}
	}
	if !params.EnergyEnabled {
		return ConsumeResult{ShortfallGas: gasNeeded}
	}
	acct := k.SimulateSettle(ctx, signer)
	remaining := gasNeeded
	res := ConsumeResult{}

	if isDeploy && acct.DeployEnergyAccrued > 0 {
		take := minU64(remaining, acct.DeployEnergyAccrued)
		res.DeployEnergyUsed = take
		remaining -= take
	}
	own := acct.TxEnergyAccrued
	if acct.DelegatedOut < own {
		own -= acct.DelegatedOut
	} else {
		own = 0
	}
	if remaining > 0 && own > 0 {
		take := minU64(remaining, own)
		res.EnergyDeducted += take
		remaining -= take
	}
	if remaining > 0 && acct.DelegatedInUsable > 0 {
		take := minU64(remaining, acct.DelegatedInUsable)
		res.EnergyDeducted += take
		remaining -= take
	}
	res.ShortfallGas = remaining
	return res
}

// attributeDelegatedConsumption attributes `amount` units of consumed
// energy to the delegatee's inbound delegations.
//
// Audit Issue 7: the prior version iterated delegations in id order
// (the natural order of the by-delegatee secondary index). Result:
// a delegation with id=5 that expires in 1 day could be consumed
// before a delegation with id=3 that expires in 30 minutes. The
// shorter-tenor delegated energy then expires unused while the
// longer-tenor one is consumed early — bad UX for both the delegator
// (their longer commitment is wasted first) and the delegatee (they
// don't extract maximum value from their soonest-expiring grants).
//
// The fix collects all inbound delegations, sorts by expires_at
// ascending, and consumes oldest-deadline-first. Tie-break on id to
// keep the order deterministic across nodes.
//
// If a delegation is fully used up, its index entries stay in place —
// the EndBlocker / Undelegate cleanup will remove them.
func (k Keeper) attributeDelegatedConsumption(ctx sdk.Context, delegatee sdk.AccAddress, amount uint64) error {
	if amount == 0 {
		return nil
	}

	// Phase 1: snapshot every inbound delegation with remaining capacity.
	var candidates []types.EnergyDelegation
	k.IterateDelegationsByDelegatee(ctx, delegatee.String(), func(d types.EnergyDelegation) bool {
		if d.Amount > d.Used {
			candidates = append(candidates, d)
		}
		return false
	})

	// Phase 2: sort by expires_at ascending (soonest first); tie-break
	// on id for determinism. sort.Slice is stable enough here because
	// id is unique per delegation.
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].ExpiresAt != candidates[j].ExpiresAt {
			return candidates[i].ExpiresAt < candidates[j].ExpiresAt
		}
		return candidates[i].Id < candidates[j].Id
	})

	// Phase 3: consume in priority order.
	remaining := amount
	var toUpdate []types.EnergyDelegation
	for _, d := range candidates {
		if remaining == 0 {
			break
		}
		avail := d.Amount - d.Used
		take := minU64(remaining, avail)
		d.Used += take
		remaining -= take
		toUpdate = append(toUpdate, d)
	}

	for _, d := range toUpdate {
		k.setDelegation(ctx, d)
	}
	if remaining > 0 {
		// Bookkeeping mismatch — DelegatedInUsable said we had more than
		// the index actually exposes. Fail loudly.
		return fmt.Errorf("delegated_in_usable accounting drift: %d unattributed", remaining)
	}
	return nil
}

func allSubsidized(p types.Params, urls []string) bool {
	if len(urls) == 0 {
		return false
	}
	for _, u := range urls {
		if !p.IsSubsidizedMsg(u) {
			return false
		}
	}
	return true
}