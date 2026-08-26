package types

import (
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/msgservice"
)

func RegisterLegacyAminoCodec(cdc *codec.LegacyAmino) {
	cdc.RegisterConcrete(&MsgClaimProjectTreasuryReward{}, "atoshi/tokenomics/MsgClaimProjectTreasuryReward", nil)
	cdc.RegisterConcrete(&MsgClaimMigrationTokens{}, "atoshi/tokenomics/MsgClaimMigrationTokens", nil)
	cdc.RegisterConcrete(&MsgUpdateParams{}, "atoshi/tokenomics/MsgUpdateParams", nil)
}

// RegisterInterfaces registers tokenomics Msg types so the SDK can decode and
// route them through the standard tx pipeline.
func RegisterInterfaces(registry codectypes.InterfaceRegistry) {
	registry.RegisterImplementations((*sdk.Msg)(nil),
		&MsgClaimProjectTreasuryReward{},
		&MsgClaimMigrationTokens{},
		&MsgUpdateParams{},
	)
	msgservice.RegisterMsgServiceDesc(registry, &_Msg_serviceDesc)
}
