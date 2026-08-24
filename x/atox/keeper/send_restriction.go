package keeper

import (
	"context"

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
//	1. subUnlockedCoins(from)     <- sender already debited
//	2. sendRestriction.Apply(...) <- we are here
//	3. addCoins(to)               <- receiver not yet credited
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

	fromPre := k.AtoxBalance(sdkCtx, from).Add(moved) // undo Evmos's early debit
	toPre := k.AtoxBalance(sdkCtx, to)                // credit has not happened yet

	if _, err := k.SettleAccountWithBalance(sdkCtx, from, fromPre, types.TriggerTransfer); err != nil {
		return to, err
	}
	if _, err := k.SettleAccountWithBalance(sdkCtx, to, toPre, types.TriggerTransfer); err != nil {
		return to, err
	}

	return to, nil
}
