// Copyright Tharsis Labs Ltd.(Evmos)
// SPDX-License-Identifier:ENCL-1.0(https://github.com/evmos/evmos/blob/main/LICENSE)
package types

import (
	"math/big"

	sdkmath "cosmossdk.io/math"
)

const (
	// BaseDenom defines the default coin denomination used in Atoshi in:
	//
	// - Staking parameters: denomination used as stake in the dPoS chain
	// - Mint parameters: denomination minted due to fee distribution rewards
	// - Governance parameters: denomination used for spam prevention in proposal deposits
	// - Crisis parameters: constant fee denomination used for spam prevention to check broken invariant
	// - EVM parameters: denomination used for running EVM state transitions in Atoshi.
	BaseDenom        string = "aatos"
	BaseDenomTestnet string = "ataatos"

	// BaseDenomUnit defines the base denomination unit for Atoshi.
	// 1 atos = 1x10^{BaseDenomUnit} aatos
	BaseDenomUnit = 18

	// DisplayDenom defines the denomination displayed to users in client applications.
	DisplayDenom        string = "atos"
	DisplayDenomTestnet string = "tatos"

	// DefaultGasPrice is default gas price for evm transactions
	DefaultGasPrice = 20
)

// PowerReduction defines the default power reduction value for staking
var PowerReduction = sdkmath.NewIntFromBigInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(BaseDenomUnit), nil))
