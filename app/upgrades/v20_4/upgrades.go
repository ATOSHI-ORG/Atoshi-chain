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
	"github.com/ethereum/go-ethereum/common"

	atoshitypes "github.com/atoshi-chain/atoshi/v20/types"
	bakeeper "github.com/atoshi-chain/atoshi/v20/x/bridgeadapter/keeper"
	evmtypes "github.com/atoshi-chain/atoshi/v20/x/evm/types"
	tokenomicstypes "github.com/atoshi-chain/atoshi/v20/x/tokenomics/types"
)

// ics20PrecompileAddress is precompiles/ics20.PrecompileAddress, repeated
// rather than imported so this package does not pull the precompile in.
const ics20PrecompileAddress = "0x0000000000000000000000000000000000000802"

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

// EVMKeeper is the subset of x/evm this upgrade needs.
type EVMKeeper interface {
	GetParams(ctx sdk.Context) evmtypes.Params
	SetParams(ctx sdk.Context, params evmtypes.Params) error
}

// StakingKeeper is the subset of x/staking this upgrade needs.
type StakingKeeper interface {
	GetDelegatorDelegations(ctx context.Context, delegator sdk.AccAddress, maxRetrieve uint16) ([]stakingtypes.Delegation, error)
	GetValidator(ctx context.Context, addr sdk.ValAddress) (stakingtypes.Validator, error)
	Unbond(ctx context.Context, delAddr sdk.AccAddress, valAddr sdk.ValAddress, shares math.LegacyDec) (math.Int, error)
	GetUnbondingDelegations(ctx context.Context, delegator sdk.AccAddress, maxRetrieve uint16) ([]stakingtypes.UnbondingDelegation, error)
	SetUnbondingDelegation(ctx context.Context, ubd stakingtypes.UnbondingDelegation) error
	RemoveUnbondingDelegation(ctx context.Context, ubd stakingtypes.UnbondingDelegation) error
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
	ek EVMKeeper,
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
		dropICS20Precompile(ctx, ek, logger)

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
	//
	// This must stay on the raw bank keeper. The 10% ATOX transfer fee lives in
	// ChargeInclusiveFee, which x/atox wires into the bank Msg server and app
	// module -- not into SendCoins. Routing this through the Msg server to make
	// it "look like a normal send" would burn 10% of the balance, so the user
	// would get back 900,000 of the 1,000,000.08 they are owed. They already
	// paid that fee once: the two sends that stranded this were 1,111,111.2
	// ATOX gross.
	//
	// The send restriction does run, and that is fine. It settles both ends
	// against the conversion index, and the index is still zero because no tier
	// release has ever fired -- so it books nothing and burns nothing. If a
	// release does land before the upgrade height, settlement would pay the
	// holder's accrued span in ATOS to an address nobody can sign for; the step
	// is non-fatal, so check the logs rather than assuming it went through.
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
// Three sources, in this order: liquid balance, in-flight unbonding entries,
// then bonded delegations. Unbonding comes before bonded because those are the
// funds that leave on their own -- an entry matures on its timer and lands
// spendable in the holder's account -- and because taking them moves no
// validator's voting power.
//
// What is taken is bounded by ClawbackAtos and nothing else. Measured on
// 2026-10-28 the address holds 61,244,945.01 ATOS across the three sources, so
// 17,244,945.01 stays with it -- that part predates the incident and is not
// ours to touch. This corrects an over-issue; it does not confiscate a balance.
// Delegations are visited in validator-address order so the outcome is
// identical on every node.
//
// Unbond runs the distribution hooks, so the holder's accrued staking rewards
// are paid out to them as part of this. That is correct -- they are rewards on
// stake, and the handler is not a punishment -- but it lands after the liquid
// sweep above, so the address finishes the upgrade with a small liquid balance
// rather than zero. Block rewards are denominated in ATOX, so the ATOS part of
// that is a share of transaction fees and is small.
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

	// Unbonding entries before bonded delegations, for two reasons.
	//
	// These are the funds that leave on their own. An entry completes on its
	// own timer and the balance lands in the holder's account, spendable, with
	// no further action from them -- the address already has one in flight,
	// 6,539,444.95 ATOS completing 2026-11-08. Anything the holder starts from
	// here lands 21 days later. If the upgrade is still pending when an entry
	// matures, that amount stops being recoverable by this handler at all.
	//
	// And taking from an unbonding entry costs nothing in consensus terms: the
	// stake has already left the bonded pool, so no validator's voting power
	// moves. Unbonding a bonded delegation does move it. Draining the free one
	// first means the upgrade disturbs the active set as little as the amount
	// allows.
	remaining, err = clawbackUnbonding(ctx, bk, sk, target, remaining, logger)
	if err != nil {
		return err
	}

	if !remaining.IsPositive() {
		logger.Info("clawback complete from unbonding entries alone",
			"target", ClawbackTarget)
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

// clawbackUnbonding takes what it can from the holder's in-flight unbonding
// entries and returns what is still owed.
//
// The coins behind an unbonding entry already sit in the not-bonded pool -- the
// entry is only a record of who may claim them and when -- so recovering one is
// a bookkeeping edit plus a pool-to-pool transfer, with no shares and no
// exchange rate involved.
//
// Entries are visited in (validator, creation height, unbonding id) order so
// every node takes the same ones in the same order. Partial entries are left
// behind with a reduced balance rather than removed, because the holder is
// still owed the part this clawback does not cover.
//
// InitialBalance is deliberately left alone. Only Balance governs what pays out
// at maturity; InitialBalance is the historical record of what was unbonded,
// and rewriting it would make the entry claim the clawback never happened.
func clawbackUnbonding(
	ctx sdk.Context,
	bk BankKeeper,
	sk StakingKeeper,
	target sdk.AccAddress,
	remaining math.Int,
	logger interface{ Info(string, ...any) },
) (math.Int, error) {
	ubds, err := sk.GetUnbondingDelegations(ctx, target, 100)
	if err != nil {
		return remaining, fmt.Errorf("read unbonding delegations: %w", err)
	}
	sort.Slice(ubds, func(i, j int) bool {
		return ubds[i].ValidatorAddress < ubds[j].ValidatorAddress
	})

	for _, ubd := range ubds {
		if !remaining.IsPositive() {
			break
		}

		sort.Slice(ubd.Entries, func(i, j int) bool {
			if ubd.Entries[i].CreationHeight != ubd.Entries[j].CreationHeight {
				return ubd.Entries[i].CreationHeight < ubd.Entries[j].CreationHeight
			}
			return ubd.Entries[i].UnbondingId < ubd.Entries[j].UnbondingId
		})

		taken := math.ZeroInt()
		kept := ubd.Entries[:0]
		for _, e := range ubd.Entries {
			switch {
			case !remaining.IsPositive(), !e.Balance.IsPositive():
				kept = append(kept, e)
				continue
			case e.UnbondingOnHoldRefCount > 0:
				// Another module is holding this entry open and expects the
				// balance to still be there when it releases it. Editing it
				// here would corrupt that module's accounting for a few ATOS.
				logger.Info("skipped an unbonding entry held by another module",
					"validator", ubd.ValidatorAddress,
					"unbonding_id", e.UnbondingId,
					"balance", e.Balance.String())
				kept = append(kept, e)
				continue
			}

			take := math.MinInt(e.Balance, remaining)
			e.Balance = e.Balance.Sub(take)
			remaining = remaining.Sub(take)
			taken = taken.Add(take)

			// A fully drained entry is dropped: leaving a zero-balance record
			// means the staking EndBlocker will mature it and pay out nothing,
			// which is noise in every log and query that reads it.
			if e.Balance.IsPositive() {
				kept = append(kept, e)
			}
		}

		if !taken.IsPositive() {
			continue
		}
		ubd.Entries = kept

		if len(ubd.Entries) == 0 {
			if err := sk.RemoveUnbondingDelegation(ctx, ubd); err != nil {
				return remaining, fmt.Errorf(
					"remove unbonding delegation at %s: %w", ubd.ValidatorAddress, err)
			}
		} else if err := sk.SetUnbondingDelegation(ctx, ubd); err != nil {
			return remaining, fmt.Errorf(
				"rewrite unbonding delegation at %s: %w", ubd.ValidatorAddress, err)
		}

		coins := sdk.NewCoins(sdk.NewCoin(atoshitypes.BaseDenom, taken))
		if err := bk.SendCoinsFromModuleToModule(
			ctx, stakingtypes.NotBondedPoolName,
			tokenomicstypes.MigrationPoolName, coins); err != nil {
			return remaining, fmt.Errorf(
				"return %s from the not-bonded pool: %w", taken, err)
		}

		logger.Info("clawed back from an unbonding entry",
			"validator", ubd.ValidatorAddress,
			"amount", taken.String(),
			"remaining", remaining.String())
	}

	return remaining, nil
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

// dropICS20Precompile removes 0x802 from the active static precompile set.
//
// # Why
//
// 0x802 is the ICS20 transfer precompile, and it is in DefaultStaticPrecompiles
// because that is how upstream Evmos ships. Its Transfer method calls
// transferKeeper.Transfer directly from inside EVM execution, where no Cosmos
// ante decorator runs -- so RejectAtoxIBCDecorator, which this release added to
// keep ATOX from being escrowed, never sees a transfer made this way. The denom
// is a plain string argument: "aatox" works.
//
// x/atox's send restriction now refuses any ATOX send whose recipient is an IBC
// escrow account, which closes this path and every other one at bank level.
// This removal is the second lock, not the only one. It is worth having anyway:
// the chain has no IBC counterparty and no use for an EVM-callable ICS20 route,
// so the precompile is attack surface with nothing on the other side of it.
//
// Governance can put it back with MsgUpdateParams if an IBC integration ever
// lands. The list must stay sorted -- ValidatePrecompiles enforces that for
// determinism -- and removing an entry preserves the order of the rest.
//
// Non-fatal, like the other params work here.
func dropICS20Precompile(ctx sdk.Context, ek EVMKeeper, logger interface {
	Info(string, ...any)
	Error(string, ...any)
},
) {
	if ek == nil {
		logger.Error("EVM keeper not wired; leaving the precompile set alone")
		return
	}

	p := ek.GetParams(ctx)
	kept := make([]string, 0, len(p.ActiveStaticPrecompiles))
	for _, addr := range p.ActiveStaticPrecompiles {
		if common.HexToAddress(addr) == common.HexToAddress(ics20PrecompileAddress) {
			continue
		}
		kept = append(kept, addr)
	}

	if len(kept) == len(p.ActiveStaticPrecompiles) {
		logger.Info("ICS20 precompile already inactive; nothing to do")
		return
	}

	p.ActiveStaticPrecompiles = kept
	if err := ek.SetParams(ctx, p); err != nil {
		logger.Error("could not drop the ICS20 precompile", "err", err)
		return
	}

	logger.Info("ICS20 precompile deactivated", "address", ics20PrecompileAddress)
}

// authModuleAddress derives a module account address without needing the
// account keeper threaded in for one lookup.
func authModuleAddress(name string) sdk.AccAddress {
	return authtypes.NewModuleAddress(name)
}
