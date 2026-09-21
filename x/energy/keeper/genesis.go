package keeper

import (
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/atoshi-chain/atoshi/v20/x/energy/types"
)

// InitGenesis writes params, accounts and delegations from a GenesisState
// snapshot. Bank balances backing locked ATOS are NOT minted here — the
// caller (app genesis) is responsible for ensuring the locked module
// account already holds the matching coins (sum of all delegation
// LockedAtos values).
func (k Keeper) InitGenesis(ctx sdk.Context, gs types.GenesisState) {
	if err := k.SetParams(ctx, gs.Params); err != nil {
		panic(err)
	}
	for _, acct := range gs.Accounts {
		k.SetEnergyAccount(ctx, acct)
	}
	for _, d := range gs.Delegations {
		k.setDelegation(ctx, d)
	}
	if gs.NextDelegationId == 0 {
		gs.NextDelegationId = 1
	}
	k.setNextDelegationID(ctx, gs.NextDelegationId)
	k.EnsureLockedPoolExists(ctx)
}

func (k Keeper) ExportGenesis(ctx sdk.Context) *types.GenesisState {
	gs := &types.GenesisState{
		Params: k.GetParams(ctx),
	}
	k.IterateAccounts(ctx, func(a types.EnergyAccount) bool {
		gs.Accounts = append(gs.Accounts, a)
		return false
	})
	// Walk every delegation by reading the primary index.
	store := ctx.KVStore(k.storeKey)
	bz := store.Get(types.KeyNextDelegationID)
	if bz != nil {
		gs.NextDelegationId = bigEndianUint64(bz)
	} else {
		gs.NextDelegationId = 1
	}
	for id := uint64(1); id < gs.NextDelegationId; id++ {
		if d, ok := k.GetDelegation(ctx, id); ok {
			gs.Delegations = append(gs.Delegations, d)
		}
	}
	return gs
}
