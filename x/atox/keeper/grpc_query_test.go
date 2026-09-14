package keeper_test

import (
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/atoshi-chain/atoshi/v20/x/atox/keeper"
	"github.com/atoshi-chain/atoshi/v20/x/atox/types"
)

// TestAccountQuery_ReasonsAreDistinct is why claim_blocked_reason exists.
//
// A wallet that only sees can_claim=false has to guess, and the guess it
// reaches for is "nothing to claim" -- which is exactly wrong for the one case
// where the holder IS owed and the pool is short.
func TestAccountQuery_ReasonsAreDistinct(t *testing.T) {
	k, ctx, _ := setup(t)
	q := keeper.NewQuerier(k)
	alice := acc("alice")

	// 1. no ATOX at all
	res, err := q.Account(ctx, &types.QueryAccountRequest{Address: alice.String()})
	require.NoError(t, err)
	require.False(t, res.CanClaim)
	require.Equal(t, "nothing_to_claim", res.ClaimBlockedReason)

	// 2. holds ATOX and a release landed -> claimable
	require.NoError(t, k.MintAtox(ctx, alice, k.GetParams(ctx).SupplyCap))
	require.NoError(t, k.AddToExchangePool(ctx, sourceModule, math.NewIntWithDecimal(1, 28)))
	res, err = q.Account(ctx, &types.QueryAccountRequest{Address: alice.String()})
	require.NoError(t, err)
	require.True(t, res.CanClaim, "reason was %q", res.ClaimBlockedReason)
	require.Equal(t, "", res.ClaimBlockedReason)
	require.True(t, res.Claimable.IsPositive())

	// 3. module switched off
	p := k.GetParams(ctx)
	p.Enabled = false
	require.NoError(t, k.SetParams(ctx, p))
	res, err = q.Account(ctx, &types.QueryAccountRequest{Address: alice.String()})
	require.NoError(t, err)
	require.False(t, res.CanClaim)
	require.Equal(t, "module_disabled", res.ClaimBlockedReason)
}

// TestAccountQuery_MaxSendableIsActuallySendable is the point of the field: a
// transfer of exactly max_sendable must go through, and one aatox more must not.
//
// Both legs matter. Offering the raw balance as "send max" fails because the
// send settles the sender first (burning) and then charges the fee on top --
// neither of which the displayed balance accounts for.
func TestAccountQuery_MaxSendableIsActuallySendable(t *testing.T) {
	k, ctx, bank := setup(t)
	q := keeper.NewQuerier(k)
	alice, bob := acc("alice"), acc("bob")

	require.NoError(t, k.MintAtox(ctx, alice, k.GetParams(ctx).SupplyCap))
	require.NoError(t, k.AddToExchangePool(ctx, sourceModule, math.NewIntWithDecimal(1, 28)))

	res, err := q.Account(ctx, &types.QueryAccountRequest{Address: alice.String()})
	require.NoError(t, err)
	require.True(t, res.MaxSendable.IsPositive())
	require.True(t, res.BurnOnSettle.IsPositive(), "a release is outstanding, so settling burns")
	require.True(t, res.MaxSendable.LT(res.AtoxBalance),
		"max_sendable must be below the raw balance, or the field buys nothing")

	// One more than the quoted maximum must fail.
	tooMuch := res.MaxSendable.AddRaw(1)
	require.Error(t, bank.SendCoins(ctx, alice, bob,
		sdk.NewCoins(sdk.NewCoin(atoxDenom, tooMuch))))

	// The quoted maximum must succeed.
	require.NoError(t, bank.SendCoins(ctx, alice, bob,
		sdk.NewCoins(sdk.NewCoin(atoxDenom, res.MaxSendable))))
}

// TestAccountQuery_BurnOnSettleMatchesUnsettled pins the 1:1 relationship the
// wallet arithmetic depends on.
func TestAccountQuery_BurnOnSettleMatchesUnsettled(t *testing.T) {
	k, ctx, _ := setup(t)
	q := keeper.NewQuerier(k)
	alice := acc("alice")

	require.NoError(t, k.MintAtox(ctx, alice, k.GetParams(ctx).SupplyCap))
	require.NoError(t, k.AddToExchangePool(ctx, sourceModule, math.NewIntWithDecimal(1, 28)))

	res, err := q.Account(ctx, &types.QueryAccountRequest{Address: alice.String()})
	require.NoError(t, err)
	require.Equal(t, res.Unsettled.String(), res.BurnOnSettle.String())
}
