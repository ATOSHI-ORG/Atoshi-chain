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

	globalIndex := k.GetGlobalState(ctx).GlobalIndex

	// An empty cursor means no pass is in flight, so this block either starts a
	// new one or stands down.
	if len(k.GetScanCursor(ctx)) == 0 {
		// Steady-state skip. The index only advances when a tier release lands,
		// and that needs 30 consecutive qualifying days, so most of the time it
		// does not move. With the index unchanged since the last pass began,
		// every account has a zero span and a pass would read the whole account
		// table to do nothing.
		//
		// Accounts left with sub-threshold `pending` by MinAutoPayout wait for
		// the pass after the next release, which is the point of the threshold:
		// by then their accrual has had time to clear it.
		if k.GetSweptIndex(ctx).Equal(globalIndex) {
			return nil
		}

		// Starting a fresh pass: stamp the index it will cover NOW, at the start,
		// not when it finishes. A pass spans many blocks, and a tier release can
		// land midway. Stamping at the end would record the post-release index and
		// so claim that accounts settled earlier in the pass — at the lower index
		// — were up to date, skipping them until some later release moved the
		// index again. Stamping at the start leaves swept_index behind the live
		// index in that case, which correctly schedules another pass.
		k.SetSweptIndex(ctx, globalIndex)
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
