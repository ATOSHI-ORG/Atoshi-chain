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

// Audit Issue 11 regression: TxEnergyCapacity used a raw uint64
// multiplication after extracting `units` from the math.Int. If
// units fit in uint64 but units*tx_energy_per_threshold exceeded
// uint64, the result wrapped around to a tiny number — collapsing
// the user's accrual ceiling. With saturatingMulU64 in place, the
// pathological inputs should clamp at MaxUint64 instead.
func TestTxEnergyCapacity_SaturatesOnMultiplyOverflow(t *testing.T) {
	// Construct params where the multiplication is guaranteed to
	// overflow uint64. units = balance/threshold; with threshold = 1
	// and balance = 2^32, units = 2^32. Setting per_threshold = 2^33
	// makes units*per_threshold = 2^65 → wraps to 0 under raw mul.
	p := Params{
		TxEnergyHoldingThreshold: math.NewInt(1),
		TxEnergyPerThreshold:     1 << 33, // 2^33
		TxEnergyMaxAccrueWindow:  86400,
		DeployHoldingThreshold:   math.NewInt(1),
		DeployEnergyCapacity:     1,
		DeployRecoverDays:        1,
		InsufficientGasPrice:     math.LegacyZeroDec(),
	}
	balance := math.NewInt(int64(1) << 32) // 2^32 units

	got := TxEnergyCapacity(balance, p)
	require.Equal(t, ^uint64(0), got,
		"capacity must saturate at MaxUint64 on multiplication overflow, "+
			"not wrap around to a small value")
}

// Sanity: a small balance gives a precise (non-saturated) capacity.
// Guards against the saturating helper accidentally clamping
// legitimate values.
func TestTxEnergyCapacity_ExactForSmallValues(t *testing.T) {
	p := DefaultParams()
	atosUnit := math.NewIntWithDecimal(1, 18)
	// 60,000 ATOS / 30,000 threshold = 2 units → 2 * 50,000 = 100,000.
	got := TxEnergyCapacity(atosUnit.Mul(math.NewInt(60_000)), p)
	require.EqualValues(t, 100_000, got)
}

// Edge: zero balance → zero capacity (already covered by an existing
// path but worth pinning to prevent regressions in the new code).
func TestTxEnergyCapacity_ZeroBalance(t *testing.T) {
	require.EqualValues(t, 0, TxEnergyCapacity(math.ZeroInt(), DefaultParams()))
}

// Direct unit tests for the local saturating multiplier.
func TestSaturatingMulU64(t *testing.T) {
	require.EqualValues(t, 0, saturatingMulU64(0, 100))
	require.EqualValues(t, 0, saturatingMulU64(100, 0))
	require.EqualValues(t, 6, saturatingMulU64(2, 3))
	require.EqualValues(t, ^uint64(0), saturatingMulU64(^uint64(0), 2))
	require.EqualValues(t, ^uint64(0), saturatingMulU64(1<<40, 1<<40)) // 2^80
	// At the boundary: a*b == MaxUint64 exactly should not saturate.
	require.EqualValues(t, ^uint64(0), saturatingMulU64(^uint64(0), 1))
}

// Audit Issue-17 (round2, round1-issue11) regression: same overflow
// class as TxEnergyCapacity but on the DeployRecoverPerSecond path.
// Pre-fix the multiplication `units.Uint64() * p.DeployEnergyCapacity`
// could wrap around uint64 and silently destroy a holder's recovery
// rate. Post-fix the multiplication saturates at MaxUint64 BEFORE
// the divide; the per-second rate is then MaxUint64 / denom — a
// finite ceiling, never zero from wraparound.
func TestDeployRecoverPerSecond_SaturatesOnMultiplyOverflow(t *testing.T) {
	// Force units*capacity to overflow: threshold = 1, balance = 2^32 →
	// units = 2^32. capacity = 2^33 → product = 2^65 → wraps to 0
	// under raw mul, then 0 / denom = 0.
	p := Params{
		TxEnergyHoldingThreshold: math.NewInt(1),
		TxEnergyPerThreshold:     1,
		TxEnergyMaxAccrueWindow:  86400,
		DeployHoldingThreshold:   math.NewInt(1),
		DeployEnergyCapacity:     1 << 33, // 2^33
		DeployRecoverDays:        1,
		InsufficientGasPrice:     math.LegacyZeroDec(),
	}
	balance := math.NewInt(int64(1) << 32) // 2^32 units

	got := DeployRecoverPerSecond(balance, p)
	// Post-fix expectation: MaxUint64 / (1 day in seconds).
	want := ^uint64(0) / (1 * 86_400)
	require.Equal(t, want, got,
		"audit Issue-17: rate must clamp via saturating multiply, not wrap to ~0")
	require.NotEqual(t, uint64(0), got, "wraparound bug would produce 0 here")
}

// Sanity: ordinary balances and default params still produce the
// expected per-second rate. Guards against the saturation helper
// accidentally clamping legitimate values.
func TestDeployRecoverPerSecond_ExactForDefaults(t *testing.T) {
	p := DefaultParams()
	atosUnit := math.NewIntWithDecimal(1, 18)
	// 2,000,000 ATOS / 1,000,000 threshold = 2 units.
	// 2 * 800,000 / (10 * 86400) = 1,600,000 / 864,000 ≈ 1 (integer)
	got := DeployRecoverPerSecond(atosUnit.Mul(math.NewInt(2_000_000)), p)
	want := uint64(2*800_000) / (10 * 86_400)
	require.Equal(t, want, got)
}
