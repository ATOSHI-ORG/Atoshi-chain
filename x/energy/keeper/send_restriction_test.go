package keeper

import (
	"testing"
	"time"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
)

// Receiving an inbound transfer must update the receiver's snapshot.
// This is the production bug we shipped: snapshot was only refreshed on
// delegation flows, so a fresh wallet that received N ATOS accrued
// energy against a zero snapshot forever.
func TestSendRestriction_ReceiverSnapshotRefreshed(t *testing.T) {
	k, ctx, bank := newKeeperForTest(t)
	alice := addr("alice___________________")
	bob := addr("bob_____________________")

	// Pre-send state: Alice holds 100k ATOS, Bob holds 0.
	moved := math.NewIntWithDecimal(50_000, 18)
	bank.balances[alice.String()] = math.NewIntWithDecimal(100_000, 18)
	bank.balances[bob.String()] = math.ZeroInt()

	// Evmos bank ordering: subUnlockedCoins(from) → SendRestriction → addCoins(to).
	// Simulate the sub-first step before invoking the hook so fromBefore
	// matches what the hook sees in production.
	bank.balances[alice.String()] = math.NewIntWithDecimal(50_000, 18) // sub from alice
	out, err := k.SendRestriction(ctx, alice, bob,
		sdk.NewCoins(sdk.NewCoin("aatos", moved)))
	require.NoError(t, err)
	require.Equal(t, bob, out)

	aliceAcct := k.GetEnergyAccount(ctx, alice)
	bobAcct := k.GetEnergyAccount(ctx, bob)

	require.True(t,
		aliceAcct.LastBalanceSnapshot.Equal(math.NewIntWithDecimal(50_000, 18)),
		"sender snapshot should reflect post-send balance, got %s",
		aliceAcct.LastBalanceSnapshot)
	require.True(t,
		bobAcct.LastBalanceSnapshot.Equal(math.NewIntWithDecimal(50_000, 18)),
		"receiver snapshot should reflect post-send balance, got %s",
		bobAcct.LastBalanceSnapshot)
}

// Non-base-denom sends (IBC vouchers, gov tokens, future assets) must
// not touch energy state — they don't contribute to eligible balance.
func TestSendRestriction_IgnoresNonBaseDenom(t *testing.T) {
	k, ctx, bank := newKeeperForTest(t)
	alice := addr("alice___________________")
	bob := addr("bob_____________________")
	bank.balances[alice.String()] = math.NewIntWithDecimal(100_000, 18)

	_, err := k.SendRestriction(ctx, alice, bob,
		sdk.NewCoins(sdk.NewCoin("ibc/USDC", math.NewInt(1_000_000))))
	require.NoError(t, err)

	require.Equal(t, int64(0), k.GetEnergyAccount(ctx, bob).LastUpdatedTime,
		"receiver energy account should not be touched by non-base-denom send")
}

// A wallet that already accrued energy under a small snapshot, then
// receives more ATOS, should immediately enjoy the higher capacity.
// (Existing accrued energy is preserved; only the cap-down branch
// reduces it, and an inbound transfer never caps down.)
func TestSendRestriction_InboundIncreasesCapacity(t *testing.T) {
	k, ctx, bank := newKeeperForTest(t)
	alice := addr("alice___________________")
	bob := addr("bob_____________________")

	// Bob starts holding exactly the threshold (30k), accrues to full
	// 1-threshold capacity (50k) over 24h.
	bank.balances[bob.String()] = math.NewIntWithDecimal(30_000, 18)
	k.Settle(ctx, bob)
	ctx = ctx.WithBlockTime(ctx.BlockTime().Add(24 * time.Hour))
	full := k.Settle(ctx, bob)
	require.EqualValues(t, 50_000, full.TxEnergyAccrued)

	// Alice sends Bob another 30k. Post-send Bob = 60k (2 thresholds → 100k cap).
	bank.balances[alice.String()] = math.NewIntWithDecimal(50_000, 18)
	moved := math.NewIntWithDecimal(30_000, 18)
	_, err := k.SendRestriction(ctx, alice, bob,
		sdk.NewCoins(sdk.NewCoin("aatos", moved)))
	require.NoError(t, err)

	bobAcct := k.GetEnergyAccount(ctx, bob)
	require.True(t, bobAcct.LastBalanceSnapshot.Equal(math.NewIntWithDecimal(60_000, 18)),
		"snapshot should reflect new 60k balance, got %s", bobAcct.LastBalanceSnapshot)
	require.EqualValues(t, 50_000, bobAcct.TxEnergyAccrued,
		"pre-existing accrued energy is preserved (50k well under new 100k cap)")
}

// Draining an account to zero in a single tx must clamp the snapshot to
// zero, not produce a negative math.Int (which would panic on serialize).
func TestSendRestriction_DrainsToZero(t *testing.T) {
	k, ctx, bank := newKeeperForTest(t)
	alice := addr("alice___________________")
	bob := addr("bob_____________________")
	bal := math.NewIntWithDecimal(30_000, 18)
	bank.balances[alice.String()] = bal

	// Evmos sub-first ordering: subUnlockedCoins drains alice's bank
	// to zero before SendRestriction fires.
	bank.balances[alice.String()] = math.ZeroInt()
	_, err := k.SendRestriction(ctx, alice, bob, sdk.NewCoins(sdk.NewCoin("aatos", bal)))
	require.NoError(t, err)

	aliceAcct := k.GetEnergyAccount(ctx, alice)
	require.True(t, aliceAcct.LastBalanceSnapshot.IsZero(),
		"sender snapshot should be zero after full drain, got %s", aliceAcct.LastBalanceSnapshot)
	require.EqualValues(t, 0, aliceAcct.TxEnergyAccrued,
		"draining to zero caps tx_energy to zero (capacity falls below 1 threshold)")
}
