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

// PriceHistoryKey returns the KV store key for a price history entry.
//
// Key layout: prefix(1) || timestamp_be(8) || feeder_addr(variable).
//
// Surfaced during the audit Issue 4 fix: the prior key was just
// prefix(1) || timestamp_be(8), so two reports landing in the SAME
// block from DIFFERENT feeders would collide on the same KV key and
// overwrite each other. The downstream MinValidReports gate
// (introduced by the Issue 4 fix) would then silently lose one
// feeder's signal and never reach quorum even with multiple feeders
// active.
//
// Appending the feeder address makes the key unique per (timestamp,
// feeder) pair. The big-endian timestamp prefix preserves the
// reverse-iteration "newest first" ordering used by GetPriceHistory.
// Sub-orderings within the same timestamp are by feeder bytes; not
// semantically meaningful, but deterministic.
func PriceHistoryKey(timestamp int64, feeder string) []byte {
	bz := make([]byte, 0, 9+len(feeder))
	bz = append(bz, prefixPriceHistory)
	bz = append(bz,
		byte(timestamp>>56),
		byte(timestamp>>48),
		byte(timestamp>>40),
		byte(timestamp>>32),
		byte(timestamp>>24),
		byte(timestamp>>16),
		byte(timestamp>>8),
		byte(timestamp),
	)
	bz = append(bz, []byte(feeder)...)
	return bz
}
