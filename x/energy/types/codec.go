package types

import (
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/msgservice"
)

func RegisterLegacyAminoCodec(cdc *codec.LegacyAmino) {
	cdc.RegisterConcrete(&MsgDelegateEnergy{}, "atoshi/energy/MsgDelegateEnergy", nil)
	cdc.RegisterConcrete(&MsgUndelegateEnergy{}, "atoshi/energy/MsgUndelegateEnergy", nil)
	cdc.RegisterConcrete(&MsgUpdateParams{}, "atoshi/energy/MsgUpdateParams", nil)
}

func RegisterInterfaces(registry codectypes.InterfaceRegistry) {
	registry.RegisterImplementations((*sdk.Msg)(nil),
		&MsgDelegateEnergy{},
		&MsgUndelegateEnergy{},
		&MsgUpdateParams{},
	)
	msgservice.RegisterMsgServiceDesc(registry, &_Msg_serviceDesc)
}
