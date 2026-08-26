package keeper

import (
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	tokenomicstypes "github.com/atoshi-chain/atoshi/v20/x/tokenomics/types"
)

func (k Keeper) InitGenesis(ctx sdk.Context, gs tokenomicstypes.GenesisState) {
	if err := k.SetParams(ctx, gs.Params); err != nil {
		panic(err)
	}
	if err := k.SetReleaseState(ctx, gs.ReleaseState); err != nil {
		panic(err)
	}
	if err := k.SetBlockRewardState(ctx, gs.BlockRewardState); err != nil {
		panic(err)
	}
	if !gs.ProjectClaimable.IsNil() {
		k.SetProjectClaimable(ctx, gs.ProjectClaimable)
	}

	params := gs.Params
	denom := k.baseDenom()
	mintToModule := func(module string, amount math.Int) {
		if !amount.IsPositive() {
			return
		}
		coin := sdk.NewCoin(denom, amount)
		if err := k.bankKeeper.MintCoins(ctx, tokenomicstypes.ModuleName, sdk.NewCoins(coin)); err != nil {
			panic(err)
		}
		if err := k.bankKeeper.SendCoinsFromModuleToModule(ctx, tokenomicstypes.ModuleName, module, sdk.NewCoins(coin)); err != nil {
			panic(err)
		}
	}

	// One miner pool, no split. It no longer pays block rewards — those are ATOX
	// now — it holds the ATOS that backs ATOX one-for-one until tier releases move
	// it into the conversion pool.
	mintToModule(tokenomicstypes.MinerPoolName, params.MinerPoolTotal)
	mintToModule(tokenomicstypes.ProjectPoolName, params.ProjectPoolTotal)
	mintToModule(tokenomicstypes.MigrationPoolName, params.MigrationPoolTotal)
}

func (k Keeper) ExportGenesis(ctx sdk.Context) *tokenomicstypes.GenesisState {
	return &tokenomicstypes.GenesisState{
		Params:           k.GetParams(ctx),
		ReleaseState:     k.GetReleaseState(ctx),
		BlockRewardState: k.GetBlockRewardState(ctx),
		ProjectClaimable: k.GetProjectClaimable(ctx),
	}
}
