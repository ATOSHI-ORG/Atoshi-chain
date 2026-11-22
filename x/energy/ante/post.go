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
// The PostHandler runs only in DeliverTx, and only for txs whose msgs
// succeeded; CheckTx pre-charges and does not refund — but CheckTx state
// is rolled back, so this is invisible to the user. Failed txs are not
// refunded anywhere (see EnergyDeductDecorator).
type EnergyRefundDecorator struct {
	energyKeeper keeper.Keeper
}

func NewEnergyRefundDecorator(ek keeper.Keeper) EnergyRefundDecorator {
	return EnergyRefundDecorator{energyKeeper: ek}
}

func (d EnergyRefundDecorator) PostHandle(
	ctx sdk.Context, tx sdk.Tx, simulate bool, success bool, next sdk.PostHandler,
) (sdk.Context, error) {
	// The PostHandler only runs when runMsgs succeeded (BaseApp skips it
	// on failure), so this is the success-path refund of the energy
	// reserved beyond what the tx actually used. A failed tx never gets
	// here and keeps its whole reservation spent -- energy is a fee
	// substitute and a failed tx pays its fee on the full gas limit.
	//
	// The refund goes to CtxKeyEnergySigner, which the ante decorator sets
	// to the fee payer: the only account energy is ever drawn from.
	//
	// The !success guard is defensive: today's BaseApp never calls a
	// PostHandler with success=false, but if one ever did, a failed tx
	// must not get energy back here either.
	if !success {
		return next(ctx, tx, simulate, success)
	}
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

	totalEnergyReserved := reserved.EnergyDeducted // TxEnergy only
	if totalEnergyReserved == 0 {
		return next(ctx, tx, simulate, success)
	}

	// Round-3 hotfix: previously this computed
	//   refund = min(totalEnergyReserved, gasLimit - gasUsed)
	// which silently refunded the FULL reserved energy whenever
	// `gasLimit - gasUsed >= totalEnergyReserved` — i.e. as long as
	// the tx had any room between actual gas use and its declared
	// limit (the common case for a typical 300k MsgSend that uses
	// ~232k), the entire energy deduction was rolled back. A
	// delegated_in recipient could therefore replay the same
	// energy-subsidized transfer indefinitely against a single 30k
	// grant; the ATOS shortfall fee was charged each time (so the
	// chain didn't bleed value), but the energy account never moved
	// and Bound.Used stayed at zero.
	//
	// Correct semantics: Consume() drains the energy bucket FIRST
	// (gasUsed counts down through reserved energy before it spills
	// over into ATOS-paid gas). So the energy that actually got
	// burned is min(totalEnergyReserved, gasUsed); the remainder is
	// what's eligible to be refunded.
	//
	// Examples (totalEnergyReserved = 30_000, gasLimit = 300_000):
	//   gasUsed = 231_837 → energy 30_000 fully consumed, refund = 0
	//   gasUsed =  20_000 → energy 10_000 unused,         refund = 10_000
	//   gasUsed =       0 → tx didn't run any logic,      refund = 30_000
	var energyUsed uint64
	if gasUsed >= totalEnergyReserved {
		energyUsed = totalEnergyReserved
	} else {
		energyUsed = gasUsed
	}
	refund := totalEnergyReserved - energyUsed
	if refund == 0 {
		return next(ctx, tx, simulate, success)
	}
	// Audit Question 1: pass the ConsumeResult so RefundEnergy can apply
	// LIFO order (refill delegated-in pool first, then own bucket).
	d.energyKeeper.RefundEnergy(ctx, signer, refund, reserved)
	return next(ctx, tx, simulate, success)
}
