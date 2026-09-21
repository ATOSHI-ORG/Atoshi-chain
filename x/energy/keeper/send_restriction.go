package keeper

import (
	"context"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	atoshitypes "github.com/atoshi-chain/atoshi/v20/types"
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
// Both energy-eligible denoms count: liao and aatox. ATOX was added to
// EligibleBalance but this gate still filtered on the base denom alone, so an
// ATOX transfer left both parties' snapshots frozen at their pre-transfer value
// -- the eligibility sum was right and nothing ever asked it to recompute.
// Other denoms (IBC vouchers, future tokens) are still ignored.
func (k Keeper) SendRestriction(ctx context.Context, from, to sdk.AccAddress, amt sdk.Coins) (sdk.AccAddress, error) {
	// Summed, not handled separately: both carry 18 decimals and both count at
	// face value toward eligibility, so their total is the amount by which the
	// recipient's eligible balance is about to rise.
	moved := amt.AmountOf(k.BaseDenom()).Add(amt.AmountOf(atoshitypes.AtoxBaseDenom))
	if !moved.IsPositive() {
		return to, nil
	}

	// Self-transfer is net-zero on balance. Running ApplyBalanceChange
	// at the transient post-subtract midpoint would clamp TxEnergyAccrued
	// down against a temporarily-reduced capacity that the to-side call
	// cannot restore. Skip.
	if from.Equals(to) {
		return to, nil
	}

	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// Read the two bank terms directly instead of calling EligibleBalance.
	//
	// EligibleBalance would be the tidier call and would never drift from the
	// definition, but it also reads the staking delegations, and this runs on
	// every transfer on the chain: routing the hot path through it pushed a
	// plain MsgSend from 200_061 gas over a 200_000 limit -- x/feemarket's
	// integration tests turned red with "out of gas in location: ReadFlat".
	//
	// So the cheap terms are summed here and the staked term is deliberately
	// left out, exactly as before. That makes this projection an approximation
	// of EligibleBalance for stakers, which is tolerable because it is only a
	// snapshot: settle (keeper/settle.go) recomputes from EligibleBalance and
	// corrects it. What was NOT tolerable is omitting ATOX, because nothing else
	// was triggering a recompute after an ATOX transfer.
	fromBefore := k.bankKeeper.GetBalance(sdkCtx, from, k.BaseDenom()).Amount.
		Add(k.bankKeeper.GetBalance(sdkCtx, from, atoshitypes.AtoxBaseDenom).Amount)
	toBefore := k.bankKeeper.GetBalance(sdkCtx, to, k.BaseDenom()).Amount.
		Add(k.bankKeeper.GetBalance(sdkCtx, to, atoshitypes.AtoxBaseDenom).Amount)

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
	// = `real_pre - 2*moved + fromLocked`. snapshot lost `moved` liao
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
