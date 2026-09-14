// Package wrapper adapts the bank keeper so that the ATOX transfer fee is taken
// OUT of the transferred amount rather than added on top of it.
package wrapper

import (
	"context"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	bankkeeper "github.com/cosmos/cosmos-sdk/x/bank/keeper"

	"github.com/atoshi-chain/atoshi/v20/x/atox/types"
)

// AtoxKeeper is the slice of x/atox this wrapper needs.
type AtoxKeeper interface {
	AtoxDenom() string
	TransferFeeBps(ctx sdk.Context) uint32
	IsModuleAccount(ctx sdk.Context, addr sdk.AccAddress) bool
}

// FeeInclusiveBank makes the ATOX transfer fee inclusive.
//
// # The two ways to charge a transfer fee
//
//	on top       "send 10" -> sender loses 11, receiver gets 10
//	inclusive    "send 10" -> sender loses 10, receiver gets 9
//
// Inclusive is what this wraps for, and the reason is ERC20. ATOX is exposed at
// 0xc2b6… through the erc20 module, so wallets and exchanges reach it with a
// plain transfer(to, amount) whose whole meaning is "move exactly amount out of
// me". Charging on top makes that call need an allowance the caller never
// granted and makes a balance-before/after reconciliation disagree with the
// amount argument -- both of which break integrations silently.
//
// It also lets an account be emptied. On top, a holder of 100 can move at most
// 90 (90 + 9 fee = 99) and the last coin is stuck forever.
//
// # Why here and not in the SendRestriction
//
// SendRestrictionFn may only redirect the recipient -- its signature returns a
// new toAddr, not a new amount -- so the fee cannot be carved out of the
// transfer there. Wrapping SendCoins is the narrowest place that still catches
// both user paths: a wallet's MsgSend and the erc20 precompile both end up in
// bank's msgServer, which dispatches SendCoins through this interface.
//
// Module-internal movement is untouched: SendCoinsFromModuleToAccount and its
// siblings call the concrete keeper's own SendCoins, not this one, so ATOX
// travelling through fee_collector or distribution is never taxed -- which is
// the same exemption the restriction already applies.
type FeeInclusiveBank struct {
	bankkeeper.Keeper
	atox AtoxKeeper
}

var _ bankkeeper.Keeper = FeeInclusiveBank{}

func NewFeeInclusiveBank(inner bankkeeper.Keeper, atox AtoxKeeper) FeeInclusiveBank {
	return FeeInclusiveBank{Keeper: inner, atox: atox}
}

// SendCoins moves `amt` out of the sender, of which the ATOX fee is retained and
// burned and the remainder reaches the recipient.
//
// Strict on funds: the sender must hold the full `amt`. Silently shrinking the
// transfer to whatever they could afford would make transfer(to, 100) move some
// other number, which is exactly the ERC20 surprise this wrapper exists to
// avoid. Sizing the transfer is the caller's job, and the account query returns
// max_sendable for it.
func (b FeeInclusiveBank) SendCoins(goCtx context.Context, from, to sdk.AccAddress, amt sdk.Coins) error {
	return ChargeInclusiveFee(goCtx, b.Keeper, b.atox, from, to, amt)
}

// SendBank is the slice of bank the fee carve-out needs. Narrower than
// bankkeeper.Keeper so tests can drive the exact same code path against a stub
// instead of reimplementing the fee rules beside it -- which is how the two
// would drift.
type SendBank interface {
	SendCoins(ctx context.Context, from, to sdk.AccAddress, amt sdk.Coins) error
	SendCoinsFromAccountToModule(ctx context.Context, from sdk.AccAddress, module string, amt sdk.Coins) error
	BurnCoins(ctx context.Context, module string, amt sdk.Coins) error
}

// ChargeInclusiveFee carves the ATOX fee out of `amt`, burns it, and forwards
// the remainder.
func ChargeInclusiveFee(
	goCtx context.Context,
	bank SendBank,
	atox AtoxKeeper,
	from, to sdk.AccAddress,
	amt sdk.Coins,
) error {
	ctx := sdk.UnwrapSDKContext(goCtx)
	b := struct {
		SendBank
		atox AtoxKeeper
	}{bank, atox}

	denom := b.atox.AtoxDenom()
	moved := amt.AmountOf(denom)
	if !moved.IsPositive() {
		return b.SendCoins(goCtx, from, to, amt)
	}

	// Module accounts are exempt on either leg. ATOX reaches holders through the
	// atox module account, fee_collector and distribution; taxing those paths
	// would take a cut out of every holder's mining income.
	if b.atox.IsModuleAccount(ctx, from) || b.atox.IsModuleAccount(ctx, to) {
		return b.SendCoins(goCtx, from, to, amt)
	}

	// A self-transfer nets to zero; charging it would be a pure toll on a no-op.
	if from.Equals(to) {
		return b.SendCoins(goCtx, from, to, amt)
	}

	fee := types.ComputeTransferFee(moved, b.atox.TransferFeeBps(ctx))
	if !fee.IsPositive() {
		return b.SendCoins(goCtx, from, to, amt)
	}

	// The fee cannot exceed what is being moved -- otherwise the recipient would
	// receive nothing (or the subtraction would go negative) while the sender
	// still paid in full.
	if fee.GTE(moved) {
		return types.ErrInvalidAmount
	}

	feeCoins := sdk.NewCoins(sdk.NewCoin(denom, fee))
	net := amt.Sub(feeCoins...)

	// Fee first, then the remainder. Doing it in this order means a sender who
	// cannot cover the whole `amt` fails on the smaller of the two moves, so the
	// error names the shortfall rather than leaving a fee collected against a
	// transfer that never happened. Both are in one tx, so a failure at either
	// step unwinds the other.
	if err := b.SendCoinsFromAccountToModule(goCtx, from, types.ModuleName, feeCoins); err != nil {
		return err
	}
	if err := b.BurnCoins(goCtx, types.ModuleName, feeCoins); err != nil {
		return err
	}
	if r, ok := atox.(feeRecorder); ok {
		if err := r.RecordFeeBurn(ctx, from, fee, moved); err != nil {
			return err
		}
	}

	return b.SendCoins(goCtx, from, to, net)
}

// recordFeeBurn is a hook point for the running total; the keeper owns the
// counter so the wrapper does not reach into its store.
type feeRecorder interface {
	RecordFeeBurn(ctx sdk.Context, from sdk.AccAddress, fee, moved math.Int) error
}

