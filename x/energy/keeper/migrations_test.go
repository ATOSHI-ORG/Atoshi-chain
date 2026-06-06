package keeper

import (
	"testing"

	"cosmossdk.io/math"
	"github.com/stretchr/testify/require"

	"github.com/atoshi-chain/atoshi/v20/x/energy/types"
)

// Reproduces the production bug at the storage layer: a wallet with a
// stale snapshot from before SendRestriction was wired. After running
// RefreshAllSnapshots, the snapshot equals the current bank balance and
// the capacity query (computed from snapshot) returns the right value
// without the user having to broadcast a warm-up tx.
func TestRefreshAllSnapshots_FixesStaleSnapshot(t *testing.T) {
	k, ctx, bank := newKeeperForTest(t)
	addr := addr("alice___________________")

	// Pre-upgrade ledger state: account thinks balance is 10k, but bank
	// (the source of truth) holds 3M because every inbound transfer
	// silently bypassed OnBalanceChange.
	k.SetEnergyAccount(ctx, types.EnergyAccount{
		Address:             addr.String(),
		LastBalanceSnapshot: math.NewIntWithDecimal(10_000, 18),
		LastUpdatedTime:     ctx.BlockTime().Unix() - 86400,
	})
	bank.balances[addr.String()] = math.NewIntWithDecimal(3_000_000, 18)

	// Capacity computed from the stale snapshot is 0 (10k < 30k threshold).
	staleCap := types.TxEnergyCapacity(
		k.GetEnergyAccount(ctx, addr).LastBalanceSnapshot, k.GetParams(ctx))
	require.EqualValues(t, 0, staleCap, "stale snapshot below threshold yields zero capacity")

	n := k.RefreshAllSnapshots(ctx)
	require.Equal(t, 1, n)

	got := k.GetEnergyAccount(ctx, addr)
	require.True(t, got.LastBalanceSnapshot.Equal(math.NewIntWithDecimal(3_000_000, 18)),
		"snapshot should now match bank, got %s", got.LastBalanceSnapshot)

	// 3M / 30k threshold = 100 units * 50_000 capacity = 5,000,000.
	fixedCap := types.TxEnergyCapacity(got.LastBalanceSnapshot, k.GetParams(ctx))
	require.EqualValues(t, 5_000_000, fixedCap)
}

// Migration is idempotent — running twice produces the same state as
// once. We rely on this so a partial upgrade retry can't corrupt
// accounts.
func TestRefreshAllSnapshots_Idempotent(t *testing.T) {
	k, ctx, bank := newKeeperForTest(t)
	a := addr("alice___________________")
	k.SetEnergyAccount(ctx, types.EnergyAccount{
		Address:             a.String(),
		LastBalanceSnapshot: math.NewIntWithDecimal(10_000, 18),
		LastUpdatedTime:     ctx.BlockTime().Unix() - 86400,
	})
	bank.balances[a.String()] = math.NewIntWithDecimal(500_000, 18)

	k.RefreshAllSnapshots(ctx)
	first := k.GetEnergyAccount(ctx, a)

	k.RefreshAllSnapshots(ctx)
	second := k.GetEnergyAccount(ctx, a)

	require.True(t, first.LastBalanceSnapshot.Equal(second.LastBalanceSnapshot))
	require.Equal(t, first.LastUpdatedTime, second.LastUpdatedTime)
	require.Equal(t, first.TxEnergyAccrued, second.TxEnergyAccrued)
}
