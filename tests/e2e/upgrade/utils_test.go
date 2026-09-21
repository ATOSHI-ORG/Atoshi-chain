// This file contains unit tests for the e2e package.
package upgrade

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckUpgradeProposalVersion(t *testing.T) {
	testCases := []struct {
		Name string
		Ver  string
		Exp  ProposalVersion
	}{
		{
			Name: "legacy proposal pre v0.47 - v10.0.1",
			Ver:  "v10.0.1",
			Exp:  LegacyProposalPreV50,
		},
		{
			Name: "normal proposal pre v0.46 - v9.1.0",
			Ver:  "v9.1.0",
			Exp:  LegacyProposalPreV46,
		},
		{
			Name: "normal proposal - version with whitespace - v9.1.0",
			Ver:  "\tv9.1.0 ",
			Exp:  LegacyProposalPreV46,
		},
		{
			Name: "normal proposal - version without v - 9.1.0",
			Ver:  "9.1.0",
			Exp:  LegacyProposalPreV46,
		},
		{
			Name: "SDK v0.50 proposal - version with whitespace - v20.0.0",
			Ver:  "\tv20.0.0 ",
			Exp:  UpgradeProposalV50,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			legacyProposal := CheckUpgradeProposalVersion(tc.Ver)
			require.Equal(t, tc.Exp, legacyProposal, "expected: %v, got: %v", tc.Exp, legacyProposal)
		})
	}
}

// TestEvmosVersionsLess tests the EvmosVersions type's Less method with
// different version strings
func TestEvmosVersionsLess(t *testing.T) {
	var version EvmosVersions

	testCases := []struct {
		Name string
		Ver  string
		Exp  bool
	}{
		{
			Name: "higher - v10.0.1",
			Ver:  "v10.0.1",
			Exp:  false,
		},
		{
			Name: "lower - v9.1.0",
			Ver:  "v9.1.0",
			Exp:  true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			version = []string{tc.Ver, "v10.0.0"}
			require.Equal(t, version.Less(0, 1), tc.Exp, "expected: %v, got: %v", tc.Exp, version)
		})
	}
}

// TestEvmosVersionsSwap tests the EvmosVersions type's Swap method
func TestEvmosVersionsSwap(t *testing.T) {
	var version EvmosVersions
	value := "v9.1.0"
	version = []string{value, "v10.0.0"}
	version.Swap(0, 1)
	require.Equal(t, value, version[1], "expected: %v, got: %v", value, version[1])
}

// TestEvmosVersionsLen tests the EvmosVersions type's Len method
func TestEvmosVersionsLen(t *testing.T) {
	var version EvmosVersions = []string{"v9.1.0", "v10.0.0"}
	require.Equal(t, 2, version.Len(), "expected: %v, got: %v", 2, version.Len())
}

// TestRetrieveUpgradesList tests if the list of available upgrades in the codebase
// can be correctly retrieved
func TestRetrieveUpgradesList(t *testing.T) {
	upgradeList, err := RetrieveUpgradesList("../../../app/upgrades")
	require.NoError(t, err, "expected no error while retrieving upgrade list")
	require.NotEmpty(t, upgradeList, "expected upgrade list to be non-empty")

	// Check that every entry parses as a version. The patch component is
	// optional: the inherited upgrades use three components (v19.2.0) but this
	// chain's own use two (v20.1, v20.2, v20.3).
	//
	// The two-component names are NOT normalized to three here on purpose. An
	// upgrade name is the on-chain plan name -- it is what the governance
	// proposal carries and what upgradekeeper looks the handler up by when a
	// node replays the upgrade height. v20.1 through v20.3 have already run on
	// a live chain, so renaming the constants would make any node syncing from
	// genesis halt at that height with "UPGRADE NEEDED" and no matching
	// handler. The test is what adapts.
	//
	// Sorting is unaffected: EvmosVersions.Less uses hashicorp/go-version,
	// which reads v20.1 as 20.1.0.
	for _, upgrade := range upgradeList {
		require.Regexp(t, `^v\d+\.\d+(\.\d+)?(-rc\d+)*$`, upgrade, "expected upgrade version to be in semantic versioning format")
	}
}
