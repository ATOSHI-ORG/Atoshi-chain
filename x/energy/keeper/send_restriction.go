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
	// EligibleBalance would return AFTER this transfer commits.
	// EligibleBalance = bank + LockedAtos.
	//
	// Evmos bank-send ordering — production bug discovered 2026-06-30
	// (testnet account 0x30F288...). Evmos
	// cosmos-sdk@v0.50.9-evmos/x/bank/keeper/send.go:208-225 invokes
	// SendRestriction AFTER subUnlockedCoins(from) and BEFORE
	// addCoins(to). Upstream cosmos-sdk has the opposite order
	// (hook → sub → add) but the Evmos fork flipped sub/hook.
	//
	// So when this hook reads the bank:
	//   - bank.GetBalance(from) returns POST-subtract balance
	//   - bank.GetBalance(to)   returns PRE-add balance
	//
	// Old code computed `projected_from = fromBefore - moved + fromLocked`,
	// which on Evmos became `(real_pre - moved) - moved + fromLocked`
	// = `real_pre - 2*moved + fromLocked`. snapshot lost `moved` aatos
	// on every transfer — even pure delegations that should be
	// cap-neutral. For a 30k ATOS lock that's exactly one
	// TxEnergyHoldingThreshold worth of eligible balance → capacity
	// dropped by one TxEnergyPerThreshold (= 50000 energy) per send.
	// That's the "5万 ATOS 凭空消失" fingerprint observed in
	// production.
	//
	// Fix:
	//   - from: fromBefore is already post-subtract; don't subtract
	//     `moved` again.
	//   - to:   toBefore is still pre-add; add `moved` to project
	//     the post-receive balance.
	//
	// Delegate / releaseDelegation update LockedAtos BEFORE the
	// bank send (see x/energy/keeper/delegation.go), so fromLocked
	// already reflects the post-delegation lock total when the hook
	// fires.
	fromLocked := k.lockedAtos(sdkCtx, from)
	toLocked := k.lockedAtos(sdkCtx, to)

	k.ApplyBalanceChange(sdkCtx, from, fromBefore.Add(fromLocked))
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
