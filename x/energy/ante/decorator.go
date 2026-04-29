// Package ante provides AnteHandler decorators that hook the energy
// module into the standard tx pipeline. The main entry point is
// EnergyDeductDecorator which is a drop-in replacement for the SDK's
// auth/ante.DeductFeeDecorator: it deducts an account's accrued
// TxEnergy / DeployEnergy first and only charges the remaining gas
// (the "shortfall") in ATOS through the standard fee path.
//
// Scope (v1): Cosmos chain only. The EVM chain's MonoDecorator does
// its own gas accounting and is intentionally NOT modified — adding
// energy support there is a separate, larger workstream.
package ante

import (
	"context"
	"fmt"

	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/cosmos/cosmos-sdk/x/auth/ante"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"github.com/atoshi-chain/atoshi/v20/x/energy/keeper"
)

// FeegrantKeeper matches the SDK x/auth/ante.FeegrantKeeper interface
// exactly so we can be passed the same instance.
type FeegrantKeeper interface {
	UseGrantedFees(ctx context.Context, granter, grantee sdk.AccAddress, fee sdk.Coins, msgs []sdk.Msg) error
}

// AccountKeeper matches what evmtypes.AccountKeeper / SDK expose. We
// only call GetAccount.
type AccountKeeper interface {
	GetAccount(ctx context.Context, addr sdk.AccAddress) sdk.AccountI
}

// BankKeeper is the subset used to actually transfer the shortfall fee.
// Matches evmtypes.BankKeeper signature.
type BankKeeper interface {
	SendCoinsFromAccountToModule(ctx context.Context, senderAddr sdk.AccAddress, recipientModule string, amt sdk.Coins) error
}

// ContextKey is a typed key for ctx.WithValue.
type ContextKey string

const (
	// CtxKeyEnergyReserved stores how much TxEnergy/DeployEnergy was
	// pre-charged against the signer in this tx. The PostHandler reads
	// it to refund the difference between gas_limit and gas_used.
	CtxKeyEnergyReserved ContextKey = "energy.reserved"

	// CtxKeyEnergySigner stores the address whose energy was charged so
	// the PostHandler does not need to re-derive it.
	CtxKeyEnergySigner ContextKey = "energy.signer"
)

// EnergyDeductDecorator is a drop-in replacement for the SDK's
// DeductFeeDecorator. It runs after ConsumeGasForTxSize and consults
// the energy keeper before falling through to fee deduction.
//
// Behavior:
//   - Subsidized msg type: skip both energy and fee deduction
//   - Energy module disabled: behave exactly like stock DeductFee
//   - Otherwise: settle, consume energy up to gas_limit, deduct only
//     the gas not covered by energy as ATOS using the standard path
type EnergyDeductDecorator struct {
	energyKeeper   keeper.Keeper
	accountKeeper  AccountKeeper
	bankKeeper     BankKeeper
	feegrantKeeper FeegrantKeeper
	txFeeChecker   ante.TxFeeChecker
}

func NewEnergyDeductDecorator(
	ek keeper.Keeper,
	ak AccountKeeper,
	bk BankKeeper,
	fk FeegrantKeeper,
	tfc ante.TxFeeChecker,
) EnergyDeductDecorator {
	return EnergyDeductDecorator{
		energyKeeper:   ek,
		accountKeeper:  ak,
		bankKeeper:     bk,
		feegrantKeeper: fk,
		txFeeChecker:   tfc,
	}
}

// AnteHandle is the SDK decorator entry point. It is called once per
// tx during CheckTx, ReCheckTx, DeliverTx and Simulate.
//
// We intentionally avoid mutating the tx's fee structure; instead we
// compute the ATOS shortfall ourselves and SendCoinsFromAccountToModule
// directly. This mirrors what the SDK's NewDeductFeeDecorator does
// internally but lets us stop at the energy-covered portion.
func (d EnergyDeductDecorator) AnteHandle(
	ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler,
) (sdk.Context, error) {
	feeTx, ok := tx.(sdk.FeeTx)
	if !ok {
		return ctx, sdkerrors.ErrTxDecode.Wrap("tx must implement FeeTx for energy decorator")
	}
	if !simulate && ctx.BlockHeight() > 0 && feeTx.GetGas() == 0 {
		return ctx, sdkerrors.ErrInvalidGasLimit.Wrap("tx gas limit must be positive")
	}

	gasLimit := feeTx.GetGas()
	msgs := tx.GetMsgs()
	msgUrls := make([]string, 0, len(msgs))
	for _, m := range msgs {
		msgUrls = append(msgUrls, sdk.MsgTypeURL(m))
	}

	// Resolve the fee payer (handles --fee-granter / x/feegrant).
	feePayer := sdk.AccAddress(feeTx.FeePayer())
	feeGranter := sdk.AccAddress(feeTx.FeeGranter())
	deductFrom := feePayer
	if !feeGranter.Empty() {
		// Honor the grant; allowance check happens below before any
		// state mutation.
		deductFrom = feeGranter
	}
	if deductFrom.Empty() {
		return ctx, sdkerrors.ErrInvalidAddress.Wrap("fee payer / granter address required")
	}

	// Make sure the account exists; required for sequence + fee deduction.
	if d.accountKeeper.GetAccount(ctx, deductFrom) == nil {
		return ctx, sdkerrors.ErrUnknownAddress.Wrapf("fee payer account %s does not exist", deductFrom)
	}

	// Probe energy first.
	isDeploy := isContractDeployMsg(msgs)
	consumed, err := d.energyKeeper.Consume(ctx, deductFrom, gasLimit, isDeploy, msgUrls)
	if err != nil {
		return ctx, err
	}

	// Stash reserved-energy info for the PostHandler refund pass.
	ctx = ctx.WithValue(CtxKeyEnergyReserved, consumed)
	ctx = ctx.WithValue(CtxKeyEnergySigner, deductFrom)

	// Fully subsidized → no fee at all. Set the priority and return.
	if consumed.Free {
		return next(ctx, tx, simulate)
	}

	// Compute the ATOS owed for the gas not covered by energy.
	stdFee := feeTx.GetFee()
	if consumed.ShortfallGas == 0 {
		// All gas covered by energy: zero out the fee for downstream.
		// We still call the txFeeChecker to validate priority.
		_, _, err := d.txFeeChecker(ctx, tx)
		if err != nil {
			return ctx, err
		}
		return next(ctx, tx, simulate)
	}

	// Partial / full ATOS payment: derive the actual amount to charge.
	chargeAtos := computeShortfallFee(d.energyKeeper, ctx, consumed.ShortfallGas, stdFee, gasLimit)
	if chargeAtos.IsZero() {
		return next(ctx, tx, simulate)
	}

	// Use feegrant if applicable.
	if !feeGranter.Empty() && !feeGranter.Equals(feePayer) {
		if d.feegrantKeeper == nil {
			return ctx, sdkerrors.ErrInvalidRequest.Wrap("fee grants are not enabled")
		}
		if err := d.feegrantKeeper.UseGrantedFees(ctx, feeGranter, feePayer, chargeAtos, msgs); err != nil {
			return ctx, err
		}
	}

	// Move the shortfall ATOS from payer to fee_collector.
	if err := d.bankKeeper.SendCoinsFromAccountToModule(
		ctx, deductFrom, authtypes.FeeCollectorName, chargeAtos,
	); err != nil {
		return ctx, sdkerrors.ErrInsufficientFee.Wrapf("shortfall fee transfer: %v", err)
	}

	// Set tx priority based on the gas price the user offered.
	priority := getTxPriority(stdFee, int64(gasLimit))
	ctx = ctx.WithPriority(priority)

	return next(ctx, tx, simulate)
}

// computeShortfallFee returns the ATOS coins that should be charged for
// `shortfallGas` gas units. We use the gas price the user offered in
// the tx fee (pro-rated), bounded by params.InsufficientGasPrice.
//
// The user's offered price = tx_fee_amount / gas_limit. We charge:
//   max(min(offeredPrice, params.gas_price), 0) * shortfallGas
// per coin in the fee, denom by denom. In practice the fee is in
// `aatos` only, so this is straightforward.
func computeShortfallFee(
	k keeper.Keeper, ctx sdk.Context, shortfallGas uint64, fee sdk.Coins, gasLimit uint64,
) sdk.Coins {
	if shortfallGas == 0 || gasLimit == 0 {
		return sdk.NewCoins()
	}
	denom := k.BaseDenom()
	offered := fee.AmountOf(denom)
	if offered.IsZero() {
		// User offered no fee at all → fall back to the param gas price
		// so we still collect SOMETHING (the alternative is letting
		// users dodge the shortfall by setting tx.fee = 0).
		params := k.GetParams(ctx)
		if params.InsufficientGasPrice.IsNil() || !params.InsufficientGasPrice.IsPositive() {
			return sdk.NewCoins()
		}
		amt := params.InsufficientGasPrice.MulInt64(int64(shortfallGas)).Ceil().TruncateInt()
		return sdk.NewCoins(sdk.NewCoin(denom, amt))
	}
	// Pro-rate: charge offered_fee * shortfallGas / gasLimit.
	num := offered.Mul(math.NewIntFromUint64(shortfallGas))
	amt := num.Quo(math.NewIntFromUint64(gasLimit))
	if amt.IsNegative() {
		amt = math.ZeroInt()
	}
	return sdk.NewCoins(sdk.NewCoin(denom, amt))
}

// getTxPriority returns the SDK priority for a tx fee. Mirrors the
// standard auth/ante implementation so behavior is unchanged when
// energy is disabled.
func getTxPriority(fee sdk.Coins, gas int64) int64 {
	var priority int64
	for _, c := range fee {
		p := int64(0)
		gasPrice := c.Amount.QuoRaw(gas)
		if gasPrice.IsInt64() {
			p = gasPrice.Int64()
		}
		if priority == 0 || p < priority {
			priority = p
		}
	}
	return priority
}

// isContractDeployMsg returns true if any msg in the tx is a contract
// deployment. For Cosmos messages this maps to Wasm Instantiate or EVM
// MsgEthereumTx with To == nil; for now we keep the heuristic minimal
// and rely on type-url matching against a known set.
func isContractDeployMsg(msgs []sdk.Msg) bool {
	for _, m := range msgs {
		switch sdk.MsgTypeURL(m) {
		case "/cosmwasm.wasm.v1.MsgInstantiateContract",
			"/cosmwasm.wasm.v1.MsgInstantiateContract2",
			"/cosmwasm.wasm.v1.MsgStoreCode":
			return true
		case "/ethermint.evm.v1.MsgEthereumTx":
			// EVM deployments are recognized by To == nil. The MonoDecorator
			// handles them outside this Cosmos chain, so reaching here
			// means the tx was wrapped as a Cosmos msg — treat as deploy.
			return true
		}
	}
	return false
}

// EnsureGasMeter is a small helper for tests / non-default gas meter
// setups. The decorator itself uses ctx.GasMeter() which is set by
// SetUpContextDecorator earlier in the chain.
func EnsureGasMeter(ctx sdk.Context, gasLimit uint64) sdk.Context {
	if _, ok := ctx.GasMeter().(storetypes.GasMeter); !ok {
		return ctx.WithGasMeter(storetypes.NewGasMeter(gasLimit))
	}
	return ctx
}

// errFmt is a placeholder to keep the file importing fmt cleanly.
var _ = fmt.Sprintf
