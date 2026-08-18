package types

const (
	ModuleName = "atox"
	StoreKey   = ModuleName
	RouterKey  = ModuleName

	// ExchangePoolName holds the ATOS that backs outstanding ATOX. Tier
	// releases pay into it; settled conversions pay out of it. It is the only
	// source of ATOS this module ever spends, which is what makes solvency
	// externally auditable: compare its balance against GlobalState.TotalPending.
	ExchangePoolName = "atox_exchange_pool"
)

const (
	prefixParams = iota + 1
	prefixGlobalState
	prefixAccount
	prefixScanCursor
)

var (
	KeyPrefixParams  = []byte{prefixParams}
	KeyGlobalState   = []byte{prefixGlobalState}
	KeyPrefixAccount = []byte{prefixAccount}
	KeyScanCursor    = []byte{prefixScanCursor}
)

// AccountKey returns the KV key for an account's settlement record.
func AccountKey(addr string) []byte {
	out := make([]byte, 0, len(KeyPrefixAccount)+len(addr))
	out = append(out, KeyPrefixAccount...)
	out = append(out, addr...)
	return out
}

// AddressFromAccountKey recovers the bech32 address from a full account key.
// The EndBlocker sweep stores its cursor as a raw store key, so it needs to get
// back to an address when it resumes.
func AddressFromAccountKey(key []byte) string {
	if len(key) <= len(KeyPrefixAccount) {
		return ""
	}
	return string(key[len(KeyPrefixAccount):])
}
