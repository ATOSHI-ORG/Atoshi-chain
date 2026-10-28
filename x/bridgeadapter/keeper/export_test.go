package keeper

import (
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/atoshi-chain/atoshi/v20/x/bridgeadapter/types"
)

// SetParamsUncheckedForTest exposes the unvalidated writer so a test can put
// params into a state SetParams would refuse -- which is the state the
// emergency halt has to keep working in.
func SetParamsUncheckedForTest(k Keeper, ctx sdk.Context, p types.Params) error {
	return k.setParamsUnchecked(ctx, p)
}
