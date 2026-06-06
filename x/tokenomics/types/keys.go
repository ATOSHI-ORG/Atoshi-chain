package types

const (
	ModuleName = "tokenomics"
	StoreKey   = ModuleName
	RouterKey  = ModuleName

	MinerPoolName       = "miner_pool"
	ProjectPoolName     = "project_pool"
	MigrationPoolName   = "migration_pool"
	MinerLockedPoolName = "miner_locked_pool"
)

const (
	prefixParams = iota + 1
	prefixMinerLocked
	prefixReleaseState
	prefixProjectClaimable
	prefixMigrationClaimed
	prefixMigrationRoot
	prefixCirculatingCache
	prefixBlockRewardState
)

var (
	KeyPrefixParams           = []byte{prefixParams}
	KeyPrefixMinerLocked      = []byte{prefixMinerLocked}
	KeyPrefixReleaseState     = []byte{prefixReleaseState}
	KeyPrefixProjectClaimable = []byte{prefixProjectClaimable}
	KeyPrefixMigrationClaimed = []byte{prefixMigrationClaimed}
	KeyPrefixMigrationRoot    = []byte{prefixMigrationRoot}
	KeyPrefixCirculatingCache = []byte{prefixCirculatingCache}
	KeyPrefixBlockRewardState = []byte{prefixBlockRewardState}
)

func MinerLockedKey(valAddr string) []byte {
	return append(KeyPrefixMinerLocked, []byte(valAddr)...)
}

func MigrationClaimedKey(addr string) []byte {
	return append(KeyPrefixMigrationClaimed, []byte(addr)...)
}
