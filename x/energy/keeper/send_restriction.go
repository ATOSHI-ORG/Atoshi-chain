package keeper

import (
	"context"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	atoshitypes "github.com/atoshi-chain/atoshi/v20/types"
	"github.com/atoshi-chain/atoshi/v20/x/energy/types"
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

	// Read the bank terms directly rather than calling EligibleBalance, because
	// the projection needs the POST-send bank figures and EligibleBalance can
	// only report the pre-send ones (see the ordering note below). The staked
	// term is added separately further down.
	//
	// An earlier version of this function left the staked term out entirely, on
	// the stated grounds that "settle recomputes from EligibleBalance and
	// corrects it". That was wrong: Settle only reads EligibleBalance on the
	// first touch of an account (LastUpdatedTime == 0) and thereafter works
	// purely from the stored snapshot. Nothing recomputed. So every transfer --
	// including the fee deduction of the very next tx -- overwrote the snapshot
	// with bank-only figures and silently erased a staker's staked ATOS from
	// their eligibility. Testnet account
	// atoshi14dpp92vlaxlpp4ctve0082zqhgys9ydv5gz5nd staked 7,500 ATOS on
	// 2026-09-22 and stayed at capacity 0, 7,481 ATOS short of a threshold it
	// had in fact crossed.
	//
	// The staked term is NOT re-read from x/staking here. Asking it costs real
	// gas on this path -- measured, it pushes a plain MsgSend to 200_061, past
	// the 200_000 a wallet sends by default, and x/feemarket's integration
	// tests go red with "out of gas in location: ReadFlat". So the term is
	// taken from the account's cached staked_snapshot, which costs nothing:
	// the account is already being loaded for locked_atos. x/energy's staking
	// hooks (keeper/staking_hooks.go) keep that cache current, which is the
	// half of the problem bank transfers can never observe -- the Evmos SDK's
	// DelegateCoins writes balances through setBalance/addCoins and never
	// enters this restriction chain at all.
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
	// bank send (see x/energy/keeper/delegation.go), so the locked
	// term already reflects the post-delegation lock total when the
	// hook fires.
	//
	// One account read per side, yielding both non-bank terms. Staked ATOS is
	// untouched by a bank transfer but still has to be carried over, because
	// ApplyBalanceChange stores an absolute figure: passing bank+locked alone
	// would not leave the staked term in place, it would erase it.
	fromAcct := k.GetEnergyAccount(sdkCtx, from)
	toAcct := k.GetEnergyAccount(sdkCtx, to)

	k.ApplyBalanceChange(sdkCtx, from, fromBefore.Add(k.nonBankTerms(sdkCtx, from, fromAcct)))
	k.ApplyBalanceChange(sdkCtx, to, toBefore.Add(moved).Add(k.nonBankTerms(sdkCtx, to, toAcct)))

	return to, nil
}

// nonBankTerms is everything in EligibleBalance that is not a bank balance:
// locked ATOS plus staked ATOS.
//
// The staked term normally comes from the account's cache, which costs nothing
// because the account is already loaded. The exception is an account that has
// never been settled: its staked_snapshot has never been written, so a zero
// there means "not yet known", not "no stake". Those accounts pay for the real
// x/staking read once; every transfer after the first uses the cache. Getting
// this wrong would erase a staker's delegation from their eligibility on their
// very first transfer -- which is the shape of the bug this whole change is
// about, just one level further in.
func (k Keeper) nonBankTerms(ctx sdk.Context, addr sdk.AccAddress, acct types.EnergyAccount) math.Int {
	if acct.LastUpdatedTime == 0 {
		return lockedAtosOf(acct).Add(k.stakedAtos(ctx, addr))
	}
	return lockedAtosOf(acct).Add(cachedStakedAtos(acct))
}

// lockedAtosOf returns the account's LockedAtos counter, defaulting to
// zero when the counter is nil (account never initialized) or the
// account has no ATOS locked.
func lockedAtosOf(acct types.EnergyAccount) math.Int {
	if acct.LockedAtos.IsNil() || !acct.LockedAtos.IsPositive() {
		return math.ZeroInt()
	}
	return acct.LockedAtos
}
