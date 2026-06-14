package v20_2

import (
	"context"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"

	inflationkeeper "github.com/atoshi-chain/atoshi/v20/x/inflation/v1/keeper"
)

// CreateUpgradeHandler returns the v20.2 upgrade handler.
//
// Bundles the post-audit fixes (Question 4 in particular: force-disable
// the inflation module so the 10-trillion ATOS supply guard in
// x/tokenomics is the only issuance path).
//
// All other audit fixes in this release are code-only and take effect
// automatically on the binary swap — they don't need state mutations
// here.
func CreateUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator,
	ik inflationkeeper.Keeper,
) upgradetypes.UpgradeHandler {
	return func(c context.Context, _ upgradetypes.Plan, vm module.VersionMap) (module.VersionMap, error) {
		ctx := sdk.UnwrapSDKContext(c)
		logger := ctx.Logger().With("upgrade", UpgradeName)

		newVM, err := mm.RunMigrations(ctx, configurator, vm)
		if err != nil {
			return newVM, err
		}

		// Audit Question 4: disable inflation. Live chains were
		// genesis-initialized with EnableInflation=true via the evmos
		// fork default; the post-audit fix flips DefaultInflation to
		// false but that only affects fresh chains. Existing chains
		// must be re-configured at upgrade time.
		infParams := ik.GetParams(ctx)
		if infParams.EnableInflation {
			infParams.EnableInflation = false
			if err := ik.SetParams(ctx, infParams); err != nil {
				return newVM, err
			}
			logger.Info("inflation module disabled by v20.2 upgrade",
				"reason", "audit Q4 — x/tokenomics is the sole issuance path")
		} else {
			logger.Info("inflation module already disabled; no-op")
		}

		return newVM, nil
	}
}
