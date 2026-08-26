package bridgeadapter

import (
	"context"
	"encoding/json"
	"fmt"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	simtypes "github.com/cosmos/cosmos-sdk/types/simulation"
	"github.com/grpc-ecosystem/grpc-gateway/runtime"
	"github.com/spf13/cobra"

	"github.com/atoshi-chain/atoshi/v20/x/bridgeadapter/client/cli"
	"github.com/atoshi-chain/atoshi/v20/x/bridgeadapter/keeper"
	batypes "github.com/atoshi-chain/atoshi/v20/x/bridgeadapter/types"
)

const consensusVersion = 1

var (
	_ module.AppModule           = AppModule{}
	_ module.AppModuleBasic      = AppModuleBasic{}
	_ module.AppModuleSimulation = AppModule{}
)

type AppModuleBasic struct{}

func (AppModuleBasic) Name() string { return batypes.ModuleName }
func (AppModuleBasic) RegisterLegacyAminoCodec(cdc *codec.LegacyAmino) {
	batypes.RegisterLegacyAminoCodec(cdc)
}
func (AppModuleBasic) ConsensusVersion() uint64 { return consensusVersion }
func (AppModuleBasic) RegisterInterfaces(registry codectypes.InterfaceRegistry) {
	batypes.RegisterInterfaces(registry)
}
func (AppModuleBasic) DefaultGenesis(_ codec.JSONCodec) json.RawMessage {
	bz, _ := json.Marshal(batypes.DefaultGenesisState())
	return bz
}
func (AppModuleBasic) ValidateGenesis(_ codec.JSONCodec, _ client.TxEncodingConfig, bz json.RawMessage) error {
	var gs batypes.GenesisState
	if err := json.Unmarshal(bz, &gs); err != nil {
		return fmt.Errorf("failed to unmarshal %s genesis state: %w", batypes.ModuleName, err)
	}
	return gs.Validate()
}
func (AppModuleBasic) RegisterGRPCGatewayRoutes(clientCtx client.Context, mux *runtime.ServeMux) {
	if err := batypes.RegisterQueryHandlerClient(context.Background(), mux, batypes.NewQueryClient(clientCtx)); err != nil {
		panic(err)
	}
}
func (AppModuleBasic) GetTxCmd() *cobra.Command    { return nil }
func (AppModuleBasic) GetQueryCmd() *cobra.Command { return cli.GetQueryCmd() }

type AppModule struct {
	AppModuleBasic
	keeper keeper.Keeper
}

func NewAppModule(k keeper.Keeper) AppModule {
	return AppModule{AppModuleBasic: AppModuleBasic{}, keeper: k}
}

func (am AppModule) RegisterServices(cfg module.Configurator) {
	batypes.RegisterMsgServer(cfg.MsgServer(), keeper.NewMsgServerImpl(am.keeper))
	batypes.RegisterQueryServer(cfg.QueryServer(), keeper.NewQuerier(am.keeper))
}

func (am AppModule) InitGenesis(ctx sdk.Context, _ codec.JSONCodec, bz json.RawMessage) []abci.ValidatorUpdate {
	var gs batypes.GenesisState
	if err := json.Unmarshal(bz, &gs); err != nil {
		panic(fmt.Errorf("failed to unmarshal %s genesis: %w", batypes.ModuleName, err))
	}
	am.keeper.InitGenesis(ctx, gs)
	return []abci.ValidatorUpdate{}
}

func (am AppModule) ExportGenesis(ctx sdk.Context, _ codec.JSONCodec) json.RawMessage {
	bz, _ := json.Marshal(am.keeper.ExportGenesis(ctx))
	return bz
}

func (AppModule) IsOnePerModuleType() {}
func (AppModule) IsAppModule()        {}

// No BeginBlock or EndBlock. This module is purely reactive: it acts only when
// the Hyperlane mailbox routes a verified receipt to it.

func (am AppModule) GenerateGenesisState(_ *module.SimulationState)       {}
func (am AppModule) RegisterStoreDecoder(_ simtypes.StoreDecoderRegistry) {}
func (am AppModule) WeightedOperations(_ module.SimulationState) []simtypes.WeightedOperation {
	return nil
}
