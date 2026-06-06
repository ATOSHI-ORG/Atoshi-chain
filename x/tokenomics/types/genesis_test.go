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
		gs.Params.ImmediateRewardBps = 1
		gs.Params.LockedRewardBps = 1
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
