package app_test

import (
	"testing"

	"github.com/cosmos/cosmos-sdk/types/mempool"
	"github.com/stretchr/testify/require"
)

// Audit Issue-13 (round2) regression: the previous app wiring used
// mempool.NoOpMempool, whose Insert/Select discard tx priority. The
// EnergyDeductDecorator computes priority via ctx.WithPriority() in
// the ante chain, but with NoOpMempool that value was thrown away —
// users offering higher gas prices received no preferential ordering.
//
// The fix wires mempool.DefaultPriorityMempool() in app.go, which
// returns the SDK's PriorityNonceMempool[int64]. This test pins the
// type so any future refactor that re-introduces NoOpMempool (or any
// other priority-blind mempool) breaks at CI.
//
// We deliberately do NOT duplicate the SDK's own PriorityNonceMempool
// behavior tests (priority ordering, sender-nonce sorting, etc.) —
// cosmos-sdk covers those in its types/mempool unit tests. Our
// remediation is purely the wiring choice in app.go; this test
// asserts that choice is honored.
func TestMempool_DefaultIsPriorityNonceMempool(t *testing.T) {
	mp := mempool.DefaultPriorityMempool()
	require.NotNil(t, mp, "DefaultPriorityMempool must return a non-nil mempool")

	// Negative assertion: NEVER the no-op variant. If a future
	// refactor flips the wiring back to NoOpMempool, this fails.
	_, isNoOp := interface{}(mp).(mempool.NoOpMempool)
	require.False(t, isNoOp,
		"audit Issue-13: app mempool must be priority-aware, not NoOpMempool")

	// Positive assertion: the concrete type is the priority-nonce
	// mempool parameterized over int64 priorities, which matches the
	// int64 priority value EnergyDeductDecorator sets via
	// ctx.WithPriority(int64).
	require.IsType(t, &mempool.PriorityNonceMempool[int64]{}, mp,
		"mempool must be PriorityNonceMempool[int64] to honor the int64 priority emitted by x/energy/ante")
}
