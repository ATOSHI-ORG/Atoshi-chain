package keeper

import (
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
)

// Audit Issue-1 (round2) regression: marker round-trips through Set →
// Iterate → Delete with every field preserved (including the LIFO
// DelegationConsumptions slice). The marker is the only state that
// connects an AnteHandler reservation to an EndBlocker refund, so a
// serialization bug here would silently re-introduce the permanent-
// loss issue.
func TestPendingReservation_RoundTrip(t *testing.T) {
	k, ctx, _ := newKeeperForTest(t)
	signer := addr("signer__________________")
	txHash := []byte("32-byte-txhash-deterministic-fix")

	res := ConsumeResult{
		EnergyDeducted:    12_000,
		OwnDeducted:       10_000,
		DelegatedDeducted: 2_000,
		DeployEnergyUsed:  0,
		ShortfallGas:      0,
		Free:              false,
		DelegationConsumptions: []DelegationConsumption{
			{DelegationID: 7, Amount: 2_000},
		},
	}
	k.SetPendingReservation(ctx, txHash, signer, 50_000, res)

	var seen int
	k.IteratePendingReservations(ctx, func(gotHash []byte, gotSigner sdk.AccAddress, gotGasLimit uint64, gotRes ConsumeResult) bool {
		seen++
		require.Equal(t, txHash, gotHash, "tx hash must be preserved verbatim")
		require.Equal(t, signer, gotSigner)
		require.EqualValues(t, 50_000, gotGasLimit)
		require.Equal(t, res, gotRes, "ConsumeResult must round-trip including DelegationConsumptions")
		return false
	})
	require.Equal(t, 1, seen)

	k.DeletePendingReservation(ctx, txHash)
	seen = 0
	k.IteratePendingReservations(ctx, func(_ []byte, _ sdk.AccAddress, _ uint64, _ ConsumeResult) bool {
		seen++
		return false
	})
	require.Equal(t, 0, seen, "delete must remove the marker")
}

// Audit Issue-1 (round2) regression: EndBlocker refunds the FULL
// reservation when a marker is left behind (= failed tx). Without this
// behavior the signer permanently loses gas_limit worth of energy on
// every failed tx — exactly what the auditor flagged.
func TestEndBlocker_RefundsLeftoverReservation(t *testing.T) {
	k, ctx, bank := newKeeperForTest(t)
	signer := addr("signer__________________")

	bank.balances[signer.String()] = math.NewIntWithDecimal(60_000, 18)
	acct := k.Settle(ctx, signer)
	acct.TxEnergyAccrued = 10_000
	k.SetEnergyAccount(ctx, acct)

	// Simulate the ante path: consume 10k own energy, write a marker.
	res, err := k.Consume(ctx, signer, 10_000, false,
		[]string{"/cosmos.bank.v1beta1.MsgSend"})
	require.NoError(t, err)
	require.EqualValues(t, 10_000, res.OwnDeducted)
	require.EqualValues(t, 0, res.DelegatedDeducted)

	post := k.GetEnergyAccount(ctx, signer)
	require.EqualValues(t, 0, post.TxEnergyAccrued, "ante drained own bucket")

	txHash := []byte("simulated-failed-tx-hash--------")
	k.SetPendingReservation(ctx, txHash, signer, 10_000, res)

	// Simulate "PostHandler did NOT run" (msgs failed): marker is still
	// present at EndBlocker time. EndBlocker must refund the full 10k.
	k.EndBlocker(ctx)

	postEnd := k.GetEnergyAccount(ctx, signer)
	require.EqualValues(t, 10_000, postEnd.TxEnergyAccrued,
		"audit Issue-1: leftover marker must be refunded in full at EndBlocker")

	// Marker must be gone — sweeping twice should not double-refund.
	var still int
	k.IteratePendingReservations(ctx, func(_ []byte, _ sdk.AccAddress, _ uint64, _ ConsumeResult) bool {
		still++
		return false
	})
	require.Equal(t, 0, still, "EndBlocker must delete consumed markers")
}

// Audit Issue-1 (round2) companion: when no markers are present (the
// happy path where every tx in the block completed PostHandler and
// deleted its own marker), EndBlocker must be a no-op for the user's
// energy state.
func TestEndBlocker_NoMarkersIsNoop(t *testing.T) {
	k, ctx, bank := newKeeperForTest(t)
	signer := addr("signer__________________")

	bank.balances[signer.String()] = math.NewIntWithDecimal(60_000, 18)
	acct := k.Settle(ctx, signer)
	acct.TxEnergyAccrued = 25_000
	k.SetEnergyAccount(ctx, acct)

	k.EndBlocker(ctx)

	post := k.GetEnergyAccount(ctx, signer)
	require.EqualValues(t, 25_000, post.TxEnergyAccrued,
		"EndBlocker with no pending markers must not mutate user energy")
}
