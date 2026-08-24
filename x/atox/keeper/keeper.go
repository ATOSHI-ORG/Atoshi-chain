package keeper

import (
	"fmt"

	"cosmossdk.io/log"
	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/atoshi-chain/atoshi/v20/x/atox/types"
)

// Keeper owns the atox module's KV store and the ATOX -> ATOS conversion
// accumulator.
type Keeper struct {
	cdc           codec.BinaryCodec
	storeKey      storetypes.StoreKey
	accountKeeper types.AccountKeeper
	bankKeeper    types.BankKeeper
	authority     string
	baseDenom     string // ATOS, paid out of the exchange pool
	atoxDenom     string // ATOX, the token that accrues the claim
}

func NewKeeper(
	cdc codec.BinaryCodec,
	storeKey storetypes.StoreKey,
	ak types.AccountKeeper,
	bk types.BankKeeper,
	authority string,
	baseDenom string,
	atoxDenom string,
) Keeper {
	if baseDenom == atoxDenom {
		panic(fmt.Sprintf("atox: base denom and atox denom must differ, both are %q", baseDenom))
	}
	return Keeper{
		cdc:           cdc,
		storeKey:      storeKey,
		accountKeeper: ak,
		bankKeeper:    bk,
		authority:     authority,
		baseDenom:     baseDenom,
		atoxDenom:     atoxDenom,
	}
}

func (k Keeper) Logger(ctx sdk.Context) log.Logger {
	return ctx.Logger().With("module", "x/"+types.ModuleName)
}

func (k Keeper) GetAuthority() string { return k.authority }
func (k Keeper) BaseDenom() string    { return k.baseDenom }
func (k Keeper) AtoxDenom() string    { return k.atoxDenom }

// ===== Params =====

func (k Keeper) GetParams(ctx sdk.Context) types.Params {
	bz := ctx.KVStore(k.storeKey).Get(types.KeyPrefixParams)
	if bz == nil {
		return types.DefaultParams()
	}
	var p types.Params
	if err := k.cdc.Unmarshal(bz, &p); err != nil {
		panic(fmt.Errorf("failed to unmarshal atox params: %w", err))
	}
	return p
}

func (k Keeper) SetParams(ctx sdk.Context, p types.Params) error {
	if err := p.Validate(); err != nil {
		return err
	}
	bz, err := k.cdc.Marshal(&p)
	if err != nil {
		return err
	}
	ctx.KVStore(k.storeKey).Set(types.KeyPrefixParams, bz)
	return nil
}

// ===== Global accumulator =====

func (k Keeper) GetGlobalState(ctx sdk.Context) types.GlobalState {
	bz := ctx.KVStore(k.storeKey).Get(types.KeyGlobalState)
	if bz == nil {
		return types.DefaultGlobalState()
	}
	var s types.GlobalState
	if err := k.cdc.Unmarshal(bz, &s); err != nil {
		panic(fmt.Errorf("failed to unmarshal atox global state: %w", err))
	}
	return s
}

func (k Keeper) SetGlobalState(ctx sdk.Context, s types.GlobalState) error {
	bz, err := k.cdc.Marshal(&s)
	if err != nil {
		return err
	}
	ctx.KVStore(k.storeKey).Set(types.KeyGlobalState, bz)
	return nil
}

// GlobalIndex is the cumulative ATOS-per-ATOX figure. Wallets show it as the
// current ATOX -> ATOS conversion rate.
func (k Keeper) GlobalIndex(ctx sdk.Context) math.LegacyDec {
	return k.GetGlobalState(ctx).GlobalIndex
}

// ===== Per-account settlement records =====

// GetAtoxAccount returns the settlement record for addr, or a zero-index record
// if none exists.
//
// Defaulting to index ZERO — not the current global index — is only safe because
// every path that increases an ATOX balance settles the recipient first, against
// their pre-increase balance. A first-time recipient therefore settles as
// `0 * (global_index - 0) = 0` and has their index moved up to the current value
// before the coins land, so the historical span is never paid out. Any new code
// path that credits ATOX without settling first would hand the recipient the
// entire index history.
func (k Keeper) GetAtoxAccount(ctx sdk.Context, addr sdk.AccAddress) types.AtoxAccount {
	a, _ := k.getAtoxAccount(ctx, addr)
	return a
}

// getAtoxAccount also reports whether a record was stored. Settlement needs to
// know: the first settlement of a new holder must persist a record even when
// nothing is owed yet, because the EndBlocker sweep iterates stored records and
// an unregistered holder would never be reached by automatic conversion.
func (k Keeper) getAtoxAccount(ctx sdk.Context, addr sdk.AccAddress) (types.AtoxAccount, bool) {
	bz := ctx.KVStore(k.storeKey).Get(types.AccountKey(addr.String()))
	if bz == nil {
		return types.NewAtoxAccount(addr.String(), math.LegacyZeroDec()), false
	}
	var a types.AtoxAccount
	if err := k.cdc.Unmarshal(bz, &a); err != nil {
		panic(fmt.Errorf("failed to unmarshal atox account %s: %w", addr, err))
	}
	return a, true
}

func (k Keeper) SetAtoxAccount(ctx sdk.Context, a types.AtoxAccount) error {
	bz, err := k.cdc.Marshal(&a)
	if err != nil {
		return err
	}
	ctx.KVStore(k.storeKey).Set(types.AccountKey(a.Address), bz)
	return nil
}

// IterateAccounts walks every settlement record. Used by ExportGenesis and the
// invariant check; the EndBlocker sweep uses a resumable cursor instead.
func (k Keeper) IterateAccounts(ctx sdk.Context, fn func(types.AtoxAccount) bool) {
	iter := storetypes.KVStorePrefixIterator(ctx.KVStore(k.storeKey), types.KeyPrefixAccount)
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		var a types.AtoxAccount
		if err := k.cdc.Unmarshal(iter.Value(), &a); err != nil {
			continue
		}
		if fn(a) {
			return
		}
	}
}

// ===== Sweep cursor =====

func (k Keeper) GetScanCursor(ctx sdk.Context) []byte {
	return ctx.KVStore(k.storeKey).Get(types.KeyScanCursor)
}

func (k Keeper) SetScanCursor(ctx sdk.Context, cursor []byte) {
	store := ctx.KVStore(k.storeKey)
	if len(cursor) == 0 {
		store.Delete(types.KeyScanCursor)
		return
	}
	store.Set(types.KeyScanCursor, cursor)
}

// ===== Balance helpers =====

// AtoxBalance is the live ATOX held by addr, in aatox.
func (k Keeper) AtoxBalance(ctx sdk.Context, addr sdk.AccAddress) math.Int {
	return k.bankKeeper.GetBalance(ctx, addr, k.atoxDenom).Amount
}

// AtoxSupply is the minted ATOX, which never exceeds Params.SupplyCap.
func (k Keeper) AtoxSupply(ctx sdk.Context) math.Int {
	return k.bankKeeper.GetSupply(ctx, k.atoxDenom).Amount
}

// ExchangePoolAddress is the module account backing outstanding ATOX.
func (k Keeper) ExchangePoolAddress() sdk.AccAddress {
	return k.accountKeeper.GetModuleAddress(types.ExchangePoolName)
}

// ExchangePoolBalance is the ATOS available to satisfy conversions. Anyone can
// compare it against GlobalState.TotalPending to verify solvency without
// trusting the module's running totals.
func (k Keeper) ExchangePoolBalance(ctx sdk.Context) math.Int {
	return k.bankKeeper.GetBalance(ctx, k.ExchangePoolAddress(), k.baseDenom).Amount
}

// isModuleAccount reports whether addr is a module account.
//
// Module accounts must never accrue a conversion claim. ATOX legitimately passes
// through several of them — the atox module account between MintCoins and the
// transfer out, then fee_collector and distribution while block rewards are in
// flight — and each of those holds a positive ATOX balance when the send hook
// fires. Settling them would book `balance * index` against the exchange pool
// for coins merely in transit, draining ATOS that belongs to real holders.
//
// Not crediting the in-flight span is safe in the other direction: that portion
// simply stays in the pool, so the module understates what it owes and can
// always cover it.
func (k Keeper) isModuleAccount(ctx sdk.Context, addr sdk.AccAddress) bool {
	acct := k.accountKeeper.GetAccount(ctx, addr)
	if acct == nil {
		return false
	}
	_, ok := acct.(sdk.ModuleAccountI)
	return ok
}

// EnsureExchangePoolExists is called at genesis init to fail fast if the module
// account was not registered in app.go's maccPerms.
func (k Keeper) EnsureExchangePoolExists(ctx sdk.Context) {
	if k.accountKeeper.GetModuleAccount(ctx, types.ExchangePoolName) == nil {
		panic(fmt.Errorf("module account %q is not registered in app", types.ExchangePoolName))
	}
}
