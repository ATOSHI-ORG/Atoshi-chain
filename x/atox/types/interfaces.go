package types

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	channeltypes "github.com/cosmos/ibc-go/v8/modules/core/04-channel/types"
)

// ChannelKeeper expected interface.
//
// Needed only to enumerate the escrow addresses ATOX must never reach. See
// Keeper.SendRestriction. Optional: a nil ChannelKeeper disables the check,
// which is what unit tests that assemble a bare keeper want.
type ChannelKeeper interface {
	GetAllChannels(ctx sdk.Context) []channeltypes.IdentifiedChannel
}

// AccountKeeper expected interface.
//
// GetAccount is needed to recognize module accounts, which must be excluded from
// settlement: ATOX passes through the atox module account, fee_collector and
// distribution in transit, and settling those would book conversion claims
// against coins that belong to nobody yet.
type AccountKeeper interface {
	GetAccount(ctx context.Context, addr sdk.AccAddress) sdk.AccountI
	GetModuleAddress(name string) sdk.AccAddress
	GetModuleAccount(ctx context.Context, name string) sdk.ModuleAccountI
}

// BankKeeper expected interface.
//
// GetSupply is read-only bookkeeping: the module enforces the ATOX cap against
// live supply, but never divides by it. The conversion index divides by the
// fixed Params.SupplyCap instead (see atox.proto).
type BankKeeper interface {
	GetBalance(ctx context.Context, addr sdk.AccAddress, denom string) sdk.Coin
	GetSupply(ctx context.Context, denom string) sdk.Coin
	MintCoins(ctx context.Context, module string, amt sdk.Coins) error
	BurnCoins(ctx context.Context, module string, amt sdk.Coins) error
	SendCoinsFromAccountToModule(ctx context.Context, sender sdk.AccAddress, module string, amt sdk.Coins) error
	SendCoinsFromModuleToAccount(ctx context.Context, module string, recipient sdk.AccAddress, amt sdk.Coins) error
	SendCoinsFromModuleToModule(ctx context.Context, from, to string, amt sdk.Coins) error
}
