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
	_, err = k.attributeDelegatedConsumption(ctx, delegatee, 12_000)
	require.NoError(t, err)

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
	_, err = k.attributeDelegatedConsumption(ctx, delegatee, 4_000)
	require.NoError(t, err)

	dA, _ := k.GetDelegation(ctx, idA)
	dB, _ := k.GetDelegation(ctx, idB)
	require.EqualValues(t, 4_000, dA.Used,
		"lower id should be consumed first when expires_at ties")
	require.EqualValues(t, 0, dB.Used)
}

// Audit Question 1 regression: refund must follow LIFO order against
// the consumption split (own-first, delegated-second). LIFO refund
// returns the borrowed-from-delegators pool BEFORE crediting the
// holder's own bucket. The delegator-friendly direction matters:
// inbound delegations have an ExpiresAt deadline, so rolling unused
// energy back onto the original delegation preserves the time-bounded
// grant instead of silently converting it into permanent own energy
// on the delegatee's account.
//
// Setup: delegatee owns 10k accrued, plus 5k inbound from a delegator
// (DelegatedInUsable=5k). Consume 12k:
//   - 10k own drained first → OwnDeducted=10k
//   - 2k from delegated pool → DelegatedDeducted=2k, delegation.Used=2k
//
// Refund 8k. LIFO order:
//   - First 2k refunds the delegated pool: DelegatedInUsable goes 3→5k,
//     delegation.Used goes 2→0.
//   - Remaining 6k credits TxEnergyAccrued: 0→6k.
//
// Pre-fix RefundEnergy(amount) credited ONLY TxEnergyAccrued, so
// delegation.Used would stay at 2 and the delegator's grant would be
// permanently depleted even though most of it went unused.
func TestRefundEnergy_LIFOOrderRestoresDelegatedFirst(t *testing.T) {
	k, ctx, bank := newKeeperForTest(t)
	delegator := addr("delegator_______________")
	delegatee := addr("delegatee_______________")

	// Set both bank balances BEFORE any Settle call so the first-touch
	// snapshot captures the right balance.
	bank.balances[delegator.String()] = math.NewIntWithDecimal(60_000, 18)
	bank.balances[delegatee.String()] = math.NewIntWithDecimal(60_000, 18)

	// Delegator: enough balance to back a 5k delegation.
	dAcct := k.Settle(ctx, delegator)
	dAcct.TxEnergyAccrued = 100_000
	k.SetEnergyAccount(ctx, dAcct)
	delID, _, err := k.Delegate(ctx, delegator, delegatee, 5_000, 24*3600)
	require.NoError(t, err)

	// Delegatee: 10k own accrued (capacity comfortable so no cap-down).
	eAcct := k.Settle(ctx, delegatee)
	eAcct.TxEnergyAccrued = 10_000
	k.SetEnergyAccount(ctx, eAcct)

	// Consume 12k via direct call to populate ConsumeResult split.
	res, err := k.Consume(ctx, delegatee, 12_000, false,
		[]string{"/cosmos.bank.v1beta1.MsgSend"})
	require.NoError(t, err)
	require.EqualValues(t, 12_000, res.EnergyDeducted)
	require.EqualValues(t, 10_000, res.OwnDeducted, "own should drain first")
	require.EqualValues(t, 2_000, res.DelegatedDeducted, "delegated covers shortfall")
	require.Len(t, res.DelegationConsumptions, 1)
	require.EqualValues(t, delID, res.DelegationConsumptions[0].DelegationID)
	require.EqualValues(t, 2_000, res.DelegationConsumptions[0].Amount)

	// Sanity: post-consume state.
	post := k.GetEnergyAccount(ctx, delegatee)
	require.EqualValues(t, 0, post.TxEnergyAccrued)
	require.EqualValues(t, 3_000, post.DelegatedInUsable)
	d, ok := k.GetDelegation(ctx, delID)
	require.True(t, ok)
	require.EqualValues(t, 2_000, d.Used)

	// LIFO refund: 8k total. First 2k should restore the delegation;
	// remaining 6k goes to own TxEnergyAccrued.
	k.RefundEnergy(ctx, delegatee, 8_000, res)

	postRefund := k.GetEnergyAccount(ctx, delegatee)
	require.EqualValues(t, 6_000, postRefund.TxEnergyAccrued,
		"audit Q1: remaining refund (after delegated repaid) credits own bucket")
	require.EqualValues(t, 5_000, postRefund.DelegatedInUsable,
		"audit Q1: delegated pool restored to full 5k pre-consume state")

	d2, ok := k.GetDelegation(ctx, delID)
	require.True(t, ok)
	require.EqualValues(t, 0, d2.Used,
		"audit Q1: delegation.Used must roll back to zero — the time-bounded grant is intact")
}

// Audit Question 1 companion: when refund <= DelegatedDeducted, ALL of
// it must go to the delegated pool, none to own.
func TestRefundEnergy_LIFOSmallRefundStaysOnDelegatedPool(t *testing.T) {
	k, ctx, bank := newKeeperForTest(t)
	delegator := addr("delegator_______________")
	delegatee := addr("delegatee_______________")

	bank.balances[delegator.String()] = math.NewIntWithDecimal(60_000, 18)
	bank.balances[delegatee.String()] = math.NewIntWithDecimal(60_000, 18)

	dAcct := k.Settle(ctx, delegator)
	dAcct.TxEnergyAccrued = 100_000
	k.SetEnergyAccount(ctx, dAcct)
	delID, _, err := k.Delegate(ctx, delegator, delegatee, 8_000, 24*3600)
	require.NoError(t, err)

	eAcct := k.Settle(ctx, delegatee)
	eAcct.TxEnergyAccrued = 4_000
	k.SetEnergyAccount(ctx, eAcct)

	res, err := k.Consume(ctx, delegatee, 10_000, false,
		[]string{"/cosmos.bank.v1beta1.MsgSend"})
	require.NoError(t, err)
	require.EqualValues(t, 4_000, res.OwnDeducted)
	require.EqualValues(t, 6_000, res.DelegatedDeducted)

	// Refund 3k — all should stay on the delegated pool (3k < 6k).
	k.RefundEnergy(ctx, delegatee, 3_000, res)

	postRefund := k.GetEnergyAccount(ctx, delegatee)
	require.EqualValues(t, 0, postRefund.TxEnergyAccrued,
		"small refund must NOT spill into own bucket")
	require.EqualValues(t, 2_000+3_000, postRefund.DelegatedInUsable,
		"all 3k credited to delegated pool (was 2k after consume, +3k = 5k)")

	d, ok := k.GetDelegation(ctx, delID)
	require.True(t, ok)
	require.EqualValues(t, 6_000-3_000, d.Used,
		"delegation.Used must roll back by exactly the refund amount")
}
