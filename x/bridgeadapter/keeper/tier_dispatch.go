package keeper

import (
	"fmt"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/bcp-innovations/hyperlane-cosmos/util"

	"github.com/atoshi-chain/atoshi/v20/x/bridgeadapter/types"
)

// DispatchTierRelease sends the forward leg of a tier release to Ethereum.
//
// This is step 1 of the four-step round trip in the design doc §3.4:
//
//	1. Atoshi books the release and dispatches the two cumulative targets  <- here
//	2. Ethereum's TierReleaseVault divides by the peg, releases the difference
//	3. Ethereum dispatches a receipt with what it actually released
//	4. Atoshi applies the receipt, moving ATOS into the conversion pool
//
// No ATOS moves here. Step 1 only tells Ethereum how much release Atoshi's tier
// state machine has authorised in total; ATOS reaches the conversion pool only
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

	// Sender is this app's own router address, the same one Ethereum is
	// configured to accept, so the contract's sender check has something stable
	// to compare against. Receipts arrive addressed to it too.
	msgID, err := k.coreKeeper.DispatchMessage(
		ctx,
		mailboxID,
		appID,
		// The module pays no interchain gas here: tier releases are triggered by
		// the chain itself, not by a user with an account to charge, so there is
		// no fee payer. An IGP hook that demands payment therefore has to be
		// left off this route; the doc's deployment step 4 configures a plain
		// mailbox without one.
		sdk.NewCoins(),
		params.EthereumDomain,
		vault,
		body,
		util.StandardHookMetadata{},
		nil,
	)
	if err != nil {
		return util.HexAddress{}, err
	}

	return msgID, nil
}
