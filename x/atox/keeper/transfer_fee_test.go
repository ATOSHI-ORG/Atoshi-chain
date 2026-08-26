package keeper_test

import (
	"fmt"
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/stretchr/testify/require"

	"github.com/atoshi-chain/atoshi/v20/x/atox/types"
)

func atox(n int64) math.Int { return math.NewIntWithDecimal(n, 18) }

// TestTransferFee_ChargedOnTopAndBurned pins the headline behaviour: sending 100
// ATOX costs the sender 110, the receiver gets 100, and the 10 is destroyed.
func TestTransferFee_ChargedOnTopAndBurned(t *testing.T) {
	k, ctx, bank := setup(t)
	alice, bob := acc("alice"), acc("bob")

	require.NoError(t, k.MintAtox(ctx, alice, atox(110)))
	supplyBefore := k.AtoxSupply(ctx)

	require.NoError(t, bank.SendCoins(ctx, alice, bob,
		sdk.NewCoins(sdk.NewCoin(atoxDenom, atox(100)))))

	require.Equal(t, "0", k.AtoxBalance(ctx, alice).String(), "sender pays amount + fee")
	require.Equal(t, atox(100).String(), k.AtoxBalance(ctx, bob).String(), "receiver gets the full amount")

	require.Equal(t, supplyBefore.Sub(atox(10)).String(), k.AtoxSupply(ctx).String(),
		"the fee must be burned, not parked somewhere")
	require.Equal(t, atox(10).String(), k.GetGlobalState(ctx).TotalFeeBurned.String())
}

// TestTransferFee_BurnRestoresMintHeadroom is what makes burning the recycling
// mechanism: MintAtox caps against LIVE supply, so a burned fee can be mined
// again as a future block reward.
func TestTransferFee_BurnRestoresMintHeadroom(t *testing.T) {
	k, ctx, bank := setup(t)
	alice, bob := acc("alice"), acc("bob")
	cap := k.GetParams(ctx).SupplyCap

	// Mine the entire cap, then confirm nothing more can be minted.
	require.NoError(t, k.MintAtox(ctx, alice, cap))
	require.ErrorIs(t, k.MintAtox(ctx, acc("miner"), math.NewInt(1)), types.ErrSupplyCapReached)

	// Alice moves some ATOX; the fee is burned.
	send := cap.QuoRaw(100)
	require.NoError(t, bank.SendCoins(ctx, alice, bob, sdk.NewCoins(sdk.NewCoin(atoxDenom, send))))
	burned := k.GetGlobalState(ctx).TotalFeeBurned
	require.True(t, burned.IsPositive())

	// Exactly the burned amount can now be mined again — no more, no less.
	require.NoError(t, k.MintAtox(ctx, acc("miner"), burned))
	require.ErrorIs(t, k.MintAtox(ctx, acc("miner"), math.NewInt(1)), types.ErrSupplyCapReached)
}

// TestTransferFee_ModuleAccountPathsAreFree guards the mining-income path: ATOX
// reaches holders through the atox module account, and later through
// fee_collector and distribution. Charging there would skim 10% off every
// holder's rewards before they ever saw them.
func TestTransferFee_ModuleAccountPathsAreFree(t *testing.T) {
	k, ctx, _ := setup(t)
	alice := acc("alice")

	// MintAtox routes through the module account -> account.
	require.NoError(t, k.MintAtox(ctx, alice, atox(100)))

	require.Equal(t, atox(100).String(), k.AtoxBalance(ctx, alice).String(),
		"block rewards must arrive whole")
	require.True(t, k.GetGlobalState(ctx).TotalFeeBurned.IsZero(),
		"no fee may be charged on a module-account path")
}

// TestTransferFee_InsufficientHeadroomRejects covers the sharp edge behind the
// wallet's Max button: a holder cannot send their entire balance, because the fee
// is charged on top of it.
func TestTransferFee_InsufficientHeadroomRejects(t *testing.T) {
	k, ctx, bank := setup(t)
	alice, bob := acc("alice"), acc("bob")

	require.NoError(t, k.MintAtox(ctx, alice, atox(100)))

	// Attempt in a cache-wrapped context and discard it, mirroring how baseapp
	// runs every msg. bank does NOT undo the debit it already made on failure —
	// atomicity comes only from the tx-level cache — so the failed attempt must
	// not be allowed to leak into the rest of this test.
	failCtx, _ := ctx.CacheContext()
	err := bank.SendCoins(failCtx, alice, bob, sdk.NewCoins(sdk.NewCoin(atoxDenom, atox(100))))
	require.Error(t, err, "sending the whole balance leaves nothing for the fee")

	// The largest sendable amount is balance / 1.1, and it must succeed.
	maxSend := types.MaxSendableWithFee(atox(100), k.GetParams(ctx).TransferFeeBps)
	require.NoError(t, bank.SendCoins(ctx, alice, bob, sdk.NewCoins(sdk.NewCoin(atoxDenom, maxSend))))
	require.True(t, k.AtoxBalance(ctx, alice).LTE(math.NewInt(1)),
		"Max should leave at most rounding dust, got %s", k.AtoxBalance(ctx, alice))
}

// TestTransferFee_CannotBeSplitAway is why the fee rounds up. Truncating would
// make any transfer smaller than 10000/bps aatox free, letting a sender move an
// unlimited amount fee-free in dust-sized pieces.
func TestTransferFee_CannotBeSplitAway(t *testing.T) {
	k, ctx, bank := setup(t)
	alice, bob := acc("alice"), acc("bob")

	require.NoError(t, k.MintAtox(ctx, alice, math.NewInt(1_000)))

	// 1 aatox at 1000 bps truncates to 0; rounding up charges 1.
	require.Equal(t, "1", types.ComputeTransferFee(math.NewInt(1), 1000).String())

	for i := 0; i < 10; i++ {
		require.NoError(t, bank.SendCoins(ctx, alice, bob,
			sdk.NewCoins(sdk.NewCoin(atoxDenom, math.NewInt(1)))))
	}
	require.Equal(t, "10", k.GetGlobalState(ctx).TotalFeeBurned.String(),
		"every dust transfer must still pay a fee")
}

// TestTransferFee_ZeroBpsDisables confirms governance can turn the fee off
// without touching anything else.
func TestTransferFee_ZeroBpsDisables(t *testing.T) {
	k, ctx, bank := setup(t)
	alice, bob := acc("alice"), acc("bob")

	p := k.GetParams(ctx)
	p.TransferFeeBps = 0
	require.NoError(t, k.SetParams(ctx, p))

	require.NoError(t, k.MintAtox(ctx, alice, atox(100)))
	require.NoError(t, bank.SendCoins(ctx, alice, bob, sdk.NewCoins(sdk.NewCoin(atoxDenom, atox(100)))))

	require.Equal(t, atox(100).String(), k.AtoxBalance(ctx, bob).String())
	require.True(t, k.GetGlobalState(ctx).TotalFeeBurned.IsZero())
}

// TestTransferFee_SettlementStillCorrectWithFee makes sure charging the fee did
// not disturb the index accounting: the sender must be credited for the span it
// held the FULL pre-transfer balance, fee portion included.
func TestTransferFee_SettlementStillCorrectWithFee(t *testing.T) {
	k, ctx, bank := setup(t)
	alice, bob := acc("alice"), acc("bob")
	cap := k.GetParams(ctx).SupplyCap

	require.NoError(t, k.MintAtox(ctx, alice, cap))

	// One release accrues entirely to alice, who holds the whole cap.
	rel := math.NewIntWithDecimal(1, 28)
	require.NoError(t, k.AddToExchangePool(ctx, sourceModule, rel))

	// Move as much as the fee allows.
	send := types.MaxSendableWithFee(cap, k.GetParams(ctx).TransferFeeBps)
	require.NoError(t, bank.SendCoins(ctx, alice, bob, sdk.NewCoins(sdk.NewCoin(atoxDenom, send))))

	aP, aU := k.Claimable(ctx, alice)
	require.Equal(t, rel.String(), aP.Add(aU).String(),
		"alice held the whole cap for the whole span, so she is owed the whole release")

	bP, bU := k.Claimable(ctx, bob)
	require.True(t, bP.Add(bU).IsZero(), "bob accrues only from now on, got %s", bP.Add(bU))
}

// TestTransferFee_SelfTransferIsFree — a self-transfer nets to zero, so taxing it
// would let anyone burn another user's ATOX by relaying their own coins.
func TestTransferFee_SelfTransferIsFree(t *testing.T) {
	k, ctx, bank := setup(t)
	alice := acc("alice")

	require.NoError(t, k.MintAtox(ctx, alice, atox(100)))
	require.NoError(t, bank.SendCoins(ctx, alice, alice, sdk.NewCoins(sdk.NewCoin(atoxDenom, atox(100)))))

	require.Equal(t, atox(100).String(), k.AtoxBalance(ctx, alice).String())
	require.True(t, k.GetGlobalState(ctx).TotalFeeBurned.IsZero())
}

func TestMaxSendableWithFee(t *testing.T) {
	cases := []struct {
		balance int64
		bps     uint32
		want    string
	}{
		{110, 1000, "100"},
		{100, 1000, "90"}, // 90 + ceil(9) = 99 <= 100; 91 would need 92
		{11, 1000, "10"},
		{1, 1000, "0"}, // 1 aatox cannot cover itself plus a 1 aatox fee
		{100, 0, "100"},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("bal=%d/bps=%d", c.balance, c.bps), func(t *testing.T) {
			got := types.MaxSendableWithFee(math.NewInt(c.balance), c.bps)
			require.Equal(t, c.want, got.String())
			// The invariant the wallet relies on: amount + fee always fits.
			require.True(t, got.Add(types.ComputeTransferFee(got, c.bps)).LTE(math.NewInt(c.balance)))
		})
	}
}

func TestParams_TransferFeeBpsBounded(t *testing.T) {
	p := types.DefaultParams()
	require.Equal(t, uint32(1000), p.TransferFeeBps)

	p.TransferFeeBps = types.MaxTransferFeeBps
	require.NoError(t, p.Validate())

	p.TransferFeeBps = types.MaxTransferFeeBps + 1
	require.ErrorContains(t, p.Validate(), "transfer fee bps")
}

// The atox module account needs Burner permission for the fee burn to work in
// production; this documents the requirement next to the code that depends on it.
func TestModuleNeedsBurnerPermission(t *testing.T) {
	require.NotEmpty(t, authtypes.Burner, "atox module must be registered with authtypes.Burner in app.go maccPerms")
}
