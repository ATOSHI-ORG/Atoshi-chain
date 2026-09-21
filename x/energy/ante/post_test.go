package ante_test

import (
	"testing"

	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	energyante "github.com/atoshi-chain/atoshi/v20/x/energy/ante"
	"github.com/atoshi-chain/atoshi/v20/x/energy/keeper"
	"github.com/atoshi-chain/atoshi/v20/x/energy/types"
)

func terminalPost(ctx sdk.Context, _ sdk.Tx, _ bool, _ bool) (sdk.Context, error) {
	return ctx, nil
}

// seedInboundDelegation directly writes an inbound delegation record
// and bumps the delegatee's DelegatedInUsable to match. This bypasses
// the full Delegate path (which would require funding the delegator's
// bank balance and accruing energy) — we just need a consistent
// (Bound, DelegatedInUsable) pair so the PostHandler refund has
// something to roll back.
func seedInboundDelegation(
	t *testing.T,
	k keeper.Keeper,
	ctx sdk.Context,
	delegator, delegatee sdk.AccAddress,
	amount uint64,
) types.EnergyDelegation {
	t.Helper()
	d := types.EnergyDelegation{
		Id:         1,
		Delegator:  delegator.String(),
		Delegatee:  delegatee.String(),
		Amount:     amount,
		LockedAtos: math.NewInt(0),
		StartTime:  ctx.BlockTime().Unix(),
		ExpiresAt:  ctx.BlockTime().Unix() + 7*24*3600,
		Used:       0,
	}
	k.SetDelegationForTest(ctx, d)
	acct := k.GetEnergyAccount(ctx, delegatee)
	if acct.Address == "" {
		acct.Address = delegatee.String()
	}
	acct.DelegatedInUsable = amount
	k.SetEnergyAccount(ctx, acct)
	return d
}

// simulateConsume mirrors what Consume() does to (Bound, account) when
// it drains `amount` units from delegated_in: Bound.Used += amount,
// DelegatedInUsable -= amount. Pre-fix bug under test: the PostHandler
// then refunded this and reverted both back to pre-state.
func simulateConsume(
	t *testing.T,
	k keeper.Keeper,
	ctx sdk.Context,
	delegatee sdk.AccAddress,
	d *types.EnergyDelegation,
	amount uint64,
) {
	t.Helper()
	d.Used += amount
	k.SetDelegationForTest(ctx, *d)
	acct := k.GetEnergyAccount(ctx, delegatee)
	acct.DelegatedInUsable -= amount
	k.SetEnergyAccount(ctx, acct)
}

func runRefundPost(
	t *testing.T,
	k keeper.Keeper,
	ctx sdk.Context,
	signer sdk.AccAddress,
	reserved keeper.ConsumeResult,
	gasLimit, gasUsed uint64,
) {
	t.Helper()
	gm := storetypes.NewGasMeter(gasLimit)
	gm.ConsumeGas(gasUsed, "test")
	ctx = ctx.WithGasMeter(gm).
		WithValue(energyante.CtxKeyEnergyReserved, reserved).
		WithValue(energyante.CtxKeyEnergySigner, signer)
	tx := fakeFeeTx{
		gas:      gasLimit,
		fee:      sdk.NewCoins(sdk.NewCoin("liao", math.NewInt(1_000))),
		feePayer: signer,
		msgs:     []sdk.Msg{mockMsg{typeURL: "/cosmos.bank.v1beta1.MsgSend"}},
	}
	d := energyante.NewEnergyRefundDecorator(k)
	_, err := d.PostHandle(ctx, tx, false, true, terminalPost)
	require.NoError(t, err)
}

// Round-3 regression test for the infinite-replay bug observed on
// testnet: a recipient with delegated_in=30000 sent three back-to-back
// 300k-gas MsgSend txs. Each tx emitted energy_consumed=30000 in its
// events, but Bound.Used and DelegatedInUsable on chain never moved —
// the same 30k subsidy was burned three times.
//
// Root cause: the PostHandler computed
//
//	refund = min(reserved_energy, gas_limit - gas_used)
//
// which silently rolled back the FULL reserved energy whenever the tx
// had any unused gas room at all (the common case: a 300k MsgSend
// uses ~232k, leaving 68k unused — that's >= 30k, so the entire 30k
// got refunded).
//
// Fix: energy is consumed FIRST inside the gas budget, so the energy
// that actually got burned is min(reserved, gas_used). Anything left
// over is what's eligible for refund.
func TestPostHandler_EnergyFullyConsumed_NoRefund(t *testing.T) {
	k, _, _, ctx := newTestEnv(t)
	delegator := sdk.AccAddress([]byte("delegator_______________"))
	delegatee := sdk.AccAddress([]byte("delegatee_______________"))

	d := seedInboundDelegation(t, k, ctx, delegator, delegatee, 30_000)
	simulateConsume(t, k, ctx, delegatee, &d, 30_000)
	require.EqualValues(t, 0, k.GetEnergyAccount(ctx, delegatee).DelegatedInUsable,
		"precondition: Consume left DelegatedInUsable at 0")
	dMid, _ := k.GetDelegation(ctx, d.Id)
	require.EqualValues(t, 30_000, dMid.Used, "precondition: Consume left Bound.Used at 30000")

	reserved := keeper.ConsumeResult{
		EnergyDeducted:    30_000,
		DelegatedDeducted: 30_000,
		ShortfallGas:      270_000,
		DelegationConsumptions: []keeper.DelegationConsumption{
			{DelegationID: d.Id, Amount: 30_000},
		},
	}
	// Real-world numbers from tx 06BE8AAC...: gasLimit=300000, gasUsed=231837.
	runRefundPost(t, k, ctx, delegatee, reserved, 300_000, 231_837)

	after := k.GetEnergyAccount(ctx, delegatee)
	require.EqualValues(t, 0, after.DelegatedInUsable,
		"BUG (pre-fix): energy was refunded even though gasUsed >> reserved. "+
			"Recipient could re-spend the same grant forever.")
	dAfter, ok := k.GetDelegation(ctx, d.Id)
	require.True(t, ok)
	require.EqualValues(t, 30_000, dAfter.Used,
		"BUG (pre-fix): Bound.Used rolled back; LIFO refund undid a real consumption.")
}

// gasUsed < reserved → refund only the unused portion of the reservation.
func TestPostHandler_EnergyPartiallyConsumed_RefundsRemainder(t *testing.T) {
	k, _, _, ctx := newTestEnv(t)
	delegator := sdk.AccAddress([]byte("delegator_______________"))
	delegatee := sdk.AccAddress([]byte("delegatee_______________"))

	d := seedInboundDelegation(t, k, ctx, delegator, delegatee, 30_000)
	simulateConsume(t, k, ctx, delegatee, &d, 30_000)

	reserved := keeper.ConsumeResult{
		EnergyDeducted:    30_000,
		DelegatedDeducted: 30_000,
		ShortfallGas:      270_000,
		DelegationConsumptions: []keeper.DelegationConsumption{
			{DelegationID: d.Id, Amount: 30_000},
		},
	}
	// gasUsed=12000 → 12000 energy actually burnt → refund 18000.
	runRefundPost(t, k, ctx, delegatee, reserved, 300_000, 12_000)

	after := k.GetEnergyAccount(ctx, delegatee)
	require.EqualValues(t, 18_000, after.DelegatedInUsable)
	dAfter, _ := k.GetDelegation(ctx, d.Id)
	require.EqualValues(t, 12_000, dAfter.Used,
		"Bound.Used should reflect only the energy that actually paid for gasUsed")
}

// gasUsed == 0 → refund everything (msg failed without consuming gas).
func TestPostHandler_GasUsedZero_RefundsAll(t *testing.T) {
	k, _, _, ctx := newTestEnv(t)
	delegator := sdk.AccAddress([]byte("delegator_______________"))
	delegatee := sdk.AccAddress([]byte("delegatee_______________"))

	d := seedInboundDelegation(t, k, ctx, delegator, delegatee, 30_000)
	simulateConsume(t, k, ctx, delegatee, &d, 30_000)

	reserved := keeper.ConsumeResult{
		EnergyDeducted:    30_000,
		DelegatedDeducted: 30_000,
		DelegationConsumptions: []keeper.DelegationConsumption{
			{DelegationID: d.Id, Amount: 30_000},
		},
	}
	runRefundPost(t, k, ctx, delegatee, reserved, 300_000, 0)

	after := k.GetEnergyAccount(ctx, delegatee)
	require.EqualValues(t, 30_000, after.DelegatedInUsable)
	dAfter, _ := k.GetDelegation(ctx, d.Id)
	require.EqualValues(t, 0, dAfter.Used)
}

// gasUsed >= gasLimit (full out-of-gas) → no refund, early-return path.
func TestPostHandler_GasUsedAtLimit_NoRefund(t *testing.T) {
	k, _, _, ctx := newTestEnv(t)
	delegator := sdk.AccAddress([]byte("delegator_______________"))
	delegatee := sdk.AccAddress([]byte("delegatee_______________"))

	d := seedInboundDelegation(t, k, ctx, delegator, delegatee, 30_000)
	simulateConsume(t, k, ctx, delegatee, &d, 30_000)

	reserved := keeper.ConsumeResult{
		EnergyDeducted:    30_000,
		DelegatedDeducted: 30_000,
		ShortfallGas:      270_000,
		DelegationConsumptions: []keeper.DelegationConsumption{
			{DelegationID: d.Id, Amount: 30_000},
		},
	}
	runRefundPost(t, k, ctx, delegatee, reserved, 300_000, 300_000)

	after := k.GetEnergyAccount(ctx, delegatee)
	require.EqualValues(t, 0, after.DelegatedInUsable,
		"out-of-gas tx should not refund")
	dAfter, _ := k.GetDelegation(ctx, d.Id)
	require.EqualValues(t, 30_000, dAfter.Used)
}

// Three back-to-back 300k-gas txs against a single 30k inbound grant.
// After tx1 the grant must be exhausted; subsequent txs cannot draw
// from it. This is the exact pattern that exposed the bug on testnet.
func TestPostHandler_NoInfiniteReplay(t *testing.T) {
	k, _, _, ctx := newTestEnv(t)
	delegator := sdk.AccAddress([]byte("delegator_______________"))
	delegatee := sdk.AccAddress([]byte("delegatee_______________"))

	d := seedInboundDelegation(t, k, ctx, delegator, delegatee, 30_000)

	for i := 0; i < 3; i++ {
		acct := k.GetEnergyAccount(ctx, delegatee)
		if acct.DelegatedInUsable == 0 {
			// Energy already exhausted by an earlier iteration: in
			// production Consume() would set ShortfallGas=gasLimit
			// and skip the PostHandler refund path. Mirror that here.
			continue
		}
		// Drain whatever the previous iteration left and run the
		// refund handler with a representative gas profile.
		draw := acct.DelegatedInUsable
		dPre, _ := k.GetDelegation(ctx, d.Id)
		simulateConsume(t, k, ctx, delegatee, &dPre, draw)

		reserved := keeper.ConsumeResult{
			EnergyDeducted:    draw,
			DelegatedDeducted: draw,
			ShortfallGas:      300_000 - draw,
			DelegationConsumptions: []keeper.DelegationConsumption{
				{DelegationID: d.Id, Amount: draw},
			},
		}
		runRefundPost(t, k, ctx, delegatee, reserved, 300_000, 231_837)
	}

	final := k.GetEnergyAccount(ctx, delegatee)
	require.EqualValues(t, 0, final.DelegatedInUsable,
		"after three 300k-gas txs the 30k grant must be exhausted, not "+
			"infinitely replayable")
	dFinal, _ := k.GetDelegation(ctx, d.Id)
	require.EqualValues(t, 30_000, dFinal.Used,
		"Bound.Used must show the one-time real consumption")
}
