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
		// Audit Issue 10: ExportGenesis populates PriceHistory via
		// GetPriceHistory, which iterates the KV store in reverse and
		// returns newest first (index 0 = latest). The previous code
		// here took PriceHistory[len-1] (the *oldest* entry) as the
		// current price, so after a snapshot restart the chain's
		// current price would be set to a stale historical value
		// rather than the most recent observation. Take index 0 to
		// preserve the newest-first invariant from export.
		latest := findLatestPrice(gs.PriceHistory)
		if err := k.SetCurrentPrice(ctx, latest); err != nil {
			panic(err)
		}
	}
}

// findLatestPrice picks the entry with the maximum Timestamp.
// We don't trust the slice ordering here: ExportGenesis returns
// newest first (index 0) but external tools may produce a genesis
// file with arbitrary ordering. Scanning by timestamp is O(n) and
// robust against either source.
func findLatestPrice(history []types.PriceData) types.PriceData {
	latest := history[0]
	for _, pd := range history[1:] {
		if pd.Timestamp > latest.Timestamp {
			latest = pd
		}
	}
	return latest
}

func (k Keeper) ExportGenesis(ctx sdk.Context) *types.GenesisState {
	params := k.GetParams(ctx)
	history := k.GetPriceHistory(ctx, 10000)
	return &types.GenesisState{
		Params:       params,
		PriceHistory: history,
	}
}
