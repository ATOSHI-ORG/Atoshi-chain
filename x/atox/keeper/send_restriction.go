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

	if err := k.chargeTransferFee(sdkCtx, from, to, moved); err != nil {
		return to, err
	}

	return to, nil
}

// feeInProgressKey marks a context as being inside the fee collection send.
type feeInProgressKey struct{}

// chargeTransferFee takes the on-top ATOX fee from the sender and burns it.
//
// SendRestrictionFn can only return a replacement recipient or an error, so it
// cannot enlarge the transfer it is inspecting. It can, however, move coins
// itself — verified against the real bank keeper — and that is how an on-top fee
// becomes possible at all. Doing it here rather than in a dedicated message is
// what makes the fee unavoidable: MsgSend, the ERC20 precompile that EVM wallets
// use, IBC and Authz all funnel into bank.SendCoins, so all of them are charged.
//
// The sender's balance is already short by `moved` at this point, so the fee is
// taken from what remains — which is exactly the on-top semantics: sending 100
// at 1000 bps costs 110 in total.
//
// If the sender cannot cover amount+fee the error propagates out of the outer
// send and baseapp discards the whole tx's writes, so the debit already made is
// never committed. Verified: the debit is NOT rolled back by bank itself, only by
// the tx-level cache, so the fee must never be silently skipped on shortfall.
//
// Exemptions matter as much as the charge. Transfers touching a module account go
// free because ATOX reaches holders through the atox module account,
// fee_collector and distribution; taxing those would take 10% out of every
// holder's mining income before they ever saw it.
func (k Keeper) chargeTransferFee(ctx sdk.Context, from, to sdk.AccAddress, moved math.Int) error {
	params := k.GetParams(ctx)
	if params.TransferFeeBps == 0 {
		return nil
	}
	if k.isModuleAccount(ctx, from) || k.isModuleAccount(ctx, to) {
		return nil
	}

	fee := types.ComputeTransferFee(moved, params.TransferFeeBps)
	if !fee.IsPositive() {
		return nil
	}

	coins := sdk.NewCoins(sdk.NewCoin(k.atoxDenom, fee))
	feeCtx := ctx.WithValue(feeInProgressKey{}, true)

	if err := k.bankKeeper.SendCoinsFromAccountToModule(feeCtx, from, types.ModuleName, coins); err != nil {
		return err
	}
	if err := k.bankKeeper.BurnCoins(ctx, types.ModuleName, coins); err != nil {
		return err
	}

	// Burning is the recycling: MintAtox caps against live supply, so destroying
	// the fee restores headroom for the same amount to be mined again as future
	// block rewards.
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
