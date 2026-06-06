package types

import (
	"testing"

	"cosmossdk.io/math"
	"github.com/stretchr/testify/require"
)

func TestParamsValidate(t *testing.T) {
	t.Run("default params valid", func(t *testing.T) {
		require.NoError(t, DefaultParams().Validate())
	})

	t.Run("reward bps must sum to 10000", func(t *testing.T) {
		params := DefaultParams()
		params.ImmediateRewardBps = 3000
		params.LockedRewardBps = 6000
		require.Error(t, params.Validate())
	})

	t.Run("release share bps must sum to 10000", func(t *testing.T) {
		params := DefaultParams()
		params.MinerReleaseShareBps = 4000
		params.ProjectReleaseShareBps = 4000
		require.Error(t, params.Validate())
	})

	t.Run("project treasury address must be valid bech32", func(t *testing.T) {
		params := DefaultParams()
		params.ProjectTreasuryAddress = "invalid-address"
		require.Error(t, params.Validate())
	})

	t.Run("halving interval must be positive", func(t *testing.T) {
		params := DefaultParams()
		params.HalvingIntervalBlocks = 0
		require.Error(t, params.Validate())
	})

	t.Run("initial block reward must be positive", func(t *testing.T) {
		params := DefaultParams()
		params.InitialBlockReward = math.ZeroInt()
		require.Error(t, params.Validate())
	})

	t.Run("migration claim end time cannot be negative", func(t *testing.T) {
		params := DefaultParams()
		params.MigrationClaimEndTimeUnix = -1
		require.Error(t, params.Validate())
	})

	t.Run("price check epoch blocks must be positive", func(t *testing.T) {
		params := DefaultParams()
		params.PriceCheckEpochBlocks = 0
		require.Error(t, params.Validate())
	})

	t.Run("default migration claim end time is unbounded", func(t *testing.T) {
		params := DefaultParams()
		require.EqualValues(t, 0, params.MigrationClaimEndTimeUnix)
	})
}
