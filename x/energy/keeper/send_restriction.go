package keeper

import (
	"context"

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

	k.ApplyBalanceChange(sdkCtx, from, fromBefore.Sub(moved))
	k.ApplyBalanceChange(sdkCtx, to, toBefore.Add(moved))

	return to, nil
}
