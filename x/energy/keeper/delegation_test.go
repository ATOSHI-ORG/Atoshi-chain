package keeper

import (
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/atoshi-chain/atoshi/v20/x/energy/types"
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

// Audit Issue-2 (round2) regression: when a delegation is undelegated,
// the energy that the delegatee already consumed (d.Used) must be
// deducted from the delegator's TxEnergyAccrued — otherwise the
// delegator can Delegate / Undelegate in a loop to recycle the same
// accrued energy budget indefinitely.
//
// Scenario:
//   - Alice TxEnergyAccrued = 100k, DelegatedOut = 0.
//   - Alice delegates 50k to Bob.
//   - Bob burns 30k via Consume (delegation.Used = 30k).
//   - Alice undelegates.
//
// Pre-fix:
//   - Alice.DelegatedOut := 0 (correct)
//   - Alice.TxEnergyAccrued := 100k (BUG — should be 70k)
//   - Alice could now Delegate 100k again, even though Bob already
//     spent 30k. Two calls' worth out of one budget.
//
// Post-fix:
//   - Alice.TxEnergyAccrued := 100k - 30k = 70k
//   - Subsequent Delegate respects the 70k cap.
func TestUndelegate_DeductsDelegateeConsumedFromTxEnergyAccrued(t *testing.T) {
	k, ctx, bank := newKeeperForTest(t)
	alice := addr("alice___________________")
	bob := addr("bob_____________________")

	// Both have bank balance so Settle initializes a non-zero snapshot.
	bank.balances[alice.String()] = math.NewIntWithDecimal(90_000, 18)
	bank.balances[bob.String()] = math.NewIntWithDecimal(60_000, 18)

	aliceAcct := k.Settle(ctx, alice)
	aliceAcct.TxEnergyAccrued = 100_000
	k.SetEnergyAccount(ctx, aliceAcct)

	// Alice delegates 50k to Bob (locks 30k ATOS = 1 threshold block).
	delID, _, err := k.Delegate(ctx, alice, bob, 50_000, 24*3600)
	require.NoError(t, err)

	// Bob burns 30k via Consume — drains his own (zero), then dips 30k
	// out of the delegated-in pool. delegation.Used becomes 30k.
	res, err := k.Consume(ctx, bob, 30_000, false,
		[]string{"/cosmos.bank.v1beta1.MsgSend"})
	require.NoError(t, err)
	require.EqualValues(t, 30_000, res.DelegatedDeducted,
		"all 30k should come from delegated-in pool")
	d, ok := k.GetDelegation(ctx, delID)
	require.True(t, ok)
	require.EqualValues(t, 30_000, d.Used)

	// Alice undelegates.
	require.NoError(t, k.Undelegate(ctx, alice, delID))

	postAlice := k.GetEnergyAccount(ctx, alice)
	require.EqualValues(t, 0, postAlice.DelegatedOut,
		"DelegatedOut must reset to zero after full undelegate")
	require.EqualValues(t, 100_000-30_000, postAlice.TxEnergyAccrued,
		"audit Issue-2: TxEnergyAccrued must be debited by delegation.Used (30k consumed by delegatee)")
}

// Audit Issue-2 (round2) end-to-end exploit guard: a delegator cannot
// recycle the same accrued budget by repeatedly Delegate / Undelegate.
// The total energy spendable across delegatees over the lifetime of
// 100k accrued must remain bounded by 100k.
func TestUndelegate_PreventsConsumedEnergyReuseAcrossLoops(t *testing.T) {
	k, ctx, bank := newKeeperForTest(t)
	alice := addr("alice___________________")
	bob := addr("bob_____________________")
	carol := addr("carol___________________")

	bank.balances[alice.String()] = math.NewIntWithDecimal(90_000, 18)
	bank.balances[bob.String()] = math.NewIntWithDecimal(60_000, 18)
	bank.balances[carol.String()] = math.NewIntWithDecimal(60_000, 18)

	aliceAcct := k.Settle(ctx, alice)
	aliceAcct.TxEnergyAccrued = 100_000
	k.SetEnergyAccount(ctx, aliceAcct)

	// Round 1: delegate 50k to Bob, Bob burns 50k entirely.
	id1, _, err := k.Delegate(ctx, alice, bob, 50_000, 24*3600)
	require.NoError(t, err)
	res, err := k.Consume(ctx, bob, 50_000, false,
		[]string{"/cosmos.bank.v1beta1.MsgSend"})
	require.NoError(t, err)
	require.EqualValues(t, 50_000, res.DelegatedDeducted)
	require.NoError(t, k.Undelegate(ctx, alice, id1))

	// Round 2: with the bug, Alice could now delegate 100k. After the
	// fix she should be capped at 50k because 50k of her accrued was
	// already burned by Bob.
	aliceMid := k.GetEnergyAccount(ctx, alice)
	require.EqualValues(t, 50_000, aliceMid.TxEnergyAccrued,
		"audit Issue-2: round-1 Used (50k) must reduce Alice's accrued")

	// Try to delegate 80k to Carol — must fail: freeEnergy = 50k < 80k.
	_, _, err = k.Delegate(ctx, alice, carol, 80_000, 24*3600)
	require.ErrorIs(t, err, types.ErrInsufficientEnergy,
		"audit Issue-2: post-fix, Alice cannot re-lend energy already burned by a prior delegatee")

	// 50k should succeed (exactly the remaining budget).
	_, _, err = k.Delegate(ctx, alice, carol, 50_000, 24*3600)
	require.NoError(t, err)
}

// Audit Issue-8 (round2) regression: Delegate must NOT clobber the
// energy-account writes performed by the SendRestriction hook during
// the bank transfer. Combined with audit Q2 (round2), which makes
// EligibleBalance count bank + LockedAtos, the per-tx accounting
// must satisfy:
//
//   1. After Delegate, the snapshot equals (post-send bank balance)
//      + (new LockedAtos counter) — the user's TOTAL stake, not
//      just liquid bank.
//   2. The hook's cap-down on TxEnergyAccrued sees the SUM, not the
//      bank-only value. With balance preservation, cap-down only
//      shaves above the natural ceiling for the user's full stake.
//
// Setup: bank = 60k ATOS, TxEnergyAccrued inflated to 200k. Delegate
// 50k energy locks 30k ATOS. After the bank send:
//   - bank = 30k (liquid)
//   - LockedAtos = 30k (in module account)
//   - eligible (Q2) = 30k + 30k = 60k  (same as pre-Delegate)
//   - newTxCap = TxEnergyCapacity(60k) = 2 × 50k = 100k
//   - floor at DelegatedOut (50k) = max(100k, 50k) = 100k
//   - TxEnergyAccrued cap-down 200k → 100k
//
// Pre-Issue-8 fix: Delegate's final SetEnergyAccount(stale delAcct)
// restored TxEnergyAccrued to 200k — the bug Issue-8 flagged.
// Pre-Q2 fix: even after Issue-8's re-read, eligible would equal
// bank only (30k), forcing cap-down to 50k — penalizing the
// delegator for locking ATOS they still effectively own.
//
// Post both fixes: TxEnergyAccrued cap-downs to 100k. Snapshot
// equals 60k (full stake). The delegator is cap-neutral relative
// to their pre-delegation TxEnergyCapacity ceiling.
func TestDelegate_DoesNotClobberHookWrites(t *testing.T) {
	k, ctx, bank := newKeeperForTest(t)
	delegator := addr("delegator_______________")
	delegatee := addr("delegatee_______________")

	bank.balances[delegator.String()] = math.NewIntWithDecimal(60_000, 18)
	bank.balances[delegatee.String()] = math.NewIntWithDecimal(60_000, 18)

	// Wire bank.SendRestriction-equivalent hook so SendCoins inside
	// Delegate triggers the same ApplyBalanceChange path that runs in
	// production. Without this, the fake bank moves balances silently
	// and neither Issue-8 nor Q2 behavior is observable.
	bank.onSend = func(from, to sdk.AccAddress, amt sdk.Coins) {
		_, _ = k.SendRestriction(ctx, from, to, amt)
	}

	a := k.Settle(ctx, delegator)
	a.TxEnergyAccrued = 200_000 // intentionally above the natural cap
	k.SetEnergyAccount(ctx, a)

	_, _, err := k.Delegate(ctx, delegator, delegatee, 50_000, 24*3600)
	require.NoError(t, err)

	post := k.GetEnergyAccount(ctx, delegator)
	require.EqualValues(t, 50_000, post.DelegatedOut)
	require.EqualValues(t, 100_000, post.TxEnergyAccrued,
		"audit Q2 + Issue-8: cap-down respects bank+LockedAtos=60k → cap=100k; pre-fix would have shown 50k (bank only) or 200k (stale clobber)")
	require.True(t, post.LastBalanceSnapshot.Equal(math.NewIntWithDecimal(60_000, 18)),
		"audit Q2: snapshot reflects bank (30k) + LockedAtos (30k) = 60k total stake")
}

// Audit Issue-8 (round2) companion: when a second Delegate runs on
// the same account in the same block, both rounds must compose
// correctly. Stale-copy bug would scramble the cumulative LockedAtos
// counter on the second call.
func TestDelegate_TwoBackToBackKeepsLockedAtosConsistent(t *testing.T) {
	k, ctx, bank := newKeeperForTest(t)
	delegator := addr("delegator_______________")
	delegatee := addr("delegatee_______________")

	bank.balances[delegator.String()] = math.NewIntWithDecimal(120_000, 18)
	bank.balances[delegatee.String()] = math.NewIntWithDecimal(60_000, 18)

	a := k.Settle(ctx, delegator)
	a.TxEnergyAccrued = 200_000
	k.SetEnergyAccount(ctx, a)

	_, locked1, err := k.Delegate(ctx, delegator, delegatee, 50_000, 24*3600)
	require.NoError(t, err)
	require.True(t, locked1.Equal(math.NewIntWithDecimal(30_000, 18)))

	mid := k.GetEnergyAccount(ctx, delegator)
	mid.TxEnergyAccrued = 200_000 // refill to simulate accrued recovery
	k.SetEnergyAccount(ctx, mid)

	_, locked2, err := k.Delegate(ctx, delegator, delegatee, 50_000, 24*3600)
	require.NoError(t, err)
	require.True(t, locked2.Equal(math.NewIntWithDecimal(30_000, 18)))

	post := k.GetEnergyAccount(ctx, delegator)
	require.True(t, post.LockedAtos.Equal(math.NewIntWithDecimal(60_000, 18)),
		"audit Issue-8: cumulative LockedAtos must equal 60k after two 30k locks; stale-copy bug would mis-sum")
	require.EqualValues(t, 100_000, post.DelegatedOut)
}

// Audit Question 2 (round2) regression: EligibleBalance must include
// LockedAtos. After Delegate, the delegator's TxEnergyCapacity must
// be the SAME as before the delegation — the delegator's total stake
// (liquid bank + locked module account) hasn't changed, only its
// distribution. This guards against the pre-fix "double penalty"
// where Delegate shrank both DelegatedOut (correctly preventing
// double-spend) AND eligible balance (incorrectly penalizing the
// holder for the existence of locked ATOS).
func TestDelegate_IsCapNeutralOnEligibleBalance(t *testing.T) {
	k, ctx, bank := newKeeperForTest(t)
	delegator := addr("delegator_______________")
	delegatee := addr("delegatee_______________")

	bank.balances[delegator.String()] = math.NewIntWithDecimal(90_000, 18)
	bank.balances[delegatee.String()] = math.NewIntWithDecimal(60_000, 18)
	bank.onSend = func(from, to sdk.AccAddress, amt sdk.Coins) {
		_, _ = k.SendRestriction(ctx, from, to, amt)
	}

	a := k.Settle(ctx, delegator)
	a.TxEnergyAccrued = 50_000
	k.SetEnergyAccount(ctx, a)

	preEligible := k.EligibleBalance(ctx, delegator)
	require.True(t, preEligible.Equal(math.NewIntWithDecimal(90_000, 18)),
		"pre-Delegate eligible = bank balance only (LockedAtos=0)")

	_, _, err := k.Delegate(ctx, delegator, delegatee, 50_000, 24*3600)
	require.NoError(t, err)

	postEligible := k.EligibleBalance(ctx, delegator)
	require.True(t, postEligible.Equal(preEligible),
		"audit Q2: Delegate is cap-neutral on EligibleBalance — got pre=%s post=%s",
		preEligible, postEligible)

	// Cross-check the components: bank dropped by 30k, LockedAtos rose
	// by 30k, sum unchanged.
	bankAmt := bank.GetBalance(ctx, delegator, "aatos").Amount
	require.True(t, bankAmt.Equal(math.NewIntWithDecimal(60_000, 18)),
		"bank balance must drop by lockedATOS=30k")
	acct := k.GetEnergyAccount(ctx, delegator)
	require.True(t, acct.LockedAtos.Equal(math.NewIntWithDecimal(30_000, 18)),
		"LockedAtos counter must rise by 30k")
	require.True(t, bankAmt.Add(acct.LockedAtos).Equal(preEligible),
		"bank + LockedAtos must reconstitute the pre-Delegate eligible total")
}

// Audit Q2 round-trip: Delegate followed by Undelegate must restore
// the original EligibleBalance bit-for-bit. The bank refund returns
// the locked ATOS to liquid; the LockedAtos counter decrements to
// zero; sum unchanged the whole way.
func TestDelegateUndelegate_RoundTripPreservesEligibleBalance(t *testing.T) {
	k, ctx, bank := newKeeperForTest(t)
	delegator := addr("delegator_______________")
	delegatee := addr("delegatee_______________")

	bank.balances[delegator.String()] = math.NewIntWithDecimal(90_000, 18)
	bank.balances[delegatee.String()] = math.NewIntWithDecimal(60_000, 18)
	bank.onSend = func(from, to sdk.AccAddress, amt sdk.Coins) {
		_, _ = k.SendRestriction(ctx, from, to, amt)
	}

	a := k.Settle(ctx, delegator)
	a.TxEnergyAccrued = 50_000
	k.SetEnergyAccount(ctx, a)

	pre := k.EligibleBalance(ctx, delegator)

	delID, _, err := k.Delegate(ctx, delegator, delegatee, 50_000, 24*3600)
	require.NoError(t, err)
	require.True(t, k.EligibleBalance(ctx, delegator).Equal(pre),
		"eligible stable through Delegate")

	require.NoError(t, k.Undelegate(ctx, delegator, delID))
	post := k.EligibleBalance(ctx, delegator)
	require.True(t, post.Equal(pre),
		"audit Q2: eligible bit-for-bit restored after Delegate/Undelegate round trip — pre=%s post=%s",
		pre, post)

	// LockedAtos returns to zero, bank returns to 90k.
	acct := k.GetEnergyAccount(ctx, delegator)
	require.True(t, acct.LockedAtos.IsZero(),
		"LockedAtos counter returns to zero after full undelegate")
	require.True(t, bank.GetBalance(ctx, delegator, "aatos").Amount.Equal(math.NewIntWithDecimal(90_000, 18)),
		"bank balance fully restored")
}
