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
//
// Audit Question 1: OwnDeducted / DelegatedDeducted / DelegationConsumptions
// were added so the PostHandler can perform a LIFO refund. Consume draws
// from the OWN bucket first, then the DELEGATED-IN pool; LIFO refund
// (reverse-order, see RefundEnergy) means we return to the
// borrowed-from-delegators pool BEFORE crediting the holder's own
// accrued energy. The delegator-friendly direction matters: their
// commitment has a deadline (ExpiresAt) and rolling unused energy back
// onto the original delegation keeps the time-bounded grant intact
// instead of effectively converting it into permanent own-energy on
// the delegatee's account.
type ConsumeResult struct {
	EnergyDeducted   uint64 // tx_energy + delegated drawn down (kept as sum for events / RPC compat)
	OwnDeducted      uint64 // portion drawn from the signer's own TxEnergyAccrued
	DelegatedDeducted uint64 // portion drawn from the inbound-delegation pool
	DeployEnergyUsed uint64 // deploy_energy drawn down (only when isDeploy)
	ShortfallGas     uint64 // gas the standard fee path must cover
	Free             bool   // msg type was on the subsidized whitelist
	// DelegationConsumptions records the in-order attribution made by
	// attributeDelegatedConsumption — one entry per active inbound
	// delegation that contributed. LIFO refund walks this slice
	// backwards (latest-consumed first) and undoes Used in lock-step.
	DelegationConsumptions []DelegationConsumption
}

// DelegationConsumption is one slice of energy charged to a specific
// inbound delegation. The PostHandler uses this to roll back Used
// when refunding unused gas.
type DelegationConsumption struct {
	DelegationID uint64
	Amount       uint64
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
		out.OwnDeducted += take
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
		out.DelegatedDeducted += take
		remaining -= take
		attrib, err := k.attributeDelegatedConsumption(ctx, signer, take)
		if err != nil {
			return out, err
		}
		out.DelegationConsumptions = attrib
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

// RefundEnergy returns up to `amount` units of previously-consumed
// energy to the signer. Called by the PostHandler with
// `reserved - actually_used` so that pre-charging by gas_limit doesn't
// penalize users whose txs consumed less than they reserved.
//
// Audit Question 1: refund uses LIFO order against the ConsumeResult
// bookkeeping captured at Consume time:
//
//  1. The DELEGATED-IN pool is refilled first, walking
//     res.DelegationConsumptions in REVERSE (last-consumed first) and
//     decrementing each delegation's Used in lock-step with the credit
//     applied to the delegatee's DelegatedInUsable cache.
//  2. Once the delegated portion is fully refunded, any remaining
//     refund goes to TxEnergyAccrued (own bucket), capped at the
//     current TxEnergyCapacity.
//
// Why LIFO? Inbound delegations have an ExpiresAt deadline; rolling
// unused energy back onto the same delegation keeps the time-bounded
// grant intact instead of silently converting it to permanent own
// energy on the delegatee's account.
//
// `amount` is clamped at res.OwnDeducted + res.DelegatedDeducted —
// callers should already pass a value <= that sum, but we defend
// against arithmetic drift.
func (k Keeper) RefundEnergy(ctx sdk.Context, signer sdk.AccAddress, amount uint64, res ConsumeResult) {
	if amount == 0 {
		return
	}
	maxRefund := saturatingAdd(res.OwnDeducted, res.DelegatedDeducted)
	if amount > maxRefund {
		amount = maxRefund
	}
	if amount == 0 {
		return
	}

	acct := k.GetEnergyAccount(ctx, signer)
	remaining := amount

	// Phase 1: delegated pool, LIFO.
	if remaining > 0 && res.DelegatedDeducted > 0 {
		delRefund := minU64(remaining, res.DelegatedDeducted)
		undone := uint64(0)
		for i := len(res.DelegationConsumptions) - 1; i >= 0 && undone < delRefund; i-- {
			c := res.DelegationConsumptions[i]
			d, ok := k.GetDelegation(ctx, c.DelegationID)
			if !ok {
				// Delegation was undelegated mid-tx (cleanup path). The
				// energy is gone; just skip — DelegatedInUsable was
				// already adjusted by undelegation in the same flow.
				continue
			}
			step := minU64(delRefund-undone, c.Amount)
			if step > d.Used {
				step = d.Used
			}
			if step == 0 {
				continue
			}
			d.Used -= step
			k.setDelegation(ctx, d)
			undone += step
		}
		// Credit the delegatee-side cache for the portion we successfully
		// rolled back. If some delegation was missing, `undone` may be
		// less than delRefund — only credit what we actually undid.
		acct.DelegatedInUsable = saturatingAdd(acct.DelegatedInUsable, undone)
		remaining -= undone
	}

	// Phase 2: own bucket, capped at current capacity.
	if remaining > 0 {
		params := k.GetParams(ctx)
		cap := types.TxEnergyCapacity(acct.LastBalanceSnapshot, params)
		acct.TxEnergyAccrued = minU64(saturatingAdd(acct.TxEnergyAccrued, remaining), cap)
	}

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
func (k Keeper) attributeDelegatedConsumption(ctx sdk.Context, delegatee sdk.AccAddress, amount uint64) ([]DelegationConsumption, error) {
	if amount == 0 {
		return nil, nil
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

	// Phase 3: consume in priority order. Record (id, take) in the same
	// order so the LIFO refund path can roll it back exactly.
	remaining := amount
	var toUpdate []types.EnergyDelegation
	var attribution []DelegationConsumption
	for _, d := range candidates {
		if remaining == 0 {
			break
		}
		avail := d.Amount - d.Used
		take := minU64(remaining, avail)
		d.Used += take
		remaining -= take
		toUpdate = append(toUpdate, d)
		attribution = append(attribution, DelegationConsumption{DelegationID: d.Id, Amount: take})
	}

	for _, d := range toUpdate {
		k.setDelegation(ctx, d)
	}
	if remaining > 0 {
		// Bookkeeping mismatch — DelegatedInUsable said we had more than
		// the index actually exposes. Fail loudly.
		return attribution, fmt.Errorf("delegated_in_usable accounting drift: %d unattributed", remaining)
	}
	return attribution, nil
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