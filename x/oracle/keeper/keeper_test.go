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
