package v20_4

import (
	"context"
	"fmt"
	"sort"

	"cosmossdk.io/math"
	upgradetypes "cosmossdk.io/x/upgrade/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	icahosttypes "github.com/cosmos/ibc-go/v8/modules/apps/27-interchain-accounts/host/types"

	atoshitypes "github.com/atoshi-chain/atoshi/v20/types"
	bakeeper "github.com/atoshi-chain/atoshi/v20/x/bridgeadapter/keeper"
	tokenomicstypes "github.com/atoshi-chain/atoshi/v20/x/tokenomics/types"
)

// BankKeeper is the subset of x/bank this upgrade needs.
type BankKeeper interface {
	GetBalance(ctx context.Context, addr sdk.AccAddress, denom string) sdk.Coin
	SendCoins(ctx context.Context, from, to sdk.AccAddress, amt sdk.Coins) error
	SendCoinsFromModuleToModule(ctx context.Context, from, to string, amt sdk.Coins) error
}

// ICAHostKeeper is the subset of the interchain-accounts host this upgrade
// needs. Declared as an interface so this package does not pull the module in.
type ICAHostKeeper interface {
	GetParams(ctx sdk.Context) icahosttypes.Params
	SetParams(ctx sdk.Context, params icahosttypes.Params)
}

// StakingKeeper is the subset of x/staking this upgrade needs.
type StakingKeeper interface {
	GetDelegatorDelegations(ctx context.Context, delegator sdk.AccAddress, maxRetrieve uint16) ([]stakingtypes.Delegation, error)
	GetValidator(ctx context.Context, addr sdk.ValAddress) (stakingtypes.Validator, error)
	Unbond(ctx context.Context, delAddr sdk.AccAddress, valAddr sdk.ValAddress, shares math.LegacyDec) (math.Int, error)
}

// CreateUpgradeHandler returns the v20.4 upgrade handler.
//
// # Why two of the three steps cannot fail the upgrade
//
// An error returned from here aborts the upgrade, and an aborted upgrade means
// the chain does not start. Every node stops at the same height and stays
// stopped until a new binary is built, shipped and rolled out -- a far worse
// outcome than any of the three steps below failing.
//
// So only the parameter write is fatal: it is pure arithmetic on data this
// binary already has, and if it fails something is wrong that a restart will
// not fix. The two steps that move funds depend on chain state that may have
// changed since this was written -- the holder may have been emptied, the
// delegations may have been withdrawn -- and a mismatch there is a reason to
// stop and look, not a reason to take the network down. They log at Error and
// carry on, leaving the work to be done by governance afterwards.
func CreateUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator,
	bk BankKeeper,
	sk StakingKeeper,
	bak bakeeper.Keeper,
	icak ICAHostKeeper,
) upgradetypes.UpgradeHandler {
	return func(c context.Context, _ upgradetypes.Plan, vm module.VersionMap) (module.VersionMap, error) {
		ctx := sdk.UnwrapSDKContext(c)
		logger := ctx.Logger().With("upgrade", UpgradeName)

		newVM, err := mm.RunMigrations(ctx, configurator, vm)
		if err != nil {
			return newVM, err
		}

		if err := retuneRateLimits(ctx, bak, logger); err != nil {
			return newVM, err
		}

		closeICAHost(ctx, icak, logger)

		if err := recoverStuckAtox(ctx, bk, logger); err != nil {
			logger.Error("stuck ATOX recovery skipped", "err", err)
		}

		if err := clawback(ctx, bk, sk, logger); err != nil {
			logger.Error("clawback skipped", "err", err)
		}

		return newVM, nil
	}
}

// retuneRateLimits writes the limits derived from 2026-10-17..26 traffic and
// sets the four fields this release adds, which unmarshal as zero on a params
// record written before they existed.
//
// Every figure is a multiple of observed peak, not a guess:
//
//	inbound daily      600,000,000 ATOS   1.35x the 445,797,500 peak day
//	inbound per-addr   16.70% of that     just above the 95,660,000 single
//	                                      -recipient-day high water mark
//	outbound daily     100,000,000 ATOS   4x the 24,751,797 peak day
//	outbound per-addr  30% of that        above the 24,152,699 legitimate high
//
// The per-transfer ceilings sit above every legitimate transfer in that window
// and below the largest the attacker moved. The per-address bps are sized so
// no address seen in those ten days would have been refused.
func retuneRateLimits(ctx sdk.Context, bak bakeeper.Keeper, logger interface {
	Info(string, ...any)
},
) error {
	p := bak.GetParams(ctx)

	atos := func(n int64) math.Int {
		return math.NewInt(n).Mul(math.NewIntWithDecimal(1, 18))
	}

	p.InboundDailyCap = atos(600_000_000)
	p.InboundDailyCapBpsOfPool = 30 // 0.3%
	p.InboundPerAddressDailyBps = 1670
	p.MaxTransferIn = atos(100_000_000)
	p.InboundSmallThreshold = atos(1_000_000)

	p.GlobalDailyCap = atos(100_000_000)
	p.GlobalDailyCapBpsOfPool = 4 // 0.04%
	p.PerAddressDailyBps = 3000   // 30% of the resolved global
	p.MaxTransferOut = atos(60_000_000)

	// Left alone deliberately: min_transfer_out, small_transfer_threshold,
	// small_quota_bps and crisis_pool_bps are all still right. crisis_pool_bps
	// keeps its value but now gates inbound rather than outbound -- that is a
	// code change in this release, not a parameter change.

	if err := bak.SetParams(ctx, p); err != nil {
		return fmt.Errorf("retune rate limits: %w", err)
	}

	logger.Info("rate limits retuned",
		"inbound_daily_cap", p.InboundDailyCap,
		"inbound_per_address_bps", p.InboundPerAddressDailyBps,
		"max_transfer_in", p.MaxTransferIn,
		"global_daily_cap", p.GlobalDailyCap,
		"max_transfer_out", p.MaxTransferOut)
	return nil
}

// recoverStuckAtox moves the ATOX sent to the ATOX contract's own address.
//
// The address is a BaseAccount with no key and the contract has no withdraw
// path, so the funds are otherwise permanent. A one-off here is the right
// shape for this: a standing recovery message would be a governance-gated way
// to take funds from any address, which is a much larger thing to own than
// one mistyped transfer.
func recoverStuckAtox(ctx sdk.Context, bk BankKeeper, logger interface {
	Info(string, ...any)
},
) error {
	if StuckAtoxRecipient == "" {
		return fmt.Errorf("no recipient configured; set StuckAtoxRecipient before the upgrade height")
	}

	from, err := sdk.AccAddressFromBech32(StuckAtoxHolder)
	if err != nil {
		return fmt.Errorf("holder address: %w", err)
	}
	to, err := sdk.AccAddressFromBech32(StuckAtoxRecipient)
	if err != nil {
		return fmt.Errorf("recipient address: %w", err)
	}

	bal := bk.GetBalance(ctx, from, StuckAtoxDenom)
	if !bal.Amount.IsPositive() {
		return fmt.Errorf("holder has no %s to recover", StuckAtoxDenom)
	}

	// Send whatever is actually there rather than a hardcoded figure. The
	// balance has drifted by a rounding-sized amount since it was recorded,
	// and a fixed number would either leave dust or fail outright.
	if err := bk.SendCoins(ctx, from, to, sdk.NewCoins(bal)); err != nil {
		return fmt.Errorf("send %s: %w", bal, err)
	}

	logger.Info("recovered stuck ATOX", "amount", bal.String(), "to", StuckAtoxRecipient)
	return nil
}

// clawback removes the over-issued ATOS that is still on this chain.
//
// The holder delegated all of it, so there is no balance to debit: the funds
// have to come out of staking first. Shares are unbonded directly rather than
// through Undelegate, which would start a 21-day timer and leave the amount
// claimable by the same address at the end of it.
//
// What is taken is bounded by ClawbackAtos and nothing else. The address holds
// roughly 10.7 million ATOS of its own that predates the incident, and that is
// not ours to touch -- this corrects an over-issue, it does not confiscate a
// balance. Delegations are visited in validator-address order so the outcome
// is identical on every node.
func clawback(ctx sdk.Context, bk BankKeeper, sk StakingKeeper, logger interface {
	Info(string, ...any)
},
) error {
	target, err := sdk.AccAddressFromBech32(ClawbackTarget)
	if err != nil {
		return fmt.Errorf("target address: %w", err)
	}

	remaining := math.NewInt(ClawbackAtos).Mul(math.NewIntWithDecimal(1, 18))

	// Liquid balance first: cheapest, and leaves staking untouched when the
	// holder has since undelegated on their own.
	if bal := bk.GetBalance(ctx, target, atoshitypes.BaseDenom); bal.Amount.IsPositive() {
		take := math.MinInt(bal.Amount, remaining)
		coins := sdk.NewCoins(sdk.NewCoin(atoshitypes.BaseDenom, take))
		if err := bk.SendCoins(ctx, target,
			authModuleAddress(tokenomicstypes.MigrationPoolName), coins); err != nil {
			return fmt.Errorf("claw back liquid balance: %w", err)
		}
		remaining = remaining.Sub(take)
		logger.Info("clawed back liquid balance", "amount", take.String())
	}

	if !remaining.IsPositive() {
		return nil
	}

	dels, err := sk.GetDelegatorDelegations(ctx, target, 100)
	if err != nil {
		return fmt.Errorf("read delegations: %w", err)
	}
	sort.Slice(dels, func(i, j int) bool {
		return dels[i].ValidatorAddress < dels[j].ValidatorAddress
	})

	for _, d := range dels {
		if !remaining.IsPositive() {
			break
		}
		valAddr, err := sdk.ValAddressFromBech32(d.ValidatorAddress)
		if err != nil {
			return fmt.Errorf("validator address %s: %w", d.ValidatorAddress, err)
		}
		val, err := sk.GetValidator(ctx, valAddr)
		if err != nil {
			return fmt.Errorf("validator %s: %w", d.ValidatorAddress, err)
		}

		// Convert what is still owed into shares at this validator's current
		// exchange rate, capped by what the delegation actually holds. Slashing
		// moves that rate, so tokens and shares are not interchangeable.
		want, err := val.SharesFromTokensTruncated(remaining)
		if err != nil {
			return fmt.Errorf("shares for %s: %w", d.ValidatorAddress, err)
		}
		shares := d.Shares
		if want.LT(shares) {
			shares = want
		}
		if !shares.IsPositive() {
			continue
		}

		returned, err := sk.Unbond(ctx, target, valAddr, shares)
		if err != nil {
			return fmt.Errorf("unbond from %s: %w", d.ValidatorAddress, err)
		}
		if !returned.IsPositive() {
			continue
		}

		// Unbond removes the shares but leaves the coins in whichever pool
		// backs this validator. Undelegate would move them to the not-bonded
		// pool and start a timer; here they go straight to the migration pool,
		// which is where they came from.
		source := stakingtypes.NotBondedPoolName
		if val.IsBonded() {
			source = stakingtypes.BondedPoolName
		}
		coins := sdk.NewCoins(sdk.NewCoin(atoshitypes.BaseDenom, returned))
		if err := bk.SendCoinsFromModuleToModule(
			ctx, source, tokenomicstypes.MigrationPoolName, coins); err != nil {
			return fmt.Errorf("return %s to the migration pool: %w", returned, err)
		}

		remaining = remaining.Sub(returned)
		logger.Info("clawed back from delegation",
			"validator", d.ValidatorAddress,
			"amount", returned.String(),
			"remaining", remaining.String())
	}

	if remaining.IsPositive() {
		return fmt.Errorf(
			"recovered all that was reachable; %s still outstanding", remaining)
	}

	logger.Info("clawback complete",
		"target", ClawbackTarget,
		"atos", math.NewInt(ClawbackAtos).String())
	return nil
}

// closeICAHost switches off the interchain-accounts host.
//
// # Why this belongs in the bridge-incident release
//
// Mainnet runs the host with host_enabled true and allow_messages ["*"]. The
// host executes a counterparty chain's messages by handing them straight to the
// Msg router -- it is not a transaction, so not one ante decorator runs. Every
// guard this chain enforces in the ante chain is therefore unenforced on that
// path, including the two that matter most here:
//
//	RejectAtoxIBCDecorator, added in this release, which stops ATOX reaching an
//	IBC escrow where the transfer burn and the conversion claim make the
//	escrowed balance unbacked and unclaimable. An ICA could send exactly that
//	MsgTransfer.
//
//	RejectMessagesDecorator, which keeps MsgEthereumTx out of Cosmos
//	transactions. With "*" the host will route one.
//
// No counterparty is affected: the chain has zero open channels, so no ICA
// exists and none is in flight. Nothing that works today stops working.
//
// The host is left registered and its state intact, so this is a parameter
// flip, not a removal. Governance turns it back on with MsgUpdateParams when
// there is an actual ICA counterparty -- and when it does, allow_messages must
// be a named list that excludes MsgTransfer and MsgEthereumTx, never "*" again.
//
// Non-fatal, like the fund movements: a failure to read or write one parameter
// is not a reason to stop the network starting.
func closeICAHost(ctx sdk.Context, icak ICAHostKeeper, logger interface {
	Info(string, ...any)
	Error(string, ...any)
},
) {
	if icak == nil {
		logger.Error("ICA host not wired; leaving its params alone")
		return
	}

	before := icak.GetParams(ctx)
	if !before.HostEnabled && len(before.AllowMessages) == 0 {
		logger.Info("ICA host already closed; nothing to do")
		return
	}

	icak.SetParams(ctx, icahosttypes.NewParams(false, nil))

	logger.Info("ICA host closed",
		"was_enabled", before.HostEnabled,
		"was_allow_messages", fmt.Sprintf("%v", before.AllowMessages))
}

// authModuleAddress derives a module account address without needing the
// account keeper threaded in for one lookup.
func authModuleAddress(name string) sdk.AccAddress {
	return authtypes.NewModuleAddress(name)
}
