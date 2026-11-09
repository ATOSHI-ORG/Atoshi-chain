package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/atoshi-chain/atoshi/v20/x/bridgeadapter/types"
)

type msgServer struct {
	Keeper
}

func NewMsgServerImpl(k Keeper) types.MsgServer {
	return &msgServer{Keeper: k}
}

var _ types.MsgServer = msgServer{}

// BridgeOut is the only way ATOS leaves for Ethereum. Everything the rate
// limiter and the peg require is enforced in the keeper, so this handler just
// decodes and delegates.
func (k msgServer) BridgeOut(goCtx context.Context, msg *types.MsgBridgeOut) (*types.MsgBridgeOutResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	sender, err := sdk.AccAddressFromBech32(msg.Sender)
	if err != nil {
		return nil, err
	}

	msgID, erc20Amount, err := k.ExecuteBridgeOut(ctx, sender, msg.Recipient, msg.Amount, msg.MaxFee)
	if err != nil {
		return nil, err
	}

	return &types.MsgBridgeOutResponse{
		MessageId:   msgID.Bytes(),
		Erc20Amount: erc20Amount,
	}, nil
}

func (k msgServer) UpdateParams(goCtx context.Context, msg *types.MsgUpdateParams) (*types.MsgUpdateParamsResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	if k.GetAuthority() != msg.Authority {
		return nil, fmt.Errorf("unauthorized: expected %s, got %s", k.GetAuthority(), msg.Authority)
	}

	current := k.GetParams(ctx)

	// The peg is not a tunable. Raising it would release more ATOS than the ERC20
	// behind a confirmed receipt covers; lowering it would strand ATOS that is
	// already backed. Either way it breaks the invariant the whole round trip
	// exists to maintain, so it is rejected outright rather than left to a
	// proposal author to get right.
	if msg.Params.AtosPerErc20 != current.AtosPerErc20 {
		return nil, fmt.Errorf(
			"atos_per_erc20 is immutable (it is the ATOS/ERC20 peg): have %d, got %d",
			current.AtosPerErc20, msg.Params.AtosPerErc20)
	}

	// Retargeting the vault mid-flight would make the cumulative totals refer to
	// a different contract's history, so a lower total from the new vault would
	// look like a backwards receipt and a higher one would release against
	// amounts the old vault reported. Changing it requires disabling first, which
	// forces the operator to reconcile deliberately.
	if current.Enabled && msg.Params.Enabled &&
		len(current.TierReleaseVault) == HexAddressLenLocal &&
		!current.VaultMatches(msg.Params.TierReleaseVault) {
		return nil, fmt.Errorf(
			"cannot retarget tier_release_vault while enabled: disable the adapter first")
	}

	// Clearing a configured ISM pin is never a deliberate act. MsgUpdateParams
	// replaces the whole struct, so a proposal that just forgets the field
	// clears it -- and the pin is the only thing authenticating inbound
	// messages. Changing it to a different ISM is allowed; dropping it is not.
	if len(current.IsmId) == HexAddressLenLocal && len(msg.Params.IsmId) != HexAddressLenLocal {
		return nil, fmt.Errorf(
			"cannot clear a configured ism_id: carry it over or set a replacement")
	}

	// The pool-aware half of the stranding check in
	// types.ValidateInboundCeilings.
	//
	// Params.Validate can only compare max_transfer_in against
	// inbound_daily_cap x inbound_per_address_daily_bps, because it is a pure
	// function of Params. The allowance actually enforced is
	// min(inbound_daily_cap, pool x inbound_daily_cap_bps_of_pool) x
	// inbound_per_address_daily_bps, so the migration pool can push it below
	// what Validate saw. Here there is a keeper, so the real figure is
	// available.
	//
	// # Why a failing check can still be accepted
	//
	// The pool moves on its own: bridge-ins drain it, bridge-outs refill it,
	// and no parameter controls either. So this condition can arrive with no
	// proposal involved and already be true when a proposal arrives. Rejecting
	// unconditionally would then refuse the very proposal that fixes it, and
	// the only way out of that deadlock is a chain upgrade.
	//
	// So the test is directional, not absolute: a proposal that widens the gap
	// -- or creates one where there was none -- is refused, and one that
	// narrows a gap that already exists is accepted with an event, because
	// partial progress beats no progress. It is still wrong afterwards, and the
	// event says so.
	poolBalance := k.tokenomicsKeeper.MigrationPoolBalance(ctx)
	poolTotal := k.tokenomicsKeeper.MigrationPoolTotal(ctx)
	proposed := types.ResolveLimits(msg.Params, poolBalance, poolTotal)

	if err := types.CheckInboundCeilings(
		proposed.MaxTransferIn, proposed.InboundPerAddress, "the live migration pool",
	); err != nil {
		live := types.ResolveLimits(current, poolBalance, poolTotal)
		liveGap, liveCanStrand := inboundGap(live)
		proposedGap, _ := inboundGap(proposed)

		// !liveCanStrand is the "safe today, broken tomorrow" case: the current
		// parameters cannot strand anything, so there is no deadlock to escape
		// and no reason to accept this.
		if !liveCanStrand || proposedGap.LTE(liveGap) {
			return nil, err
		}

		k.Logger(ctx).Error(
			"accepting params that still leave inbound ceilings able to strand funds; "+
				"the gap narrowed but is not closed",
			"max_transfer_in", proposed.MaxTransferIn.String(),
			"inbound_per_address", proposed.InboundPerAddress.String(),
			"previous_gap", liveGap.String(),
			"gap", proposedGap.String(),
		)
		ctx.EventManager().EmitEvent(sdk.NewEvent(
			types.EventTypeInboundCeilingStranding,
			sdk.NewAttribute(types.AttributeKeyMaxTransferIn, proposed.MaxTransferIn.String()),
			sdk.NewAttribute(types.AttributeKeyInboundPerAddress, proposed.InboundPerAddress.String()),
			sdk.NewAttribute(types.AttributeKeyPreviousGap, liveGap.String()),
			sdk.NewAttribute(types.AttributeKeyGap, proposedGap.String()),
		))
	}

	if err := k.SetParams(ctx, msg.Params); err != nil {
		return nil, err
	}
	ctx.EventManager().EmitEvent(sdk.NewEvent(types.EventTypeUpdateParams))
	return &types.MsgUpdateParamsResponse{}, nil
}

// inboundGap reports inbound_per_address minus max_transfer_in, and whether the
// pair can strand funds at all.
//
// Negative means there is a band of amounts Ethereum accepts and this chain can
// never deliver; the more negative, the wider the band. The bool is false when
// either ceiling resolves to zero, which CheckInbound reads as "no limit" -- and
// with no per-transfer ceiling, or no per-address allowance to retry against,
// this failure mode does not exist. A false here is therefore "cannot strand",
// not "gap of zero", and the two must not be conflated: a zero gap is the
// tightest configuration that is still correct.
func inboundGap(l types.Limits) (math.Int, bool) {
	if !l.MaxTransferIn.IsPositive() || !l.InboundPerAddress.IsPositive() {
		return math.ZeroInt(), false
	}
	return l.InboundPerAddress.Sub(l.MaxTransferIn), true
}

// EmergencyHalt closes the bridge from a guardian key instead of a governance
// vote.
//
// Governance is the right owner for every other parameter and the wrong one
// for this. On 2026-10-26 the Ethereum side was paused within minutes through
// EmergencyGuard while this side waited three hours for a proposal, and the
// ledger shows the cost of that gap exactly: every ERC20 the attacker took
// left the vault before the pause, and nothing left after it.
//
// The message can only close. There is no resume counterpart -- reopening goes
// through UpdateParams, which is governance -- so a guardian key that leaks
// costs availability, which a proposal restores, and never funds, which it
// cannot. That asymmetry is what makes this safe to hold in a hot key.
//
// Halting an already-halted bridge succeeds rather than erroring. An operator
// who is not sure of the current state should send this, not go and check.
func (k msgServer) EmergencyHalt(goCtx context.Context, msg *types.MsgEmergencyHalt) (*types.MsgEmergencyHaltResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	params := k.GetParams(ctx)
	if !params.IsGuardian(msg.Guardian) {
		return nil, fmt.Errorf("%w: %s", types.ErrNotGuardian, msg.Guardian)
	}

	alreadyHalted := !params.Enabled && !params.BridgeEnabled
	params.Enabled = false
	params.BridgeEnabled = false

	// Deliberately not SetParams: Validate requires a configured ISM and vault
	// while either switch is on, and a half-configured chain must still be able
	// to shut its bridge. Turning both switches off can never make params less
	// safe, so it does not need the full check.
	if err := k.setParamsUnchecked(ctx, params); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeEmergencyHalt,
		sdk.NewAttribute(types.AttributeKeyGuardian, msg.Guardian),
		sdk.NewAttribute(types.AttributeKeyReason, msg.Reason),
	))
	k.Logger(ctx).Error("bridge halted by guardian",
		"guardian", msg.Guardian, "reason", msg.Reason, "already_halted", alreadyHalted)

	return &types.MsgEmergencyHaltResponse{}, nil
}

// HexAddressLenLocal mirrors types.HexAddressLen for readability at the call
// site above.
const HexAddressLenLocal = types.HexAddressLen
