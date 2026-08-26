package keeper

import (
	"fmt"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/bcp-innovations/hyperlane-cosmos/util"

	"github.com/atoshi-chain/atoshi/v20/x/bridgeadapter/types"
)

// ===== rate-limit state =====

// GetRateLimitState returns today's global usage. A stored day that is not today
// reads as zero, which is how the counters roll over without an EndBlocker.
func (k Keeper) GetRateLimitState(ctx sdk.Context) types.RateLimitState {
	today := types.DayOf(ctx.BlockTime().Unix())

	bz := ctx.KVStore(k.storeKey).Get(types.KeyRateLimit)
	if bz == nil {
		return types.RateLimitState{Day: today, Used: math.ZeroInt(), UsedLarge: math.ZeroInt()}
	}
	var s types.RateLimitState
	if err := k.cdc.Unmarshal(bz, &s); err != nil {
		panic(fmt.Errorf("failed to unmarshal bridge rate limit state: %w", err))
	}
	if s.Day != today {
		return types.RateLimitState{Day: today, Used: math.ZeroInt(), UsedLarge: math.ZeroInt()}
	}
	return s
}

func (k Keeper) setRateLimitState(ctx sdk.Context, s types.RateLimitState) error {
	bz, err := k.cdc.Marshal(&s)
	if err != nil {
		return err
	}
	ctx.KVStore(k.storeKey).Set(types.KeyRateLimit, bz)
	return nil
}

// GetAddressUsage returns one address's usage today, zero if the record belongs
// to an earlier day.
func (k Keeper) GetAddressUsage(ctx sdk.Context, addr string) math.Int {
	today := types.DayOf(ctx.BlockTime().Unix())

	bz := ctx.KVStore(k.storeKey).Get(types.AddressUsageKey(addr))
	if bz == nil {
		return math.ZeroInt()
	}
	var u types.AddressUsage
	if err := k.cdc.Unmarshal(bz, &u); err != nil {
		panic(fmt.Errorf("failed to unmarshal bridge address usage: %w", err))
	}
	if u.Day != today || u.Used.IsNil() {
		return math.ZeroInt()
	}
	return u.Used
}

func (k Keeper) setAddressUsage(ctx sdk.Context, addr string, used math.Int) error {
	u := types.AddressUsage{Day: types.DayOf(ctx.BlockTime().Unix()), Used: used}
	bz, err := k.cdc.Marshal(&u)
	if err != nil {
		return err
	}
	ctx.KVStore(k.storeKey).Set(types.AddressUsageKey(addr), bz)
	return nil
}

// Limits resolves the effective caps for the current block.
func (k Keeper) Limits(ctx sdk.Context) types.Limits {
	return types.ResolveLimits(
		k.GetParams(ctx),
		k.tokenomicsKeeper.MigrationPoolBalance(ctx),
		k.tokenomicsKeeper.MigrationPoolTotal(ctx),
	)
}

// ===== outbound =====

// ExecuteBridgeOut locks ATOS in the migration pool and dispatches a Hyperlane message
// so Ethereum releases the matching ERC20.
//
// The ATOS is locked, not burned: neither side of this bridge can mint, so the
// pool IS the counterparty that funds inbound transfers. Burning here would make
// the bridge one-way after the pool ran dry.
func (k Keeper) ExecuteBridgeOut(
	ctx sdk.Context,
	sender sdk.AccAddress,
	recipient []byte,
	amount math.Int,
	maxFee sdk.Coins,
) (util.HexAddress, math.Int, error) {
	params := k.GetParams(ctx)
	if !params.BridgeEnabled {
		return util.HexAddress{}, math.ZeroInt(), types.ErrBridgeDisabled
	}
	if len(params.MailboxId) != types.HexAddressLen || len(params.RemoteBridgeVault) != types.HexAddressLen {
		return util.HexAddress{}, math.ZeroInt(), fmt.Errorf(
			"%w: mailbox_id and remote_bridge_vault must both be set", types.ErrNotConfigured)
	}
	if len(recipient) != types.HexAddressLen {
		return util.HexAddress{}, math.ZeroInt(), fmt.Errorf(
			"%w: recipient must be %d bytes", types.ErrInvalidPayload, types.HexAddressLen)
	}

	// Convert before charging anything. A non-multiple of the peg is refused
	// rather than truncated: truncating would lock the full ATOS here while asking
	// Ethereum for less, quietly confiscating the difference.
	erc20Amount, err := types.AtosToErc20(amount, params.AtosPerErc20)
	if err != nil {
		return util.HexAddress{}, math.ZeroInt(), err
	}

	limits := k.Limits(ctx)
	rl := k.GetRateLimitState(ctx)
	addrUsed := k.GetAddressUsage(ctx, sender.String())
	if err := types.CheckOutbound(limits, amount, rl.Used, rl.UsedLarge, addrUsed); err != nil {
		return util.HexAddress{}, math.ZeroInt(), err
	}

	// Lock the ATOS first. If the dispatch below fails the whole message reverts,
	// so there is no window where the coins are gone without a message — but doing
	// it in this order also means a dispatch failure cannot leave Ethereum owing
	// ERC20 for ATOS that was never locked.
	coins := sdk.NewCoins(sdk.NewCoin(k.tokenomicsKeeper.BaseDenom(), amount))
	if err := k.bankKeeper.SendCoinsFromAccountToModule(
		ctx, sender, k.tokenomicsKeeper.MigrationPoolName(), coins,
	); err != nil {
		return util.HexAddress{}, math.ZeroInt(), err
	}

	body, err := types.BuildAssetPayload(recipient, erc20Amount)
	if err != nil {
		return util.HexAddress{}, math.ZeroInt(), err
	}

	var mailboxID, vault, assetApp util.HexAddress
	copy(mailboxID[:], params.MailboxId)
	copy(vault[:], params.RemoteBridgeVault)
	state := k.GetReceiptState(ctx)
	if len(state.AssetAppId) != types.HexAddressLen {
		return util.HexAddress{}, math.ZeroInt(), types.ErrAppNotInitialized
	}
	copy(assetApp[:], state.AssetAppId)

	msgID, err := k.coreKeeper.DispatchMessage(
		ctx,
		mailboxID,
		assetApp, // sender: this app, so Ethereum can verify provenance the same way we do
		maxFee,
		params.EthereumDomain,
		vault,
		body,
		util.StandardHookMetadata{Address: sender},
		nil,
	)
	if err != nil {
		return util.HexAddress{}, math.ZeroInt(), err
	}

	// Book usage only after everything succeeded, so a rejected transfer does not
	// consume someone else's allowance.
	rl.Used = rl.Used.Add(amount)
	if !limits.IsSmall(amount) {
		rl.UsedLarge = rl.UsedLarge.Add(amount)
	}
	if err := k.setRateLimitState(ctx, rl); err != nil {
		return util.HexAddress{}, math.ZeroInt(), err
	}
	if err := k.setAddressUsage(ctx, sender.String(), addrUsed.Add(amount)); err != nil {
		return util.HexAddress{}, math.ZeroInt(), err
	}

	state.TotalBridgedOut = state.TotalBridgedOut.Add(amount)
	if err := k.SetReceiptState(ctx, state); err != nil {
		return util.HexAddress{}, math.ZeroInt(), err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeBridgeOut,
		sdk.NewAttribute(types.AttributeKeySender, sender.String()),
		sdk.NewAttribute(types.AttributeKeyAmount, amount.String()),
		sdk.NewAttribute(types.AttributeKeyErc20Amount, erc20Amount.String()),
		sdk.NewAttribute(types.AttributeKeyMessageID, msgID.String()),
	))

	return msgID, erc20Amount, nil
}

// ===== inbound =====

// handleAssetTransfer releases ATOS from the migration pool for ERC20 that
// Ethereum has already locked.
//
// There is deliberately NO rate limit here. The ERC20 is committed on the other
// side by the time this runs, so refusing a valid transfer would strand the
// user's funds rather than delay them. Inbound volume is limited at the source,
// in the Ethereum contract, where a rejection costs the sender nothing.
//
// A pool that cannot cover the transfer is the one case that does return an
// error, and that is the right behaviour rather than a partial release:
// Hyperlane keeps the message deliverable, so the transfer completes once the
// pool is topped up from the project pool. Deferring is recoverable; a partial
// release is not.
func (k Keeper) handleAssetTransfer(ctx sdk.Context, message util.HyperlaneMessage) error {
	params := k.GetParams(ctx)
	if !params.BridgeEnabled {
		return types.ErrBridgeDisabled
	}
	if message.Origin != params.EthereumDomain {
		return fmt.Errorf("%w: got domain %d, want %d",
			types.ErrWrongOrigin, message.Origin, params.EthereumDomain)
	}
	if len(params.RemoteBridgeVault) != types.HexAddressLen ||
		string(params.RemoteBridgeVault) != string(message.Sender[:]) {
		return fmt.Errorf("%w: %s is not the configured bridge vault",
			types.ErrWrongSender, message.Sender.String())
	}

	recipientBz, erc20Amount, err := types.ParseAssetPayload(message.Body)
	if err != nil {
		return err
	}
	if !erc20Amount.IsPositive() {
		return types.ErrInvalidAmount
	}

	// Hyperlane addresses are 32 bytes with a Cosmos account left-padded into the
	// low 20; anything else is a payload for a different chain's address format
	// and must not be coerced into an account here.
	recipient, err := types.CosmosAddressFromHyperlane(recipientBz)
	if err != nil {
		return err
	}

	atosAmount := types.Erc20ToAtos(erc20Amount, params.AtosPerErc20)

	available := k.tokenomicsKeeper.MigrationPoolBalance(ctx)
	if available.LT(atosAmount) {
		return fmt.Errorf("%w: need %s, pool holds %s",
			types.ErrPoolInsufficient, atosAmount, available)
	}

	coins := sdk.NewCoins(sdk.NewCoin(k.tokenomicsKeeper.BaseDenom(), atosAmount))
	if err := k.bankKeeper.SendCoinsFromModuleToAccount(
		ctx, k.tokenomicsKeeper.MigrationPoolName(), recipient, coins,
	); err != nil {
		return err
	}

	state := k.GetReceiptState(ctx)
	state.TotalBridgedIn = state.TotalBridgedIn.Add(atosAmount)
	if err := k.SetReceiptState(ctx, state); err != nil {
		return err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeBridgeIn,
		sdk.NewAttribute(types.AttributeKeyRecipient, recipient.String()),
		sdk.NewAttribute(types.AttributeKeyAmount, atosAmount.String()),
		sdk.NewAttribute(types.AttributeKeyErc20Amount, erc20Amount.String()),
		sdk.NewAttribute(types.AttributeKeyMessageID, message.Id().String()),
	))

	return nil
}
