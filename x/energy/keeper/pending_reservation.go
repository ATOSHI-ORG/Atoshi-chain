package keeper

import (
	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/atoshi-chain/atoshi/v20/x/energy/types"
)

// legacyReservationPruneBatch bounds how many leftover pending-reservation
// markers one EndBlocker deletes. Markers are inert now, so there is no
// hurry: a large backlog is drained over several blocks instead of in one
// unbounded loop.
const legacyReservationPruneBatch = 1_000

// pruneLegacyPendingReservations deletes pending-reservation markers
// written by older binaries, WITHOUT refunding anything.
//
// History (Audit Issue-1, round2): the AnteHandler used to write a marker
// per tx after Consume(), the PostHandler deleted it on success, and
// EndBlocker refunded the full reservation of every marker still present --
// i.e. of every failed tx. That made failed txs cost no energy at all, so
// an account could spend its whole energy capacity on failing txs in every
// block and get it all back at EndBlock. Energy is a fee substitute; in the
// SDK a failed tx still pays its fee on the full gas limit, so a failed
// tx's energy now simply stays spent and no marker is written.
//
// Markers can still be in state after the upgrade: the old EndBlocker
// returned before its sweep whenever EnergyEnabled was false, while the old
// AnteHandler kept writing markers, so they accumulated across any period
// the module was disabled. They are deleted here and never refunded --
// refunding them would hand out exactly the energy this change stops
// handing out. Malformed values are deleted too (the old sweep skipped and
// kept them forever).
func (k Keeper) pruneLegacyPendingReservations(ctx sdk.Context) int {
	store := ctx.KVStore(k.storeKey)
	it := storetypes.KVStorePrefixIterator(store, types.KeyPrefixPendingReservation)
	var keys [][]byte
	for ; it.Valid() && len(keys) < legacyReservationPruneBatch; it.Next() {
		keys = append(keys, append([]byte(nil), it.Key()...))
	}
	it.Close()
	// Collect first, delete after: deleting under a live iterator on the
	// same prefix is not safe.
	for _, key := range keys {
		store.Delete(key)
	}
	return len(keys)
}
