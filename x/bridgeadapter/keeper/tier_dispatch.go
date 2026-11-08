package keeper

import (
	"fmt"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"github.com/bcp-innovations/hyperlane-cosmos/util"

	"github.com/atoshi-chain/atoshi/v20/x/bridgeadapter/types"
)

// DispatchTierRelease sends the forward leg of a tier release to Ethereum.
//
// This is step 1 of the four-step round trip in the design doc §3.4:
//
//  1. Atoshi books the release and dispatches the two cumulative targets  <- here
//  2. Ethereum's TierReleaseVault divides by the peg, releases the difference
//  3. Ethereum dispatches a receipt with what it actually released
//  4. Atoshi applies the receipt, moving ATOS into the conversion pool
//
// No ATOS moves here. Step 1 only tells Ethereum how much release Atoshi's tier
// state machine has authorized in total; ATOS reaches the conversion pool only
// once step 4 confirms the ERC20 is actually in the bridge. That ordering is
// what keeps the invariant the doc requires: at every instant the ERC20 in the
// bridge is at least the ATOS handed out by the conversion pool divided by the
// peg. Dispatching is therefore safe to retry -- the amounts are cumulative
// targets, so a duplicate resolves to a zero difference on the Ethereum side.
//
// Amounts are in ATOS units. Ethereum divides by params.AtosPerErc20 itself;
// converting here would lose the remainder of a non-divisible target and make
// the two sides' ledgers disagree by the truncation.
func (k Keeper) DispatchTierRelease(
	ctx sdk.Context,
	cumulativeMiner math.Int,
	cumulativeProject math.Int,
) (util.HexAddress, error) {
	params := k.GetParams(ctx)
	if !params.Enabled {
		return util.HexAddress{}, types.ErrDisabled
	}
	if len(params.MailboxId) != types.HexAddressLen ||
		len(params.TierReleaseVault) != types.HexAddressLen {
		return util.HexAddress{}, fmt.Errorf(
			"%w: mailbox_id and tier_release_vault must be set before dispatching",
			types.ErrNotConfigured)
	}
	if cumulativeMiner.IsNil() || cumulativeMiner.IsNegative() ||
		cumulativeProject.IsNil() || cumulativeProject.IsNegative() {
		return util.HexAddress{}, fmt.Errorf(
			"%w: cumulative amounts must be non-negative", types.ErrInvalidPayload)
	}

	state := k.GetReceiptState(ctx)
	if len(state.AppId) != types.HexAddressLen {
		return util.HexAddress{}, types.ErrAppNotInitialized
	}

	body, err := types.BuildTierRelease(cumulativeMiner, cumulativeProject)
	if err != nil {
		return util.HexAddress{}, err
	}

	var mailboxID, vault, appID util.HexAddress
	copy(mailboxID[:], params.MailboxId)
	copy(vault[:], params.TierReleaseVault)
	copy(appID[:], state.AppId)

	// A tier release is triggered by the chain, not by a user, so there is no
	// account to charge: this module's own account pays, and it has to be funded
	// out of band for that to work.
	//
	// Both halves matter once any paying hook is attached to the mailbox. An
	// empty maxFee is rejected outright (hyperlane-cosmos v1.1.0,
	// x/core/02_post_dispatch/keeper/hook_igp.go:46), and an empty payer address
	// has no balance to debit. Neither shows up anywhere an operator looks: this
	// runs from EndBlock, where a failure produces no transaction result, so the
	// release simply stops happening.
	maxFee := sdk.NewCoins()
	// Derived, not looked up: this module is deliberately absent from maccPerms,
	// so there is no module account to fetch. Absent is the right state -- the
	// address must stay fundable by an ordinary transfer, and BlockedAddrs is
	// built from maccPerms, so registering it would make it unfundable.
	payer := authtypes.NewModuleAddress(types.ModuleName)
	if fee := params.EffectiveTierDispatchMaxFee(); fee.IsPositive() {
		maxFee = sdk.NewCoins(sdk.NewCoin(k.tokenomicsKeeper.BaseDenom(), fee))
		// Checked here rather than left to the bank, because the bank's error
		// would surface in the same place the rest of this does: nowhere.
		balance := k.bankKeeper.GetBalance(ctx, payer, k.tokenomicsKeeper.BaseDenom())
		if balance.Amount.LT(fee) {
			k.Logger(ctx).Error(
				"tier release dispatch is underfunded; the release will not reach Ethereum",
				"module_account", payer.String(),
				"balance", balance.String(),
				"required", fee.String(),
			)
			ctx.EventManager().EmitEvent(sdk.NewEvent(
				types.EventTypeTierDispatchUnderfunded,
				sdk.NewAttribute(types.AttributeKeyAddress, payer.String()),
				sdk.NewAttribute(types.AttributeKeyBalance, balance.Amount.String()),
				sdk.NewAttribute(types.AttributeKeyRequired, fee.String()),
			))
			return util.HexAddress{}, fmt.Errorf(
				"%w: %s holds %s, tier dispatch needs %s",
				types.ErrInsufficientDispatchFee, payer, balance.Amount, fee)
		}
	}

	// Sender is this app's own router address, the same one Ethereum is
	// configured to accept, so the contract's sender check has something stable
	// to compare against. Receipts arrive addressed to it too.
	msgID, err := k.coreKeeper.DispatchMessage(
		ctx,
		mailboxID,
		appID,
		maxFee,
		params.EthereumDomain,
		vault,
		body,
		util.StandardHookMetadata{
			Address:  payer,
			GasLimit: params.EffectiveDestinationGas(),
		},
		nil,
	)
	if err != nil {
		return util.HexAddress{}, err
	}

	return msgID, nil
}
