package keeper

import (
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/atoshi-chain/atoshi/v20/x/atox/types"
)

func (k Keeper) InitGenesis(ctx sdk.Context, gs types.GenesisState) {
	k.EnsureExchangePoolExists(ctx)

	if err := k.SetParams(ctx, gs.Params); err != nil {
		panic(err)
	}
	if err := k.SetGlobalState(ctx, gs.GlobalState); err != nil {
		panic(err)
	}
	for _, acct := range gs.Accounts {
		if err := k.SetAtoxAccount(ctx, acct); err != nil {
			panic(err)
		}
	}
	k.SetScanCursor(ctx, gs.ScanCursor)

	// No ATOX is minted here and the exchange pool starts empty. Supply grows
	// only through block rewards, and the pool is only ever funded by tier
	// releases against ERC20 already confirmed on Ethereum — pre-funding either
	// side at genesis would hand out claims with nothing backing them.
}

func (k Keeper) ExportGenesis(ctx sdk.Context) *types.GenesisState {
	var accounts []types.AtoxAccount
	k.IterateAccounts(ctx, func(a types.AtoxAccount) bool {
		accounts = append(accounts, a)
		return false
	})

	return &types.GenesisState{
		Params:      k.GetParams(ctx),
		GlobalState: k.GetGlobalState(ctx),
		Accounts:    accounts,
		ScanCursor:  k.GetScanCursor(ctx),
	}
}
