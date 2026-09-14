// Copyright Tharsis Labs Ltd.(Evmos)
// SPDX-License-Identifier:ENCL-1.0(https://github.com/evmos/evmos/blob/main/LICENSE)

package keeper

import (
	"fmt"

	"cosmossdk.io/log"
	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authzkeeper "github.com/cosmos/cosmos-sdk/x/authz/keeper"
	bankkeeper "github.com/cosmos/cosmos-sdk/x/bank/keeper"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	transferkeeper "github.com/atoshi-chain/atoshi/v20/x/ibc/transfer/keeper"

	"github.com/atoshi-chain/atoshi/v20/x/erc20/types"
)

// Keeper of this module maintains collections of erc20.
type Keeper struct {
	storeKey storetypes.StoreKey
	cdc      codec.BinaryCodec
	// the address capable of executing a MsgUpdateParams message. Typically, this should be the x/gov module account.
	authority sdk.AccAddress

	accountKeeper  types.AccountKeeper
	bankKeeper     bankkeeper.Keeper
	evmKeeper      types.EVMKeeper
	stakingKeeper  types.StakingKeeper
	authzKeeper    authzkeeper.Keeper
	transferKeeper *transferkeeper.Keeper

	// bankMsgServer is what the token-pair precompiles send transfers through.
	//
	// Not derived from bankKeeper on purpose: this chain binds bank's Msg
	// service to a wrapper that charges the ATOX transfer fee, and a locally
	// built server is the stock one -- which would make ERC20 transfer() a
	// fee-free route for ATOX. app.go injects the real one with
	// SetBankMsgServer; nil keeps the stock behaviour.
	bankMsgServer banktypes.MsgServer
}

// SetBankMsgServer injects the Msg server the token-pair precompiles transfer
// through. Call it once at wiring time, before any precompile is instantiated
// (they are built lazily per token pair, so this must happen during app setup).
func (k *Keeper) SetBankMsgServer(srv banktypes.MsgServer) { k.bankMsgServer = srv }

// BankMsgServer exposes what the token-pair precompiles will transfer through,
// so the wiring can be asserted from outside instead of being trusted.
func (k Keeper) BankMsgServer() banktypes.MsgServer { return k.bankMsgServer }

// NewKeeper creates new instances of the erc20 Keeper
func NewKeeper(
	storeKey storetypes.StoreKey,
	cdc codec.BinaryCodec,
	authority sdk.AccAddress,
	ak types.AccountKeeper,
	bk bankkeeper.Keeper,
	evmKeeper types.EVMKeeper,
	sk types.StakingKeeper,
	authzKeeper authzkeeper.Keeper,
	transferKeeper *transferkeeper.Keeper,
) Keeper {
	// ensure gov module account is set and is not nil
	if err := sdk.VerifyAddressFormat(authority); err != nil {
		panic(err)
	}

	return Keeper{
		authority:      authority,
		storeKey:       storeKey,
		cdc:            cdc,
		accountKeeper:  ak,
		bankKeeper:     bk,
		evmKeeper:      evmKeeper,
		stakingKeeper:  sk,
		authzKeeper:    authzKeeper,
		transferKeeper: transferKeeper,
	}
}

// Logger returns a module-specific logger.
func (k Keeper) Logger(ctx sdk.Context) log.Logger {
	return ctx.Logger().With("module", fmt.Sprintf("x/%s", types.ModuleName))
}
