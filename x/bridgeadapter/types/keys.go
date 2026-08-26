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

	// ReceiptPayloadLen is the fixed size of a tier-release receipt body.
	ReceiptPayloadLen = 64

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
