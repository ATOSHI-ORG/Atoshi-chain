package v20_1

import (
	"context"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"

	energykeeper "github.com/atoshi-chain/atoshi/v20/x/energy/keeper"
)

// CreateUpgradeHandler returns the v20.1 upgrade handler.
//
// It first runs standard module migrations (a no-op for this release,
// since no consensus version was bumped), then sweeps every stored
// EnergyAccount and re-snapshots its LastBalanceSnapshot against the
// current bank balance. After the handler returns, the next block's
// AnteHandler observes correct capacities for every existing wallet
// without requiring users to broadcast a "warm-up" tx.
func CreateUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator,
	ek energykeeper.Keeper,
) upgradetypes.UpgradeHandler {
	return func(c context.Context, _ upgradetypes.Plan, vm module.VersionMap) (module.VersionMap, error) {
		ctx := sdk.UnwrapSDKContext(c)
		logger := ctx.Logger().With("upgrade", UpgradeName)

		newVM, err := mm.RunMigrations(ctx, configurator, vm)
		if err != nil {
			return newVM, err
		}

		refreshed := ek.RefreshAllSnapshots(ctx)
		logger.Info("energy snapshot refresh complete", "accounts_refreshed", refreshed)

		return newVM, nil
	}
}
