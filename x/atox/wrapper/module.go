package wrapper

import (
	"fmt"

	"github.com/cosmos/cosmos-sdk/types/module"
	"github.com/cosmos/cosmos-sdk/x/bank"
	"github.com/cosmos/cosmos-sdk/x/bank/exported"
	bankkeeper "github.com/cosmos/cosmos-sdk/x/bank/keeper"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
)

// BankAppModule is x/bank with its Msg service bound to the fee-inclusive
// keeper.
//
// # Why this exists
//
// The fee carve-out lives in a keeper wrapper, and every consumer that resolves
// SendCoins through the bankkeeper.Keeper interface picks it up automatically.
// The bank module itself is the exception: its RegisterServices does
//
//	m := keeper.NewMigrator(am.keeper.(keeper.BaseKeeper), am.legacySubspace)
//
// a type assertion to the CONCRETE BaseKeeper, purely to build the store
// migrator for versions 1->4. Handing it the wrapper panics at startup; handing
// it the base keeper registers a Msg server that bypasses the fee, so a wallet's
// MsgSend would be untaxed while the same transfer through the erc20 precompile
// was taxed.
//
// So the module takes both: the base keeper for the migrations that need its
// concrete type, and the wrapper for the Msg server that users actually reach.
//
// The migrations are re-registered here rather than delegated, because
// AppModule.RegisterServices registers the Msg server too and the router panics
// on a duplicate registration -- there is no way to call it and then replace
// just that one binding.
type BankAppModule struct {
	bank.AppModule

	base           bankkeeper.BaseKeeper
	atox           AtoxKeeper
	legacySubspace exported.Subspace
}

// NewBankAppModule wires the module. `base` must be the concrete keeper the
// migrator needs; `wrapped` is what Msg handling should go through.
func NewBankAppModule(
	inner bank.AppModule,
	base bankkeeper.BaseKeeper,
	atox AtoxKeeper,
	legacySubspace exported.Subspace,
) BankAppModule {
	return BankAppModule{
		AppModule:      inner,
		base:           base,
		atox:           atox,
		legacySubspace: legacySubspace,
	}
}

// RegisterServices mirrors bank.AppModule.RegisterServices, with the Msg server
// bound to the wrapped keeper.
//
// Kept deliberately close to the upstream body so a future SDK bump that adds a
// migration shows up as a diff here rather than as a silently missing upgrade
// step.
func (am BankAppModule) RegisterServices(cfg module.Configurator) {
	banktypes.RegisterMsgServer(cfg.MsgServer(),
		NewMsgServer(bankkeeper.NewMsgServerImpl(am.base), am.base, am.atox))
	banktypes.RegisterQueryServer(cfg.QueryServer(), am.base)

	m := bankkeeper.NewMigrator(am.base, am.legacySubspace)
	if err := cfg.RegisterMigration(banktypes.ModuleName, 1, m.Migrate1to2); err != nil {
		panic(fmt.Sprintf("failed to migrate x/bank from version 1 to 2: %v", err))
	}
	if err := cfg.RegisterMigration(banktypes.ModuleName, 2, m.Migrate2to3); err != nil {
		panic(fmt.Sprintf("failed to migrate x/bank from version 2 to 3: %v", err))
	}
	if err := cfg.RegisterMigration(banktypes.ModuleName, 3, m.Migrate3to4); err != nil {
		panic(fmt.Sprintf("failed to migrate x/bank from version 3 to 4: %v", err))
	}
}
