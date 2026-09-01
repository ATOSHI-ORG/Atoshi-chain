package types

const (
	ModuleName = "bridgeadapter"
	StoreKey   = ModuleName
	RouterKey  = ModuleName

	// AppModuleID is this app's slot in the Hyperlane core app router. The
	// router keys on the recipient address's type field, so every app needs a
	// distinct id: x/warp already claims 1 (collateral) and 2 (synthetic), and
	// 10 leaves room for token types it may add.
	AppModuleID uint8 = 10

	// TierMessagePayloadLen is the fixed size of both tier-release messages.
	//
	// The wire format is abi.encode(uint8 msgType, uint256, uint256) -- three
	// 32-byte big-endian words, 96 bytes, identical in both directions. The
	// design doc (§3.4) fixes the fields but not the byte layout; abi.encode was
	// chosen over a packed 65-byte body so the Solidity side can use abi.decode
	// natively instead of hand-rolled assembly offsets, which is precisely where
	// the StakingCaller bech32-length bug came from.
	TierMessagePayloadLen = 96

	// Message types, per the design doc §3.4. The type tag is carried in the
	// first word of every tier message so a body delivered to the wrong channel
	// is rejected instead of being silently misread as amounts.
	MsgTypeTierRelease    uint8 = 1 // Atoshi -> Ethereum: cumulative release targets, ATOS units
	MsgTypeReleaseReceipt uint8 = 2 // Ethereum -> Atoshi: cumulative amounts released, ERC20 units

	// HexAddressLen is the width of a Hyperlane address.
	HexAddressLen = 32

	// BpsDenominator is the basis-point scale.
	BpsDenominator = 10000

	// AssetPayloadLen is the fixed size of an asset-transfer body, matching
	// Hyperlane's own warp layout so third-party tooling can read it.
	AssetPayloadLen = 64
)

const (
	prefixParams = iota + 1
	prefixReceiptState
	prefixRateLimit
	prefixAddressUsage
)

var (
	KeyParams       = []byte{prefixParams}
	KeyReceiptState = []byte{prefixReceiptState}
	KeyRateLimit    = []byte{prefixRateLimit}

	KeyPrefixAddressUsage = []byte{prefixAddressUsage}
)

// AddressUsageKey indexes one address's daily outbound usage.
func AddressUsageKey(addr string) []byte {
	out := make([]byte, 0, len(KeyPrefixAddressUsage)+len(addr))
	out = append(out, KeyPrefixAddressUsage...)
	out = append(out, addr...)
	return out
}
