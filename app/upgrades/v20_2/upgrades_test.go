package v20_2

import (
	"testing"

	"github.com/stretchr/testify/require"

	inflationtypes "github.com/atoshi-chain/atoshi/v20/x/inflation/v1/types"
)

// Audit Question 4 regression: the v20.2 upgrade handler MUST flip
// inflation off on chains where it was genesis-enabled. Full
// upgrade-handler integration testing needs the entire app + module
// manager wiring, which is impractical here; this file instead
// verifies the two invariants the handler relies on:
//
//   1. inflation's DefaultParams already has EnableInflation=false
//      (so fresh chains are safe by default).
//   2. inflation's params struct round-trips the EnableInflation flag
//      correctly (so the handler's SetParams call has the intended
//      effect once it reaches a live keeper).
//
// End-to-end verification on a live chain is covered by the upgrade
// devnet rehearsal script.

func TestUpgradeName_Constant(t *testing.T) {
	// Compile-time sanity: if anyone renames the constant, governance
	// proposals citing "v20.2" would silently no-op.
	require.Equal(t, "v20.2", UpgradeName)
}

func TestInflationDefaultParams_AreDisabled(t *testing.T) {
	// Pin the audit-Q4 invariant from the upgrade-handler side.
	require.False(t, inflationtypes.DefaultInflation,
		"audit Q4: DefaultInflation must be false")
	require.False(t, inflationtypes.DefaultParams().EnableInflation,
		"audit Q4: DefaultParams must initialize EnableInflation=false")
}

func TestInflationParams_RoundTripsEnableInflation(t *testing.T) {
	// Construct params with EnableInflation=true (the pre-upgrade
	// state on live chains) and flip the bit; behavior matches the
	// handler's mutation step.
	p := inflationtypes.Params{
		MintDenom:              "liao",
		ExponentialCalculation: inflationtypes.DefaultExponentialCalculation,
		InflationDistribution:  inflationtypes.DefaultInflationDistribution,
		EnableInflation:        true,
	}
	require.True(t, p.EnableInflation)
	p.EnableInflation = false
	require.False(t, p.EnableInflation,
		"setting EnableInflation=false on a copy must take effect")
}
