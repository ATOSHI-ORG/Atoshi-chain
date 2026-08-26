package keeper

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/atoshi-chain/atoshi/v20/x/bridgeadapter/types"
)

func (k Keeper) InitGenesis(ctx sdk.Context, gs types.GenesisState) {
	if err := k.SetParams(ctx, gs.Params); err != nil {
		panic(err)
	}

	state := gs.ReceiptState

	// Draw this app's recipient address from the core app router unless genesis
	// already carries one (an exported chain does). The address encodes our
	// module id in its type field, which is what makes the mailbox route
	// receipts here rather than to warp.
	if len(state.AppId) != types.HexAddressLen {
		appID, err := k.coreKeeper.AppRouter().GetNextSequence(ctx, types.AppModuleID)
		if err != nil {
			panic(fmt.Errorf("failed to assign bridgeadapter app id: %w", err))
		}
		state.AppId = appID[:]
	}

	// The asset bridge gets its own recipient address from the same sequence.
	// Separate addresses are what keep the two channels from being confused: both
	// payloads are 64 bytes, so nothing else would distinguish a hostile asset
	// transfer from a tier release.
	if len(state.AssetAppId) != types.HexAddressLen {
		assetID, err := k.coreKeeper.AppRouter().GetNextSequence(ctx, types.AppModuleID)
		if err != nil {
			panic(fmt.Errorf("failed to assign bridgeadapter asset app id: %w", err))
		}
		state.AssetAppId = assetID[:]
	}

	if err := k.SetReceiptState(ctx, state); err != nil {
		panic(err)
	}
}

func (k Keeper) ExportGenesis(ctx sdk.Context) *types.GenesisState {
	return &types.GenesisState{
		Params:       k.GetParams(ctx),
		ReceiptState: k.GetReceiptState(ctx),
	}
}
