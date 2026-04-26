package types

import (
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/msgservice"
)

func RegisterLegacyAminoCodec(cdc *codec.LegacyAmino) {
	cdc.RegisterConcrete(&MsgReportPrice{}, "atoshi/oracle/MsgReportPrice", nil)
	cdc.RegisterConcrete(&MsgUpdateParams{}, "atoshi/oracle/MsgUpdateParams", nil)
}

// RegisterInterfaces registers the oracle module Msg implementations on the
// global InterfaceRegistry so that the SDK can decode them from any-typed
// transactions and route them to the MsgServer.
func RegisterInterfaces(registry codectypes.InterfaceRegistry) {
	registry.RegisterImplementations((*sdk.Msg)(nil),
		&MsgReportPrice{},
		&MsgUpdateParams{},
	)
	msgservice.RegisterMsgServiceDesc(registry, &_Msg_serviceDesc)
}
