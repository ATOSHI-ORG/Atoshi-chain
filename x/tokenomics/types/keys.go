package types

const (
	ModuleName = "tokenomics"
	StoreKey   = ModuleName
	RouterKey  = ModuleName

	MinerPoolName     = "miner_pool"
	ProjectPoolName   = "project_pool"
	MigrationPoolName = "migration_pool"
)

const (
	prefixParams = iota + 1
	// prefixMinerLocked is retired. The number stays occupied on purpose: a new
	// prefix reusing it would collide with any locked-balance rows still on disk
	// from before the ATOX switch and read them as something else.
	prefixMinerLockedRetired
	prefixReleaseState
	prefixProjectClaimable
	prefixMigrationClaimed
	prefixMigrationRoot
	prefixCirculatingCache
	prefixBlockRewardState
)

var (
	KeyPrefixParams           = []byte{prefixParams}
	KeyPrefixReleaseState     = []byte{prefixReleaseState}
	KeyPrefixProjectClaimable = []byte{prefixProjectClaimable}
	KeyPrefixMigrationClaimed = []byte{prefixMigrationClaimed}
	KeyPrefixMigrationRoot    = []byte{prefixMigrationRoot}
	KeyPrefixCirculatingCache = []byte{prefixCirculatingCache}
	KeyPrefixBlockRewardState = []byte{prefixBlockRewardState}
)

func MigrationClaimedKey(addr string) []byte {
	return append(KeyPrefixMigrationClaimed, []byte(addr)...)
}
