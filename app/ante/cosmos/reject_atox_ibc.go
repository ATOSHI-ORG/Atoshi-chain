package cosmos

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	errortypes "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/cosmos/cosmos-sdk/x/authz"
	ibctransfertypes "github.com/cosmos/ibc-go/v8/modules/apps/transfer/types"
)

// maxNestedAtoxMsgs bounds the MsgExec recursion below. authz lets a MsgExec
// carry another MsgExec, so an unbounded walk is a cheap way to burn a node's
// stack before any gas is charged. Matches AuthzLimiterDecorator's limit.
const maxNestedAtoxMsgs = 7

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
//
// The ante chain has one hole, and it is not this decorator's to plug: the ICA
// host executes a counterparty's messages straight through the Msg router,
// skipping every decorator here. v20.4 closes that separately by switching the
// host off; see app/upgrades/v20_4. If it is ever switched back on, its
// allow_messages list must exclude MsgTransfer or this decorator is bypassable
// from any chain that opens an ICA channel.
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

	if err := d.reject(tx.GetMsgs(), 1); err != nil {
		return ctx, err
	}
	return next(ctx, tx, simulate)
}

// reject walks the message tree, descending into authz MsgExec.
//
// tx.GetMsgs() returns the top-level messages only, so a MsgTransfer wrapped in
// a MsgExec is invisible to a flat scan -- and authz grants are permissionless,
// so wrapping is available to anyone. Everything this decorator blocks would be
// reachable through one MsgExec without this walk.
func (d RejectAtoxIBCDecorator) reject(msgs []sdk.Msg, depth int) error {
	if depth >= maxNestedAtoxMsgs {
		return errorsmod.Wrapf(errortypes.ErrInvalidRequest,
			"too many nested authz messages: limit is %d", maxNestedAtoxMsgs)
	}

	for _, msg := range msgs {
		switch msg := msg.(type) {
		case *authz.MsgExec:
			inner, err := msg.GetMessages()
			if err != nil {
				// Undecodable inner messages cannot be cleared, and a tx that
				// cannot be inspected is not a tx that may carry ATOX.
				return errorsmod.Wrapf(errortypes.ErrInvalidRequest,
					"cannot decode authz inner messages: %s", err)
			}
			if err := d.reject(inner, depth+1); err != nil {
				return err
			}
		case *ibctransfertypes.MsgTransfer:
			// ibc-go v8.5 carries exactly one Token per MsgTransfer. If a later
			// version adds a multi-denom field, this check has to grow with it
			// -- the test below pins the single-token shape so the upgrade
			// fails loudly rather than silently letting ATOX through.
			if msg.Token.Denom == d.AtoxDenom {
				return atoxIBCError(d.AtoxDenom)
			}
		}
	}
	return nil
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
