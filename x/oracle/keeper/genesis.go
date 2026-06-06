package keeper

import (
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/atoshi-chain/atoshi/v20/x/oracle/types"
)

func (k Keeper) InitGenesis(ctx sdk.Context, gs types.GenesisState) {
	if err := k.SetParams(ctx, gs.Params); err != nil {
		panic(err)
	}
	for _, pd := range gs.PriceHistory {
		if err := k.AppendPriceHistory(ctx, pd); err != nil {
			panic(err)
		}
	}
	if len(gs.PriceHistory) > 0 {
		latest := gs.PriceHistory[len(gs.PriceHistory)-1]
		if err := k.SetCurrentPrice(ctx, latest); err != nil {
			panic(err)
		}
	}
}

func (k Keeper) ExportGenesis(ctx sdk.Context) *types.GenesisState {
	params := k.GetParams(ctx)
	history := k.GetPriceHistory(ctx, 10000)
	return &types.GenesisState{
		Params:       params,
		PriceHistory: history,
	}
}
