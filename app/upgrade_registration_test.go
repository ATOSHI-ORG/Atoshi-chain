package app_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	v21_2 "github.com/atoshi-chain/atoshi/v20/app/upgrades/v21_2"
	"github.com/atoshi-chain/atoshi/v20/testutil/integration/evmos/network"
	"github.com/atoshi-chain/atoshi/v20/utils"
)

// TestUpgradeHandlerIsRegistered is the one failure in this release that takes
// the chain down rather than logging.
//
// x/upgrade panics in BeginBlock when the plan height arrives and no handler is
// registered under the plan's name, and every node panics at the same height.
// Recovery is a new binary, built and rolled out to every validator while the
// chain is stopped. Nothing else in the handler can do that: every step inside
// it logs and continues.
//
// The name is also the half that is easy to get wrong, and did get wrong once
// -- an earlier release was published as v20.4 while the repository's newest
// tag was v21.1, because the plan name was being drawn from a separate
// numbering that does not exist. The proposal and the binary have to agree on
// one string, so it is asserted literally here rather than through the constant
// alone.
func TestUpgradeHandlerIsRegistered(t *testing.T) {
	nw := network.NewUnitTestNetwork()

	require.Equal(t, "v21.2", v21_2.UpgradeName,
		"the plan name in the governance proposal must match this exactly")
	require.True(t, nw.App.UpgradeKeeper.HasHandler(v21_2.UpgradeName),
		"no handler for %q: the chain would panic at the upgrade height on every node",
		v21_2.UpgradeName)

	// The predecessor stays registered. A node that has not yet replayed past
	// the v20.4 height needs it to sync from genesis.
	require.True(t, nw.App.UpgradeKeeper.HasHandler("v20.4"))
}

// TestUpgradeHandler_CompensationIsMainnetOnly pins the gate that keeps every
// other network out of the payout.
//
// Asserted against the predicate rather than against a running network on
// purpose: the unit-test harness reports the mainnet chain id itself
// (app.Setup is handed utils.MainnetChainID+"-1"), so a test that asked the
// network would be measuring the harness. What matters for the deployment is
// that a chain on utils.TestnetChainID is declined, and that is stated here.
func TestUpgradeHandler_CompensationIsMainnetOnly(t *testing.T) {
	require.Equal(t, utils.MainnetChainID+"-1", v21_2.MainnetChainID)

	require.True(t, v21_2.ShouldCompensate(utils.MainnetChainID+"-1"))
	require.False(t, v21_2.ShouldCompensate(utils.TestnetChainID+"-1"),
		"the test network must not mint the list: nobody on it holds a key "+
			"for those 1,843 addresses")
	require.False(t, v21_2.ShouldCompensate(utils.MainnetChainID),
		"the epoch suffix is part of the chain id; a bare prefix is not mainnet")
	require.False(t, v21_2.ShouldCompensate(""))
}
