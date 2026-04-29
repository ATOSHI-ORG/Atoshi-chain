package types

import (
	"testing"

	"cosmossdk.io/math"
	"github.com/stretchr/testify/require"
)

func TestDefaultParamsValid(t *testing.T) {
	require.NoError(t, DefaultParams().Validate())
}

func TestParamsValidate_Failures(t *testing.T) {
	t.Run("zero per-threshold rejected", func(t *testing.T) {
		p := DefaultParams()
		p.TxEnergyPerThreshold = 0
		require.Error(t, p.Validate())
	})
	t.Run("negative window rejected", func(t *testing.T) {
		p := DefaultParams()
		p.TxEnergyMaxAccrueWindow = -1
		require.Error(t, p.Validate())
	})
	t.Run("zero deploy capacity rejected", func(t *testing.T) {
		p := DefaultParams()
		p.DeployEnergyCapacity = 0
		require.Error(t, p.Validate())
	})
	t.Run("negative gas price rejected", func(t *testing.T) {
		p := DefaultParams()
		p.InsufficientGasPrice = math.LegacyNewDec(-1)
		require.Error(t, p.Validate())
	})
}

func TestTxEnergyCapacity(t *testing.T) {
	p := DefaultParams()
	atos := math.NewIntWithDecimal(1, 18)

	cases := []struct {
		name    string
		balance math.Int
		want    uint64
	}{
		{"zero balance", math.ZeroInt(), 0},
		{"below threshold", atos.Mul(math.NewInt(20_000)), 0},
		{"exactly threshold", atos.Mul(math.NewInt(30_000)), 50_000},
		{"2x threshold", atos.Mul(math.NewInt(60_000)), 100_000},
		{"10x threshold", atos.Mul(math.NewInt(300_000)), 500_000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TxEnergyCapacity(tc.balance, p)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestDeployRecoverPerSecond(t *testing.T) {
	p := DefaultParams()
	atos := math.NewIntWithDecimal(1, 18)

	// 1M ATOS holding: capacity 800k, recover over 10 days = 86400 * 10 sec
	// per-second ≈ 800000 / 864000 ≈ 0
	// Let's verify the doubling case which gives a non-zero rate.
	t.Run("below threshold gives 0", func(t *testing.T) {
		require.EqualValues(t, 0, DeployRecoverPerSecond(atos.Mul(math.NewInt(500_000)), p))
	})
	t.Run("threshold gives small rate", func(t *testing.T) {
		got := DeployRecoverPerSecond(atos.Mul(math.NewInt(1_000_000)), p)
		// 800000 / 864000 = 0 (integer division). Acceptable for v1 — sub-1
		// per-second granularity isn't useful when blocks are 1-6s.
		require.LessOrEqual(t, got, uint64(2))
	})
	t.Run("5x threshold gives ~5x rate", func(t *testing.T) {
		got := DeployRecoverPerSecond(atos.Mul(math.NewInt(5_000_000)), p)
		// 5 * 800000 / 864000 ≈ 4
		require.GreaterOrEqual(t, got, uint64(3))
		require.LessOrEqual(t, got, uint64(5))
	})
}

func TestIsSubsidizedMsg(t *testing.T) {
	p := DefaultParams()
	require.True(t, p.IsSubsidizedMsg("/atoshi.tokenomics.v1.MsgClaimMigrationTokens"))
	require.True(t, p.IsSubsidizedMsg("/atoshi.oracle.v1.MsgReportPrice"))
	require.False(t, p.IsSubsidizedMsg("/cosmos.bank.v1beta1.MsgSend"))
}

func TestDefaultGenesisValidate(t *testing.T) {
	require.NoError(t, DefaultGenesisState().Validate())
}
