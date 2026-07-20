package v20_3

import (
	"context"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"

	energykeeper "github.com/atoshi-chain/atoshi/v20/x/energy/keeper"
)

// CreateUpgradeHandler returns the v20.3 upgrade handler.
//
// Sets the two new energy params (default/max delegation duration) to
// 86400 (24h) so post-upgrade state has explicit values rather than
// relying on the compiled-constant fallback in msg_server.
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

		params := ek.GetParams(ctx)
		params.DefaultDelegationDurationSeconds = 86_400 // 24h
		params.MaxDelegationDurationSeconds = 86_400     // 24h hard cap
		if err := ek.SetParams(ctx, params); err != nil {
			return newVM, err
		}
		logger.Info("set delegation duration params",
			"default", params.DefaultDelegationDurationSeconds,
			"max", params.MaxDelegationDurationSeconds)

		return newVM, nil
	}
}
