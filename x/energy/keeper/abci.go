package keeper

import sdk "github.com/cosmos/cosmos-sdk/types"

// EndBlocker fires once per block. Today its only job is to release any
// EnergyDelegation whose expires_at has passed.
//
// We deliberately do NOT settle every account here — accounts settle
// lazily on touch. Sweeping by expiry uses a sorted secondary index, so
// the work is O(expired) per block, not O(total accounts).
func (k Keeper) EndBlocker(ctx sdk.Context) {
	if !k.GetParams(ctx).EnergyEnabled {
		return
	}
	k.SweepExpiredDelegations(ctx)
}