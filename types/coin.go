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

	// AtoxBaseDenom is the on-chain denomination of ATOX, the mining-reward
	// token. ATOX is deliberately NOT usable for anything ATOS is used for:
	//
	//   - not the staking bond denom
	//   - not the EVM denom
	//   - not accepted as a transaction fee (app/ante/cosmos/min_price.go
	//     restricts fees to BaseDenom)
	//
	// Its only function is to accrue a pro-rata claim on the ATOX exchange
	// pool, which x/atox converts to ATOS as tier releases land.
	AtoxBaseDenom string = "aatox"

	// AtoxBaseDenomUnit mirrors BaseDenomUnit: 1 ATOX = 1x10^18 aatox.
	// Keeping the two tokens at the same precision is what lets the x/atox
	// index be a plain aatos-per-aatox ratio with no scaling factor.
	AtoxBaseDenomUnit = 18

	// AtoxDisplayDenom is the user-facing ATOX symbol.
	AtoxDisplayDenom        string = "ATOX"
	AtoxDisplayDenomTestnet string = "ATOXtest"

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
