package keeper

import (
	"context"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// SendRestriction is registered with bank.AppendSendRestriction at app
// init. Bank calls it inside SendCoins for every transfer (single or
// multi-coin), before the actual store write. We never block or rewrite
// the send — return `to` unchanged — and use the call purely as an
// "about to mutate balance" hook to refresh the energy snapshot for
// both sides.
//
// Why a SendRestriction rather than a separate post-send hook: the
// Cosmos SDK bank module exposes no official AfterSend hook in
// v0.50; SendRestriction is the only injection point that fires on
// every send path (MsgSend, SendCoinsFromModuleToAccount, EVM
// statedb transitions, IBC transfers).
//
// Because the bank write has not happened yet, we cannot read the
// post-send balance from bank.GetBalance. Instead we read the
// pre-send balance, subtract/add the moved amount, and pass the
// projected post-send balance to ApplyBalanceChange.
//
// Only transfers of the base denom (aatos) move energy-eligible
// funds. Other denoms (IBC vouchers, future tokens) are ignored.
func (k Keeper) SendRestriction(ctx context.Context, from, to sdk.AccAddress, amt sdk.Coins) (sdk.AccAddress, error) {
	moved := amt.AmountOf(k.baseDenom)
	if !moved.IsPositive() {
		return to, nil
	}

	sdkCtx := sdk.UnwrapSDKContext(ctx)

	fromBefore := k.bankKeeper.GetBalance(sdkCtx, from, k.baseDenom).Amount
	toBefore := k.bankKeeper.GetBalance(sdkCtx, to, k.baseDenom).Amount

	// Audit Question 2 (round2): the projected post-send "eligible
	// balance" we pass into ApplyBalanceChange must match what
	// EligibleBalance would return AFTER this transfer commits. With
	// Q2, EligibleBalance = bank + LockedAtos, so we add each side's
	// current LockedAtos to the projected bank balance. A pure bank
	// send between two users does NOT change either side's
	// LockedAtos counter (that counter is moved only by Delegate /
	// releaseDelegation), so reading the current value is correct.
	//
	// Delegate / releaseDelegation update LockedAtos BEFORE calling
	// the bank-side send so the new counter value is already in store
	// when the hook fires — see x/energy/keeper/delegation.go.
	fromLocked := k.lockedAtos(sdkCtx, from)
	toLocked := k.lockedAtos(sdkCtx, to)

	k.ApplyBalanceChange(sdkCtx, from, fromBefore.Sub(moved).Add(fromLocked))
	k.ApplyBalanceChange(sdkCtx, to, toBefore.Add(moved).Add(toLocked))

	return to, nil
}

// lockedAtos returns the addr's LockedAtos counter, defaulting to
// zero when the counter is nil (account never initialized) or the
// account has no ATOS locked.
func (k Keeper) lockedAtos(ctx sdk.Context, addr sdk.AccAddress) math.Int {
	acct := k.GetEnergyAccount(ctx, addr)
	if acct.LockedAtos.IsNil() || !acct.LockedAtos.IsPositive() {
		return math.ZeroInt()
	}
	return acct.LockedAtos
}
