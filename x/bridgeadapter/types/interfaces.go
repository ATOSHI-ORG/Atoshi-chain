package types

import (
	"context"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/bcp-innovations/hyperlane-cosmos/util"
)

// AtoxKeeper receives the miner share of a confirmed release.
type AtoxKeeper interface {
	// AddToExchangePool moves ATOS from fromModule into the ATOX conversion pool
	// and advances the conversion index by the matching amount.
	AddToExchangePool(ctx sdk.Context, fromModule string, amount math.Int) error
}

// TokenomicsKeeper supplies what tier judgments authorised and receives the
// project share of a confirmed release.
//
// The authorised totals are read to cross-check receipts: Ethereum can only
// release what Atoshi's tier engine authorised, so a receipt claiming more means
// either a bug or a forged message, and is rejected rather than trusted.
type TokenomicsKeeper interface {
	// AuthorizedReleases returns the cumulative miner and project shares that
	// tier judgments have authorised, in ATOS.
	AuthorizedReleases(ctx sdk.Context) (miner, project math.Int)

	// GetProjectClaimable / SetProjectClaimable carry the counter authorising
	// migration-pool top-ups out of the project pool.
	GetProjectClaimable(ctx sdk.Context) math.Int
	SetProjectClaimable(ctx sdk.Context, amount math.Int)

	// MinerPoolName is the module account holding the ATOS that backs ATOX,
	// which is where the conversion pool draws from.
	MinerPoolName() string

	// MigrationPoolName is the module account the asset bridge locks ATOS into
	// and releases from. It is the bridge's counterparty: neither side can mint,
	// so outbound locks here and inbound pays out of the same balance.
	MigrationPoolName() string
	MigrationPoolBalance(ctx sdk.Context) math.Int

	// MigrationPoolTotal is the pool's configured size, used as the denominator
	// for the crisis-mode floor.
	MigrationPoolTotal(ctx sdk.Context) math.Int

	// BaseDenom is the ATOS denom.
	BaseDenom() string
}

// BankKeeper expected interface.
type BankKeeper interface {
	SendCoinsFromAccountToModule(ctx context.Context, sender sdk.AccAddress, module string, amt sdk.Coins) error
	SendCoinsFromModuleToAccount(ctx context.Context, module string, recipient sdk.AccAddress, amt sdk.Coins) error
}

// CoreKeeper is the slice of Hyperlane x/core this module needs.
//
// Only the app router: the adapter registers itself there at app init so the
// mailbox can route receipts to it, and draws its own recipient address from the
// router's sequence at genesis.
type CoreKeeper interface {
	AppRouter() *util.Router[util.HyperlaneApp]

	// DispatchMessage sends an outbound message through the mailbox.
	DispatchMessage(
		ctx sdk.Context,
		originMailboxId util.HexAddress,
		sender util.HexAddress,
		maxFee sdk.Coins,
		destinationDomain uint32,
		recipient util.HexAddress,
		body []byte,
		metadata util.StandardHookMetadata,
		postDispatchHookId *util.HexAddress,
	) (util.HexAddress, error)
}
