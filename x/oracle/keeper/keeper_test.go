package keeper

import (
	"testing"
	"time"

	"cosmossdk.io/log"
	"cosmossdk.io/math"
	"cosmossdk.io/store"
	"cosmossdk.io/store/metrics"
	storetypes "cosmossdk.io/store/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	tmproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/stretchr/testify/require"

	"github.com/atoshi-chain/atoshi/v20/x/oracle/types"
)

// newOracleKeeperForTest builds a minimal in-memory oracle keeper.
func newOracleKeeperForTest(t *testing.T) (Keeper, sdk.Context) {
	t.Helper()
	storeKey := storetypes.NewKVStoreKey(types.StoreKey)
	db := dbm.NewMemDB()
	cms := store.NewCommitMultiStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	cms.MountStoreWithDB(storeKey, storetypes.StoreTypeIAVL, db)
	require.NoError(t, cms.LoadLatestVersion())

	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)
	authority := sdk.AccAddress([]byte("oracle-authority____"))
	k := NewKeeper(storeKey, cdc, authority)

	header := tmproto.Header{Time: time.Unix(1_700_000_000, 0)}
	ctx := sdk.NewContext(cms, header, false, log.NewNopLogger())
	require.NoError(t, k.SetParams(ctx, types.DefaultParams()))
	return k, ctx
}

// Audit Issue 9 regression: GetPriceHistory previously hardcoded
// []byte{byte(2)} as the iterator prefix, which collides with
// prefixCurrentPrice (= 2). Real history lives under prefixPriceHistory
// (= 3). This test writes N history rows, then verifies the keeper
// returns all of them — not just the current-price row.
func TestGetPriceHistory_UsesCorrectPrefix(t *testing.T) {
	k, ctx := newOracleKeeperForTest(t)

	// Set a current-price entry first so the broken (wrong-prefix)
	// implementation would have picked it up and masked the real bug.
	require.NoError(t, k.SetCurrentPrice(ctx, types.PriceData{
		Price:     math.LegacyMustNewDecFromStr("9.99"),
		Volume24h: math.LegacyMustNewDecFromStr("999"),
		Timestamp: 1_700_000_000,
		Feeder:    "feeder-current",
		Source:    "current",
	}))

	// Write three history rows at distinct timestamps.
	for i, ts := range []int64{1_700_000_100, 1_700_000_200, 1_700_000_300} {
		require.NoError(t, k.AppendPriceHistory(ctx, types.PriceData{
			Price:     math.LegacyMustNewDecFromStr("1.00"),
			Volume24h: math.LegacyMustNewDecFromStr("100"),
			Timestamp: ts,
			Feeder:    "feeder-history",
			Source:    "history",
		}))
		_ = i
	}

	got := k.GetPriceHistory(ctx, 10)
	require.Len(t, got, 3, "expected all three history entries; got %d", len(got))

	for _, pd := range got {
		require.Equal(t, "history", pd.Source,
			"history query must not return current-price entries")
		require.Equal(t, "feeder-history", pd.Feeder)
	}

	// Reverse-prefix iterator returns newest first; verify ordering.
	require.Equal(t, int64(1_700_000_300), got[0].Timestamp)
	require.Equal(t, int64(1_700_000_200), got[1].Timestamp)
	require.Equal(t, int64(1_700_000_100), got[2].Timestamp)
}

// Empty store returns no entries (and does not surface the
// current-price entry as a history row).
func TestGetPriceHistory_EmptyStore(t *testing.T) {
	k, ctx := newOracleKeeperForTest(t)
	require.NoError(t, k.SetCurrentPrice(ctx, types.PriceData{
		Price:     math.LegacyMustNewDecFromStr("1.00"),
		Volume24h: math.LegacyMustNewDecFromStr("1"),
		Timestamp: 1_700_000_000,
	}))
	got := k.GetPriceHistory(ctx, 10)
	require.Empty(t, got)
}

// Audit Issue 10 regression: ExportGenesis → InitGenesis round-trip
// must preserve the newest price as current. Previously InitGenesis
// took PriceHistory[len-1], which after ExportGenesis is the OLDEST
// entry; restarting from a snapshot would publish a stale price.
func TestInitGenesis_PicksNewestPrice(t *testing.T) {
	// Step 1: populate a source keeper with 3 history entries + a current.
	src, srcCtx := newOracleKeeperForTest(t)

	entries := []types.PriceData{
		{Price: math.LegacyMustNewDecFromStr("0.10"), Volume24h: math.LegacyMustNewDecFromStr("1"), Timestamp: 1_700_000_100, Feeder: "oldest"},
		{Price: math.LegacyMustNewDecFromStr("0.15"), Volume24h: math.LegacyMustNewDecFromStr("1"), Timestamp: 1_700_000_200, Feeder: "middle"},
		{Price: math.LegacyMustNewDecFromStr("0.20"), Volume24h: math.LegacyMustNewDecFromStr("1"), Timestamp: 1_700_000_300, Feeder: "newest"},
	}
	for _, pd := range entries {
		require.NoError(t, src.AppendPriceHistory(srcCtx, pd))
	}
	require.NoError(t, src.SetCurrentPrice(srcCtx, entries[2])) // newest

	// Step 2: export from source.
	gs := src.ExportGenesis(srcCtx)
	require.Len(t, gs.PriceHistory, 3)
	require.Equal(t, "newest", gs.PriceHistory[0].Feeder,
		"ExportGenesis returns newest first")

	// Step 3: init a fresh keeper from the exported state.
	dst, dstCtx := newOracleKeeperForTest(t)
	dst.InitGenesis(dstCtx, *gs)

	current, err := dst.GetCurrentPrice(dstCtx)
	require.NoError(t, err)
	require.Equal(t, "newest", current.Feeder,
		"InitGenesis must set the newest entry as current price, not the oldest")
	require.Equal(t, int64(1_700_000_300), current.Timestamp)
}

// End-to-end ReportPrice test driving the msg server. First report
// always accepted (no baseline). Second report within deviation is
// accepted. Third report exceeding deviation is rejected and current
// price stays at the second report.
func TestReportPrice_EnforcesDeviationBps(t *testing.T) {
	k, ctx := newOracleKeeperForTest(t)

	// Allow-list one feeder and tighten params to 10% deviation.
	params := types.DefaultParams()
	params.AllowedFeeders = []string{sdk.AccAddress([]byte("feeder-0-test-account")).String()}
	params.MaxPriceDeviationBps = 1000 // 10%
	params.MinValidReports = 1          // disable multi-feeder gate for this test
	require.NoError(t, k.SetParams(ctx, params))

	srv := NewMsgServerImpl(k)

	// Report 1: first observation, no baseline → accepted at 1.00.
	_, err := srv.ReportPrice(ctx, &types.MsgReportPrice{
		Feeder:    params.AllowedFeeders[0],
		Price:     math.LegacyMustNewDecFromStr("1.00"),
		Volume24h: math.LegacyMustNewDecFromStr("1000"),
		Source:    "test",
	})
	require.NoError(t, err)
	cur, err := k.GetCurrentPrice(ctx)
	require.NoError(t, err)
	require.Equal(t, "1.000000000000000000", cur.Price.String())

	// Report 2: 5% up → accepted (within 10% cap).
	_, err = srv.ReportPrice(ctx, &types.MsgReportPrice{
		Feeder:    params.AllowedFeeders[0],
		Price:     math.LegacyMustNewDecFromStr("1.05"),
		Volume24h: math.LegacyMustNewDecFromStr("1000"),
		Source:    "test",
	})
	require.NoError(t, err)

	// Report 3: 50% up vs 1.05 → rejected.
	_, err = srv.ReportPrice(ctx, &types.MsgReportPrice{
		Feeder:    params.AllowedFeeders[0],
		Price:     math.LegacyMustNewDecFromStr("1.575"),
		Volume24h: math.LegacyMustNewDecFromStr("1000"),
		Source:    "test",
	})
	require.Error(t, err, "deviation > 10% must be rejected")
	require.ErrorIs(t, err, types.ErrPriceDeviationTooHigh)

	// Current price must not have moved to the rejected value.
	cur, err = k.GetCurrentPrice(ctx)
	require.NoError(t, err)
	require.Equal(t, "1.050000000000000000", cur.Price.String())
}

// Audit Issue 4 (b): when MinValidReports > 1, a single feeder's
// report must NOT update the current price; multiple distinct feeders
// must contribute within the staleness window first. History should
// still grow per-report so TWAP and downstream consumers have data.
func TestReportPrice_MinValidReportsGate(t *testing.T) {
	k, ctx := newOracleKeeperForTest(t)
	ctx = ctx.WithBlockTime(time.Unix(1_700_000_000, 0))

	params := types.DefaultParams()
	params.AllowedFeeders = []string{
		sdk.AccAddress([]byte("feeder-1-test-account")).String(),
		sdk.AccAddress([]byte("feeder-2-test-account")).String(),
		sdk.AccAddress([]byte("feeder-3-test-account")).String(),
	}
	params.MaxPriceDeviationBps = 0 // disable deviation cap for this test
	params.MinValidReports = 2
	params.MaxPriceAgeSeconds = 3600
	require.NoError(t, k.SetParams(ctx, params))

	srv := NewMsgServerImpl(k)

	// Report from feeder1 only → history appended but current NOT set.
	_, err := srv.ReportPrice(ctx, &types.MsgReportPrice{
		Feeder:    params.AllowedFeeders[0],
		Price:     math.LegacyMustNewDecFromStr("1.00"),
		Volume24h: math.LegacyMustNewDecFromStr("1000"),
		Source:    "test",
	})
	require.NoError(t, err)

	_, err = k.GetCurrentPrice(ctx)
	require.ErrorIs(t, err, types.ErrPriceNotFound,
		"with MinValidReports=2 and only feeder1 reporting, current should be unset")

	// History should already have feeder1's entry.
	hist := k.GetPriceHistory(ctx, 10)
	require.Len(t, hist, 1)

	// Second distinct feeder reports → quorum met, current updates.
	// (The keys-collision fix lets this work even at the same block
	// time; see TestPriceHistoryKey_NoCollisionWithinSameBlock.)
	_, err = srv.ReportPrice(ctx, &types.MsgReportPrice{
		Feeder:    params.AllowedFeeders[1],
		Price:     math.LegacyMustNewDecFromStr("1.00"),
		Volume24h: math.LegacyMustNewDecFromStr("1100"),
		Source:    "test",
	})
	require.NoError(t, err)
	cur, err := k.GetCurrentPrice(ctx)
	require.NoError(t, err)
	require.Equal(t, params.AllowedFeeders[1], cur.Feeder)
}

// Repeated reports from the SAME feeder don't satisfy MinValidReports.
// Without this distinction, a single rogue feeder could trivially
// fake the quorum.
func TestReportPrice_SameFeederDoesNotSatisfyQuorum(t *testing.T) {
	k, ctx := newOracleKeeperForTest(t)
	ctx = ctx.WithBlockTime(time.Unix(1_700_000_000, 0))

	params := types.DefaultParams()
	params.AllowedFeeders = []string{sdk.AccAddress([]byte("feeder-1-test-account")).String()}
	params.MaxPriceDeviationBps = 0
	params.MinValidReports = 2
	params.MaxPriceAgeSeconds = 3600
	require.NoError(t, k.SetParams(ctx, params))

	srv := NewMsgServerImpl(k)

	for i := 0; i < 5; i++ {
		_, err := srv.ReportPrice(ctx, &types.MsgReportPrice{
			Feeder:    params.AllowedFeeders[0],
			Price:     math.LegacyMustNewDecFromStr("1.00"),
			Volume24h: math.LegacyMustNewDecFromStr("1000"),
			Source:    "test",
		})
		require.NoError(t, err)
	}

	_, err := k.GetCurrentPrice(ctx)
	require.ErrorIs(t, err, types.ErrPriceNotFound,
		"5 reports from the same feeder must not satisfy MinValidReports=2")
}

// Audit Issue 4 (a): MaxPriceDeviationBps must reject reports
// whose price moves more than the configured bps from the current
// on-chain price. Default is 1000 bps (10%).
func TestWithinDeviation_AcceptsAndRejects(t *testing.T) {
	prev := math.LegacyMustNewDecFromStr("1.00")

	// 5% move with 10% cap → accept
	require.True(t, withinDeviation(prev, math.LegacyMustNewDecFromStr("1.05"), 1000))
	require.True(t, withinDeviation(prev, math.LegacyMustNewDecFromStr("0.95"), 1000))

	// 10% move with 10% cap → accept (boundary inclusive)
	require.True(t, withinDeviation(prev, math.LegacyMustNewDecFromStr("1.10"), 1000))

	// 15% move with 10% cap → reject
	require.False(t, withinDeviation(prev, math.LegacyMustNewDecFromStr("1.15"), 1000))
	require.False(t, withinDeviation(prev, math.LegacyMustNewDecFromStr("0.85"), 1000))

	// 100x move with 10% cap → reject (the worst case Issue 4 calls out)
	require.False(t, withinDeviation(prev, math.LegacyMustNewDecFromStr("100"), 1000))

	// Zero-previous baseline → no cap applicable, always accept
	require.True(t, withinDeviation(math.LegacyZeroDec(), math.LegacyMustNewDecFromStr("1.50"), 1000))
}

// Even if a genesis file is hand-edited with entries in arbitrary
// order, findLatestPrice picks by max timestamp.
func TestInitGenesis_RobustToReorderedInput(t *testing.T) {
	k, ctx := newOracleKeeperForTest(t)
	// Deliberately scrambled: middle, newest, oldest.
	gs := types.GenesisState{
		Params: types.DefaultParams(),
		PriceHistory: []types.PriceData{
			{Price: math.LegacyMustNewDecFromStr("0.15"), Volume24h: math.LegacyMustNewDecFromStr("1"), Timestamp: 1_700_000_200, Feeder: "middle"},
			{Price: math.LegacyMustNewDecFromStr("0.20"), Volume24h: math.LegacyMustNewDecFromStr("1"), Timestamp: 1_700_000_300, Feeder: "newest"},
			{Price: math.LegacyMustNewDecFromStr("0.10"), Volume24h: math.LegacyMustNewDecFromStr("1"), Timestamp: 1_700_000_100, Feeder: "oldest"},
		},
	}
	k.InitGenesis(ctx, gs)
	current, err := k.GetCurrentPrice(ctx)
	require.NoError(t, err)
	require.Equal(t, "newest", current.Feeder)
}

// Surfaced during audit Issue 4 fix: PriceHistoryKey used to be
// keyed by timestamp alone, so two reports landing in the same
// block from different feeders would collide on the same KV key
// and the second would overwrite the first. The key now includes
// the feeder address; both reports must coexist in history.
func TestPriceHistoryKey_NoCollisionWithinSameBlock(t *testing.T) {
	k, ctx := newOracleKeeperForTest(t)
	feederA := sdk.AccAddress([]byte("feeder-A-test-account")).String()
	feederB := sdk.AccAddress([]byte("feeder-B-test-account")).String()
	ts := ctx.BlockTime().Unix()

	require.NoError(t, k.AppendPriceHistory(ctx, types.PriceData{
		Price: math.LegacyMustNewDecFromStr("1.00"), Volume24h: math.LegacyMustNewDecFromStr("100"),
		Timestamp: ts, Feeder: feederA,
	}))
	require.NoError(t, k.AppendPriceHistory(ctx, types.PriceData{
		Price: math.LegacyMustNewDecFromStr("1.05"), Volume24h: math.LegacyMustNewDecFromStr("200"),
		Timestamp: ts, Feeder: feederB,
	}))

	hist := k.GetPriceHistory(ctx, 10)
	require.Len(t, hist, 2, "two same-timestamp reports from distinct feeders must both persist")

	// Each feeder's report must be findable, not overwritten.
	feeders := map[string]bool{}
	for _, pd := range hist {
		feeders[pd.Feeder] = true
	}
	require.True(t, feeders[feederA], "feederA's report missing from history")
	require.True(t, feeders[feederB], "feederB's report missing from history")
}

// GetPricesSince also benefits from the key-with-feeder fix: it
// must return all reports from all feeders within the timestamp
// window, not just the last-written one per timestamp.
func TestGetPricesSince_ReturnsAllFeedersAtSameTimestamp(t *testing.T) {
	k, ctx := newOracleKeeperForTest(t)
	ctx = ctx.WithBlockTime(time.Unix(1_700_000_000, 0))

	feeders := []string{
		sdk.AccAddress([]byte("feeder-X-test-account")).String(),
		sdk.AccAddress([]byte("feeder-Y-test-account")).String(),
		sdk.AccAddress([]byte("feeder-Z-test-account")).String(),
	}
	for _, f := range feeders {
		require.NoError(t, k.AppendPriceHistory(ctx, types.PriceData{
			Price: math.LegacyMustNewDecFromStr("1.00"), Volume24h: math.LegacyMustNewDecFromStr("100"),
			Timestamp: ctx.BlockTime().Unix(), Feeder: f,
		}))
	}

	got := k.GetPricesSince(ctx, ctx.BlockTime().Unix()-60)
	require.Len(t, got, 3, "all three same-block reports must be retrievable")
}
