package ante

import (
	"math"
	"testing"

	sdkmath "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
)

// Audit Issue 12: getTxPriority must default to MaxInt64 (not 0) when
// a per-denom gasPrice overflows int64. Aligns with upstream Evmos SDK
// behavior at x/auth/ante/validator_tx_fee.go so the mempool ordering
// is consistent whether energy is enabled or disabled.
func TestGetTxPriority_OverflowDefaultsToMaxInt64(t *testing.T) {
	// gasPrice = fee.Amount / gas. Make Amount = 2^65, gas = 1 → gasPrice
	// = 2^65, which doesn't fit in int64. Old code returned 0 here;
	// fixed code should return MaxInt64.
	amount, ok := sdkmath.NewIntFromString("36893488147419103232") // 2^65
	require.True(t, ok)
	fee := sdk.NewCoins(sdk.NewCoin("aatos", amount))

	got := getTxPriority(fee, 1)
	require.EqualValues(t, int64(math.MaxInt64), got,
		"priority must clamp to MaxInt64 on int64 overflow, "+
			"matching upstream SDK behavior; got %d", got)
}

// Sanity: ordinary gas prices map straight through.
func TestGetTxPriority_NormalGasPrice(t *testing.T) {
	// 1 gwei × 100,000 gas = 100,000,000,000,000 aatos total fee.
	// Divided by gas = 1 gwei = 10^9. Fits comfortably in int64.
	amount, ok := sdkmath.NewIntFromString("100000000000000")
	require.True(t, ok)
	fee := sdk.NewCoins(sdk.NewCoin("aatos", amount))

	got := getTxPriority(fee, 100_000)
	require.EqualValues(t, int64(1_000_000_000), got,
		"expected gasPrice = 1 gwei (10^9); got %d", got)
}

// Multi-denom fee: the lowest per-denom priority wins (per SDK
// semantics). Verifies the overflow branch interacts correctly with
// the priority-min selection logic.
func TestGetTxPriority_MultiDenomTakesMin(t *testing.T) {
	// One denom with normal gas price, another with overflowing price.
	normalAmt, ok := sdkmath.NewIntFromString("100000000000000")
	require.True(t, ok)
	hugeAmt, ok := sdkmath.NewIntFromString("36893488147419103232")
	require.True(t, ok)
	fee := sdk.NewCoins(
		sdk.NewCoin("aatos", normalAmt), // priority 10^9
		sdk.NewCoin("usdc", hugeAmt),    // priority MaxInt64 after fix
	)

	got := getTxPriority(fee, 100_000)
	// Min of (10^9, MaxInt64) = 10^9.
	require.EqualValues(t, int64(1_000_000_000), got)
}

// The local constant must equal stdlib math.MaxInt64.
func TestMaxInt64PriorityConstant(t *testing.T) {
	require.EqualValues(t, int64(math.MaxInt64), maxInt64Priority)
}
