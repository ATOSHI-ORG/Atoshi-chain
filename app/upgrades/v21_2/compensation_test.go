package v21_2_test

import (
	"testing"

	"cosmossdk.io/math"
	"github.com/stretchr/testify/require"

	v21_2 "github.com/atoshi-chain/atoshi/v20/app/upgrades/v21_2"
)

// TestLoadPayouts_MatchesTheList is the arithmetic the mainnet step depends on,
// checked where it costs nothing to check.
//
// The handler mints exactly what this returns, so every figure here is one that
// cannot be corrected afterwards by any proposal. Running it on every build is
// the only place a re-exported or hand-edited list gets caught before it
// becomes supply.
func TestLoadPayouts_MatchesTheList(t *testing.T) {
	payouts, total, err := v21_2.LoadPayouts()
	require.NoError(t, err)

	require.Len(t, payouts, 1843)
	require.Equal(t, "41965283235217000000000000", total.String(),
		"41,965,283.235217 ATOS")

	// Re-added independently of the running total the loader keeps, so a bug in
	// that accumulator cannot agree with itself.
	sum := math.ZeroInt()
	for _, p := range payouts {
		require.False(t, p.Amount.IsNil())
		require.True(t, p.Amount.IsPositive())
		require.Len(t, p.Address, 20)
		require.NotEmpty(t, p.Bech32)
		sum = sum.Add(p.Amount)
	}
	require.Equal(t, total.String(), sum.String())
}

// TestLoadPayouts_ScalesTheSixDecimalColumnExactly pins the unit conversion.
//
// The column is whole ATOS with six decimals and the chain stores base units,
// so every amount is multiplied by 1e18. Getting this wrong by a factor of ten
// is the single most expensive mistake available in this file, and it would
// still satisfy a row count.
func TestLoadPayouts_ScalesTheSixDecimalColumnExactly(t *testing.T) {
	payouts, _, err := v21_2.LoadPayouts()
	require.NoError(t, err)

	// The first row of the list: 1,011,103.999000 ATOS, six decimals, which is
	// where a truncation or a rounding mode would show.
	require.Equal(t,
		"atoshi1veze43cdajxcm2g3xgfgcnulqp36ce0f0v82sa", payouts[0].Bech32)
	require.Equal(t, "1011103999000000000000000", payouts[0].Amount.String())
}

// TestOwnSelfDelegations_AreInTheList guards the switch in constants.go. If
// either address were misspelled the flag would silently do nothing, and the
// difference -- 2,000,000 ATOS of new supply -- would never surface.
func TestOwnSelfDelegations_AreInTheList(t *testing.T) {
	payouts, _, err := v21_2.LoadPayouts()
	require.NoError(t, err)

	require.Len(t, v21_2.OwnSelfDelegations, 2)

	found := map[string]math.Int{}
	for _, p := range payouts {
		if _, own := v21_2.OwnSelfDelegations[p.Bech32]; own {
			found[p.Bech32] = p.Amount
		}
	}

	if v21_2.CompensateOwnSelfDelegations {
		require.Len(t, found, 2, "both self-delegations must be present when they are paid")
		for addr, amt := range found {
			require.Equal(t, "1000000000000000000000000", amt.String(),
				"%s is listed at 1,000,000 ATOS", addr)
		}
		require.Equal(t, 1843, len(payouts))
	} else {
		require.Empty(t, found, "the self-delegations must be dropped when they are not paid")
		require.Equal(t, 1841, len(payouts))
	}
}
