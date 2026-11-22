package keeper

import (
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/atoshi-chain/atoshi/v20/x/energy/types"
)

// EndBlocker fires once per block. Two responsibilities:
//
//  1. Release any EnergyDelegation whose expires_at has passed.
//  2. Delete pending-reservation markers left by older binaries, without
//     refunding them (see pruneLegacyPendingReservations). Failed txs no
//     longer get their energy back: like a fee, it stays spent.
//
// We deliberately do NOT settle every account here — accounts settle
// lazily on touch. Sweeping by expiry uses a sorted secondary index, so
// the work is O(expired) per block, not O(total accounts).
func (k Keeper) EndBlocker(ctx sdk.Context) {
	// Runs before the EnergyEnabled gate: the old binary accumulated
	// markers precisely while the module was disabled, so the cleanup
	// must not depend on it being enabled. With no markers left this is
	// a single empty prefix seek.
	if n := k.pruneLegacyPendingReservations(ctx); n > 0 {
		ctx.Logger().With("module", "x/"+types.ModuleName).
			Info("deleted legacy pending-reservation markers without refund", "count", n)
	}
	if !k.GetParams(ctx).EnergyEnabled {
		return
	}
	k.SweepExpiredDelegations(ctx)
}
