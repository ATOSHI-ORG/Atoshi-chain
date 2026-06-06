package types

import (
	"encoding/binary"
)

const (
	ModuleName = "energy"
	StoreKey   = ModuleName
	RouterKey  = ModuleName

	// LockedEnergyPoolName is the module account that holds ATOS frozen
	// because of outbound delegations. Coins are moved here on Delegate
	// and back to the delegator's bank account on Undelegate / expire.
	LockedEnergyPoolName = "energy_locked_pool"
)

const (
	prefixParams = iota + 1
	prefixAccount
	prefixDelegation
	prefixDelegationByExpiry
	prefixDelegationsByDelegator
	prefixDelegationsByDelegatee
	prefixNextDelegationID
)

var (
	KeyPrefixParams                = []byte{prefixParams}
	KeyPrefixAccount               = []byte{prefixAccount}
	KeyPrefixDelegation            = []byte{prefixDelegation}
	KeyPrefixDelegationByExpiry    = []byte{prefixDelegationByExpiry}
	KeyPrefixDelegationsByDeleg    = []byte{prefixDelegationsByDelegator}
	KeyPrefixDelegationsByDelegee  = []byte{prefixDelegationsByDelegatee}
	KeyNextDelegationID            = []byte{prefixNextDelegationID}
)

// AccountKey returns the KV key for an account's energy state.
func AccountKey(addr string) []byte {
	return append(KeyPrefixAccount, []byte(addr)...)
}

// DelegationKey indexes a delegation by its primary id.
func DelegationKey(id uint64) []byte {
	bz := make([]byte, 1+8)
	bz[0] = prefixDelegation
	binary.BigEndian.PutUint64(bz[1:], id)
	return bz
}

// DelegationByExpiryKey is a secondary index ordered by expiry time, used
// by EndBlocker to clean up expired delegations without scanning the
// whole table.
func DelegationByExpiryKey(expiresAt int64, id uint64) []byte {
	bz := make([]byte, 1+8+8)
	bz[0] = prefixDelegationByExpiry
	// Big-endian for natural lexicographic ordering.
	binary.BigEndian.PutUint64(bz[1:9], uint64(expiresAt))
	binary.BigEndian.PutUint64(bz[9:], id)
	return bz
}

// DelegationByDelegatorKey indexes outbound delegations.
func DelegationByDelegatorKey(delegator string, id uint64) []byte {
	prefix := append(KeyPrefixDelegationsByDeleg, []byte(delegator)...)
	prefix = append(prefix, '/')
	idBz := make([]byte, 8)
	binary.BigEndian.PutUint64(idBz, id)
	return append(prefix, idBz...)
}

// DelegationByDelegateeKey indexes inbound delegations.
func DelegationByDelegateeKey(delegatee string, id uint64) []byte {
	prefix := append(KeyPrefixDelegationsByDelegee, []byte(delegatee)...)
	prefix = append(prefix, '/')
	idBz := make([]byte, 8)
	binary.BigEndian.PutUint64(idBz, id)
	return append(prefix, idBz...)
}