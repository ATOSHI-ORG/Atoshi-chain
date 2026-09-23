package keeper

import (
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/atoshi-chain/atoshi/v20/x/energy/types"
)

// The production bug, reproduced exactly.
//
// Testnet account atoshi14dpp92vlaxlpp4ctve0082zqhgys9ydv5gz5nd, 2026-09-22:
// 22,500 ATOS in the wallet, 18 ATOX, and 7,500 ATOS staked. That sums to
// 30,018 eligible -- past the 30,000 threshold and worth 50,000 energy.
// The holder saw capacity 0.
//
// Cause: every bank transfer, including the fee deduction of the tx right
// after the delegation, called SendRestriction, which rewrote the snapshot
// from bank balances alone and dropped the staked term. Settle never
// recomputed it (it only reads EligibleBalance on an account's first touch),
// so the loss was permanent.
func TestSendRestriction_KeepsStakedTermInSnapshot(t *testing.T) {
	k, ctx, bank, staking := newKeeperWithStaking(t)
	holder := addr("holder__________________")
	sink := addr("sink____________________")

	ctx = ctx.WithBlockHeight(100)
	bank.balances[holder.String()] = atos(22_500)
	bank.atox[holder.String()] = atos(18)

	// The holder delegates 7,500. x/staking moves the coins without ever
	// entering the bank send-restriction chain, so the staking hook is what
	// records it.
	staking.bonded[holder.String()] = atos(7_500)
	require.NoError(t, k.Hooks().AfterDelegationModified(ctx, holder,
		sdk.ValAddress([]byte("validator_______________"))))
	require.True(t, k.GetEnergyAccount(ctx, holder).LastBalanceSnapshot.Equal(atos(30_018)),
		"delegation must land in the snapshot immediately")

	// Now any outbound transfer at all -- here a one-ATOS fee-sized send. This
	// is what used to wipe the staked term out again. Evmos bank ordering means
	// the hook sees the sender already debited.
	moved := atos(1)
	bank.balances[holder.String()] = atos(22_499)
	_, err := k.SendRestriction(ctx, holder, sink,
		sdk.NewCoins(sdk.NewCoin("liao", moved)))
	require.NoError(t, err)

	got := k.GetEnergyAccount(ctx, holder).LastBalanceSnapshot
	want := atos(22_499).Add(atos(18)).Add(atos(7_500)) // 30,017
	require.True(t, got.Equal(want),
		"staked ATOS must survive a transfer: want %s, got %s", want, got)

	// And the whole point: the holder is over the threshold, so capacity is
	// no longer zero.
	params := k.GetParams(ctx)
	require.EqualValues(t, params.TxEnergyPerThreshold,
		types.TxEnergyCapacity(got, params),
		"30,017 eligible is one threshold's worth of capacity")
}

// A delegation has to be visible to x/energy the moment it happens.
//
// It is not visible through the bank hook: the Evmos SDK's
// BaseKeeper.DelegateCoins moves the coins with setBalance/addCoins and never
// enters the send-restriction chain. The staking hook is the only signal.
func TestStakingHooks_DelegationRefreshesSnapshot(t *testing.T) {
	k, ctx, bank, staking := newKeeperWithStaking(t)
	ctx = ctx.WithBlockHeight(100) // hooks no-op at genesis height
	holder := addr("holder__________________")
	val := sdk.ValAddress([]byte("validator_______________"))

	// Holder starts under the threshold and has touched energy before, so the
	// first-touch recompute path is not what saves us here.
	bank.balances[holder.String()] = atos(30_000)
	k.Settle(ctx, holder)
	bank.balances[holder.String()] = atos(22_500)
	k.ApplyBalanceChange(ctx, holder, atos(22_500))
	require.EqualValues(t, 0,
		types.TxEnergyCapacity(k.GetEnergyAccount(ctx, holder).LastBalanceSnapshot, k.GetParams(ctx)))

	// Stake 7,500. x/staking moves the coins and fires the hook.
	staking.bonded[holder.String()] = atos(7_500)
	require.NoError(t, k.Hooks().AfterDelegationModified(ctx, holder, val))

	got := k.GetEnergyAccount(ctx, holder).LastBalanceSnapshot
	require.True(t, got.Equal(atos(30_000)),
		"snapshot must include the new delegation: want %s, got %s", atos(30_000), got)
	require.EqualValues(t, k.GetParams(ctx).TxEnergyPerThreshold,
		types.TxEnergyCapacity(got, k.GetParams(ctx)))
}

// Undelegating fully deletes the delegation record, so AfterDelegationModified
// never fires -- BeforeDelegationRemoved is the only hook that runs. Bonded
// becomes unbonding, and both count, so eligibility is unchanged.
func TestStakingHooks_FullUndelegateKeepsEligibility(t *testing.T) {
	k, ctx, bank, staking := newKeeperWithStaking(t)
	ctx = ctx.WithBlockHeight(100)
	holder := addr("holder__________________")
	val := sdk.ValAddress([]byte("validator_______________"))

	bank.balances[holder.String()] = atos(22_500)
	staking.bonded[holder.String()] = atos(7_500)
	require.NoError(t, k.Hooks().AfterDelegationModified(ctx, holder, val))
	require.True(t, k.GetEnergyAccount(ctx, holder).LastBalanceSnapshot.Equal(atos(30_000)))

	// Full undelegate: bonded → unbonding.
	staking.bonded[holder.String()] = math.ZeroInt()
	staking.unbonding[holder.String()] = atos(7_500)
	require.NoError(t, k.Hooks().BeforeDelegationRemoved(ctx, holder, val))

	require.True(t, k.GetEnergyAccount(ctx, holder).LastBalanceSnapshot.Equal(atos(30_000)),
		"unbonding ATOS still counts, so the 21-day exit must not cost energy")
}

// Genesis self-delegations must not create energy accounts: genutil delivers
// the gentxs before x/energy's InitGenesis has written params, so anything
// built here would be built against DefaultParams.
func TestStakingHooks_NoOpAtGenesisHeight(t *testing.T) {
	k, ctx, bank, staking := newKeeperWithStaking(t)
	ctx = ctx.WithBlockHeight(0)
	holder := addr("holder__________________")
	val := sdk.ValAddress([]byte("validator_______________"))

	bank.balances[holder.String()] = atos(100_000_000)
	staking.bonded[holder.String()] = atos(100_000_000)
	require.NoError(t, k.Hooks().AfterDelegationModified(ctx, holder, val))

	require.EqualValues(t, 0, k.GetEnergyAccount(ctx, holder).LastUpdatedTime,
		"no energy account should exist yet at genesis height")
}

// The genesis-validator path. The staking hooks no-op at height 0, so a
// validator's 100M self-delegation is first recorded by Settle's first touch.
// That write has to cache the staked term too, or the validator's next
// transaction -- its own fee deduction -- reads staked_snapshot as zero and
// drops 100M ATOS out of its eligibility.
func TestSettle_FirstTouchCachesStakedTerm(t *testing.T) {
	k, ctx, bank, staking := newKeeperWithStaking(t)
	val := addr("validator_______________")
	sink := addr("sink____________________")

	// Mainnet genesis: 100,000 ATOS for gas in the wallet, 100M self-delegated.
	bank.balances[val.String()] = atos(100_000)
	staking.bonded[val.String()] = atos(100_000_000)

	acct := k.Settle(ctx, val)
	require.True(t, acct.LastBalanceSnapshot.Equal(atos(100_100_000)))
	require.True(t, acct.StakedSnapshot.Equal(atos(100_000_000)),
		"first touch must cache the staked term, not just the sum")

	// Its first transaction deducts a fee.
	bank.balances[val.String()] = atos(99_999)
	_, err := k.SendRestriction(ctx, val, sink,
		sdk.NewCoins(sdk.NewCoin("liao", atos(1))))
	require.NoError(t, err)

	got := k.GetEnergyAccount(ctx, val).LastBalanceSnapshot
	require.True(t, got.Equal(atos(100_099_999)),
		"the self-delegation must survive the fee deduction: want %s, got %s",
		atos(100_099_999), got)
}

// A staker whose energy account has never been settled: the cache is empty,
// and an empty cache there means "unknown", not "zero". The first transfer has
// to fall back to reading x/staking rather than writing the delegation away.
func TestSendRestriction_NeverSettledStakerReadsStakingOnce(t *testing.T) {
	k, ctx, bank, staking := newKeeperWithStaking(t)
	ctx = ctx.WithBlockHeight(100)
	holder := addr("holder__________________")
	sink := addr("sink____________________")

	bank.balances[holder.String()] = atos(22_500)
	staking.bonded[holder.String()] = atos(7_500)
	require.EqualValues(t, 0, k.GetEnergyAccount(ctx, holder).LastUpdatedTime,
		"precondition: account has never been settled")

	bank.balances[holder.String()] = atos(22_499)
	_, err := k.SendRestriction(ctx, holder, sink,
		sdk.NewCoins(sdk.NewCoin("liao", atos(1))))
	require.NoError(t, err)

	acct := k.GetEnergyAccount(ctx, holder)
	require.True(t, acct.LastBalanceSnapshot.Equal(atos(29_999)),
		"want %s, got %s", atos(29_999), acct.LastBalanceSnapshot)
	require.True(t, acct.StakedSnapshot.Equal(atos(7_500)),
		"and the cache must be populated for next time, got %s", acct.StakedSnapshot)
}
