package types

import (
	"context"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	oracletypes "github.com/atoshi-chain/atoshi/v20/x/oracle/types"
)

// AccountKeeper defines the expected account keeper interface.
type AccountKeeper interface {
	GetModuleAddress(name string) sdk.AccAddress
	GetModuleAccount(ctx context.Context, moduleName string) sdk.ModuleAccountI
	GetAccount(ctx context.Context, addr sdk.AccAddress) sdk.AccountI
	SetAccount(ctx context.Context, acc sdk.AccountI)
}

// BankKeeper defines the expected bank keeper interface.
type BankKeeper interface {
	GetBalance(ctx context.Context, addr sdk.AccAddress, denom string) sdk.Coin
	GetAllBalances(ctx context.Context, addr sdk.AccAddress) sdk.Coins
	SendCoinsFromModuleToAccount(ctx context.Context, senderModule string, recipientAddr sdk.AccAddress, amt sdk.Coins) error
	SendCoinsFromModuleToModule(ctx context.Context, senderModule, recipientModule string, amt sdk.Coins) error
	MintCoins(ctx context.Context, name string, amt sdk.Coins) error
	GetSupply(ctx context.Context, denom string) sdk.Coin
}

// StakingKeeper defines the expected staking keeper interface.
type StakingKeeper interface {
	TotalBondedTokens(ctx context.Context) (math.Int, error)
	IterateBondedValidatorsByPower(ctx context.Context, fn func(index int64, validator stakingtypes.ValidatorI) bool) error
	GetValidator(ctx context.Context, addr sdk.ValAddress) (stakingtypes.Validator, error)
	Validator(ctx context.Context, addr sdk.ValAddress) (stakingtypes.ValidatorI, error)
}

// DistrKeeper defines the expected distribution keeper interface.
type DistrKeeper interface {
	FundCommunityPool(ctx context.Context, amount sdk.Coins, sender sdk.AccAddress) error
}

// OracleKeeper defines the interface to query the oracle module.
type OracleKeeper interface {
	GetCurrentPrice(ctx sdk.Context) (oracletypes.PriceData, error)
	// Audit Issue 3: tokenomics needs MaxPriceAgeSeconds to reject
	// stale price data before incrementing the tier-release streak.
	GetParams(ctx sdk.Context) oracletypes.Params
}

// AtoxKeeper is the slice of x/atox that tokenomics drives.
//
// Block rewards are denominated in ATOX, and tier releases move the ATOS that
// backs it into the conversion pool. Both live in x/atox, so tokenomics calls
// across rather than duplicating the accounting. The interface takes and returns
// only math.Int and strings, so there is no import of x/atox/types here and no
// cycle: x/atox never references tokenomics, it takes a source module name as a
// plain string.
type AtoxKeeper interface {
	// MintAtoxToModule emits ATOX to a module account. Block rewards go to the
	// fee collector so x/distribution splits them across the active set and their
	// delegators by commission, exactly as it does transaction fees.
	MintAtoxToModule(ctx sdk.Context, module string, amount math.Int) error

	// AddToExchangePool moves ATOS from fromModule into the ATOX conversion pool
	// and advances the conversion index by the matching amount.
	AddToExchangePool(ctx sdk.Context, fromModule string, amount math.Int) error

	// AtoxSupply is the live minted ATOX, used to stop block rewards once the cap
	// is reached instead of erroring every block.
	AtoxSupply(ctx sdk.Context) math.Int

	// AtoxSupplyCap is the ceiling live supply may not exceed.
	AtoxSupplyCap(ctx sdk.Context) math.Int
}
