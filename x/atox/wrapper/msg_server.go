package wrapper

import (
	"context"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	bankkeeper "github.com/cosmos/cosmos-sdk/x/bank/keeper"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
)

// MsgServer is bank's Msg service with the ATOX transfer fee taken out of the
// transferred amount.
//
// # Why not wrap the keeper instead
//
// Wrapping bankkeeper.Keeper looks like the smaller change -- every consumer
// resolves SendCoins through that interface -- but bank's own msgServer.Send
// begins with
//
//	if base, ok := k.Keeper.(BaseKeeper); ok { ... } else { invalid keeper type }
//
// a type assertion to the CONCRETE keeper, so that it can reach an unexported
// address codec. A wrapper fails that assertion and every MsgSend on the chain
// errors out -- ATOS transfers included, not just ATOX. Bank's AppModule does
// the same assertion again for its store migrator.
//
// So the fee is applied here, one level up, where the keeper stays concrete.
//
// # Coverage
//
// Send and MultiSend are the only paths a user's ATOX can leave on: a wallet's
// MsgSend arrives here through the Msg router, and the erc20 precompile builds
// the same MsgSend and calls a Msg server directly. Module-internal movement
// (SendCoinsFromModuleToAccount and friends) never passes through a Msg server,
// which is the exemption we want -- ATOX reaches holders via fee_collector and
// distribution, and taxing those would take a cut of every holder's mining
// income before they saw it.
type MsgServer struct {
	banktypes.MsgServer

	base bankkeeper.BaseKeeper
	atox AtoxKeeper
}

var _ banktypes.MsgServer = MsgServer{}

// NewMsgServer wraps bank's Msg server. `inner` handles everything except the
// two transfer methods.
func NewMsgServer(inner banktypes.MsgServer, base bankkeeper.BaseKeeper, atox AtoxKeeper) MsgServer {
	return MsgServer{MsgServer: inner, base: base, atox: atox}
}

// Send applies the inclusive fee and forwards the remainder.
//
// The validation below mirrors bank's own Send. It is repeated rather than
// delegated because delegating would perform the transfer too, leaving no point
// at which the amount can still be changed.
func (s MsgServer) Send(goCtx context.Context, msg *banktypes.MsgSend) (*banktypes.MsgSendResponse, error) {
	from, err := sdk.AccAddressFromBech32(msg.FromAddress)
	if err != nil {
		return nil, sdkerrors.ErrInvalidAddress.Wrapf("invalid from address: %s", err)
	}
	to, err := sdk.AccAddressFromBech32(msg.ToAddress)
	if err != nil {
		return nil, sdkerrors.ErrInvalidAddress.Wrapf("invalid to address: %s", err)
	}
	if !msg.Amount.IsValid() {
		return nil, errorsmod.Wrap(sdkerrors.ErrInvalidCoins, msg.Amount.String())
	}
	if !msg.Amount.IsAllPositive() {
		return nil, errorsmod.Wrap(sdkerrors.ErrInvalidCoins, msg.Amount.String())
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	if err := s.base.IsSendEnabledCoins(ctx, msg.Amount...); err != nil {
		return nil, err
	}
	if s.base.BlockedAddr(to) {
		return nil, errorsmod.Wrapf(sdkerrors.ErrUnauthorized, "%s is not allowed to receive funds", msg.ToAddress)
	}

	if err := ChargeInclusiveFee(goCtx, s.base, s.atox, from, to, msg.Amount); err != nil {
		return nil, err
	}
	return &banktypes.MsgSendResponse{}, nil
}

// MultiSend applies the fee to each output.
//
// Inputs are not netted against outputs first: the fee is a per-delivery charge,
// so splitting one transfer into several outputs must cost the same as sending
// them one at a time, or the fee would be avoidable by batching.
func (s MsgServer) MultiSend(goCtx context.Context, msg *banktypes.MsgMultiSend) (*banktypes.MsgMultiSendResponse, error) {
	if len(msg.Inputs) == 0 {
		return nil, errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "no inputs to send transaction")
	}
	// Bank restricts MultiSend to a single input; keeping the same restriction
	// means the fee has exactly one payer to charge.
	if len(msg.Inputs) != 1 {
		return nil, errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "multiple senders not allowed")
	}
	if len(msg.Outputs) == 0 {
		return nil, errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "no outputs to send transaction")
	}

	from, err := sdk.AccAddressFromBech32(msg.Inputs[0].Address)
	if err != nil {
		return nil, sdkerrors.ErrInvalidAddress.Wrapf("invalid from address: %s", err)
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	if err := s.base.IsSendEnabledCoins(ctx, msg.Inputs[0].Coins...); err != nil {
		return nil, err
	}

	for _, out := range msg.Outputs {
		to, err := sdk.AccAddressFromBech32(out.Address)
		if err != nil {
			return nil, sdkerrors.ErrInvalidAddress.Wrapf("invalid output address: %s", err)
		}
		if s.base.BlockedAddr(to) {
			return nil, errorsmod.Wrapf(sdkerrors.ErrUnauthorized, "%s is not allowed to receive funds", out.Address)
		}
		if err := ChargeInclusiveFee(goCtx, s.base, s.atox, from, to, out.Coins); err != nil {
			return nil, err
		}
	}
	return &banktypes.MsgMultiSendResponse{}, nil
}
