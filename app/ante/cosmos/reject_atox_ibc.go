package cosmos

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	errortypes "github.com/cosmos/cosmos-sdk/types/errors"
	ibctransfertypes "github.com/cosmos/ibc-go/v8/modules/apps/transfer/types"
)

// RejectAtoxIBCDecorator refuses to let ATOX leave over IBC.
//
// Three independent reasons, any one of which is sufficient:
//
//	Settlement. The ATOX send restriction settles both sides of every transfer
//	so each holder is paid only for the span they actually held. It exempts
//	module accounts, because those never accrue a claim. An IBC escrow address
//	is NOT a module account -- it is a sha256 of "port/channel" that becomes a
//	plain BaseAccount the first time it is funded (ibc-go
//	transfer/types/keys.go:51) -- so escrowing ATOX makes the escrow accrue a
//	conversion claim nobody can ever exercise, and an unfunded exchange pool
//	turns every escrow and un-escrow into a failing transfer. Reported
//	externally on 2026-10-09.
//
//	The transfer fee. ATOX burns transfer_fee_bps on every account-to-account
//	move, currently 10%. Escrowing 100 ATOX would lock 90 and destroy 10 while
//	minting a voucher for 100 on the counterparty chain. The voucher is
//	unbacked from the first transfer.
//
//	What the voucher would even be. An ATOX balance is a claim on this chain's
//	exchange pool that accrues against a global index. A voucher on another
//	chain tracks no index and accrues nothing, so it is not ATOX in any sense
//	that matters -- it is a number that looks like ATOX and is worth less every
//	block.
//
// Nothing is lost by refusing. There are no open transfer channels today, so
// no ATOX has ever been escrowed and no counterparty holds a voucher. Opening
// a channel is permissionless, though, which is why this cannot wait for
// someone to decide to open one.
//
// Placed in the ante chain rather than in the send restriction because
// MsgTransfer is the only way ATOX can reach an escrow, and matching on the
// message is exact -- the alternative, recognising escrow addresses from the
// bank hook, means enumerating every open channel on every ATOX transfer.
type RejectAtoxIBCDecorator struct {
	// AtoxDenom is the base denom to refuse. Injected rather than imported so
	// this package keeps no dependency on x/atox.
	AtoxDenom string
}

// AnteHandle rejects any MsgTransfer carrying ATOX.
func (d RejectAtoxIBCDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (sdk.Context, error) {
	if d.AtoxDenom == "" {
		return next(ctx, tx, simulate)
	}

	for _, msg := range tx.GetMsgs() {
		transfer, ok := msg.(*ibctransfertypes.MsgTransfer)
		if !ok {
			continue
		}
		// ibc-go v8.5 carries exactly one Token per MsgTransfer. If a later
		// version adds a multi-denom field, this check has to grow with it --
		// the test below pins the single-token shape so the upgrade fails
		// loudly rather than silently letting ATOX through.
		if transfer.Token.Denom == d.AtoxDenom {
			return ctx, atoxIBCError(d.AtoxDenom)
		}
	}
	return next(ctx, tx, simulate)
}

func atoxIBCError(denom string) error {
	return errorsmod.Wrapf(
		errortypes.ErrInvalidRequest,
		"%s cannot be transferred over IBC: it carries a transfer burn and a "+
			"conversion claim that only exist on this chain, so an escrowed "+
			"balance would be unbacked and unclaimable",
		denom,
	)
}
