package keeper

import (
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
)

// Audit Issue 5 regression: prior to the fix, Delegate computed
// freeBalance = bank_balance - currentLockedAtos. Because the previously
// locked ATOS had already been moved out of the delegator's bank
// balance (SendCoinsFromAccountToModule), subtracting LockedAtos again
// double-counted the lock. The second Delegate call from the same
// account would fail with ErrInsufficientBalance even when the user
// still held plenty of free ATOS.
//
// This test simulates two sequential delegations and asserts both
// succeed. With the bug present, the second call returned the
// double-deduct error.
func TestDelegate_DoesNotDoubleCountLockedBalance(t *testing.T) {
	k, ctx, bank := newKeeperForTest(t)

	delegator := addr("delegator_______________")
	delegatee1 := addr("delegatee1______________")
	delegatee2 := addr("delegatee2______________")

	// Delegator starts with 90,000 ATOS, capacity = 150k.
	// Default params: threshold = 30k ATOS, per_threshold = 50k energy.
	bank.balances[delegator.String()] = math.NewIntWithDecimal(90_000, 18)

	// Settle once so TxEnergyAccrued initializes against the 90k snapshot.
	delAcct := k.Settle(ctx, delegator)
	delAcct.TxEnergyAccrued = 150_000 // simulate fully refilled
	k.SetEnergyAccount(ctx, delAcct)

	// First delegation: 50,000 energy → locks 30,000 ATOS (1 threshold block).
	id1, locked1, err := k.Delegate(ctx, delegator, delegatee1, 50_000, 3600)
	require.NoError(t, err, "first delegation should succeed")
	require.NotZero(t, id1)
	require.True(t, locked1.Equal(math.NewIntWithDecimal(30_000, 18)),
		"first lock should be 30k ATOS; got %s", locked1)

	// At this point bank.balance(delegator) = 90k - 30k = 60k.
	// energy account: LockedAtos = 30k, DelegatedOut = 50,000.
	postFirstBal := bank.balances[delegator.String()]
	require.True(t, postFirstBal.Equal(math.NewIntWithDecimal(60_000, 18)),
		"bank balance after first delegate should drop to 60k; got %s", postFirstBal)

	postFirst := k.GetEnergyAccount(ctx, delegator)
	require.True(t, postFirst.LockedAtos.Equal(math.NewIntWithDecimal(30_000, 18)),
		"LockedAtos should track 30k after first delegate")

	// Second delegation: another 50,000 energy → another 30,000 ATOS lock.
	// Free balance is 60k, the new lock is 30k, so this must succeed.
	// With the bug present, the keeper computed freeBalance = 60k - 30k = 30k
	// and saw it as exactly equal to the new lock — which still passed —
	// but a delegator with anywhere from 60k to 89k would have a tighter
	// margin and fail. Here we drop accrued and try the same call.

	// Re-fill accrued to allow the second 50k delegation.
	postFirst.TxEnergyAccrued = 150_000
	k.SetEnergyAccount(ctx, postFirst)

	id2, locked2, err := k.Delegate(ctx, delegator, delegatee2, 50_000, 3600)
	require.NoError(t, err, "second delegation should succeed; got: %v", err)
	require.NotZero(t, id2)
	require.True(t, locked2.Equal(math.NewIntWithDecimal(30_000, 18)),
		"second lock should also be 30k ATOS")

	// After both delegations: bank balance = 30k, total locked = 60k.
	postSecondBal := bank.balances[delegator.String()]
	require.True(t, postSecondBal.Equal(math.NewIntWithDecimal(30_000, 18)),
		"bank balance after two delegations should be 30k; got %s", postSecondBal)

	postSecond := k.GetEnergyAccount(ctx, delegator)
	require.True(t, postSecond.LockedAtos.Equal(math.NewIntWithDecimal(60_000, 18)),
		"LockedAtos should accumulate to 60k after two delegations")
}

// Tightly-bounded scenario that the old (buggy) code would have
// rejected. Delegator's free bank balance after the first lock is
// exactly equal to the next required lock. The old code did
// 60k - 30k = 30k and compared >= 30k (passes), so this case is
// edge but valid. Add a stricter scenario: free balance just barely
// covers the lock.
func TestDelegate_TightBalanceCheck(t *testing.T) {
	k, ctx, bank := newKeeperForTest(t)
	delegator := addr("delegator_______________")
	delegatee := addr("delegatee_______________")

	// Holder has exactly 30k ATOS. Delegate 50k energy locks 30k.
	bank.balances[delegator.String()] = math.NewIntWithDecimal(30_000, 18)
	a := k.Settle(ctx, delegator)
	a.TxEnergyAccrued = 50_000
	k.SetEnergyAccount(ctx, a)

	_, locked, err := k.Delegate(ctx, delegator, delegatee, 50_000, 3600)
	require.NoError(t, err)
	require.True(t, locked.Equal(math.NewIntWithDecimal(30_000, 18)))

	// Bank balance should now be exactly 0.
	require.True(t, bank.balances[delegator.String()].IsZero())
}

// Audit Issue 7 regression: when a delegatee consumes energy across
// multiple inbound delegations, the soonest-expiring delegation must
// be consumed first. Prior code iterated by id order, so a longer-
// tenor delegation with a small id could be consumed before a
// shorter-tenor delegation with a larger id — wasting the
// shorter-tenor energy that was about to expire anyway.
func TestAttributeDelegatedConsumption_ConsumesSoonestExpiringFirst(t *testing.T) {
	k, ctx, bank := newKeeperForTest(t)
	delegatee := addr("delegatee_______________")

	// Create 3 distinct delegators so we can place 3 delegations with
	// different expires_at values and observe consumption order.
	delegators := []sdk.AccAddress{
		addr("delegator1______________"),
		addr("delegator2______________"),
		addr("delegator3______________"),
	}
	for _, d := range delegators {
		bank.balances[d.String()] = math.NewIntWithDecimal(30_000, 18)
		acct := k.Settle(ctx, d)
		acct.TxEnergyAccrued = 50_000
		k.SetEnergyAccount(ctx, acct)
	}

	// Set block time so we can reason about expires_at deterministically.
	now := ctx.BlockTime().Unix()

	// Three delegations with INVERTED id/expiry relationship:
	//   delegator1 → id=1, duration=72h (expires latest)
	//   delegator2 → id=2, duration=1h  (expires SOONEST)
	//   delegator3 → id=3, duration=24h (expires middle)
	//
	// Old (buggy) code would consume in id order 1,2,3.
	// New code should consume in expiry order 2,3,1.

	id1, _, err := k.Delegate(ctx, delegators[0], delegatee, 10_000, 72*3600)
	require.NoError(t, err)
	id2, _, err := k.Delegate(ctx, delegators[1], delegatee, 10_000, 3600)
	require.NoError(t, err)
	id3, _, err := k.Delegate(ctx, delegators[2], delegatee, 10_000, 24*3600)
	require.NoError(t, err)

	// Verify the expires_at ordering matches our intent.
	d1, ok := k.GetDelegation(ctx, id1)
	require.True(t, ok)
	d2, ok := k.GetDelegation(ctx, id2)
	require.True(t, ok)
	d3, ok := k.GetDelegation(ctx, id3)
	require.True(t, ok)
	require.Equal(t, now+72*3600, d1.ExpiresAt)
	require.Equal(t, now+3600, d2.ExpiresAt)
	require.Equal(t, now+24*3600, d3.ExpiresAt)

	// Consume 12,000 energy. This fully drains d2 (10k expiring soonest)
	// and partially drains d3 (the next-soonest); d1 should still be
	// untouched after the call.
	require.NoError(t, k.attributeDelegatedConsumption(ctx, delegatee, 12_000))

	d1, _ = k.GetDelegation(ctx, id1)
	d2, _ = k.GetDelegation(ctx, id2)
	d3, _ = k.GetDelegation(ctx, id3)
	require.EqualValues(t, 10_000, d2.Used,
		"soonest-expiring delegation (id=2, 1h) must be drained first")
	require.EqualValues(t, 2_000, d3.Used,
		"middle-expiry delegation (id=3, 24h) takes the remainder")
	require.EqualValues(t, 0, d1.Used,
		"longest-tenor delegation (id=1, 72h) must NOT be touched")
}

// Tie-break: when two delegations share the same expires_at, the
// lower-id one is consumed first. Keeps consumption deterministic
// across nodes.
func TestAttributeDelegatedConsumption_TieBreaksOnId(t *testing.T) {
	k, ctx, bank := newKeeperForTest(t)
	delegatee := addr("delegatee_______________")
	delegators := []sdk.AccAddress{
		addr("delegatorA______________"),
		addr("delegatorB______________"),
	}
	for _, d := range delegators {
		bank.balances[d.String()] = math.NewIntWithDecimal(30_000, 18)
		acct := k.Settle(ctx, d)
		acct.TxEnergyAccrued = 50_000
		k.SetEnergyAccount(ctx, acct)
	}

	// Same duration → same expires_at (block time is constant in this ctx).
	idA, _, err := k.Delegate(ctx, delegators[0], delegatee, 5_000, 3600)
	require.NoError(t, err)
	idB, _, err := k.Delegate(ctx, delegators[1], delegatee, 5_000, 3600)
	require.NoError(t, err)

	// Consume 4,000; should fully come out of the lower-id delegation.
	require.NoError(t, k.attributeDelegatedConsumption(ctx, delegatee, 4_000))

	dA, _ := k.GetDelegation(ctx, idA)
	dB, _ := k.GetDelegation(ctx, idB)
	require.EqualValues(t, 4_000, dA.Used,
		"lower id should be consumed first when expires_at ties")
	require.EqualValues(t, 0, dB.Used)
}
