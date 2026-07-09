package keeper

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/atoshi-chain/atoshi/v20/x/energy/types"
)

// EndBlocker fires once per block. Two responsibilities:
//
//  1. Release any EnergyDelegation whose expires_at has passed.
//  2. Audit Issue-1 (round2): refund energy reservations whose
//     AnteHandler marker survived to end-of-block. The
//     AnteHandler writes the marker after Consume() and commits it as
//     part of ante state; the PostHandler deletes the marker on
//     successful tx commit. A marker still in state at EndBlocker
//     time therefore belongs to a tx whose runMsgs returned an error
//     (post-handler never ran) and whose energy deduction would
//     otherwise be permanently lost.
//
// We deliberately do NOT settle every account here — accounts settle
// lazily on touch. Sweeping by expiry uses a sorted secondary index, so
// the work is O(expired) per block, not O(total accounts). Pending
// reservations are also iterated via a typed prefix.
func (k Keeper) EndBlocker(ctx sdk.Context) {
	if !k.GetParams(ctx).EnergyEnabled {
		return
	}
	k.SweepExpiredDelegations(ctx)
	k.refundFailedTxReservations(ctx)
}

// refundFailedTxReservations restores the energy reserved by failed
// txs. We refund the FULL reservation (not gas_limit - gas_used)
// because EndBlocker has no access to per-tx gas-used data — the
// CometBFT result list isn't available at this layer. Refunding the
// full amount is the conservative choice: the user is made whole, and
// DoS attempts are still discouraged by the ATOS shortfall fee that
// the EnergyDeductDecorator deducts in ante (which is NOT touched
// here, matching standard cosmos-sdk fee semantics).
//
// Each marker is consumed (deleted) after its refund. Two-phase
// (collect then mutate) iteration keeps the underlying KV store
// iterator stable while we both read it and delete from the same
// prefix.
func (k Keeper) refundFailedTxReservations(ctx sdk.Context) {
	type entry struct {
		txHash   []byte
		signer   sdk.AccAddress
		gasLimit uint64
		res      ConsumeResult
	}
	var pending []entry
	k.IteratePendingReservations(ctx, func(txHash []byte, signer sdk.AccAddress, gasLimit uint64, res ConsumeResult) bool {
		pending = append(pending, entry{txHash: txHash, signer: signer, gasLimit: gasLimit, res: res})
		return false
	})
	if len(pending) == 0 {
		return
	}
	logger := ctx.Logger().With("module", "x/"+types.ModuleName, "audit", "Issue-1")
	for _, e := range pending {
		refund := saturatingAdd(e.res.OwnDeducted, e.res.DelegatedDeducted)
		if refund > 0 {
			k.RefundEnergy(ctx, e.signer, refund, e.res)
		}
		k.DeletePendingReservation(ctx, e.txHash)
		logger.Debug("refunded failed-tx energy reservation",
			"signer", e.signer.String(),
			"gas_limit", fmt.Sprintf("%d", e.gasLimit),
			"refunded_energy", fmt.Sprintf("%d", refund),
		)
	}
}