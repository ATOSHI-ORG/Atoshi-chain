package types_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/atoshi-chain/atoshi/v20/x/oracle/types"
)

// TestParams_DeviationCapCannotBeDisabled pins the invariant that a parameter
// change cannot switch the price deviation check off.
//
// Reported externally as [F3]. ReportPrice guards the check with
// `if params.MaxPriceDeviationBps > 0`, so storing 0 silently removes the only
// bound on what a feeder may report -- nothing errors, nothing logs, reports
// simply stop being checked. Governance widening the cap is fine and still
// allowed; removing it is not.
func TestParams_DeviationCapCannotBeDisabled(t *testing.T) {
	p := types.DefaultParams()
	require.NoError(t, p.Validate(), "defaults must be valid")
	require.NotZero(t, p.MaxPriceDeviationBps, "the default must not be the off switch")

	p.MaxPriceDeviationBps = 0
	require.Error(t, p.Validate(),
		"0 disables the deviation check entirely and must be rejected")

	// A very loose cap is still a cap -- this rejects only the off switch.
	p.MaxPriceDeviationBps = 1_000_000
	require.NoError(t, p.Validate())
}
