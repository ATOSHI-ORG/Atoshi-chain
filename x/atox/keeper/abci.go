package keeper

import (
	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/atoshi-chain/atoshi/v20/x/atox/types"
)

// EndBlocker settles and pays out a bounded slice of accounts each block,
// resuming from a stored cursor so the whole keyspace is covered over time.
//
// This is what makes conversion automatic. Without it a holder who never
// transacts would accrue `pending` forever and have to send MsgClaimAtos to see
// any ATOS — which also costs gas, so small holders might never bother. The
// sweep reaches everyone: at the default 50 accounts per block and 5s blocks
// that is ~864k accounts a day.
//
// The per-block count is bounded by Params.AutoSettlePerBlock (capped in turn by
// types.MaxAutoSettlePerBlock) because each account costs a store read and
// possibly a bank transfer, and an unbounded value set by a bad proposal could
// push block execution past the consensus timeout.
//
// Collection and processing are deliberately separate passes. Settlement writes
// account records under the very prefix being walked, and mutating a store while
// an iterator over it is open is undefined behaviour in the SDK's cachekv layer —
// doing both at once made the sweep skip accounts and lose its cursor.
//
// A failure on one account is logged and skipped rather than returned. Returning
// would abort the block for every node hitting the same record, turning one bad
// account into a chain halt; skipping leaves that account settled and claimable
// via MsgClaimAtos.
func (k Keeper) EndBlocker(ctx sdk.Context) error {
	params := k.GetParams(ctx)
	if !params.Enabled || params.AutoSettlePerBlock == 0 {
		return nil
	}

	keys, hasMore := k.collectSweepBatch(ctx, params.AutoSettlePerBlock)

	for _, key := range keys {
		addrStr := types.AddressFromAccountKey(key)
		addr, err := sdk.AccAddressFromBech32(addrStr)
		if err != nil {
			k.Logger(ctx).Error("atox sweep: undecodable account key", "key", addrStr, "err", err)
			continue
		}
		if _, err := k.PayoutPending(ctx, addr, params.MinAutoPayout, types.TriggerSweep); err != nil {
			k.Logger(ctx).Error("atox sweep: payout failed", "address", addrStr, "err", err)
		}
	}

	if !hasMore {
		// Range exhausted: clear the cursor so the next block restarts from the
		// beginning. Keeping the last key would park the sweep at the tail of the
		// keyspace and never revisit the head.
		k.SetScanCursor(ctx, nil)
		return nil
	}

	// Resume strictly after the last key handled; resuming AT it would re-settle
	// that account every block and never advance.
	k.SetScanCursor(ctx, append(keys[len(keys)-1], 0x00))
	return nil
}

// collectSweepBatch returns up to `limit` account keys starting at the stored
// cursor, and whether more remain beyond them.
func (k Keeper) collectSweepBatch(ctx sdk.Context, limit uint32) (keys [][]byte, hasMore bool) {
	start := k.GetScanCursor(ctx)
	if len(start) == 0 {
		start = types.KeyPrefixAccount
	}

	iter := ctx.KVStore(k.storeKey).Iterator(start, storetypes.PrefixEndBytes(types.KeyPrefixAccount))
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		if uint32(len(keys)) == limit {
			return keys, true
		}
		keys = append(keys, append([]byte(nil), iter.Key()...))
	}
	return keys, false
}
