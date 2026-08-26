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

// ----- Receipt payload (pure; the wire format Ethereum writes) -----

// ParseReceipt decodes a tier-release receipt body.
//
// Layout mirrors Hyperlane's own warp payload — two big-endian 32-byte words —
// so third-party relayers, explorers and Hyperlane's tooling can read our
// messages without special-casing them:
//
//	[ 0:32] cumulative ERC20 released into the bridge vault
//	[32:64] cumulative ERC20 sent to the project cold wallet
//
// Both are CUMULATIVE totals rather than deltas. That is what removes the need
// for nonces or a dedup table: a repeat is a no-op, a gap is repaired by the
// next message, and a stale message reports less than what is already applied
// and gets rejected.
func ParseReceipt(payload []byte) (toBridge, toProject math.Int, err error) {
	if len(payload) != ReceiptPayloadLen {
		return math.Int{}, math.Int{}, fmt.Errorf(
			"%w: expected %d bytes, got %d", ErrInvalidPayload, ReceiptPayloadLen, len(payload))
	}

	toBridge = math.NewIntFromBigInt(new(big.Int).SetBytes(payload[0:32]))
	toProject = math.NewIntFromBigInt(new(big.Int).SetBytes(payload[32:64]))
	return toBridge, toProject, nil
}

// BuildReceipt encodes a receipt body. Used by tests and by tooling that
// simulates the Ethereum side; the chain itself only ever decodes.
func BuildReceipt(toBridge, toProject math.Int) ([]byte, error) {
	if toBridge.IsNil() || toBridge.IsNegative() || toProject.IsNil() || toProject.IsNegative() {
		return nil, fmt.Errorf("%w: amounts must be non-negative", ErrInvalidPayload)
	}
	b := toBridge.BigInt().Bytes()
	p := toProject.BigInt().Bytes()
	if len(b) > 32 || len(p) > 32 {
		return nil, fmt.Errorf("%w: amount exceeds 32 bytes", ErrInvalidPayload)
	}

	out := make([]byte, ReceiptPayloadLen)
	copy(out[32-len(b):32], b)
	copy(out[64-len(p):64], p)
	return out, nil
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
