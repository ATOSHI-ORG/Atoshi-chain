package types

import (
	"fmt"
	"math/big"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// SecondsPerDay is the rate-limit window.
const SecondsPerDay int64 = 86_400

// DayOf returns the UTC day number a block time falls in. Counters are keyed by
// it and reset lazily, so a stored day that is not today reads as zero and no
// EndBlocker sweep is needed to roll them over.
func DayOf(blockTimeUnix int64) int64 {
	if blockTimeUnix < 0 {
		return 0
	}
	return blockTimeUnix / SecondsPerDay
}

// Limits is the resolved set of caps for one block, given the params and the
// migration pool's current balance.
type Limits struct {
	// Global is the day's total outbound allowance.
	Global math.Int
	// LargeBudget is the portion large transfers may use: Global minus the
	// small-transfer reserve.
	LargeBudget math.Int
	// PerAddress is one address's share of Global.
	PerAddress math.Int
	// SmallThreshold is the amount at or below which an OUTBOUND transfer
	// counts as small, for the small-transfer reserve.
	SmallThreshold math.Int
	// MinTransfer is the floor on a single outbound transfer.
	MinTransfer math.Int
	// MaxTransferOut is the ceiling on a single outbound transfer. Zero means
	// no ceiling.
	MaxTransferOut math.Int
	// MaxTransferIn is the ceiling on a single inbound transfer. Zero means no
	// ceiling.
	MaxTransferIn math.Int
	// InboundPerAddress is one recipient's share of Inbound. Zero means no
	// per-recipient limit.
	InboundPerAddress math.Int
	// InboundSmallThreshold is the amount at or below which an INBOUND
	// transfer counts as small during crisis mode. Separate from
	// SmallThreshold because inbound amounts run an order of magnitude larger.
	InboundSmallThreshold math.Int
	// CrisisMode is true when the pool has fallen below the crisis floor, in
	// which case only small INBOUND transfers are accepted.
	//
	// It gates inbound, not outbound, because inbound is the direction that
	// drains the pool: a bridge-in pays ATOS out of it, a bridge-out locks ATOS
	// into it. Throttling outbound here -- which is what this used to do --
	// blocked the one direction that refills the pool while leaving the
	// draining direction wide open, and stranded holders trying to exit at
	// exactly the moment they most wanted to.
	CrisisMode bool
	// Inbound is the day's ceiling on ATOS paid out to bridge-ins. Resolved the
	// same way as Global -- the smaller of the fixed parameter and a fraction of
	// the pool -- so it tightens on its own as the pool drains.
	Inbound math.Int
	// Disabled mirrors Params.RateLimitsDisabled. True makes every check above a
	// no-op while leaving the resolved figures intact, so a query can still show
	// what the limits WOULD be and turning them back on needs no retuning.
	//
	// Negative here for the same reason the param is negative: the zero value
	// has to be the enforcing one. A Limits built as a struct literal -- which
	// every test does -- would otherwise silently opt out of every check it
	// meant to exercise, and pass.
	Disabled bool
}

// ResolveLimits computes the effective caps.
//
// The global cap is the SMALLER of the fixed parameter and a fraction of the
// pool, which is what makes the limit self-tightening: as the pool drains the
// allowance shrinks with it, instead of holding a stale figure until someone
// files a proposal.
func ResolveLimits(p Params, poolBalance, poolTotal math.Int) Limits {
	// A configured share never rounds down to zero.
	//
	// Several checks read a resolved zero as "not configured, no limit", while
	// a zero Global means "fully throttled". So when a positive bps multiplies
	// out to less than one unit -- base is under 10000 wei, or the pool is
	// nearly empty -- truncation would flip that limit from binding to absent
	// without anyone changing it. One unit is as close to "allow nothing" as
	// this can get while staying on the enforcing side of that distinction.
	bps := func(v math.Int, b uint32) math.Int {
		if v.IsNil() || !v.IsPositive() || b == 0 {
			return math.ZeroInt()
		}
		out := v.MulRaw(int64(b)).QuoRaw(BpsDenominator)
		if !out.IsPositive() {
			return math.OneInt()
		}
		return out
	}

	global := p.GlobalDailyCap
	if global.IsNil() || global.IsNegative() {
		global = math.ZeroInt()
	}
	if fromPool := bps(poolBalance, p.GlobalDailyCapBpsOfPool); p.GlobalDailyCapBpsOfPool > 0 {
		if global.IsZero() || fromPool.LT(global) {
			global = fromPool
		}
	}

	reserved := bps(global, p.SmallQuotaBps)
	largeBudget := global.Sub(reserved)
	if largeBudget.IsNegative() {
		largeBudget = math.ZeroInt()
	}

	crisis := false
	if p.CrisisPoolBps > 0 && !poolTotal.IsNil() && poolTotal.IsPositive() {
		crisis = poolBalance.LT(bps(poolTotal, p.CrisisPoolBps))
	}

	minTransfer := p.MinTransferOut
	if minTransfer.IsNil() || minTransfer.IsNegative() {
		minTransfer = math.ZeroInt()
	}
	smallThreshold := p.SmallTransferThreshold
	if smallThreshold.IsNil() || smallThreshold.IsNegative() {
		smallThreshold = math.ZeroInt()
	}

	inbound := p.InboundDailyCap
	if inbound.IsNil() || inbound.IsNegative() {
		inbound = math.ZeroInt()
	}
	if fromPool := bps(poolBalance, p.InboundDailyCapBpsOfPool); p.InboundDailyCapBpsOfPool > 0 {
		if inbound.IsZero() || fromPool.LT(inbound) {
			inbound = fromPool
		}
	}

	nonNegative := func(v math.Int) math.Int {
		if v.IsNil() || v.IsNegative() {
			return math.ZeroInt()
		}
		return v
	}

	return Limits{
		Disabled:              p.RateLimitsDisabled,
		Inbound:               inbound,
		Global:                global,
		LargeBudget:           largeBudget,
		PerAddress:            bps(global, p.PerAddressDailyBps),
		SmallThreshold:        smallThreshold,
		MinTransfer:           minTransfer,
		MaxTransferOut:        nonNegative(p.MaxTransferOut),
		MaxTransferIn:         nonNegative(p.MaxTransferIn),
		InboundPerAddress:     bps(inbound, p.InboundPerAddressDailyBps),
		InboundSmallThreshold: nonNegative(p.InboundSmallThreshold),
		CrisisMode:            crisis,
	}
}

// set reports whether a limit field is configured.
//
// math.Int's zero value wraps a nil big.Int and IsPositive panics on it, while
// a Limits assembled as a struct literal -- which every test does, and which
// nothing stops production code from doing -- leaves unmentioned fields exactly
// that way. Checking IsNil first means an omitted field reads as "no limit"
// rather than taking down the block that happened to touch it.
func set(v math.Int) bool { return !v.IsNil() && v.IsPositive() }

// IsSmall reports whether an amount counts as a small OUTBOUND transfer.
func (l Limits) IsSmall(amount math.Int) bool {
	return !set(l.SmallThreshold) || amount.LTE(l.SmallThreshold)
}

// IsSmallInbound reports whether an amount counts as small on the inbound side
// during crisis mode.
func (l Limits) IsSmallInbound(amount math.Int) bool {
	return !set(l.InboundSmallThreshold) || amount.LTE(l.InboundSmallThreshold)
}

// CheckOutbound applies all five layers to one proposed transfer.
//
// Ordering is deliberate: the cheapest and most specific rejections come first,
// so an error message names the actual binding constraint rather than whichever
// limit happened to be checked first.
//
// The layers, and what each one is for:
//
//  1. Per-transfer floor — dust costs a cross-chain message like anything else.
//  2. Per-transfer ceiling — no single exit drains the vault in one step.
//  3. Global daily cap — bounds a single day's total drain.
//  4. Large-transfer budget — the global cap minus the small reserve. This is
//     what keeps a few whales from consuming the day's allowance in the first
//     minutes and locking ordinary holders out exactly when they most want out.
//  5. Per-address daily cap — one actor cannot take the whole day alone.
//
// Crisis mode is NOT here any more; it gates inbound. See Limits.CrisisMode.
func CheckOutbound(
	l Limits,
	amount math.Int,
	globalUsed, globalUsedLarge, addressUsed math.Int,
) error {
	if amount.IsNil() || !amount.IsPositive() {
		return ErrInvalidAmount
	}

	// The amount check above still applies: a non-positive transfer is invalid
	// input, not a throttled one. Everything below is a throttle.
	if l.Disabled {
		return nil
	}

	if set(l.MinTransfer) && amount.LT(l.MinTransfer) {
		return fmt.Errorf("%w: %s is below the %s minimum", ErrBelowMinimum, amount, l.MinTransfer)
	}

	if set(l.MaxTransferOut) && amount.GT(l.MaxTransferOut) {
		return fmt.Errorf("%w: %s exceeds the %s per-transfer ceiling",
			ErrAboveMaximum, amount, l.MaxTransferOut)
	}

	small := l.IsSmall(amount)

	if !set(l.Global) {
		return fmt.Errorf("%w: outbound bridging is fully throttled", ErrDailyCapReached)
	}
	if globalUsed.Add(amount).GT(l.Global) {
		return fmt.Errorf("%w: %s used of %s today", ErrDailyCapReached, globalUsed, l.Global)
	}

	if !small && globalUsedLarge.Add(amount).GT(l.LargeBudget) {
		return fmt.Errorf(
			"%w: large transfers have used %s of %s today; the rest of the daily cap is reserved for transfers up to %s",
			ErrLargeQuotaReached, globalUsedLarge, l.LargeBudget, l.SmallThreshold)
	}

	if set(l.PerAddress) && addressUsed.Add(amount).GT(l.PerAddress) {
		return fmt.Errorf("%w: this address has used %s of its %s daily allowance",
			ErrAddressCapReached, addressUsed, l.PerAddress)
	}

	return nil
}

// CheckInbound applies the inbound layers to a proposed bridge-in payout.
//
// The layers, in order:
//
//  1. Per-transfer ceiling — one oversized message cannot drain the pool in a
//     single step.
//  2. Crisis mode — below the pool floor, only small bridge-ins are paid, so
//     the remaining liquidity serves many holders rather than one.
//  3. Per-recipient daily cap — bounds what any one address can receive.
//  4. Global daily cap — bounds the day's total payout.
//
// Layer 3 was argued against here for a long time, on the grounds that
// addresses are free so a per-recipient cap stops nobody who is attacking.
// 2026-10-26 refuted that. A replay cannot change the recipient: the recipient
// is inside the signed message, so changing it changes the message id and
// invalidates the signature. The attacker therefore had to funnel
// 152,603,225.60 ATOS through a single address, and of the limits available
// this is the only one that would have bitten.
//
// A zero or unset cap means that layer is off, not that inbound is blocked.
// Outbound treats a zero Global as "fully throttled" because a chain with no
// outbound cap configured should not be paying out; inbound defaults the other
// way because the migration pool balance is already a hard ceiling, and
// failing closed would strand every bridge-in on a chain that had simply not
// set the parameter.
func CheckInbound(l Limits, amount, usedInbound, recipientUsed math.Int) error {
	if amount.IsNil() || !amount.IsPositive() {
		return ErrInvalidAmount
	}
	if l.Disabled {
		return nil
	}
	if usedInbound.IsNil() {
		usedInbound = math.ZeroInt()
	}
	if recipientUsed.IsNil() {
		recipientUsed = math.ZeroInt()
	}

	if set(l.MaxTransferIn) && amount.GT(l.MaxTransferIn) {
		return fmt.Errorf("%w: %s exceeds the %s per-transfer ceiling",
			ErrAboveMaximum, amount, l.MaxTransferIn)
	}

	// Crisis mode gates this direction, not outbound: inbound is what drains
	// the pool, outbound is what refills it.
	if l.CrisisMode && !l.IsSmallInbound(amount) {
		return fmt.Errorf(
			"%w: migration pool is below the crisis floor, only inbound transfers up to %s are accepted",
			ErrCrisisMode, l.InboundSmallThreshold)
	}

	if set(l.InboundPerAddress) && recipientUsed.Add(amount).GT(l.InboundPerAddress) {
		return fmt.Errorf("%w: this recipient has received %s of its %s daily allowance",
			ErrAddressCapReached, recipientUsed, l.InboundPerAddress)
	}

	if set(l.Inbound) && usedInbound.Add(amount).GT(l.Inbound) {
		return fmt.Errorf("%w: %s of %s used today, %s more requested",
			ErrInboundCapReached, usedInbound, l.Inbound, amount)
	}
	return nil
}

// ----- peg conversion -----

// AtosToErc20 converts an outbound ATOS amount to ERC20 units, and reports
// whether the conversion is exact.
//
// A remainder must be refused rather than truncated. Truncating would lock the
// full ATOS on this side while asking Ethereum for less, quietly confiscating
// the difference — and there is no reason for a caller to hit it, since the
// wallet can round to the peg before submitting.
func AtosToErc20(atos math.Int, atosPerErc20 uint64) (math.Int, error) {
	if atos.IsNil() || !atos.IsPositive() {
		return math.ZeroInt(), ErrInvalidAmount
	}
	if atosPerErc20 == 0 {
		return math.ZeroInt(), fmt.Errorf("atos_per_erc20 must be positive")
	}
	den := math.NewIntFromUint64(atosPerErc20)
	if !atos.Mod(den).IsZero() {
		return math.ZeroInt(), fmt.Errorf(
			"%w: %s is not a multiple of the %d:1 peg", ErrIndivisibleAmount, atos, atosPerErc20)
	}
	return atos.Quo(den), nil
}

// ----- asset transfer payload -----

// BuildAssetPayload encodes an outbound transfer body: recipient then amount,
// two big-endian 32-byte words. Same layout as Hyperlane's own warp payload, so
// third-party relayers and explorers read it without special-casing.
func BuildAssetPayload(recipient []byte, erc20Amount math.Int) ([]byte, error) {
	if len(recipient) != HexAddressLen {
		return nil, fmt.Errorf("%w: recipient must be %d bytes", ErrInvalidPayload, HexAddressLen)
	}
	if erc20Amount.IsNil() || !erc20Amount.IsPositive() {
		return nil, ErrInvalidAmount
	}
	amt := erc20Amount.BigInt().Bytes()
	if len(amt) > 32 {
		return nil, fmt.Errorf("%w: amount exceeds 32 bytes", ErrInvalidPayload)
	}

	out := make([]byte, AssetPayloadLen)
	copy(out[0:32], recipient)
	copy(out[64-len(amt):64], amt)
	return out, nil
}

// ParseAssetPayload decodes an inbound transfer body.
func ParseAssetPayload(payload []byte) ([]byte, math.Int, error) {
	if len(payload) != AssetPayloadLen {
		return nil, math.Int{}, fmt.Errorf(
			"%w: expected %d bytes, got %d", ErrInvalidPayload, AssetPayloadLen, len(payload))
	}
	recipient := make([]byte, 32)
	copy(recipient, payload[0:32])
	amount := math.NewIntFromBigInt(new(big.Int).SetBytes(payload[32:64]))
	return recipient, amount, nil
}

// CosmosAddressFromHyperlane extracts a Cosmos account from a 32-byte Hyperlane
// address.
//
// The convention is a 20-byte account left-padded with zeros. Requiring those 12
// leading zeros matters: a payload carrying a 32-byte address for some other
// chain's format would otherwise be silently truncated into a valid-looking
// Cosmos account, and the ATOS would be released to whatever that happened to
// be — unrecoverably.
func CosmosAddressFromHyperlane(addr []byte) (sdk.AccAddress, error) {
	if len(addr) != HexAddressLen {
		return nil, fmt.Errorf("%w: address must be %d bytes", ErrInvalidPayload, HexAddressLen)
	}
	for _, b := range addr[0:12] {
		if b != 0 {
			return nil, fmt.Errorf(
				"%w: recipient is not a zero-padded 20-byte account", ErrInvalidPayload)
		}
	}
	// AccAddress.Empty() only checks the length, and 20 zero bytes has length 20 —
	// so the zero address would pass and the ATOS would land somewhere nobody can
	// spend from. Check the bytes.
	allZero := true
	for _, b := range addr[12:32] {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return nil, fmt.Errorf("%w: recipient is the zero address", ErrInvalidPayload)
	}
	return sdk.AccAddress(addr[12:32]), nil
}
