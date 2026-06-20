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

	got := getTxPriority(fee, 1, "aatos")
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

	got := getTxPriority(fee, 100_000, "aatos")
	require.EqualValues(t, int64(1_000_000_000), got,
		"expected gasPrice = 1 gwei (10^9); got %d", got)
}

// Audit Issue-15 (round1-issue8) regression: non-base-denom coins in
// the fee bag must NOT participate in priority calculation. Pre-fix,
// getTxPriority iterated every coin and let the min-selection pick
// up arbitrary alt-coin denominations — an attacker could pad a tx
// with a tiny IBC voucher amount (e.g. 1 unit) to drive their
// per-gas priority for that denom to zero (1 / 200k = 0 via integer
// QuoRaw), then ride to the front of the mempool min-ordering.
//
// Post-fix the function takes baseDenom and skips everything else.
// The bag below has a normal aatos fee plus a 1-unit IBC voucher
// that, pre-fix, would have shrunk priority to 0; post-fix the
// voucher is ignored and priority reflects the aatos gas price.
func TestGetTxPriority_OnlyBaseDenomCounts(t *testing.T) {
	normalAmt, ok := sdkmath.NewIntFromString("100000000000000")
	require.True(t, ok)
	fee := sdk.NewCoins(
		sdk.NewCoin("aatos", normalAmt),                // gasPrice 10^9
		sdk.NewCoin("ibc/abc123", sdkmath.NewInt(1)),   // would give priority=0 pre-fix
		sdk.NewCoin("usdc", sdkmath.NewInt(999_999_9)), // arbitrary noise
	)

	got := getTxPriority(fee, 100_000, "aatos")
	require.EqualValues(t, int64(1_000_000_000), got,
		"audit Issue-15: non-base coins must be skipped; pre-fix a 1-unit IBC voucher would have dragged priority to 0")
}

// Audit Issue-15 (round1-issue8) companion: when the fee bag carries
// NO base-denom coin at all, priority stays 0 (lowest). This matches
// "no valid fee offered → no preferential ordering" — the chain
// hasn't been paid in the unit that gas is denominated in.
func TestGetTxPriority_NoBaseDenomReturnsZero(t *testing.T) {
	fee := sdk.NewCoins(
		sdk.NewCoin("ibc/abc123", sdkmath.NewInt(1_000_000)),
		sdk.NewCoin("usdc", sdkmath.NewInt(50_000_000)),
	)
	got := getTxPriority(fee, 100_000, "aatos")
	require.EqualValues(t, int64(0), got,
		"audit Issue-15: a fee bag without aatos earns no priority")
}

// The local constant must equal stdlib math.MaxInt64.
func TestMaxInt64PriorityConstant(t *testing.T) {
	require.EqualValues(t, int64(math.MaxInt64), maxInt64Priority)
}
