package types

import (
	"bytes"
	"fmt"
	"math/big"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// DefaultAtosPerErc20 is the peg: 100 ATOS to one ERC20 ATOS, which is what
// makes 10 trillion ATOS correspond to 100 billion ERC20.
const DefaultAtosPerErc20 = 100

// DefaultEthereumDomain is Ethereum mainnet's Hyperlane domain, by convention
// its chain id.
const DefaultEthereumDomain = 1

// ----- Tier-release messages (pure; the wire format shared with Ethereum) -----
//
// Tier release is a four-step round trip (design doc §3.4), and both legs carry
// the same 96-byte body: abi.encode(uint8 msgType, uint256, uint256).
//
//	[ 0:32] msgType, right-aligned as abi.encode pads a uint8
//	[32:64] first cumulative amount
//	[64:96] second cumulative amount
//
//	msgType 1  TIER_RELEASE     Atoshi -> Ethereum
//	           [32:64] cumulativeMinerRelease    ATOS units
//	           [64:96] cumulativeProjectRelease  ATOS units
//	msgType 2  RELEASE_RECEIPT  Ethereum -> Atoshi
//	           [32:64] totalReleasedToBridge     ERC20 units
//	           [64:96] totalReleasedToProject    ERC20 units
//
// Every amount is a CUMULATIVE total, never a delta. That is what removes the
// need for nonces or a dedup table: a repeat is a no-op, a gap is repaired by
// the next message, and a stale message reports less than what is already
// applied and gets rejected. The doc argues this explicitly -- with deltas, 29
// lost messages mean 29 releases lost forever, and one duplicate means one
// double release.
//
// The msgType tag is checked on decode so a body delivered to the wrong channel
// is rejected rather than silently misread as amounts.

// parseTierMessage decodes either leg, verifying length and message type.
func parseTierMessage(payload []byte, wantType uint8) (first, second math.Int, err error) {
	if len(payload) != TierMessagePayloadLen {
		return math.Int{}, math.Int{}, fmt.Errorf(
			"%w: expected %d bytes, got %d", ErrInvalidPayload, TierMessagePayloadLen, len(payload))
	}

	// abi.encode pads a uint8 into a full word, so every byte but the last must
	// be zero. Checking the whole word rather than just payload[31] stops a body
	// whose leading bytes carry data from being accepted as a valid type tag.
	for i := 0; i < 31; i++ {
		if payload[i] != 0 {
			return math.Int{}, math.Int{}, fmt.Errorf(
				"%w: message type word is not a padded uint8", ErrInvalidPayload)
		}
	}
	if payload[31] != wantType {
		return math.Int{}, math.Int{}, fmt.Errorf(
			"%w: expected message type %d, got %d", ErrInvalidPayload, wantType, payload[31])
	}

	first = math.NewIntFromBigInt(new(big.Int).SetBytes(payload[32:64]))
	second = math.NewIntFromBigInt(new(big.Int).SetBytes(payload[64:96]))
	return first, second, nil
}

// buildTierMessage encodes either leg.
func buildTierMessage(msgType uint8, first, second math.Int) ([]byte, error) {
	if first.IsNil() || first.IsNegative() || second.IsNil() || second.IsNegative() {
		return nil, fmt.Errorf("%w: amounts must be non-negative", ErrInvalidPayload)
	}
	a := first.BigInt().Bytes()
	b := second.BigInt().Bytes()
	if len(a) > 32 || len(b) > 32 {
		return nil, fmt.Errorf("%w: amount exceeds 32 bytes", ErrInvalidPayload)
	}

	out := make([]byte, TierMessagePayloadLen)
	out[31] = msgType
	copy(out[64-len(a):64], a)
	copy(out[96-len(b):96], b)
	return out, nil
}

// ParseReceipt decodes the reverse leg: what Ethereum actually released, in
// ERC20 units.
func ParseReceipt(payload []byte) (toBridge, toProject math.Int, err error) {
	return parseTierMessage(payload, MsgTypeReleaseReceipt)
}

// BuildReceipt encodes the reverse leg. Used by tests and by tooling that
// simulates the Ethereum side; in production the chain only decodes this leg.
func BuildReceipt(toBridge, toProject math.Int) ([]byte, error) {
	return buildTierMessage(MsgTypeReleaseReceipt, toBridge, toProject)
}

// BuildTierRelease encodes the forward leg: the cumulative amounts Atoshi has
// authorized for release, in ATOS units. Ethereum divides by the peg to get its
// own ERC20 targets.
func BuildTierRelease(cumulativeMiner, cumulativeProject math.Int) ([]byte, error) {
	return buildTierMessage(MsgTypeTierRelease, cumulativeMiner, cumulativeProject)
}

// ParseTierRelease decodes the forward leg. The chain only ever builds this
// message; the decoder exists so tests can assert the exact bytes Ethereum will
// receive, and so tooling can inspect a dispatched body.
func ParseTierRelease(payload []byte) (cumulativeMiner, cumulativeProject math.Int, err error) {
	return parseTierMessage(payload, MsgTypeTierRelease)
}

// Erc20ToAtos converts an ERC20 amount to ATOS at the configured peg. Both are
// 18-decimal, so this is a plain multiply.
func Erc20ToAtos(erc20 math.Int, atosPerErc20 uint64) math.Int {
	if erc20.IsNil() || !erc20.IsPositive() || atosPerErc20 == 0 {
		return math.ZeroInt()
	}
	return erc20.MulRaw(int64(atosPerErc20))
}

// ----- Params -----

func DefaultParams() Params {
	return Params{
		// Off at genesis. The vault address is not known until the Ethereum
		// contract is deployed, and an adapter enabled with a zero vault address
		// would accept a receipt from anyone able to send from domain 1.
		Enabled:          false,
		EthereumDomain:   DefaultEthereumDomain,
		TierReleaseVault: nil,
		AtosPerErc20:     DefaultAtosPerErc20,
		IsmId:            nil,

		// Also off at genesis: the mailbox and the Ethereum vault do not exist yet.
		BridgeEnabled:     false,
		MailboxId:         nil,
		RemoteBridgeVault: nil,

		// 5 billion ATOS a day, or 5% of the migration pool, whichever is smaller.
		// Against a 300-billion pool the percentage binds first, so the cap tracks
		// the pool down as it drains instead of holding a stale figure.
		GlobalDailyCap:          math.NewIntWithDecimal(5, 27),
		GlobalDailyCapBpsOfPool: 500,

		// 2% of the daily cap per address, so no single actor can take the day.
		PerAddressDailyBps: 200,

		// 1,000 ATOS floor: each transfer costs a cross-chain message regardless
		// of size.
		MinTransferOut: math.NewIntWithDecimal(1, 21),

		// 100,000 ATOS or less counts as small, and 20% of the daily cap is
		// reserved for those. Without the reserve a few large exits take the whole
		// allowance in the first minutes of the day.
		SmallTransferThreshold: math.NewIntWithDecimal(1, 23),
		SmallQuotaBps:          2000,

		// Below 10% of the pool, large exits stop entirely so the remaining
		// liquidity serves many small holders.
		CrisisPoolBps: 1000,
	}
}

func (p Params) Validate() error {
	if p.AtosPerErc20 == 0 {
		return fmt.Errorf("atos_per_erc20 must be positive")
	}
	if p.EthereumDomain == 0 {
		return fmt.Errorf("ethereum_domain must be set")
	}
	if len(p.TierReleaseVault) != 0 && len(p.TierReleaseVault) != HexAddressLen {
		return fmt.Errorf("tier_release_vault must be %d bytes, got %d", HexAddressLen, len(p.TierReleaseVault))
	}
	if len(p.IsmId) != 0 && len(p.IsmId) != HexAddressLen {
		return fmt.Errorf("ism_id must be %d bytes, got %d", HexAddressLen, len(p.IsmId))
	}
	// Enabling without a vault address would accept a receipt from any contract
	// on the origin chain, since the sender check would have nothing to compare
	// against.
	if p.Enabled && len(p.TierReleaseVault) != HexAddressLen {
		return fmt.Errorf("tier_release_vault must be set before enabling the adapter")
	}

	for _, c := range []struct {
		name string
		v    []byte
	}{{"mailbox_id", p.MailboxId}, {"remote_bridge_vault", p.RemoteBridgeVault}} {
		if len(c.v) != 0 && len(c.v) != HexAddressLen {
			return fmt.Errorf("%s must be %d bytes, got %d", c.name, HexAddressLen, len(c.v))
		}
	}
	// Same reasoning as the tier vault: with no configured counterparty the
	// inbound sender check has nothing to compare against, and an outbound
	// dispatch has nowhere to go.
	if p.BridgeEnabled &&
		(len(p.MailboxId) != HexAddressLen || len(p.RemoteBridgeVault) != HexAddressLen) {
		return fmt.Errorf("mailbox_id and remote_bridge_vault must be set before enabling the bridge")
	}

	for _, c := range []struct {
		name string
		v    math.Int
	}{
		{"global_daily_cap", p.GlobalDailyCap},
		{"min_transfer_out", p.MinTransferOut},
		{"small_transfer_threshold", p.SmallTransferThreshold},
	} {
		if c.v.IsNil() || c.v.IsNegative() {
			return fmt.Errorf("%s must not be negative", c.name)
		}
	}
	for _, c := range []struct {
		name string
		v    uint32
	}{
		{"global_daily_cap_bps_of_pool", p.GlobalDailyCapBpsOfPool},
		{"per_address_daily_bps", p.PerAddressDailyBps},
		{"small_quota_bps", p.SmallQuotaBps},
		{"crisis_pool_bps", p.CrisisPoolBps},
	} {
		if c.v > BpsDenominator {
			return fmt.Errorf("%s must be <= %d, got %d", c.name, BpsDenominator, c.v)
		}
	}
	// A full small-transfer reserve would leave large transfers no budget at all,
	// which is a silent ban rather than a limit.
	if p.SmallQuotaBps == BpsDenominator {
		return fmt.Errorf("small_quota_bps must be below %d, or large transfers are banned outright", BpsDenominator)
	}
	return nil
}

// VaultMatches reports whether a message sender is the configured vault.
func (p Params) VaultMatches(sender []byte) bool {
	if len(p.TierReleaseVault) != HexAddressLen {
		return false
	}
	return bytes.Equal(p.TierReleaseVault, sender)
}

// ----- State -----

func DefaultReceiptState() ReceiptState {
	return ReceiptState{
		AppliedToBridge:  math.ZeroInt(),
		AppliedToProject: math.ZeroInt(),
		AppId:            nil,
		LastMessageId:    nil,
		AssetAppId:       nil,
		TotalBridgedOut:  math.ZeroInt(),
		TotalBridgedIn:   math.ZeroInt(),
	}
}

func (s ReceiptState) Validate() error {
	for _, c := range []struct {
		name string
		v    math.Int
	}{
		{"applied_to_bridge", s.AppliedToBridge},
		{"applied_to_project", s.AppliedToProject},
		{"total_bridged_out", s.TotalBridgedOut},
		{"total_bridged_in", s.TotalBridgedIn},
	} {
		if c.v.IsNil() {
			return fmt.Errorf("%s: nil", c.name)
		}
		if c.v.IsNegative() {
			return fmt.Errorf("%s: must not be negative, got %s", c.name, c.v)
		}
	}
	for _, c := range []struct {
		name string
		v    []byte
	}{{"app_id", s.AppId}, {"asset_app_id", s.AssetAppId}} {
		if len(c.v) != 0 && len(c.v) != HexAddressLen {
			return fmt.Errorf("%s must be %d bytes, got %d", c.name, HexAddressLen, len(c.v))
		}
	}
	return nil
}

// ----- Genesis -----

func DefaultGenesisState() *GenesisState {
	return &GenesisState{
		Params:       DefaultParams(),
		ReceiptState: DefaultReceiptState(),
	}
}

func (gs GenesisState) Validate() error {
	if err := gs.Params.Validate(); err != nil {
		return fmt.Errorf("invalid bridgeadapter params: %w", err)
	}
	if err := gs.ReceiptState.Validate(); err != nil {
		return fmt.Errorf("invalid receipt_state: %w", err)
	}
	return nil
}

// ----- Msg -----

func (msg MsgUpdateParams) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(msg.Authority); err != nil {
		return err
	}
	return msg.Params.Validate()
}
