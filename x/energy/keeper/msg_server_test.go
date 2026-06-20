package keeper

import (
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/atoshi-chain/atoshi/v20/x/energy/types"
)

// Default-duration regression: the MsgDelegateEnergy server normalizes
// DurationSeconds == 0 to types.DefaultDelegationDurationSeconds before
// calling the keeper. Wallets that omit the duration field get the
// 7-day default automatically — they do not need to encode the
// constant on their side.
func TestMsgDelegateEnergy_ZeroDurationUsesProtocolDefault(t *testing.T) {
	k, ctx, bank := newKeeperForTest(t)
	srv := NewMsgServerImpl(k)

	delegator := addr("delegator_______________")
	delegatee := addr("delegatee_______________")

	bank.balances[delegator.String()] = math.NewIntWithDecimal(90_000, 18)
	bank.balances[delegatee.String()] = math.NewIntWithDecimal(60_000, 18)
	bank.onSend = func(from, to sdk.AccAddress, amt sdk.Coins) {
		_, _ = k.SendRestriction(ctx, from, to, amt)
	}

	a := k.Settle(ctx, delegator)
	a.TxEnergyAccrued = 150_000
	k.SetEnergyAccount(ctx, a)

	resp, err := srv.DelegateEnergy(ctx, &types.MsgDelegateEnergy{
		Delegator:       delegator.String(),
		Delegatee:       delegatee.String(),
		Amount:          50_000,
		DurationSeconds: 0, // wallet omits the field → server applies default
	})
	require.NoError(t, err)
	require.NotZero(t, resp.DelegationId)

	d, ok := k.GetDelegation(ctx, resp.DelegationId)
	require.True(t, ok)

	startTime := ctx.BlockTime().Unix()
	want := startTime + types.DefaultDelegationDurationSeconds
	require.Equal(t, want, d.ExpiresAt,
		"DurationSeconds=0 must be normalized to DefaultDelegationDurationSeconds (7 days)")
	require.EqualValues(t, 604_800, types.DefaultDelegationDurationSeconds,
		"7 days in seconds")
}

// Sanity: an explicitly-set duration is honored verbatim. The default
// only kicks in when the client sends 0.
func TestMsgDelegateEnergy_ExplicitDurationHonored(t *testing.T) {
	k, ctx, bank := newKeeperForTest(t)
	srv := NewMsgServerImpl(k)

	delegator := addr("delegator_______________")
	delegatee := addr("delegatee_______________")

	bank.balances[delegator.String()] = math.NewIntWithDecimal(90_000, 18)
	bank.balances[delegatee.String()] = math.NewIntWithDecimal(60_000, 18)
	bank.onSend = func(from, to sdk.AccAddress, amt sdk.Coins) {
		_, _ = k.SendRestriction(ctx, from, to, amt)
	}

	a := k.Settle(ctx, delegator)
	a.TxEnergyAccrued = 150_000
	k.SetEnergyAccount(ctx, a)

	const explicit int64 = 3 * 24 * 60 * 60 // 3 days
	resp, err := srv.DelegateEnergy(ctx, &types.MsgDelegateEnergy{
		Delegator:       delegator.String(),
		Delegatee:       delegatee.String(),
		Amount:          50_000,
		DurationSeconds: explicit,
	})
	require.NoError(t, err)

	d, ok := k.GetDelegation(ctx, resp.DelegationId)
	require.True(t, ok)
	require.Equal(t, ctx.BlockTime().Unix()+explicit, d.ExpiresAt)
}

// ValidateBasic: 0 must be ACCEPTED (means "use default"); negative
// must be REJECTED.
func TestMsgDelegateEnergy_ValidateBasic_DurationSemantics(t *testing.T) {
	delegator := addr("delegator_______________").String()
	delegatee := addr("delegatee_______________").String()

	t.Run("zero accepted", func(t *testing.T) {
		msg := types.MsgDelegateEnergy{
			Delegator:       delegator,
			Delegatee:       delegatee,
			Amount:          1,
			DurationSeconds: 0,
		}
		require.NoError(t, msg.ValidateBasic(),
			"DurationSeconds=0 is the 'use default' signal — must NOT be rejected")
	})

	t.Run("negative rejected", func(t *testing.T) {
		msg := types.MsgDelegateEnergy{
			Delegator:       delegator,
			Delegatee:       delegatee,
			Amount:          1,
			DurationSeconds: -1,
		}
		require.ErrorIs(t, msg.ValidateBasic(), types.ErrInvalidDuration)
	})
}
