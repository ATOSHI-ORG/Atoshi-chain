package types

const (
	ModuleName = "oracle"
	StoreKey   = ModuleName
	RouterKey  = ModuleName
)

// KVStore key prefixes
const (
	prefixParams = iota + 1
	prefixCurrentPrice
	prefixPriceHistory
)

var (
	KeyPrefixParams       = []byte{prefixParams}
	KeyPrefixCurrentPrice = []byte{prefixCurrentPrice}
	KeyPrefixPriceHistory = []byte{prefixPriceHistory}
)

// PriceHistoryKey returns the KV store key for a price history entry at a given timestamp.
func PriceHistoryKey(timestamp int64) []byte {
	bz := make([]byte, 9)
	bz[0] = prefixPriceHistory
	// Big-endian encoding for natural ordering
	bz[1] = byte(timestamp >> 56)
	bz[2] = byte(timestamp >> 48)
	bz[3] = byte(timestamp >> 40)
	bz[4] = byte(timestamp >> 32)
	bz[5] = byte(timestamp >> 24)
	bz[6] = byte(timestamp >> 16)
	bz[7] = byte(timestamp >> 8)
	bz[8] = byte(timestamp)
	return bz
}
