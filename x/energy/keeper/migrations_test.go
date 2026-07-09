package keeper

import (
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
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

// All stored accounts get refreshed in a single sweep — the iterator
// walks the full store, not a single record. Production has thousands
// of accounts; this guards against an off-by-one in the iteration.
func TestRefreshAllSnapshots_MultipleAccounts(t *testing.T) {
	k, ctx, bank := newKeeperForTest(t)

	type fixture struct {
		addr     sdk.AccAddress
		bank     math.Int
		expected math.Int
	}
	cases := []fixture{
		{addr("alice___________________"), math.NewIntWithDecimal(3_000_000, 18), math.NewIntWithDecimal(3_000_000, 18)},
		{addr("bob_____________________"), math.NewIntWithDecimal(60_000, 18), math.NewIntWithDecimal(60_000, 18)},
		{addr("carol___________________"), math.NewIntWithDecimal(120_000, 18), math.NewIntWithDecimal(120_000, 18)},
		{addr("dave____________________"), math.NewIntWithDecimal(15_000, 18), math.NewIntWithDecimal(15_000, 18)}, // below threshold, still gets refreshed
	}

	for _, f := range cases {
		k.SetEnergyAccount(ctx, types.EnergyAccount{
			Address:             f.addr.String(),
			LastBalanceSnapshot: math.NewIntWithDecimal(10_000, 18), // all stale
			LastUpdatedTime:     ctx.BlockTime().Unix() - 86400,
		})
		bank.balances[f.addr.String()] = f.bank
	}

	n := k.RefreshAllSnapshots(ctx)
	require.Equal(t, len(cases), n, "every account should be refreshed in one pass")

	for _, f := range cases {
		got := k.GetEnergyAccount(ctx, f.addr)
		require.True(t, got.LastBalanceSnapshot.Equal(f.expected),
			"addr=%s expected snapshot=%s got=%s", f.addr.String(), f.expected, got.LastBalanceSnapshot)
	}
}

// If an account's previously-accrued energy now exceeds the new capacity
// (because the holder lost ATOS but the bug left the snapshot inflated),
// the refresh must clip accrued down. Otherwise the holder would walk
// away from the upgrade with more spendable energy than their balance
// backs — equivalent to free gas.
func TestRefreshAllSnapshots_CapsDownAccrued(t *testing.T) {
	k, ctx, bank := newKeeperForTest(t)
	a := addr("alice___________________")

	// Pre-upgrade: snapshot reflected a 90k ATOS holding (capacity 150k);
	// account accrued 120k. But the holder has since transferred funds
	// out and now holds only 30k ATOS (capacity 50k). Snapshot didn't
	// follow the outflow because of the bug.
	k.SetEnergyAccount(ctx, types.EnergyAccount{
		Address:             a.String(),
		LastBalanceSnapshot: math.NewIntWithDecimal(90_000, 18),
		LastUpdatedTime:     ctx.BlockTime().Unix() - 86400,
		TxEnergyAccrued:     120_000, // over the post-refresh cap
		DeployEnergyAccrued: 0,
	})
	bank.balances[a.String()] = math.NewIntWithDecimal(30_000, 18)

	n := k.RefreshAllSnapshots(ctx)
	require.Equal(t, 1, n)

	got := k.GetEnergyAccount(ctx, a)
	require.True(t, got.LastBalanceSnapshot.Equal(math.NewIntWithDecimal(30_000, 18)),
		"snapshot follows actual balance")

	// New capacity = floor(30k / 30k) × 50k = 50k. Accrued must be clipped.
	require.EqualValues(t, 50_000, got.TxEnergyAccrued,
		"accrued energy must be capped to the new capacity, not retained at 120k")
}

// Empty store is a no-op — no iteration target, returns 0 cleanly. This
// happens if the upgrade fires on a chain where no account has ever had
// its EnergyAccount persisted (e.g. very early in testnet life).
func TestRefreshAllSnapshots_EmptyStore(t *testing.T) {
	k, ctx, _ := newKeeperForTest(t)
	n := k.RefreshAllSnapshots(ctx)
	require.Equal(t, 0, n, "empty store yields zero refreshed accounts")
}
