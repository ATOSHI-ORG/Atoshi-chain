package keeper

import (
	"encoding/json"
	"fmt"

	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/atoshi-chain/atoshi/v20/x/energy/types"
)

// pendingReservation is the on-disk representation of a single Consume()
// reservation that has not been resolved by a PostHandler refund yet.
//
// Audit Issue-1 (round2): the AnteHandler commits the energy deduction
// before runMsgs executes; the PostHandler refunds the unused portion
// only on tx success. When runMsgs fails, the PostHandler does not
// run and the msg-context state is discarded — so a refund call
// scheduled there is lost. Without compensation, a failed tx
// permanently consumes gas_limit worth of energy from the signer.
//
// We record the reservation here in ante (committed alongside the
// deduction itself), have the post-handler delete it on success, and
// have EndBlocker sweep any leftover markers as failed-tx refunds.
//
// The marker lives in state for at most one block — it is either
// deleted by PostHandler later in the same tx or by EndBlocker at the
// end of the same block — so JSON encoding (instead of a dedicated
// proto message) keeps the change surgical without committing us to a
// long-lived schema.
//
// Audit Recommendation 4 (documented exception): x/oracle and
// x/tokenomics KV values were migrated from JSON to k.cdc.Marshal
// against their existing proto messages so state layout is
// schema-controlled and consistent with the rest of the SDK. This
// marker is exempted intentionally:
//
//  1. Lifetime is bounded to one block. No marker survives across an
//     upgrade boundary, so schema drift cannot break state.
//  2. Never read externally (no query endpoint, no genesis
//     import/export, no cross-module reader). It is a keeper-private
//     coordination primitive between AnteHandler / PostHandler /
//     EndBlocker inside the same block.
//  3. Adding a proto message would also require one for
//     ConsumeResult, whose fields track keeper internals — churn
//     with no correctness upside for a marker that self-drains
//     every block.
//
// The consistency goal of Recommendation 4 is met on the two
// long-lived stores. This one stays JSON by design.
type pendingReservation struct {
	Signer        string                  `json:"signer"`
	GasLimit      uint64                  `json:"gas_limit"`
	ConsumeResult types.ConsumeResultJSON `json:"consume_result"`
}

// SetPendingReservation writes a marker keyed by tx hash. Idempotent.
//
// Audit Recommendation 2 (round 3 follow-up): previously this
// function panicked on json.Marshal failure. Even though the marshal
// only fails on type/cycle bugs (not data content), panicking in an
// AnteHandler path halts the entire chain. Convert to an error
// return so the decorator can reject the tx cleanly and let CometBFT
// keep producing blocks.
func (k Keeper) SetPendingReservation(
	ctx sdk.Context, txHash []byte, signer sdk.AccAddress, gasLimit uint64, res ConsumeResult,
) error {
	if len(txHash) == 0 {
		return nil
	}
	bz, err := json.Marshal(pendingReservation{
		Signer:        signer.String(),
		GasLimit:      gasLimit,
		ConsumeResult: consumeResultToJSON(res),
	})
	if err != nil {
		return fmt.Errorf("marshal pending reservation: %w", err)
	}
	ctx.KVStore(k.storeKey).Set(types.PendingReservationKey(txHash), bz)
	return nil
}

// DeletePendingReservation removes the marker once it has been resolved.
// Called by the PostHandler after a successful refund pass.
func (k Keeper) DeletePendingReservation(ctx sdk.Context, txHash []byte) {
	if len(txHash) == 0 {
		return
	}
	ctx.KVStore(k.storeKey).Delete(types.PendingReservationKey(txHash))
}

// IteratePendingReservations walks every pending reservation. Used by
// EndBlocker to refund failed-tx reservations.
func (k Keeper) IteratePendingReservations(
	ctx sdk.Context, fn func(txHash []byte, signer sdk.AccAddress, gasLimit uint64, res ConsumeResult) (stop bool),
) {
	store := ctx.KVStore(k.storeKey)
	it := storetypes.KVStorePrefixIterator(store, types.KeyPrefixPendingReservation)
	defer it.Close()
	for ; it.Valid(); it.Next() {
		var p pendingReservation
		if err := json.Unmarshal(it.Value(), &p); err != nil {
			// Malformed marker — skip rather than panic. Should be
			// impossible in practice (we wrote it ourselves), but a
			// panic in EndBlocker would halt the chain.
			continue
		}
		signer, err := sdk.AccAddressFromBech32(p.Signer)
		if err != nil {
			continue
		}
		key := it.Key()
		// Strip the prefix byte to recover the raw tx hash.
		txHash := make([]byte, len(key)-1)
		copy(txHash, key[1:])
		if stop := fn(txHash, signer, p.GasLimit, consumeResultFromJSON(p.ConsumeResult)); stop {
			return
		}
	}
}

// consumeResultToJSON / consumeResultFromJSON shuttle the in-memory
// ConsumeResult through a JSON-friendly type. Required because
// ConsumeResult is defined in the keeper package (to keep its receiver
// methods close to keeper state) and we need types.ConsumeResultJSON
// (in the types package) to break an import cycle when the marker
// schema would otherwise reference keeper-package symbols.
func consumeResultToJSON(r ConsumeResult) types.ConsumeResultJSON {
	out := types.ConsumeResultJSON{
		EnergyDeducted:    r.EnergyDeducted,
		OwnDeducted:       r.OwnDeducted,
		DelegatedDeducted: r.DelegatedDeducted,
		DeployEnergyUsed:  r.DeployEnergyUsed,
		ShortfallGas:      r.ShortfallGas,
		Free:              r.Free,
	}
	if len(r.DelegationConsumptions) > 0 {
		out.DelegationConsumptions = make([]types.DelegationConsumptionJSON, len(r.DelegationConsumptions))
		for i, c := range r.DelegationConsumptions {
			out.DelegationConsumptions[i] = types.DelegationConsumptionJSON{
				DelegationID: c.DelegationID,
				Amount:       c.Amount,
			}
		}
	}
	return out
}

func consumeResultFromJSON(j types.ConsumeResultJSON) ConsumeResult {
	out := ConsumeResult{
		EnergyDeducted:    j.EnergyDeducted,
		OwnDeducted:       j.OwnDeducted,
		DelegatedDeducted: j.DelegatedDeducted,
		DeployEnergyUsed:  j.DeployEnergyUsed,
		ShortfallGas:      j.ShortfallGas,
		Free:              j.Free,
	}
	if len(j.DelegationConsumptions) > 0 {
		out.DelegationConsumptions = make([]DelegationConsumption, len(j.DelegationConsumptions))
		for i, c := range j.DelegationConsumptions {
			out.DelegationConsumptions[i] = DelegationConsumption{
				DelegationID: c.DelegationID,
				Amount:       c.Amount,
			}
		}
	}
	return out
}
