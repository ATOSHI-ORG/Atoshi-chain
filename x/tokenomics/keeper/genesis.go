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

	for _, addr := range gs.MigrationClaimed {
		k.SetMigrationClaimed(ctx, addr)
	}

	params := gs.Params
	denom := k.baseDenom()

	// Mint only into a pool that is empty.
	//
	// This function runs on every chain start from a genesis file, not only on
	// the first one. `atoshid export` -- disaster recovery, a hard fork, a
	// rebuild after an incident -- produces a file in which x/bank already
	// carries every balance, including these three module accounts, and bank
	// restores them at init position 3. Tokenomics runs at position 25, so
	// bank's own "genesis supply is incorrect" check has long since passed by
	// the time we get here and cannot catch a second mint.
	//
	// The three pools are the entire supply (helpers.go: 10,000 + 87,000 +
	// 3,000 billion ATOS = 10 trillion), so an unconditional mint brings the
	// chain back up with 20 trillion ATOS against 100 billion ERC20 on
	// Ethereum -- the 100:1 peg gone on the first restore. x/atox avoids this
	// by minting nothing at genesis; this module has to mint, so it checks.
	//
	// The balance, not a height or a flag: a pool that already holds coins has
	// been funded, whoever funded it. Pinned by
	// TestGenesisRoundTrip_DoesNotRemintTheSupply.
	mintToModule := func(module string, amount math.Int) {
		if !amount.IsPositive() {
			return
		}
		if addr := k.accountKeeper.GetModuleAddress(module); addr != nil {
			if k.bankKeeper.GetBalance(ctx, addr, denom).Amount.IsPositive() {
				return
			}
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
		MigrationClaimed: k.AllMigrationClaimed(ctx),
	}
}
