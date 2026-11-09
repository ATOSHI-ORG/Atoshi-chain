package v21_2

import (
	"context"
	"fmt"

	"cosmossdk.io/math"
	upgradetypes "cosmossdk.io/x/upgrade/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"

	atoshitypes "github.com/atoshi-chain/atoshi/v20/types"
	bakeeper "github.com/atoshi-chain/atoshi/v20/x/bridgeadapter/keeper"
	tokenomicstypes "github.com/atoshi-chain/atoshi/v20/x/tokenomics/types"
)

// BankKeeper is the subset of x/bank this upgrade needs.
type BankKeeper interface {
	MintCoins(ctx context.Context, moduleName string, amt sdk.Coins) error
	SendCoinsFromModuleToAccount(ctx context.Context, senderModule string, recipient sdk.AccAddress, amt sdk.Coins) error
	GetBalance(ctx context.Context, addr sdk.AccAddress, denom string) sdk.Coin
}

// AccountKeeper is the subset of x/auth this upgrade needs.
type AccountKeeper interface {
	GetModuleAccount(ctx context.Context, moduleName string) sdk.ModuleAccountI
	GetModuleAddress(moduleName string) sdk.AccAddress
}

type logger interface {
	Info(string, ...any)
	Error(string, ...any)
}

// CreateUpgradeHandler returns the v21.2 upgrade handler.
//
// # Why nothing here is fatal
//
// An error returned from an upgrade handler aborts the upgrade, and an aborted
// upgrade means every node stops at the same height and stays stopped until a
// new binary is built, shipped and rolled out. That is worse than any step
// below failing, so each one logs at Error and the upgrade continues. Three of
// the four have a working fallback or no effect on consensus at all:
//
//	destination_gas      Params.EffectiveDestinationGas already substitutes
//	                     the same figure, so this write is for readability
//	slash_pool           created on first use by SendCoinsFromModuleToModule
//	TotalBridgedIn       statistics; nothing on chain reads it
//	compensation         funds; left to governance if it cannot run here
//
// The parameter write is the one that could plausibly fail, and not for its own
// arithmetic: SetParams validates, and this release adds a rule to Validate
// that can reject a parameter set written before it existed. A proposal landing
// between now and the upgrade height could leave state this binary refuses.
// That is a reason to log and carry on, not to take the network down -- the
// fallback keeps the gas figure correct either way.
func CreateUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator,
	ak AccountKeeper,
	bk BankKeeper,
	bak bakeeper.Keeper,
) upgradetypes.UpgradeHandler {
	return func(c context.Context, _ upgradetypes.Plan, vm module.VersionMap) (module.VersionMap, error) {
		ctx := sdk.UnwrapSDKContext(c)
		log := ctx.Logger().With("upgrade", UpgradeName)

		newVM, err := mm.RunMigrations(ctx, configurator, vm)
		if err != nil {
			return newVM, err
		}

		if err := isolate(ctx, "destination_gas", log, func(ctx sdk.Context) error {
			return setDestinationGas(ctx, bak, log)
		}); err != nil {
			log.Error("destination_gas write skipped; the parameter falls back to the same figure", "err", err)
		}

		if err := isolate(ctx, "slash_pool", log, func(ctx sdk.Context) error {
			return createSlashPool(ctx, ak, log)
		}); err != nil {
			log.Error("slash_pool instantiation skipped; it will be created on first use", "err", err)
		}

		if err := isolate(ctx, "TotalBridgedIn correction", log, func(ctx sdk.Context) error {
			return correctTotalBridgedIn(ctx, bak, log)
		}); err != nil {
			log.Error("TotalBridgedIn correction skipped", "err", err)
		}

		if err := isolate(ctx, "slashing compensation", log, func(ctx sdk.Context) error {
			return compensate(ctx, ak, bk, log)
		}); err != nil {
			log.Error("slashing compensation skipped; it remains for governance", "err", err)
		}

		return newVM, nil
	}
}

// setDestinationGas writes the gas limit declared for AtosCollateral.handle.
//
// EffectiveDestinationGas substitutes the identical figure for an unset field,
// so this changes no behaviour. It exists so the value can be read back from
// state: an operator checking why a quote came out as it did should find the
// answer in params, not in a Go constant.
func setDestinationGas(ctx sdk.Context, bak bakeeper.Keeper, log logger) error {
	p := bak.GetParams(ctx)
	if p.DestinationGas == DestinationGas {
		log.Info("destination_gas already set", "gas", DestinationGas)
		return nil
	}
	was := p.DestinationGas
	p.DestinationGas = DestinationGas
	if err := bak.SetParams(ctx, p); err != nil {
		return err
	}
	log.Info("destination_gas written", "was", was, "now", DestinationGas)
	return nil
}

// createSlashPool instantiates the module account the staking Slash override
// moves confiscated stake into.
//
// GetModuleAccount creates it if absent, which is all this does. Done here so
// the account exists from a known height rather than appearing at whatever
// block the first slash happens to land on -- a module account that materialises
// mid-life looks like something went wrong when it is read back later.
//
// It holds no permissions: no Minter, so nothing can be created in it, and no
// Burner, so a penalty cannot be destroyed -- which is the behaviour being
// replaced. The only way out is MsgSpendSlashPool, which is governance.
func createSlashPool(ctx sdk.Context, ak AccountKeeper, log logger) error {
	acc := ak.GetModuleAccount(ctx, tokenomicstypes.SlashPoolName)
	if acc == nil {
		return fmt.Errorf("slash_pool module account could not be created")
	}
	log.Info("slash_pool ready", "address", acc.GetAddress().String())
	return nil
}

// correctTotalBridgedIn subtracts the ATOS that v20.4 clawed back.
//
// The counter is append-only and v20.4's clawback handler did not decrement it,
// so net bridge-in has read 44,000,000 ATOS high ever since -- 440,000 ERC20 at
// the peg, which the solvency panel reported as a gap that does not exist.
//
// Clamped at zero rather than allowed to go negative. A negative counter would
// be a worse lie than a high one, and if the stored figure is somehow below the
// correction then the premise behind this subtraction is already wrong and the
// right move is to stop and log.
func correctTotalBridgedIn(ctx sdk.Context, bak bakeeper.Keeper, log logger) error {
	st := bak.GetReceiptState(ctx)

	correction := math.NewInt(ClawbackAtos).Mul(math.NewIntWithDecimal(1, 18))
	was := st.TotalBridgedIn
	if was.IsNil() {
		return fmt.Errorf("total_bridged_in is unset; nothing to correct")
	}
	if was.LT(correction) {
		return fmt.Errorf(
			"total_bridged_in is %s, below the %s correction; the premise of this step no longer holds",
			was, correction)
	}

	st.TotalBridgedIn = was.Sub(correction)
	if err := bak.SetReceiptState(ctx, st); err != nil {
		return err
	}
	log.Info("total_bridged_in corrected for the v20.4 clawback",
		"was", was.String(), "now", st.TotalBridgedIn.String(), "correction", correction.String())
	return nil
}

// compensate mints and pays the 2026-10-18 slashing compensation.
//
// # Why minting is the right instrument
//
// The slash destroyed 41,965,283.24 ATOS. Nothing on the Ethereum side moved,
// so the collateral behind the peg is unchanged and the chain has been
// over-collateralised by that amount ever since. Minting it back restores the
// ratio the slash broke; it does not create ATOS the vault cannot cover. That
// is the whole argument, and it is why this is not the "no new supply" rule
// being bent -- there is a hole from a burn, and this fills exactly it.
//
// The ATOS is minted into the tokenomics module account, which holds Minter.
// Deliberately not the migration pool, which also holds Minter: that pool's
// balance is the sole input to the solvency derivation, so paying out of it
// would register as 4 million ERC20 of phantom bridge-in and widen the reported
// gap by the amount this is meant to settle.
//
// Mainnet only -- see MainnetChainID.
func compensate(ctx sdk.Context, ak AccountKeeper, bk BankKeeper, log logger) error {
	if !ShouldCompensate(ctx.ChainID()) {
		log.Info("slashing compensation skipped: not the mainnet chain id",
			"chain_id", ctx.ChainID(), "expected", MainnetChainID)
		return nil
	}

	payouts, total, err := LoadPayouts()
	if err != nil {
		return err
	}

	module := tokenomicstypes.ModuleName
	moduleAddr := ak.GetModuleAddress(module)
	if moduleAddr == nil {
		return fmt.Errorf("module account %s does not exist", module)
	}
	before := bk.GetBalance(ctx, moduleAddr, atoshitypes.BaseDenom).Amount

	coins := sdk.NewCoins(sdk.NewCoin(atoshitypes.BaseDenom, total))
	if err := bk.MintCoins(ctx, module, coins); err != nil {
		return fmt.Errorf("minting %s failed: %w", total, err)
	}

	paid := math.ZeroInt()
	for _, p := range payouts {
		out := sdk.NewCoins(sdk.NewCoin(atoshitypes.BaseDenom, p.Amount))
		if err := bk.SendCoinsFromModuleToAccount(ctx, module, p.Address, out); err != nil {
			// Returning here unwinds the mint too: the whole step runs in a
			// cache context and only commits on success, so a failure part way
			// through leaves no new supply and no partial payout.
			return fmt.Errorf("paying %s failed after %s of %s: %w",
				p.Bech32, paid, total, err)
		}
		paid = paid.Add(p.Amount)
	}

	// The minted amount and the paid amount are computed from the same list, so
	// a mismatch means an arithmetic error rather than a chain-state surprise.
	// Checked anyway, because what is left behind would be unbacked supply
	// sitting in a module account with Minter, and nothing downstream would
	// flag it.
	after := bk.GetBalance(ctx, moduleAddr, atoshitypes.BaseDenom).Amount
	if !after.Equal(before) {
		return fmt.Errorf(
			"%s holds %s after paying out, expected the pre-upgrade %s: %s was minted and %s paid",
			module, after, before, total, paid)
	}

	log.Info("slashing compensation paid",
		"recipients", len(payouts),
		"minted", total.String(),
		"include_own_self_delegations", CompensateOwnSelfDelegations)
	return nil
}

// isolate runs a step in a cache context: all-or-nothing, and a panic inside it
// cannot take the chain down.
//
// Copied from v20.4 rather than shared, so neither release can change the
// other's failure behaviour by editing a common helper. The commit on success
// is what makes a partial payout impossible in compensate.
func isolate(ctx sdk.Context, name string, log logger, fn func(sdk.Context) error) (err error) {
	cacheCtx, commit := ctx.CacheContext()

	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%s panicked: %v", name, r)
			log.Error("recovered from a panic; the chain continues",
				"step", name, "panic", fmt.Sprintf("%v", r))
		}
	}()

	if err := fn(cacheCtx); err != nil {
		return err
	}

	commit()
	return nil
}
