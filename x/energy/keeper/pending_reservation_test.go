package keeper

import (
	"encoding/json"
	"fmt"
	"testing"

	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/atoshi-chain/atoshi/v20/x/energy/types"
)

// plantLegacyMarker writes a pending-reservation marker in the exact JSON
// shape the pre-fix AnteHandler wrote, so the tests exercise the state an
// upgraded node actually finds.
func plantLegacyMarker(t *testing.T, k Keeper, ctx sdk.Context, txHash []byte, signer sdk.AccAddress, gasLimit uint64, res ConsumeResult) {
	t.Helper()
	bz, err := json.Marshal(map[string]any{
		"signer":    signer.String(),
		"gas_limit": gasLimit,
		"consume_result": map[string]any{
			"energy_deducted":    res.EnergyDeducted,
			"own_deducted":       res.OwnDeducted,
			"delegated_deducted": res.DelegatedDeducted,
			"deploy_energy_used": res.DeployEnergyUsed,
			"shortfall_gas":      res.ShortfallGas,
			"free":               res.Free,
		},
	})
	require.NoError(t, err)
	ctx.KVStore(k.storeKey).Set(types.PendingReservationKey(txHash), bz)
}

func countMarkers(k Keeper, ctx sdk.Context) int {
	it := storetypes.KVStorePrefixIterator(ctx.KVStore(k.storeKey), types.KeyPrefixPendingReservation)
	defer it.Close()
	n := 0
	for ; it.Valid(); it.Next() {
		n++
	}
	return n
}

// M3 regression: a failed tx keeps its energy spent. The pre-fix
// EndBlocker found the failed tx's marker and refunded the full
// reservation, so failing txs cost nothing and the same energy could be
// reused every block. A marker left in state (by an older binary) must now
// be deleted WITHOUT refund.
func TestEndBlocker_LeftoverReservationIsNotRefunded(t *testing.T) {
	k, ctx, bank := newKeeperForTest(t)
	signer := addr("signer__________________")

	bank.balances[signer.String()] = math.NewIntWithDecimal(60_000, 18)
	acct := k.Settle(ctx, signer)
	acct.TxEnergyAccrued = 10_000
	k.SetEnergyAccount(ctx, acct)

	// The ante path of a tx whose msgs then fail: energy is consumed, and
	// (under the old binary) a marker was left behind.
	res, err := k.Consume(ctx, signer, 10_000, false,
		[]string{"/cosmos.bank.v1beta1.MsgSend"})
	require.NoError(t, err)
	require.EqualValues(t, 10_000, res.OwnDeducted)
	require.EqualValues(t, 0, k.GetEnergyAccount(ctx, signer).TxEnergyAccrued)
	plantLegacyMarker(t, k, ctx, []byte("simulated-failed-tx-hash--------"), signer, 10_000, res)

	k.EndBlocker(ctx)

	require.EqualValues(t, 0, k.GetEnergyAccount(ctx, signer).TxEnergyAccrued,
		"a failed tx's energy is a fee: EndBlocker must not give it back")
	require.Equal(t, 0, countMarkers(k, ctx), "EndBlocker must delete leftover markers")
}

// Old binaries accumulated markers while EnergyEnabled was false (their
// EndBlocker returned before sweeping). The cleanup must run regardless of
// the flag, and must also remove values it cannot parse.
func TestEndBlocker_PrunesLegacyMarkersEvenWhenDisabled(t *testing.T) {
	k, ctx, _ := newKeeperForTest(t)
	p := k.GetParams(ctx)
	p.EnergyEnabled = false
	require.NoError(t, k.SetParams(ctx, p))

	signer := addr("signer__________________")
	plantLegacyMarker(t, k, ctx, []byte("hash-a"), signer, 21_000, ConsumeResult{ShortfallGas: 21_000})
	ctx.KVStore(k.storeKey).Set(types.PendingReservationKey([]byte("hash-garbage")), []byte("{not json"))
	require.Equal(t, 2, countMarkers(k, ctx))

	k.EndBlocker(ctx)
	require.Equal(t, 0, countMarkers(k, ctx))
}

// A backlog larger than one batch drains over consecutive blocks.
func TestEndBlocker_PrunesLegacyMarkersInBatches(t *testing.T) {
	k, ctx, _ := newKeeperForTest(t)
	signer := addr("signer__________________")
	total := legacyReservationPruneBatch + 7
	for i := 0; i < total; i++ {
		plantLegacyMarker(t, k, ctx, []byte(fmt.Sprintf("hash-%05d", i)), signer, 1, ConsumeResult{})
	}

	k.EndBlocker(ctx)
	require.Equal(t, 7, countMarkers(k, ctx))
	k.EndBlocker(ctx)
	require.Equal(t, 0, countMarkers(k, ctx))
}

// When no markers are present EndBlocker must not touch user energy.
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
