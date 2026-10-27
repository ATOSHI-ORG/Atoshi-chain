package keeper

import (
	"bytes"
	"context"
	"fmt"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/bcp-innovations/hyperlane-cosmos/util"

	"github.com/atoshi-chain/atoshi/v20/x/bridgeadapter/types"
)

var _ util.HyperlaneApp = (*Keeper)(nil)

// Exists reports whether a recipient address belongs to this app. The core
// mailbox calls it before routing, so an address we do not own is rejected there
// rather than reaching Handle.
func (k Keeper) Exists(ctx context.Context, recipient util.HexAddress) (bool, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	st := k.GetReceiptState(sdkCtx)
	for _, id := range [][]byte{st.AppId, st.AssetAppId} {
		if len(id) == types.HexAddressLen && bytes.Equal(id, recipient[:]) {
			return true, nil
		}
	}
	return false, nil
}

// ReceiverIsmId pins the interchain security module used to verify messages
// addressed to this app.
//
// Pinning it in params means a later change to the mailbox default — a
// different proposal, possibly for an unrelated route — cannot silently weaken
// the verification standing between Ethereum and a tier release.
//
// An unset pin is a hard error, and the two obvious alternatives are both
// worse:
//
//   - Returning (nil, nil) panics. Upstream dereferences the pointer without a
//     nil check (hyperlane-cosmos v1.1.0, x/core/keeper/keeper.go:99), so an
//     unset pin takes the whole inbound channel down. Params.Validate does not
//     currently force IsmId when Enabled, and DefaultParams leaves it nil, so
//     this is a single mistyped proposal away.
//   - Returning ErrNoReceiverISM falls back to mailbox.DefaultIsm
//     (logic_message.go:65-67). After 2026-10-26 we know who owns the other
//     mailboxes on this chain: the attacker created 99 of them. Falling back
//     would hand verification to an ISM they control, turning a replay into
//     arbitrary forgery.
//
// So: an explicit error, distinct from both.
//
// The name is dictated by Hyperlane's app-router interface, so it keeps the
// Id spelling revive would rather have as ID.
func (k Keeper) ReceiverIsmId(ctx context.Context, recipient util.HexAddress) (*util.HexAddress, error) { //nolint:revive,stylecheck // interface method name fixed by Hyperlane
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	ok, err := k.Exists(ctx, recipient)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, types.ErrUnknownRecipient
	}

	ismID := k.GetParams(sdkCtx).IsmId
	if len(ismID) != types.HexAddressLen {
		return nil, types.ErrIsmNotConfigured
	}
	var out util.HexAddress
	copy(out[:], ismID)
	return &out, nil
}

// Handle applies a tier-release receipt.
//
// By the time this runs the mailbox has already verified the message against the
// ISM, so its origin chain is proven. What the ISM cannot prove is WHICH
// contract on that chain sent it, or that the amounts are sane — that is this
// function's job.
//
// # Provenance
//
// Three checks, and each closes a hole the others do not:
//
//  1. mailboxId == params.MailboxId. The message must have arrived through the
//     mailbox this module was configured with.
//
//     An earlier version of this function discarded the mailbox id and claimed
//     here that "the mailbox is the only caller, guaranteed by construction".
//     That was wrong, and it cost 196,284,881 ATOS on 2026-10-26.
//     MsgCreateMailbox is permissionless — no owner check, no deposit, no rate
//     limit — and x/core keys its replay protection on (mailboxId, messageId)
//     rather than on messageId alone (hyperlane-cosmos v1.1.0,
//     x/core/keeper/logic_message.go:50). An attacker created 99 mailboxes,
//     pointed each at our own pinned ISM, and resubmitted two genuine,
//     correctly signed messages through them: 493 extra deliveries, each with
//     a replay-protection key the chain had never seen, each paid out again.
//     No signature was forged and no ISM was bypassed — every delivery
//     verified honestly. The pair key is what made the same message new.
//
//     The ISM cannot close this. A Hyperlane message has seven fields
//     (util/hyperlane_message.go:24) and the mailbox is not among them, so the
//     id validators sign says nothing about which mailbox carried it. Binding
//     a message to a mailbox is the application's job; upstream does exactly
//     this in its own warp module (x/warp/keeper/keeper.go:117).
//
//     The check sits before the recipient routing switch so it covers both the
//     asset bridge and the tier release.
//  2. origin == params.EthereumDomain. Hyperlane connects dozens of chains, and
//     without this a contract on any cheap L2 could address a message here and
//     claim a release.
//  3. sender == params.TierReleaseVault. This is the one the ISM structurally
//     cannot make. Validators sign for messages from the origin chain, not for
//     particular senders, so without this any contract on Ethereum could forge a
//     release and the ISM would sign it happily.
//
// # Amounts
//
// The receipt carries CUMULATIVE totals, so the delta is what to apply. That
// makes the channel idempotent and self-healing with no replay bookkeeping: a
// duplicate yields a zero delta, a lost message is repaired by the next one, and
// a reordered message reports less than what is applied and is rejected.
//
// The totals are also cross-checked against what tier judgments authorized.
// Ethereum can only release what Atoshi authorized, so a receipt claiming more
// is a bug or a forgery either way, and releasing on it would put ATOS into the
// conversion pool with no ERC20 behind it — the one failure this module exists
// to prevent.
//
// # Ordering
//
// ATOS moves only after Ethereum has confirmed the matching ERC20 is in the
// bridge vault. That ordering is the whole point of the receipt: releasing first
// and confirming later would leave holders able to convert ATOX into ATOS that
// nothing backs.
func (k Keeper) Handle(ctx context.Context, mailboxId util.HexAddress, message util.HyperlaneMessage) error { //nolint:revive,stylecheck // parameter name fixed by Hyperlane's interface
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// Check 1. See the Provenance section above for why this is not redundant.
	// Keep it first: it is the cheapest check and the one a replay fails.
	if cfg := k.GetParams(sdkCtx).MailboxId; len(cfg) != types.HexAddressLen ||
		!bytes.Equal(cfg, mailboxId[:]) {
		return fmt.Errorf("%w: got %s", types.ErrWrongMailbox, mailboxId.String())
	}

	// Route by which recipient the message was addressed to. Keeping the two
	// channels on separate addresses means a message for one is never parsed as
	// the other, so a malformed or hostile asset transfer cannot be read as a
	// tier release — the two payloads are the same length, so nothing else would
	// distinguish them.
	st := k.GetReceiptState(sdkCtx)
	switch {
	case len(st.AssetAppId) == types.HexAddressLen &&
		bytes.Equal(st.AssetAppId, message.Recipient[:]):
		return k.handleAssetTransfer(sdkCtx, message)
	case len(st.AppId) == types.HexAddressLen &&
		bytes.Equal(st.AppId, message.Recipient[:]):
		// fall through to the tier-release path below
	default:
		return types.ErrUnknownRecipient
	}

	params := k.GetParams(sdkCtx)

	// Reject rather than drop. Hyperlane keeps an undelivered message
	// deliverable, so a paused adapter defers the release instead of losing it.
	if !params.Enabled {
		return types.ErrDisabled
	}

	if message.Origin != params.EthereumDomain {
		return fmt.Errorf("%w: got domain %d, want %d",
			types.ErrWrongOrigin, message.Origin, params.EthereumDomain)
	}
	if !params.VaultMatches(message.Sender[:]) {
		return fmt.Errorf("%w: got %s", types.ErrWrongSender, message.Sender.String())
	}

	targetBridge, targetProject, err := types.ParseReceipt(message.Body)
	if err != nil {
		return err
	}

	state := k.GetReceiptState(sdkCtx)

	// Monotonicity. A stale or replayed receipt reports a total at or below what
	// is applied; at-or-below is not an error for a duplicate, but strictly below
	// means the sequence went backwards and something is wrong upstream.
	if targetBridge.LT(state.AppliedToBridge) || targetProject.LT(state.AppliedToProject) {
		return fmt.Errorf("%w: got (%s, %s), applied (%s, %s)",
			types.ErrTargetWentBackward,
			targetBridge, targetProject, state.AppliedToBridge, state.AppliedToProject)
	}

	deltaBridge := targetBridge.Sub(state.AppliedToBridge)
	deltaProject := targetProject.Sub(state.AppliedToProject)

	// A duplicate receipt lands here with both deltas zero. Record the message id
	// for operators and return success, so Hyperlane marks it delivered instead
	// of retrying forever.
	if deltaBridge.IsZero() && deltaProject.IsZero() {
		state.LastMessageId = message.Id().Bytes()
		return k.SetReceiptState(sdkCtx, state)
	}

	atosToPool, err := types.Erc20ToAtos(deltaBridge, params.AtosPerErc20)
	if err != nil {
		return err
	}
	atosToProject, err := types.Erc20ToAtos(deltaProject, params.AtosPerErc20)
	if err != nil {
		return err
	}

	if err := k.assertWithinAuthorized(sdkCtx, targetBridge, targetProject, params.AtosPerErc20); err != nil {
		return err
	}

	if atosToPool.IsPositive() {
		if err := k.atoxKeeper.AddToExchangePool(
			sdkCtx, k.tokenomicsKeeper.MinerPoolName(), atosToPool,
		); err != nil {
			return err
		}
	}

	if atosToProject.IsPositive() {
		claimable := k.tokenomicsKeeper.GetProjectClaimable(sdkCtx)
		k.tokenomicsKeeper.SetProjectClaimable(sdkCtx, claimable.Add(atosToProject))
	}

	state.AppliedToBridge = targetBridge
	state.AppliedToProject = targetProject
	state.LastMessageId = message.Id().Bytes()
	if err := k.SetReceiptState(sdkCtx, state); err != nil {
		return err
	}

	sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeReceiptApplied,
		sdk.NewAttribute(types.AttributeKeyMessageID, message.Id().String()),
		sdk.NewAttribute(types.AttributeKeyBridgeDelta, deltaBridge.String()),
		sdk.NewAttribute(types.AttributeKeyProjectDelta, deltaProject.String()),
		sdk.NewAttribute(types.AttributeKeyAtosToPool, atosToPool.String()),
		sdk.NewAttribute(types.AttributeKeyAtosToProject, atosToProject.String()),
		sdk.NewAttribute(types.AttributeKeyCumulativeBridge, targetBridge.String()),
		sdk.NewAttribute(types.AttributeKeyCumulativeProject, targetProject.String()),
	))

	return nil
}

// assertWithinAuthorized rejects a receipt reporting more than tier judgments
// authorized.
//
// Comparison happens in ERC20 units with the authorized ATOS figure divided down
// and truncated, which rounds in the strict direction: a receipt exactly at the
// boundary of a non-round authorisation is refused rather than let through.
func (k Keeper) assertWithinAuthorized(
	ctx sdk.Context,
	targetBridge, targetProject math.Int,
	atosPerErc20 uint64,
) error {
	authMiner, authProject := k.tokenomicsKeeper.AuthorizedReleases(ctx)

	toErc20 := func(atos math.Int) math.Int {
		if atos.IsNil() || !atos.IsPositive() {
			return math.ZeroInt()
		}
		return atos.QuoRaw(int64(atosPerErc20)) //nolint:gosec // G115: Params.Validate bounds this to MaxInt64
	}

	maxBridge := toErc20(authMiner)
	maxProject := toErc20(authProject)

	if targetBridge.GT(maxBridge) {
		return fmt.Errorf("%w: bridge target %s exceeds authorized %s (ERC20)",
			types.ErrExceedsAuthorized, targetBridge, maxBridge)
	}
	if targetProject.GT(maxProject) {
		return fmt.Errorf("%w: project target %s exceeds authorized %s (ERC20)",
			types.ErrExceedsAuthorized, targetProject, maxProject)
	}
	return nil
}
