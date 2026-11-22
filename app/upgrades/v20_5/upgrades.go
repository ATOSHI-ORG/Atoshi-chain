package v20_5

import (
	"context"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	"github.com/cosmos/cosmos-sdk/x/authz"

	vestingtypes "github.com/atoshi-chain/atoshi/v20/x/vesting/types"
)

// AuthzKeeper is the subset of x/authz this upgrade needs.
type AuthzKeeper interface {
	IterateGrants(ctx context.Context, handler func(granterAddr, granteeAddr sdk.AccAddress, grant authz.Grant) bool)
	DeleteGrant(ctx context.Context, grantee, granter sdk.AccAddress, msgType string) error
}

// revokedMsgTypes are the vesting messages whose grants are revoked.
//
// The vesting precompile's approve granted these with tx.origin as the
// granter, so any contract a user called could plant a grant over the user's
// funds for an address of its choosing; a planted MsgFundVestingAccount grant
// lets the grantee keep draining the granter with plain Cosmos MsgExec txs
// after the precompile is fixed. Planted grants cannot be told apart from ones
// a user made on purpose, and legitimate use of these grants is rare, so all
// of them go. Anyone who needs one back can grant it again with MsgGrant.
var revokedMsgTypes = map[string]bool{
	sdk.MsgTypeURL(&vestingtypes.MsgFundVestingAccount{}):  true,
	sdk.MsgTypeURL(&vestingtypes.MsgClawback{}):            true,
	sdk.MsgTypeURL(&vestingtypes.MsgUpdateVestingFunder{}): true,
}

type grantKey struct {
	granter, grantee sdk.AccAddress
	msgType          string
}

// CreateUpgradeHandler returns the v20.5 upgrade handler.
//
// Revoking grants is not allowed to fail the upgrade: an aborted upgrade halts
// every node until a new binary ships, which is worse than leaving a grant for
// governance to deal with. Failures are logged at Error and skipped.
func CreateUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator,
	ak AuthzKeeper,
) upgradetypes.UpgradeHandler {
	return func(c context.Context, _ upgradetypes.Plan, vm module.VersionMap) (module.VersionMap, error) {
		ctx := sdk.UnwrapSDKContext(c)

		newVM, err := mm.RunMigrations(ctx, configurator, vm)
		if err != nil {
			return newVM, err
		}

		revokeVestingGrants(ctx, ak)
		return newVM, nil
	}
}

// revokeVestingGrants deletes every grant of a vesting message in
// revokedMsgTypes. Grants are collected first and deleted afterwards, since
// deleting while iterating the store is not safe. Iteration is in store key
// order, so the result is deterministic.
func revokeVestingGrants(ctx sdk.Context, ak AuthzKeeper) {
	logger := ctx.Logger().With("upgrade", UpgradeName)

	var toRevoke []grantKey
	ak.IterateGrants(ctx, func(granter, grantee sdk.AccAddress, grant authz.Grant) bool {
		a, err := grant.GetAuthorization()
		if err != nil || a == nil {
			return false
		}
		if revokedMsgTypes[a.MsgTypeURL()] {
			toRevoke = append(toRevoke, grantKey{granter, grantee, a.MsgTypeURL()})
		}
		return false
	})

	revoked := 0
	for _, g := range toRevoke {
		if err := ak.DeleteGrant(ctx, g.grantee, g.granter, g.msgType); err != nil {
			logger.Error("could not revoke vesting grant",
				"granter", g.granter.String(), "grantee", g.grantee.String(), "msg_type", g.msgType, "error", err)
			continue
		}
		logger.Info("revoked vesting grant",
			"granter", g.granter.String(), "grantee", g.grantee.String(), "msg_type", g.msgType)
		revoked++
	}
	logger.Info("vesting grants revoked", "count", revoked, "found", len(toRevoke))
}
