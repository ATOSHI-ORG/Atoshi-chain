package keeper

import (
	"fmt"

	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/atoshi-chain/atoshi/v20/x/energy/types"
)

// Delegate locks ATOS, transfers it to the locked module account, and
// records an EnergyDelegation. The delegatee's account gets `amount`
// added to DelegatedInUsable so it can spend the borrowed energy
// alongside its own accrued energy. Returns the new delegation id and
// the amount of ATOS locked.
//
// Locked ATOS amount is computed as `amount / TxEnergyPerThreshold *
// TxEnergyHoldingThreshold` — i.e. you must lock the bank balance that
// would have backed the lent capacity. This prevents lending energy
// you do not actually back with stake (the TRON-style freeze model).
func (k Keeper) Delegate(
	ctx sdk.Context, delegator, delegatee sdk.AccAddress, amount uint64, durationSeconds int64,
) (uint64, math.Int, error) {
	if amount == 0 {
		return 0, math.ZeroInt(), types.ErrInvalidAmount
	}
	if durationSeconds <= 0 {
		return 0, math.ZeroInt(), types.ErrInvalidDuration
	}
	if delegator.Equals(delegatee) {
		return 0, math.ZeroInt(), types.ErrSelfDelegation
	}

	params := k.GetParams(ctx)

	// Lock ATOS = amount / per_threshold * threshold (rounded UP so that
	// fractional units still freeze a full block of ATOS).
	if params.TxEnergyPerThreshold == 0 || params.TxEnergyHoldingThreshold.IsZero() {
		return 0, math.ZeroInt(), fmt.Errorf("invalid params")
	}
	thresholdUnits := (amount + params.TxEnergyPerThreshold - 1) / params.TxEnergyPerThreshold
	if thresholdUnits == 0 {
		thresholdUnits = 1
	}
	lockedATOS := params.TxEnergyHoldingThreshold.MulRaw(int64(thresholdUnits))

	// Settle delegator first so their TxEnergyAccrued reflects the moment.
	delAcct := k.Settle(ctx, delegator)

	// Verify the delegator actually owns enough free energy to lend.
	freeEnergy := delAcct.TxEnergyAccrued
	if delAcct.DelegatedOut > freeEnergy {
		// defensive — should never happen
		freeEnergy = 0
	} else {
		freeEnergy -= delAcct.DelegatedOut
	}
	if freeEnergy < amount {
		return 0, math.ZeroInt(), types.ErrInsufficientEnergy
	}

	// Verify free bank balance covers the lock.
	//
	// Audit Issue 5: prior code computed freeBalance = bal - currentLocked
	// for the balance check. That double-counts the existing lock: when
	// a previous Delegate ran, lockedATOS was moved to the locked module
	// account via SendCoinsFromAccountToModule, so the live
	// bank.GetBalance already excludes it. Subtracting LockedAtos again
	// rejected legitimate follow-up delegations with ErrInsufficientBalance
	// even when the delegator had the funds on hand. The cumulative
	// LockedAtos tracking on the account is still needed for the per-
	// account total (used by EligibleBalance/SetEnergyAccount below) —
	// just not for the bank balance check.
	currentLocked := delAcct.LockedAtos
	if currentLocked.IsNil() {
		currentLocked = math.ZeroInt()
	}
	bal := k.bankKeeper.GetBalance(ctx, delegator, k.baseDenom).Amount
	if bal.LT(lockedATOS) {
		return 0, math.ZeroInt(), types.ErrInsufficientBalance
	}

	// Move the locked ATOS to the module account.
	if err := k.bankKeeper.SendCoinsFromAccountToModule(
		ctx, delegator, types.LockedEnergyPoolName, sdk.NewCoins(sdk.NewCoin(k.baseDenom, lockedATOS)),
	); err != nil {
		return 0, math.ZeroInt(), err
	}

	// Record on the delegator's account.
	delAcct.DelegatedOut += amount
	delAcct.LockedAtos = currentLocked.Add(lockedATOS)
	// Bank balance dropped: snapshot reflects the new eligible balance.
	delAcct.LastBalanceSnapshot = k.EligibleBalance(ctx, delegator)
	k.SetEnergyAccount(ctx, delAcct)

	// Settle delegatee then bump their usable inbound.
	deeAcct := k.Settle(ctx, delegatee)
	deeAcct.DelegatedInUsable = saturatingAdd(deeAcct.DelegatedInUsable, amount)
	k.SetEnergyAccount(ctx, deeAcct)

	// Allocate id and persist the delegation record.
	id := k.nextDelegationID(ctx)
	now := ctx.BlockTime().Unix()
	d := types.EnergyDelegation{
		Id:         id,
		Delegator:  delegator.String(),
		Delegatee:  delegatee.String(),
		Amount:     amount,
		LockedAtos: lockedATOS,
		StartTime:  now,
		ExpiresAt:  now + durationSeconds,
		Used:       0,
	}
	k.setDelegation(ctx, d)
	return id, lockedATOS, nil
}

// Undelegate releases an outstanding delegation early. Only callable by
// the original delegator. Unused energy is removed from the delegatee
// (capped at remaining), the delegator's DelegatedOut shrinks, and the
// locked ATOS is returned from the module account. Already-consumed
// energy stays consumed.
func (k Keeper) Undelegate(ctx sdk.Context, delegator sdk.AccAddress, id uint64) error {
	d, ok := k.GetDelegation(ctx, id)
	if !ok {
		return types.ErrDelegationNotFound
	}
	if d.Delegator != delegator.String() {
		return types.ErrUnauthorized
	}
	return k.releaseDelegation(ctx, d, types.EventTypeEnergyUndelegated)
}

// releaseDelegation is the shared implementation between Undelegate and
// the EndBlocker expiry sweep. It returns locked ATOS, decrements
// counters, removes the record, and emits an event.
func (k Keeper) releaseDelegation(ctx sdk.Context, d types.EnergyDelegation, eventType string) error {
	delegator, err := sdk.AccAddressFromBech32(d.Delegator)
	if err != nil {
		return err
	}
	delegatee, err := sdk.AccAddressFromBech32(d.Delegatee)
	if err != nil {
		return err
	}

	remaining := uint64(0)
	if d.Amount > d.Used {
		remaining = d.Amount - d.Used
	}

	// Refund locked ATOS to delegator.
	if d.LockedAtos.IsPositive() {
		if err := k.bankKeeper.SendCoinsFromModuleToAccount(
			ctx, types.LockedEnergyPoolName, delegator, sdk.NewCoins(sdk.NewCoin(k.baseDenom, d.LockedAtos)),
		); err != nil {
			return err
		}
	}

	// Update delegator account.
	delAcct := k.Settle(ctx, delegator)
	// Audit Issue-2 (round2): when a delegation is released, the energy
	// that the delegatee already CONSUMED (d.Used) must be deducted from
	// the delegator's TxEnergyAccrued.
	//
	// The pre-fix code only shrank DelegatedOut by d.Amount, leaving
	// TxEnergyAccrued untouched. But Consume() in consume.go computes
	// ownAvail = TxEnergyAccrued - DelegatedOut. Pre-undelegate that
	// equation correctly reserves the d.Used portion: DelegatedOut
	// covers it. Post-undelegate, DelegatedOut shrinks to zero but
	// TxEnergyAccrued still counts d.Used as own-available — so the
	// delegator can spend (or re-delegate) energy the delegatee has
	// already burned. Two calls' worth of energy out of one accrued
	// budget.
	//
	// A delegator that repeatedly Delegate/Undelegate against a
	// delegatee that consumes the borrowed energy each round can
	// multiply their effective energy budget without bound — this is
	// the "reuse consumed energy" exploit the auditor flagged.
	//
	// Floor at zero defensively. Under normal operation d.Used <=
	// d.Amount <= DelegatedOut <= TxEnergyAccrued (Consume() does not
	// let the delegatee burn more than it was lent, and Delegate()
	// rejects DelegatedOut > TxEnergyAccrued), so the clamp should
	// never trigger — but a stale-state path leaking under the floor
	// would silently re-open the same exploit, so we keep the guard.
	if d.Used > 0 {
		if delAcct.TxEnergyAccrued >= d.Used {
			delAcct.TxEnergyAccrued -= d.Used
		} else {
			delAcct.TxEnergyAccrued = 0
		}
	}
	if delAcct.DelegatedOut >= d.Amount {
		delAcct.DelegatedOut -= d.Amount
	} else {
		delAcct.DelegatedOut = 0
	}
	if delAcct.LockedAtos.GTE(d.LockedAtos) {
		delAcct.LockedAtos = delAcct.LockedAtos.Sub(d.LockedAtos)
	} else {
		delAcct.LockedAtos = math.ZeroInt()
	}
	delAcct.LastBalanceSnapshot = k.EligibleBalance(ctx, delegator)
	k.SetEnergyAccount(ctx, delAcct)

	// Update delegatee account: shrink DelegatedInUsable by the unused part.
	deeAcct := k.Settle(ctx, delegatee)
	if deeAcct.DelegatedInUsable >= remaining {
		deeAcct.DelegatedInUsable -= remaining
	} else {
		deeAcct.DelegatedInUsable = 0
	}
	k.SetEnergyAccount(ctx, deeAcct)

	k.removeDelegation(ctx, d)

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		eventType,
		sdk.NewAttribute(types.AttributeKeyDelegationID, fmt.Sprintf("%d", d.Id)),
		sdk.NewAttribute(types.AttributeKeyDelegator, d.Delegator),
		sdk.NewAttribute(types.AttributeKeyDelegatee, d.Delegatee),
		sdk.NewAttribute(types.AttributeKeyAmount, fmt.Sprintf("%d", d.Amount)),
	))
	return nil
}

// SweepExpiredDelegations is called from EndBlocker to release any
// delegation whose expires_at has passed. The secondary index orders
// records by expiry, so this is O(expired) per block, not O(total).
func (k Keeper) SweepExpiredDelegations(ctx sdk.Context) {
	now := ctx.BlockTime().Unix()
	store := ctx.KVStore(k.storeKey)
	it := storetypes.KVStorePrefixIterator(store, types.KeyPrefixDelegationByExpiry)
	defer it.Close()

	var ids []uint64
	for ; it.Valid(); it.Next() {
		key := it.Key()
		// key layout: prefix(1) | expires_at(8 BE) | id(8 BE)
		if len(key) < 1+8+8 {
			continue
		}
		expiresAt := int64(bigEndianUint64(key[1:9]))
		if expiresAt > now {
			break // index is sorted by expiry — done
		}
		id := bigEndianUint64(key[9:])
		ids = append(ids, id)
	}
	for _, id := range ids {
		d, ok := k.GetDelegation(ctx, id)
		if !ok {
			continue
		}
		if err := k.releaseDelegation(ctx, d, types.EventTypeEnergyExpired); err != nil {
			k.Logger(ctx).Error("failed to release expired delegation", "id", id, "err", err)
		}
	}
}

func bigEndianUint64(b []byte) uint64 {
	if len(b) < 8 {
		return 0
	}
	return uint64(b[0])<<56 | uint64(b[1])<<48 | uint64(b[2])<<40 | uint64(b[3])<<32 |
		uint64(b[4])<<24 | uint64(b[5])<<16 | uint64(b[6])<<8 | uint64(b[7])
}