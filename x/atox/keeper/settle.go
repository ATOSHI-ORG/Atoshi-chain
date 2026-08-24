package keeper

import (
	"fmt"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/atoshi-chain/atoshi/v20/x/atox/types"
)

// SettleAccount credits addr for the index span accrued since its last
// settlement, using its live ATOX balance.
//
// Safe to call from anywhere EXCEPT a path that is mid-way through changing the
// ATOX balance — there the live balance is a transient value and the caller must
// use SettleAccountWithBalance with the pre-change figure instead.
func (k Keeper) SettleAccount(ctx sdk.Context, addr sdk.AccAddress, trigger string) (math.Int, error) {
	return k.SettleAccountWithBalance(ctx, addr, k.AtoxBalance(ctx, addr), trigger)
}

// SettleAccountWithBalance credits addr over the span
// `global_index - account.index`, valued at the supplied ATOX balance, and moves
// the account's index up to the global one.
//
// The balance is an explicit parameter because the bank send hook runs part-way
// through a transfer, where neither side's stored balance is the one that was
// held while the span accrued. Passing the pre-transfer balance is what makes
// repeated transfers non-extractive: the sender is paid for the span it actually
// held, and the receiver starts a fresh span from now.
//
// The credit is only booked to `pending`, never paid out here. Settlement runs
// inside a bank SendRestriction, so transferring would re-enter bank mid-write;
// payout happens from MsgClaimAtos or the EndBlocker sweep.
//
// Module accounts are skipped — see isModuleAccount.
func (k Keeper) SettleAccountWithBalance(
	ctx sdk.Context,
	addr sdk.AccAddress,
	atoxBalance math.Int,
	trigger string,
) (math.Int, error) {
	if k.isModuleAccount(ctx, addr) {
		return math.ZeroInt(), nil
	}

	gs := k.GetGlobalState(ctx)
	acct, found := k.getAtoxAccount(ctx, addr)

	// An index above the global one means corrupted state. Clamp rather than
	// producing a negative credit, and say so loudly.
	if acct.Index.GT(gs.GlobalIndex) {
		k.Logger(ctx).Error("atox account index exceeds global index; clamping",
			"address", addr.String(), "account_index", acct.Index, "global_index", gs.GlobalIndex)
		acct.Index = gs.GlobalIndex
		return math.ZeroInt(), k.SetAtoxAccount(ctx, acct)
	}

	delta := gs.GlobalIndex.Sub(acct.Index)
	owed := types.ComputeOwed(atoxBalance, delta)

	// Nothing accrued. Still persist when the record is new or its index moved:
	// the sweep iterates stored records, so a holder who received ATOX before any
	// release existed (owed 0, index already 0) would otherwise never be
	// registered and automatic conversion would silently never reach them. Skip
	// the global-state write either way, since no debt was booked.
	if !owed.IsPositive() {
		if !found || !acct.Index.Equal(gs.GlobalIndex) {
			acct.Index = gs.GlobalIndex
			return math.ZeroInt(), k.SetAtoxAccount(ctx, acct)
		}
		return math.ZeroInt(), nil
	}

	acct.Pending = acct.Pending.Add(owed)
	acct.Index = gs.GlobalIndex
	if err := k.SetAtoxAccount(ctx, acct); err != nil {
		return math.ZeroInt(), err
	}

	gs.TotalPending = gs.TotalPending.Add(owed)
	if err := k.SetGlobalState(ctx, gs); err != nil {
		return math.ZeroInt(), err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeSettle,
		sdk.NewAttribute(types.AttributeKeyAddress, addr.String()),
		sdk.NewAttribute(types.AttributeKeyAmount, owed.String()),
		sdk.NewAttribute(types.AttributeKeyAtoxBalance, atoxBalance.String()),
		sdk.NewAttribute(types.AttributeKeyIndexDelta, delta.String()),
		sdk.NewAttribute(types.AttributeKeyTrigger, trigger),
	))

	return owed, nil
}

// Claimable is what MsgClaimAtos would pay addr right now: already-settled
// pending plus what settling at the current index would add.
func (k Keeper) Claimable(ctx sdk.Context, addr sdk.AccAddress) (pending, unsettled math.Int) {
	acct := k.GetAtoxAccount(ctx, addr)
	pending = acct.Pending
	if pending.IsNil() {
		pending = math.ZeroInt()
	}

	if k.isModuleAccount(ctx, addr) {
		return pending, math.ZeroInt()
	}

	gs := k.GetGlobalState(ctx)
	if acct.Index.GTE(gs.GlobalIndex) {
		return pending, math.ZeroInt()
	}
	return pending, types.ComputeOwed(k.AtoxBalance(ctx, addr), gs.GlobalIndex.Sub(acct.Index))
}

// AddToExchangePool moves `amount` ATOS from fromModule into the exchange pool
// and advances the conversion index by the corresponding amount.
//
// Called by the bridge adapter when Ethereum confirms, via Hyperlane receipt,
// that the matching ERC20 has landed in the bridge vault. The coin movement and
// the index bump happen together so the pool balance and what the index promises
// can never diverge: a caller that moved coins without bumping would strand
// them, and one that bumped without moving would promise ATOS the pool does not
// hold.
func (k Keeper) AddToExchangePool(ctx sdk.Context, fromModule string, amount math.Int) error {
	params := k.GetParams(ctx)
	if !params.Enabled {
		return types.ErrAtoxDisabled
	}
	if amount.IsNil() || !amount.IsPositive() {
		return types.ErrInvalidAmount
	}

	gs := k.GetGlobalState(ctx)

	delta, remainder, err := types.ComputeIndexDelta(amount, gs.IndexRemainder, params.SupplyCap)
	if err != nil {
		return err
	}

	coins := sdk.NewCoins(sdk.NewCoin(k.baseDenom, amount))
	if err := k.bankKeeper.SendCoinsFromModuleToModule(ctx, fromModule, types.ExchangePoolName, coins); err != nil {
		return err
	}

	gs.GlobalIndex = gs.GlobalIndex.Add(delta)
	gs.IndexRemainder = remainder
	gs.TotalReleasedToPool = gs.TotalReleasedToPool.Add(amount)
	if err := k.SetGlobalState(ctx, gs); err != nil {
		return err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypePoolRelease,
		sdk.NewAttribute(types.AttributeKeyAmount, amount.String()),
		sdk.NewAttribute(types.AttributeKeyIndexDelta, delta.String()),
		sdk.NewAttribute(types.AttributeKeyGlobalIndex, gs.GlobalIndex.String()),
	))

	return nil
}

// MintAtox mints ATOX to recipient, settling them first.
//
// Called by x/tokenomics when paying block rewards. The pre-mint settlement is
// mandatory: without it a first-time recipient keeps index 0 while holding a
// positive balance, and their next settlement would claim the entire index
// history — ATOS released before they held any ATOX at all.
func (k Keeper) MintAtox(ctx sdk.Context, recipient sdk.AccAddress, amount math.Int) error {
	params := k.GetParams(ctx)
	if !params.Enabled {
		return types.ErrAtoxDisabled
	}
	if amount.IsNil() || !amount.IsPositive() {
		return types.ErrInvalidAmount
	}

	// The cap is what the index divides by, so exceeding it would break the
	// solvency bound sum(balances)*delta <= cap*delta.
	if k.AtoxSupply(ctx).Add(amount).GT(params.SupplyCap) {
		return types.ErrSupplyCapReached
	}

	if _, err := k.SettleAccountWithBalance(
		ctx, recipient, k.AtoxBalance(ctx, recipient), types.TriggerMint,
	); err != nil {
		return err
	}

	coins := sdk.NewCoins(sdk.NewCoin(k.atoxDenom, amount))
	if err := k.bankKeeper.MintCoins(ctx, types.ModuleName, coins); err != nil {
		return err
	}
	if err := k.bankKeeper.SendCoinsFromModuleToAccount(ctx, types.ModuleName, recipient, coins); err != nil {
		return err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeMintAtox,
		sdk.NewAttribute(types.AttributeKeyAddress, recipient.String()),
		sdk.NewAttribute(types.AttributeKeyAmount, amount.String()),
	))

	return nil
}

// PayoutPending settles addr and transfers the whole resulting pending balance
// out of the exchange pool.
//
// `minPayout` skips the transfer when the balance is below it, so the EndBlocker
// sweep does not fill blocks with dust transfers; the settlement itself still
// happens, so nothing is lost. MsgClaimAtos passes zero to always pay.
//
// Returns the amount actually transferred.
func (k Keeper) PayoutPending(
	ctx sdk.Context,
	addr sdk.AccAddress,
	minPayout math.Int,
	trigger string,
) (math.Int, error) {
	if _, err := k.SettleAccount(ctx, addr, trigger); err != nil {
		return math.ZeroInt(), err
	}

	acct := k.GetAtoxAccount(ctx, addr)
	amount := acct.Pending
	if amount.IsNil() || !amount.IsPositive() {
		return math.ZeroInt(), nil
	}
	if !minPayout.IsNil() && minPayout.IsPositive() && amount.LT(minPayout) {
		return math.ZeroInt(), nil
	}

	// Defence in depth against a pool that cannot cover its books. The running
	// totals say this cannot happen, but paying out here is the only place the
	// module spends ATOS, so it is the right place to verify against the actual
	// bank balance rather than trust the counters.
	if available := k.ExchangePoolBalance(ctx); available.LT(amount) {
		k.Logger(ctx).Error("atox exchange pool cannot cover a settled payout",
			"address", addr.String(), "owed", amount.String(), "available", available.String())
		return math.ZeroInt(), types.ErrPoolInsolvent
	}

	coins := sdk.NewCoins(sdk.NewCoin(k.baseDenom, amount))
	if err := k.bankKeeper.SendCoinsFromModuleToAccount(ctx, types.ExchangePoolName, addr, coins); err != nil {
		return math.ZeroInt(), err
	}

	acct.Pending = math.ZeroInt()
	acct.TotalClaimed = acct.TotalClaimed.Add(amount)
	if err := k.SetAtoxAccount(ctx, acct); err != nil {
		return math.ZeroInt(), err
	}

	gs := k.GetGlobalState(ctx)
	if gs.TotalPending.LT(amount) {
		// Would underflow: the cached total disagrees with the per-account books.
		return math.ZeroInt(), fmt.Errorf(
			"atox: total_pending (%s) is below the payout (%s) for %s",
			gs.TotalPending, amount, addr)
	}
	gs.TotalPending = gs.TotalPending.Sub(amount)
	gs.TotalPaidOut = gs.TotalPaidOut.Add(amount)
	if err := k.SetGlobalState(ctx, gs); err != nil {
		return math.ZeroInt(), err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeClaim,
		sdk.NewAttribute(types.AttributeKeyAddress, addr.String()),
		sdk.NewAttribute(types.AttributeKeyAmount, amount.String()),
		sdk.NewAttribute(types.AttributeKeyTrigger, trigger),
	))

	return amount, nil
}
