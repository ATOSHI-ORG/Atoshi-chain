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
	for _, bal := range gs.MinerLockedBalances {
		if err := k.SetMinerLockedBalance(ctx, bal); err != nil {
			panic(err)
		}
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

	minerImmediate := params.MinerPoolTotal.MulRaw(int64(params.ImmediateRewardBps)).QuoRaw(10000)
	minerLocked := params.MinerPoolTotal.Sub(minerImmediate)

	mintToModule(tokenomicstypes.MinerPoolName, minerImmediate)
	mintToModule(tokenomicstypes.MinerLockedPoolName, minerLocked)
	mintToModule(tokenomicstypes.ProjectPoolName, params.ProjectPoolTotal)
	mintToModule(tokenomicstypes.MigrationPoolName, params.MigrationPoolTotal)
}

func (k Keeper) ExportGenesis(ctx sdk.Context) *tokenomicstypes.GenesisState {
	var balances []tokenomicstypes.MinerLockedBalance
	k.IterateMinerLockedBalances(ctx, func(balance tokenomicstypes.MinerLockedBalance) bool {
		balances = append(balances, balance)
		return false
	})
	return &tokenomicstypes.GenesisState{
		Params:              k.GetParams(ctx),
		ReleaseState:        k.GetReleaseState(ctx),
		BlockRewardState:    k.GetBlockRewardState(ctx),
		MinerLockedBalances: balances,
		ProjectClaimable:    k.GetProjectClaimable(ctx),
	}
}
