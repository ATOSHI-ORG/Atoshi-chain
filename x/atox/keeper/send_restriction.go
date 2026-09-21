package keeper

import (
	"context"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/atoshi-chain/atoshi/v20/x/atox/types"
)

// SendRestriction is registered with bank.AppendSendRestriction at app init.
// It never blocks or redirects a transfer — `to` is always returned unchanged —
// and exists solely to settle both parties before their ATOX balances move.
//
// This is the single most important function in the module. Getting it wrong
// reintroduces the over-issuance the index exists to prevent.
//
// # Why both sides must settle
//
// The claim on the exchange pool accrues per ATOX per unit of index. If only the
// sender settled, the receiver would inherit an index of zero (or their own
// stale one) while holding the coins, and their next settlement would pay them
// for a span during which someone else held the ATOX. Repeat that across hands
// and the same pot of ATOX collects the same span again and again: with a naive
// "balance * rate at claim time" scheme, ~90 hops extract on the order of 20x
// the pool. Settling the receiver pins their index to now, so each holder is
// only ever paid for the span they actually held.
//
// # Why the balances are computed, not read
//
// bank has not finished writing when this runs, and the Evmos fork's ordering is
// not the upstream one. cosmos-sdk@v0.50.9-evmos x/bank/keeper/send.go does:
//
//  1. subUnlockedCoins(from)     <- sender already debited
//  2. sendRestriction.Apply(...) <- we are here
//  3. addCoins(to)               <- receiver not yet credited
//
// Upstream cosmos-sdk runs the hook BEFORE the subtraction. So on this chain:
//
//	bank.GetBalance(from) == pre-transfer balance MINUS moved
//	bank.GetBalance(to)   == pre-transfer balance (unchanged)
//
// The span being settled accrued against the PRE-transfer balances, so we add
// `moved` back for the sender and leave the receiver's reading alone. This is
// exactly the mistake that produced the "50,000 energy vanishing per transfer"
// bug in x/energy: that code subtracted `moved` from a figure Evmos had already
// subtracted it from, double-counting the movement.
//
// Only ATOX transfers matter here; ATOS and every other denom return
// immediately, which keeps this off the hot path for ordinary transfers.
func (k Keeper) SendRestriction(ctx context.Context, from, to sdk.AccAddress, amt sdk.Coins) (sdk.AccAddress, error) {
	moved := amt.AmountOf(k.atoxDenom)
	if !moved.IsPositive() {
		return to, nil
	}

	// A self-transfer nets to zero. Settling twice at the transient midpoint
	// would value the same balance inconsistently between the two calls.
	if from.Equals(to) {
		return to, nil
	}

	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// The fee is collected by a nested bank send, which re-enters this hook.
	// Bail out immediately on the inner call: the outer one has already settled
	// both parties at their true pre-transfer balances, and re-settling against
	// the mid-transfer figures would be wrong. The flag lives on the context
	// rather than the keeper so concurrent CheckTx and DeliverTx cannot race.
	if sdkCtx.Value(feeInProgressKey{}) != nil {
		return to, nil
	}

	fromPre := k.AtoxBalance(sdkCtx, from).Add(moved) // undo Evmos's early debit
	toPre := k.AtoxBalance(sdkCtx, to)                // credit has not happened yet

	if _, err := k.SettleAccountWithBalance(sdkCtx, from, fromPre, types.TriggerTransfer); err != nil {
		return to, err
	}
	if _, err := k.SettleAccountWithBalance(sdkCtx, to, toPre, types.TriggerTransfer); err != nil {
		return to, err
	}

	// The transfer fee is NOT charged here. A SendRestrictionFn can only redirect
	// the recipient, so it cannot carve the fee out of the transferred amount --
	// charging from this point can only add it on top. x/atox/wrapper takes it
	// out of the amount instead; this function is left with the one job it can
	// do correctly, which is settling both sides before their balances move.
	return to, nil
}

// feeInProgressKey marks a context as being inside the fee collection send.
type feeInProgressKey struct{}

// RecordFeeBurn books a transfer fee that the bank wrapper has already collected
// and burned.
//
// The wrapper owns the carve-out (only it can change the transferred amount) but
// this keeper owns the counter, so the two are split rather than letting the
// wrapper write into this module's store.
//
// Burning IS the recycling: MintAtox measures against live supply, so destroying
// the fee restores headroom for the same amount to be mined again as future
// block rewards. Conversion burn is tracked separately in TotalBurned precisely
// because it must NOT restore headroom.
func (k Keeper) RecordFeeBurn(ctx sdk.Context, from sdk.AccAddress, fee, moved math.Int) error {
	gs := k.GetGlobalState(ctx)
	gs.TotalFeeBurned = gs.TotalFeeBurned.Add(fee)
	if err := k.SetGlobalState(ctx, gs); err != nil {
		return err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeTransferFee,
		sdk.NewAttribute(types.AttributeKeyAddress, from.String()),
		sdk.NewAttribute(types.AttributeKeyAmount, fee.String()),
		sdk.NewAttribute(types.AttributeKeyTransferAmount, moved.String()),
	))
	return nil
}

// TransferFeeBps exposes the fee rate to the bank wrapper.
func (k Keeper) TransferFeeBps(ctx sdk.Context) uint32 { return k.GetParams(ctx).TransferFeeBps }

// IsModuleAccount exposes the exemption test to the bank wrapper.
func (k Keeper) IsModuleAccount(ctx sdk.Context, addr sdk.AccAddress) bool {
	return k.isModuleAccount(ctx, addr)
}
