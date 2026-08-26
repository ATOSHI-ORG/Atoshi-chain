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
	// SmallThreshold is the amount at or below which a transfer counts as small.
	SmallThreshold math.Int
	// MinTransfer is the floor on a single transfer.
	MinTransfer math.Int
	// CrisisMode is true when the pool has fallen below the crisis floor, in
	// which case only small transfers are allowed at all.
	CrisisMode bool
}

// ResolveLimits computes the effective caps.
//
// The global cap is the SMALLER of the fixed parameter and a fraction of the
// pool, which is what makes the limit self-tightening: as the pool drains the
// allowance shrinks with it, instead of holding a stale figure until someone
// files a proposal.
func ResolveLimits(p Params, poolBalance, poolTotal math.Int) Limits {
	bps := func(v math.Int, b uint32) math.Int {
		if v.IsNil() || !v.IsPositive() || b == 0 {
			return math.ZeroInt()
		}
		return v.MulRaw(int64(b)).QuoRaw(BpsDenominator)
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

	return Limits{
		Global:         global,
		LargeBudget:    largeBudget,
		PerAddress:     bps(global, p.PerAddressDailyBps),
		SmallThreshold: smallThreshold,
		MinTransfer:    minTransfer,
		CrisisMode:     crisis,
	}
}

// IsSmall reports whether an amount counts as a small transfer.
func (l Limits) IsSmall(amount math.Int) bool {
	return !l.SmallThreshold.IsPositive() || amount.LTE(l.SmallThreshold)
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
//  2. Crisis mode — below the pool floor, remaining liquidity serves many small
//     holders rather than one large exit.
//  3. Global daily cap — bounds a single day's total drain.
//  4. Large-transfer budget — the global cap minus the small reserve. This is
//     what keeps a few whales from consuming the day's allowance in the first
//     minutes and locking ordinary holders out exactly when they most want out.
//  5. Per-address daily cap — one actor cannot take the whole day alone.
func CheckOutbound(
	l Limits,
	amount math.Int,
	globalUsed, globalUsedLarge, addressUsed math.Int,
) error {
	if amount.IsNil() || !amount.IsPositive() {
		return ErrInvalidAmount
	}
	if l.MinTransfer.IsPositive() && amount.LT(l.MinTransfer) {
		return fmt.Errorf("%w: %s is below the %s minimum", ErrBelowMinimum, amount, l.MinTransfer)
	}

	small := l.IsSmall(amount)

	if l.CrisisMode && !small {
		return fmt.Errorf(
			"%w: migration pool is below the crisis floor, only transfers up to %s are accepted",
			ErrCrisisMode, l.SmallThreshold)
	}

	if !l.Global.IsPositive() {
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

	if l.PerAddress.IsPositive() && addressUsed.Add(amount).GT(l.PerAddress) {
		return fmt.Errorf("%w: this address has used %s of its %s daily allowance",
			ErrAddressCapReached, addressUsed, l.PerAddress)
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
