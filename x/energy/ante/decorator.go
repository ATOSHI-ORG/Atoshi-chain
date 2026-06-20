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
	"crypto/sha256"
	"fmt"

	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/cosmos/cosmos-sdk/x/auth/ante"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"github.com/atoshi-chain/atoshi/v20/x/energy/keeper"
)

// txHashFromCtx returns the sha256 hash of the raw tx bytes carried in
// the context, matching the canonical Cosmos tx-hash format used by
// CometBFT (tmhash = sha256 truncated to the first 20 bytes — we keep
// the full 32-byte digest here because the KV key only needs
// collision-resistance, not interoperability with CometBFT's lookup).
//
// Returns nil when ctx.TxBytes() is empty (Simulate path, or in tests
// that synthesize a ctx without going through BaseApp); callers MUST
// treat that as "no marker, skip the audit Issue-1 pending-reservation
// write" because two distinct simulated txs could otherwise collide on
// an empty key.
func txHashFromCtx(ctx sdk.Context) []byte {
	bz := ctx.TxBytes()
	if len(bz) == 0 {
		return nil
	}
	sum := sha256.Sum256(bz)
	return sum[:]
}

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

	// Audit Issue-1 (round2): persist a pending-reservation marker. The
	// AnteHandler's writes (including the energy deduction inside
	// Consume()) commit before runMsgs executes. If runMsgs returns an
	// error, the cosmos-sdk BaseApp discards the msg-context state and
	// does NOT invoke the PostHandler — so the refund call scheduled in
	// post.go never runs, and the user's deducted energy is permanently
	// lost. We side-step this by writing a marker here (in ante state,
	// which IS committed) keyed by tx hash; the PostHandler deletes it
	// on success, and EndBlocker refunds any leftover markers as
	// failed-tx compensations. CheckTx and Simulate paths roll back
	// before commit so marker writes there are harmless.
	if !consumed.Free && !simulate {
		if txHash := txHashFromCtx(ctx); len(txHash) > 0 {
			d.energyKeeper.SetPendingReservation(ctx, txHash, deductFrom, gasLimit, consumed)
		}
	}

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

	// Audit Question 1 (round2): priority is computed from chargeAtos
	// (the ATOS actually paid for shortfall gas), NOT from stdFee (the
	// declared fee). Previously a user with a large accrued-energy
	// buffer could declare an arbitrarily large stdFee — paying
	// (almost) nothing in ATOS because energy covered the gas — yet
	// claim a high mempool priority. That decoupled "willingness to
	// pay" from "actually paid", letting energy whales jump the queue
	// without economic stake in the slot.
	//
	// Using chargeAtos ties priority to real ATOS-out-of-pocket. The
	// per-gas denominator is `consumed.ShortfallGas` (the gas the
	// user is actually paying for in ATOS), not `gasLimit`. Subsidized
	// txs with ShortfallGas == 0 already returned early above; here we
	// always have a positive ShortfallGas.
	priority := getTxPriority(chargeAtos, int64(consumed.ShortfallGas), d.energyKeeper.BaseDenom())
	ctx = ctx.WithPriority(priority)

	return next(ctx, tx, simulate)
}

// computeShortfallFee returns the ATOS coins that should be charged for
// `shortfallGas` gas units. We pro-rate the user's offered fee over
// the shortfall portion of gas, with a hard floor at
// params.InsufficientGasPrice.
//
// Audit Question 6 (round2): the prior implementation used integer
// arithmetic without the floor on the non-zero-offered branch. A user
// could submit fee = 1 aatos against gasLimit = 200_000:
//   num = 1 × shortfallGas
//   amt = num / 200_000 = 0   (integer division truncation)
// chargeAtos.IsZero() then short-circuited the ante to next(), letting
// the user pay literally one aatos in fee while consuming the chain's
// gas for an arbitrary tx — a complete shortfall fee evasion.
//
// The zero-offered branch already floored at InsufficientGasPrice, but
// a 1-aatos offer dodged that branch. The fix unifies both paths
// through a single gas-price floor:
//
//   offeredPerGas := offered / gasLimit  (Dec to avoid truncation)
//   if offeredPerGas < InsufficientGasPrice:
//       offeredPerGas = InsufficientGasPrice
//   amt := ceil(offeredPerGas × shortfallGas)
//
// Properties:
//   - A user offering >= InsufficientGasPrice * gasLimit pays the
//     pro-rated portion — same as the old code's intended path.
//   - A user offering < InsufficientGasPrice * gasLimit (including 0,
//     including the 1-aatos evasion shape) pays at least
//     InsufficientGasPrice × shortfallGas — minimum economic cost.
//   - shortfallGas == 0 or gasLimit == 0 still short-circuits to
//     empty coins (no charge — Consume covered all gas).
//   - If InsufficientGasPrice is unset (nil or non-positive) the
//     floor disappears; the chain hasn't configured a minimum, so
//     we charge whatever pro-rate yields (or nothing if also zero).
func computeShortfallFee(
	k keeper.Keeper, ctx sdk.Context, shortfallGas uint64, fee sdk.Coins, gasLimit uint64,
) sdk.Coins {
	if shortfallGas == 0 || gasLimit == 0 {
		return sdk.NewCoins()
	}
	denom := k.BaseDenom()
	offered := fee.AmountOf(denom)
	params := k.GetParams(ctx)

	// Step 1: compute the user's offered per-gas rate. Use LegacyDec
	// so truncation doesn't happen here (integer Quo would lose
	// fractional precision below 1 aatos/gas, and that fraction
	// matters when shortfallGas < gasLimit).
	gasLimitDec := math.LegacyNewDec(int64(gasLimit))
	offeredPerGas := math.LegacyZeroDec()
	if offered.IsPositive() {
		offeredPerGas = math.LegacyNewDecFromInt(offered).Quo(gasLimitDec)
	}

	// Step 2: floor at InsufficientGasPrice. This is the single
	// chokepoint that prevents the 1-aatos evasion the audit flagged.
	if !params.InsufficientGasPrice.IsNil() && params.InsufficientGasPrice.IsPositive() &&
		offeredPerGas.LT(params.InsufficientGasPrice) {
		offeredPerGas = params.InsufficientGasPrice
	}

	// Step 3: if there is no rate at all (offered=0 AND
	// InsufficientGasPrice unset), there is nothing to charge.
	if !offeredPerGas.IsPositive() {
		return sdk.NewCoins()
	}

	// Step 4: chargeAtos = ceil(rate × shortfallGas). Ceil rounds the
	// fractional remainder UP so a sub-unit per-gas rate still
	// collects something rather than silently truncating.
	amt := offeredPerGas.MulInt64(int64(shortfallGas)).Ceil().TruncateInt()
	if !amt.IsPositive() {
		return sdk.NewCoins()
	}
	return sdk.NewCoins(sdk.NewCoin(denom, amt))
}

// getTxPriority returns the SDK priority for a tx fee. Mirrors the
// standard auth/ante implementation so behavior is unchanged when
// energy is disabled.
//
// Audit Issue 12: the prior version initialized `p := int64(0)` before
// the IsInt64 check. When a per-denom gasPrice overflowed int64 the
// fallback was 0 — exactly opposite of the upstream Evmos SDK behavior,
// which defaults to math.MaxInt64 in that case. The discrepancy meant
// txs offering an extremely high fee got demoted to the back of the
// mempool by this decorator, but would have been promoted to the front
// by the standard SDK path. Mainnet operators relying on uniform
// mempool ordering would observe inconsistent prioritization.
//
// Default to MaxInt64 on overflow to match SDK upstream:
// https://github.com/evmos/cosmos-sdk/blob/v0.50.9-evmos/x/auth/ante/validator_tx_fee.go#L54-L68
//
// The literal here is math.MaxInt64 from the stdlib; we use the
// constant directly because the file's `math` import is
// cosmossdk.io/math, not the stdlib package.
const maxInt64Priority = int64(^uint64(0) >> 1)

// Audit Issue-15 (round1-issue8): getTxPriority must only consider the
// chain's base denom (aatos). The previous signature took no denom
// and iterated every coin in the fee bag, treating each as if it
// could move the priority needle. The chain is single-fee-denom
// today, so the bug is latent — but as soon as governance enables
// IBC vouchers, gov-staked alt-coins, or any non-base fee path, an
// attacker could attach a high-amount alt-coin to a low-aatos tx and
// either crowd into the mempool's front (if their alt-coin yielded
// MaxInt64 priority and the min-selection happened to pick it) or
// drag priority down (if the alt-coin yielded a tiny per-gas number
// after QuoRaw — e.g. a 1-unit USDC fee divided by 200k gas = 0).
//
// Fix: filter on baseDenom. Non-base coins in the fee bag are
// inert from a priority standpoint — they don't pay for gas
// consumption (computeShortfallFee also reads only baseDenom), so
// they should not influence the mempool ordering either.
//
// If no base-denom coin is present, priority stays 0 (lowest), which
// matches "no valid fee offered → no preferential ordering".
func getTxPriority(fee sdk.Coins, gas int64, baseDenom string) int64 {
	var priority int64
	for _, c := range fee {
		if c.Denom != baseDenom {
			continue
		}
		p := maxInt64Priority
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
// deployment that should be charged against the DeployEnergy bucket.
//
// Audit Issue-3 (round1-issue1): NO currently-reachable msg type
// triggers DeployEnergy consumption under the present chain
// configuration:
//
//   - The CosmWasm module (wasmd) is not integrated; none of the
//     `/cosmwasm.wasm.v1.MsgInstantiateContract*` or
//     `/cosmwasm.wasm.v1.MsgStoreCode` URLs can appear in a tx that
//     this chain accepts.
//
//   - EVM deployment txs (`/ethermint.evm.v1.MsgEthereumTx`) DO NOT
//     flow through this decorator. The Cosmos ante chain installs
//     RejectMessagesDecorator BEFORE the energy fee step
//     (see app/ante/cosmos.go), which explicitly rejects
//     MsgEthereumTx; the EVM ante chain (MonoDecorator) handles
//     those txs end-to-end and does its own gas accounting. The
//     previous code's MsgEthereumTx branch was therefore unreachable
//     in production AND misleading to reviewers — it implied a code
//     path that does not exist.
//
// We keep the CosmWasm URL cases ready so that when wasmd is added
// in a future release, DeployEnergy starts charging automatically
// with no further wiring. We removed the MsgEthereumTx branch
// because keeping it would re-introduce the same misleading dead
// code the audit flagged; if EVM-side energy accounting becomes a
// requirement it must be done in the EVM ante chain, not here.
//
// In the present configuration this function effectively always
// returns false. That is intentional — there is no DeployEnergy
// consumer right now. Consume() in consume.go handles isDeploy=false
// cleanly (the deploy bucket is skipped and only the TxEnergy /
// delegated-in pools are drawn from).
func isContractDeployMsg(msgs []sdk.Msg) bool {
	for _, m := range msgs {
		switch sdk.MsgTypeURL(m) {
		case "/cosmwasm.wasm.v1.MsgInstantiateContract",
			"/cosmwasm.wasm.v1.MsgInstantiateContract2",
			"/cosmwasm.wasm.v1.MsgStoreCode":
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
