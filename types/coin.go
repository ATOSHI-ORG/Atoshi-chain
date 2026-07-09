// Copyright Tharsis Labs Ltd.(Evmos)
// SPDX-License-Identifier:ENCL-1.0(https://github.com/evmos/evmos/blob/main/LICENSE)
package types

import (
	"math/big"

	sdkmath "cosmossdk.io/math"
)

const (
	// BaseDenom defines the on-chain (technical) denomination used in
	// Atoshi for:
	//
	// - Staking parameters: denomination used as stake in the dPoS chain
	// - Mint parameters: denomination minted due to fee distribution rewards
	// - Governance parameters: denomination used for spam prevention in proposal deposits
	// - Crisis parameters: constant fee denomination used for spam prevention to check broken invariant
	// - EVM parameters: denomination used for running EVM state transitions in Atoshi.
	//
	// Mainnet and testnet share the same BaseDenom (`aatos`) per the
	// Cosmos-ecosystem convention; the chain ID differentiates the
	// two networks. The atto-prefix `a` follows the `uatom` / `uosmo`
	// pattern (10^-18 multiplier, given BaseDenomUnit = 18).
	BaseDenom        string = "aatos"
	BaseDenomTestnet string = "aatos"

	// BaseDenomUnit defines the base denomination unit for Atoshi.
	// 1 ATOS = 1x10^{BaseDenomUnit} aatos
	BaseDenomUnit = 18

	// DisplayDenom is the user-facing token symbol shown in wallets,
	// explorers, and exchange listings. Mainnet uses ATOS, testnet
	// uses ATOStest so users can visually distinguish the two
	// networks even when both bech32 prefixes and chain IDs are
	// similar.
	DisplayDenom        string = "ATOS"
	DisplayDenomTestnet string = "ATOStest"

	// DefaultGasPrice is default gas price for evm transactions
	DefaultGasPrice = 20
)

// PowerReduction defines the default power reduction value for staking
var PowerReduction = sdkmath.NewIntFromBigInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(BaseDenomUnit), nil))
