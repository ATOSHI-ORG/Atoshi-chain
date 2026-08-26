package types

import (
	"context"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// AccountKeeper expected interface (we only need address derivation).
type AccountKeeper interface {
	GetModuleAddress(name string) sdk.AccAddress
	GetModuleAccount(ctx context.Context, name string) sdk.ModuleAccountI
}

// BankKeeper expected interface.
type BankKeeper interface {
	GetBalance(ctx context.Context, addr sdk.AccAddress, denom string) sdk.Coin
	SendCoinsFromAccountToModule(ctx context.Context, sender sdk.AccAddress, module string, amt sdk.Coins) error
	SendCoinsFromModuleToAccount(ctx context.Context, module string, recipient sdk.AccAddress, amt sdk.Coins) error
}

// StakingKeeper exposes the two aggregate accessors EligibleBalance needs.
//
// Staked ATOS never leaves the delegator's ownership — Delegate only moves the
// coins into bonded_tokens_pool, and Undelegate brings them back — so it belongs
// in the energy-eligible total just as locked ATOS does. Without this, staking
// drops the bank balance and the holder's energy is clamped to the reduced
// capacity, forcing a choice between mining ATOX and keeping free transfers.
//
// GetDelegatorUnbonding is included for the same reason: unbonding coins sit in
// not_bonded_tokens_pool, so they are in neither the bank balance nor the bonded
// total. Counting only bonded would zero a holder's energy for the whole
// unbonding period the moment they start undelegating — the same bug in a
// different place.
//
// Both are aggregate reads (math.Int), which keeps x/energy from importing
// x/staking/types.
type StakingKeeper interface {
	GetDelegatorBonded(ctx context.Context, delegator sdk.AccAddress) (math.Int, error)
	GetDelegatorUnbonding(ctx context.Context, delegator sdk.AccAddress) (math.Int, error)
}

// FeemarketKeeper exposes the subset of x/feemarket the energy module
// needs. Used by the EstimateFee query to mirror real-world charging:
// txs that the wallet actually broadcasts offer fee = gasLimit ×
// min_gas_price, so estimate_fee should compute against the same rate
// rather than against InsufficientGasPrice (which is the audit floor,
// not the typical charge price). Without this, /estimate_fee under-
// reports ATOS gas by ~10^12 — 270k shortfall returns 567 liao
// (≈5.67e-16 ATOS) instead of 0.00027 ATOS.
//
// Wired as a single accessor rather than the full FeeMarketKeeper to
// keep this interface independent of x/feemarket/types (no Go import
// of that package from x/energy, no risk of an import cycle if
// feemarket later depends on something in energy).
type FeemarketKeeper interface {
	GetMinGasPrice(ctx sdk.Context) math.LegacyDec
}

// MathInt aliases avoid leaking the math package into call sites that
// already import sdk.
type MathInt = math.Int
