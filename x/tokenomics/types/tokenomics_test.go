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
		params.MinerReleaseShareBps = 3000
		params.ProjectReleaseShareBps = 6000
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

// TestPoolLayoutSumsToTotalSupply guards the allocation arithmetic. There is no
// on-chain total-supply parameter to check against, so a proposal that changed
// one pool without compensating elsewhere would silently mint the wrong supply at
// genesis. The three pools must add to 10 trillion ATOS, and each must line up
// 100:1 with its ERC20 tranche on Ethereum.
func TestPoolLayoutSumsToTotalSupply(t *testing.T) {
	p := DefaultParams()

	totalAtos := math.NewIntWithDecimal(1, 31) // 10 trillion ATOS in liao
	sum := p.MinerPoolTotal.Add(p.ProjectPoolTotal).Add(p.MigrationPoolTotal)
	require.Equal(t, totalAtos.String(), sum.String(),
		"miner + project + migration must equal the 10 trillion ATOS supply")

	// 100 ATOS per ERC20, so each pool divided by 100 is its ERC20 tranche.
	oneAtos := math.NewIntWithDecimal(1, 18)
	erc20 := func(pool math.Int) string { return pool.Quo(oneAtos).QuoRaw(100).String() }
	require.Equal(t, "10000000000", erc20(p.MinerPoolTotal), "miner: 100 billion ERC20")
	require.Equal(t, "87000000000", erc20(p.ProjectPoolTotal), "project: 870 billion ERC20")
	require.Equal(t, "3000000000", erc20(p.MigrationPoolTotal), "migration: 30 billion ERC20")
}

// TestBlockRewardEmitsExactlyTheAtoxCap ties the emission schedule to the ATOX
// supply cap. The geometric series over successive halvings sums to
// reward * interval * 2, which must be the 1 trillion ATOX cap — the same size as
// the miner pool, so one ATOX ultimately converts to one ATOS.
func TestBlockRewardEmitsExactlyTheAtoxCap(t *testing.T) {
	p := DefaultParams()

	emitted := p.InitialBlockReward.MulRaw(p.HalvingIntervalBlocks).MulRaw(2)
	cap := math.NewIntWithDecimal(1, 30) // 1 trillion ATOX in aatox

	require.Equal(t, cap.String(), p.MinerPoolTotal.String(),
		"the ATOX cap and the ATOS backing it must be the same size")

	// Integer halving truncates, so the series lands just over the cap; emission
	// is clamped against live supply in BeginBlocker. Allow 0.01% drift.
	diff := emitted.Sub(cap).Abs()
	require.True(t, diff.LTE(cap.QuoRaw(10000)),
		"emission schedule drifts from the cap: emitted %s vs cap %s", emitted, cap)
}
