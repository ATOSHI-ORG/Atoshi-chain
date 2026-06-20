package ante

import (
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/atoshi-chain/atoshi/v20/x/energy/keeper"
)

// EnergyRefundDecorator runs as a PostHandler. It returns the
// difference between the energy reserved up-front by
// EnergyDeductDecorator and the gas the tx actually consumed.
//
// We only refund TxEnergy. Refunding DeployEnergy is intentionally
// skipped — over-reserving on a deploy tx is rare (deploy gas is
// usually estimated tightly) and trying to attribute the refund
// between the deploy bucket and the tx bucket adds complexity
// without proportional value.
//
// The PostHandler runs only in DeliverTx; CheckTx pre-charges and
// does not refund — but CheckTx state is rolled back, so this is
// invisible to the user.
type EnergyRefundDecorator struct {
	energyKeeper keeper.Keeper
}

func NewEnergyRefundDecorator(ek keeper.Keeper) EnergyRefundDecorator {
	return EnergyRefundDecorator{energyKeeper: ek}
}

func (d EnergyRefundDecorator) PostHandle(
	ctx sdk.Context, tx sdk.Tx, simulate bool, success bool, next sdk.PostHandler,
) (sdk.Context, error) {
	// Audit Issue-1 (round2): Reaching PostHandler at all means runMsgs
	// did NOT error — the cosmos-sdk BaseApp only invokes the
	// post-handler on the success branch. So regardless of which early
	// return we take below (no reservation stashed, fully-subsidized,
	// gas_used >= gas_limit, etc.), the AnteHandler's pending-
	// reservation marker MUST be deleted here. If we leave the marker,
	// the EndBlocker sweep will incorrectly treat this successful tx
	// as a failed one and refund the full reservation — silently
	// gifting the user back the energy they actually consumed.
	//
	// `defer` covers every return path; the deletion is committed iff
	// PostHandle's state changes commit, which itself only happens on
	// successful tx finalization. Marker write was committed by ante
	// (so it survives if a later decorator errors out), but in that
	// case the EndBlocker treatment is still correct.
	defer func() {
		if txHash := txHashFromCtx(ctx); len(txHash) > 0 {
			d.energyKeeper.DeletePendingReservation(ctx, txHash)
		}
	}()

	// Even on tx failure we still refund — the energy was reserved
	// optimistically; failed txs that did less work shouldn't penalize
	// the user.
	reservedV := ctx.Value(CtxKeyEnergyReserved)
	signerV := ctx.Value(CtxKeyEnergySigner)
	if reservedV == nil || signerV == nil {
		return next(ctx, tx, simulate, success)
	}
	reserved, ok := reservedV.(keeper.ConsumeResult)
	if !ok || reserved.Free {
		return next(ctx, tx, simulate, success)
	}
	signer, ok := signerV.(sdk.AccAddress)
	if !ok {
		return next(ctx, tx, simulate, success)
	}

	gasUsed := ctx.GasMeter().GasConsumed()
	feeTx, ok := tx.(sdk.FeeTx)
	if !ok {
		return next(ctx, tx, simulate, success)
	}
	gasLimit := feeTx.GetGas()
	if gasUsed >= gasLimit {
		// no over-reservation
		return next(ctx, tx, simulate, success)
	}

	// Energy actually used == reserved.EnergyDeducted + reserved.DeployEnergyUsed
	// minus the proportion of unused gas. We only refund the TxEnergy
	// share for simplicity (see comment above).
	totalEnergyReserved := reserved.EnergyDeducted // TxEnergy only
	if totalEnergyReserved == 0 {
		return next(ctx, tx, simulate, success)
	}
	unusedGas := gasLimit - gasUsed
	refund := minU64(totalEnergyReserved, unusedGas)
	if refund == 0 {
		return next(ctx, tx, simulate, success)
	}
	// Audit Question 1: pass the ConsumeResult so RefundEnergy can apply
	// LIFO order (refill delegated-in pool first, then own bucket).
	d.energyKeeper.RefundEnergy(ctx, signer, refund, reserved)
	return next(ctx, tx, simulate, success)
}

func minU64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}
