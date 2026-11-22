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

	// DefaultDelegationDurationSeconds is the compiled fallback used by
	// the MsgDelegateEnergy server ONLY when Params.DefaultDelegationDurationSeconds
	// is zero (i.e. pre-upgrade state that predates that field). Under
	// normal operation the governance-set params value is preferred.
	// Kept aligned with the DefaultParams() value for consistency.
	DefaultDelegationDurationSeconds int64 = 24 * 60 * 60 // 24h = 86400 s
)

const (
	prefixParams = iota + 1
	prefixAccount
	prefixDelegation
	prefixDelegationByExpiry
	prefixDelegationsByDelegator
	prefixDelegationsByDelegatee
	prefixNextDelegationID
	// Retired. Held the Audit Issue-1 (round2) per-tx reservation marker
	// that let EndBlocker refund the energy of failed txs. Failed txs now
	// keep their energy spent (like a fee), so nothing writes here any
	// more; EndBlocker only deletes markers an older binary left behind.
	// The byte stays reserved -- never reuse it for another store.
	prefixPendingReservation
)

var (
	KeyPrefixParams               = []byte{prefixParams}
	KeyPrefixAccount              = []byte{prefixAccount}
	KeyPrefixDelegation           = []byte{prefixDelegation}
	KeyPrefixDelegationByExpiry   = []byte{prefixDelegationByExpiry}
	KeyPrefixDelegationsByDeleg   = []byte{prefixDelegationsByDelegator}
	KeyPrefixDelegationsByDelegee = []byte{prefixDelegationsByDelegatee}
	KeyNextDelegationID           = []byte{prefixNextDelegationID}
	KeyPrefixPendingReservation   = []byte{prefixPendingReservation}
)

// PendingReservationKey is the key a legacy reservation marker was
// stored under (prefix + sha256 of the raw tx bytes). Only tests write
// it now, to plant markers for the EndBlocker cleanup.
func PendingReservationKey(txHash []byte) []byte {
	out := make([]byte, 0, 1+len(txHash))
	out = append(out, KeyPrefixPendingReservation...)
	out = append(out, txHash...)
	return out
}

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
