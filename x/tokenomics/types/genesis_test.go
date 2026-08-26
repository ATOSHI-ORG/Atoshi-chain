package types

import (
	"testing"

	"cosmossdk.io/math"
	"github.com/stretchr/testify/require"
)

func TestGenesisStateValidate(t *testing.T) {
	t.Run("default genesis valid", func(t *testing.T) {
		require.NoError(t, DefaultGenesisState().Validate())
	})

	t.Run("invalid params make genesis invalid", func(t *testing.T) {
		gs := DefaultGenesisState()
		gs.Params.MinerPoolTotal = math.NewInt(-1)
		require.Error(t, gs.Validate())
	})

	t.Run("default genesis project claimable initialized", func(t *testing.T) {
		gs := DefaultGenesisState()
		require.True(t, gs.ProjectClaimable.Equal(math.ZeroInt()))
	})

	t.Run("release state carries last check time", func(t *testing.T) {
		gs := DefaultGenesisState()
		gs.ReleaseState.LastCheckTimeUnix = 123
		require.NoError(t, gs.Validate())
		require.Equal(t, int64(123), gs.ReleaseState.LastCheckTimeUnix)
	})
}

// Audit Recommendation-1 (round2) regression: GenesisState.Validate
// must reject malformed values in every subfield, not just Params.
// Pre-fix the function returned nil for any non-Params problem,
// which would have let a hostile or buggy genesis ship the chain
// with nil math.Int fields (panic on first arithmetic), negative
// running totals, duplicate validator entries, or
// (claimed + claimable) > accrued in the miner locked balance table.
func TestGenesisStateValidate_RejectsMalformedSubFields(t *testing.T) {
	t.Run("release_state nil math.Int rejected", func(t *testing.T) {
		gs := DefaultGenesisState()
		gs.ReleaseState.TotalMinerReleased = math.Int{} // nil
		require.ErrorContains(t, gs.Validate(), "total_miner_released")
	})

	t.Run("release_state negative running total rejected", func(t *testing.T) {
		gs := DefaultGenesisState()
		gs.ReleaseState.TotalProjectReleased = math.NewInt(-1)
		require.ErrorContains(t, gs.Validate(), "total_project_released")
	})

	t.Run("release_state negative LastCheckBlock rejected", func(t *testing.T) {
		gs := DefaultGenesisState()
		gs.ReleaseState.LastCheckBlock = -5
		require.ErrorContains(t, gs.Validate(), "last_check_block")
	})

	t.Run("block_reward_state nil rejected", func(t *testing.T) {
		gs := DefaultGenesisState()
		gs.BlockRewardState.TotalDistributed = math.Int{}
		require.ErrorContains(t, gs.Validate(), "total_distributed")
	})

	t.Run("block_reward_state negative rejected", func(t *testing.T) {
		gs := DefaultGenesisState()
		gs.BlockRewardState.TotalDistributed = math.NewInt(-1)
		require.ErrorContains(t, gs.Validate(), "total_distributed")
	})

	t.Run("project_claimable nil rejected", func(t *testing.T) {
		gs := DefaultGenesisState()
		gs.ProjectClaimable = math.Int{}
		require.ErrorContains(t, gs.Validate(), "project_claimable")
	})

}
