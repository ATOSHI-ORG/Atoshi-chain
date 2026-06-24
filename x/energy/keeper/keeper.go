package keeper

import (
	"encoding/binary"
	"fmt"

	"cosmossdk.io/log"
	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/atoshi-chain/atoshi/v20/x/energy/types"
)

// Keeper owns the energy module's KV store and exposes the in-process
// API used by the AnteHandler, the bank send hook, msg_server and tests.
type Keeper struct {
	cdc             codec.BinaryCodec
	storeKey        storetypes.StoreKey
	accountKeeper   types.AccountKeeper
	bankKeeper      types.BankKeeper
	feemarketKeeper types.FeemarketKeeper // optional; nil-safe (EstimateFee falls back to InsufficientGasPrice)
	authority       string
	baseDenom       string
}

func NewKeeper(
	cdc codec.BinaryCodec,
	storeKey storetypes.StoreKey,
	ak types.AccountKeeper,
	bk types.BankKeeper,
	fk types.FeemarketKeeper,
	authority string,
	baseDenom string,
) Keeper {
	return Keeper{
		cdc:             cdc,
		storeKey:        storeKey,
		accountKeeper:   ak,
		bankKeeper:      bk,
		feemarketKeeper: fk,
		authority:       authority,
		baseDenom:       baseDenom,
	}
}

func (k Keeper) Logger(ctx sdk.Context) log.Logger {
	return ctx.Logger().With("module", "x/"+types.ModuleName)
}

func (k Keeper) GetAuthority() string { return k.authority }
func (k Keeper) BaseDenom() string    { return k.baseDenom }

// ===== Params =====

func (k Keeper) GetParams(ctx sdk.Context) types.Params {
	store := ctx.KVStore(k.storeKey)
	bz := store.Get(types.KeyPrefixParams)
	if bz == nil {
		return types.DefaultParams()
	}
	var p types.Params
	k.cdc.MustUnmarshal(bz, &p)
	return p
}

func (k Keeper) SetParams(ctx sdk.Context, p types.Params) error {
	if err := p.Validate(); err != nil {
		return err
	}
	store := ctx.KVStore(k.storeKey)
	store.Set(types.KeyPrefixParams, k.cdc.MustMarshal(&p))
	return nil
}

// ===== Account =====

// GetEnergyAccount returns the stored account or a fresh zero-value one.
// The caller is responsible for calling Settle before reading energy.
func (k Keeper) GetEnergyAccount(ctx sdk.Context, addr sdk.AccAddress) types.EnergyAccount {
	store := ctx.KVStore(k.storeKey)
	bz := store.Get(types.AccountKey(addr.String()))
	if bz == nil {
		return types.NewEnergyAccount(addr.String())
	}
	var acct types.EnergyAccount
	k.cdc.MustUnmarshal(bz, &acct)
	return acct
}

func (k Keeper) SetEnergyAccount(ctx sdk.Context, acct types.EnergyAccount) {
	store := ctx.KVStore(k.storeKey)
	store.Set(types.AccountKey(acct.Address), k.cdc.MustMarshal(&acct))
}

// IterateAccounts walks every stored energy account. Stop when fn returns true.
func (k Keeper) IterateAccounts(ctx sdk.Context, fn func(types.EnergyAccount) bool) {
	store := ctx.KVStore(k.storeKey)
	it := storetypes.KVStorePrefixIterator(store, types.KeyPrefixAccount)
	defer it.Close()
	for ; it.Valid(); it.Next() {
		var acct types.EnergyAccount
		k.cdc.MustUnmarshal(it.Value(), &acct)
		if fn(acct) {
			return
		}
	}
}

// ===== Delegation primary store =====

func (k Keeper) GetDelegation(ctx sdk.Context, id uint64) (types.EnergyDelegation, bool) {
	store := ctx.KVStore(k.storeKey)
	bz := store.Get(types.DelegationKey(id))
	if bz == nil {
		return types.EnergyDelegation{}, false
	}
	var d types.EnergyDelegation
	k.cdc.MustUnmarshal(bz, &d)
	return d, true
}

// setDelegation writes the primary record and refreshes secondary indexes.
// Called by Delegate / consume / Undelegate / EndBlocker.
func (k Keeper) setDelegation(ctx sdk.Context, d types.EnergyDelegation) {
	store := ctx.KVStore(k.storeKey)
	store.Set(types.DelegationKey(d.Id), k.cdc.MustMarshal(&d))
	store.Set(types.DelegationByExpiryKey(d.ExpiresAt, d.Id), []byte{1})
	store.Set(types.DelegationByDelegatorKey(d.Delegator, d.Id), []byte{1})
	store.Set(types.DelegationByDelegateeKey(d.Delegatee, d.Id), []byte{1})
}

// SetDelegationForTest is a public wrapper around setDelegation that
// exists ONLY so unit tests (which live in `*_test` packages and can't
// reach unexported methods) can seed delegation records directly. It
// must NOT be called from production code paths.
func (k Keeper) SetDelegationForTest(ctx sdk.Context, d types.EnergyDelegation) {
	k.setDelegation(ctx, d)
}

// removeDelegation deletes both primary record and all secondary index rows.
func (k Keeper) removeDelegation(ctx sdk.Context, d types.EnergyDelegation) {
	store := ctx.KVStore(k.storeKey)
	store.Delete(types.DelegationKey(d.Id))
	store.Delete(types.DelegationByExpiryKey(d.ExpiresAt, d.Id))
	store.Delete(types.DelegationByDelegatorKey(d.Delegator, d.Id))
	store.Delete(types.DelegationByDelegateeKey(d.Delegatee, d.Id))
}

// nextDelegationID atomically allocates a new monotonically-increasing id.
func (k Keeper) nextDelegationID(ctx sdk.Context) uint64 {
	store := ctx.KVStore(k.storeKey)
	bz := store.Get(types.KeyNextDelegationID)
	id := uint64(1)
	if bz != nil {
		id = binary.BigEndian.Uint64(bz)
	}
	next := make([]byte, 8)
	binary.BigEndian.PutUint64(next, id+1)
	store.Set(types.KeyNextDelegationID, next)
	return id
}

func (k Keeper) setNextDelegationID(ctx sdk.Context, id uint64) {
	store := ctx.KVStore(k.storeKey)
	bz := make([]byte, 8)
	binary.BigEndian.PutUint64(bz, id)
	store.Set(types.KeyNextDelegationID, bz)
}

// IterateDelegationsByDelegator walks all outbound delegations for an addr.
func (k Keeper) IterateDelegationsByDelegator(ctx sdk.Context, delegator string, fn func(types.EnergyDelegation) bool) {
	k.iterateDelegationsByIndex(ctx, types.DelegationByDelegatorKey(delegator, 0)[:1+len(delegator)+1], fn)
}

func (k Keeper) IterateDelegationsByDelegatee(ctx sdk.Context, delegatee string, fn func(types.EnergyDelegation) bool) {
	k.iterateDelegationsByIndex(ctx, types.DelegationByDelegateeKey(delegatee, 0)[:1+len(delegatee)+1], fn)
}

func (k Keeper) iterateDelegationsByIndex(ctx sdk.Context, prefix []byte, fn func(types.EnergyDelegation) bool) {
	store := ctx.KVStore(k.storeKey)
	it := storetypes.KVStorePrefixIterator(store, prefix)
	defer it.Close()
	for ; it.Valid(); it.Next() {
		// Last 8 bytes of the index key are the delegation id.
		key := it.Key()
		if len(key) < 8 {
			continue
		}
		id := binary.BigEndian.Uint64(key[len(key)-8:])
		d, ok := k.GetDelegation(ctx, id)
		if !ok {
			continue
		}
		if fn(d) {
			return
		}
	}
}

// ===== Eligible balance =====

// EligibleBalance returns bank balance minus locked ATOS. This is the
// number used for capacity / accrual computations.
//
// Locked ATOS is held in the LockedEnergyPoolName module account, but
// for accounting we keep a per-account `LockedAtos` record so we can
// short-circuit reads without touching the bank twice.
// Audit Question 2 (round2): EligibleBalance returns the holder's
// ENERGY-eligible stake — the sum that backs their TxEnergyCapacity.
// This includes both the liquid bank balance AND any ATOS the holder
// has locked into LockedEnergyPool via outbound delegations.
//
// Pre-fix the function returned bank balance only. Combined with the
// fact that Delegate transfers lockedATOS out of the user's bank
// account into the module pool, that meant a delegator's cap shrank
// immediately upon delegating — a double penalty on top of the
// DelegatedOut bookkeeping which already prevents double-spending
// the lent energy. The audit's hint: locked ATOS is still part of
// the delegator's economic stake (recoverable via Undelegate), so it
// should count toward their capacity ceiling. The DelegatedOut
// counter handles the "you can't spend energy you already lent out"
// part; cap-down should not also penalize the delegator for the
// existence of the locked ATOS.
//
// Net effect: Delegate is now cap-neutral. Bank balance drops by
// lockedATOS, LockedAtos counter rises by lockedATOS, sum unchanged.
// Holder transfers to/from other users still move the eligible
// balance normally (LockedAtos doesn't change on bank sends to other
// holders).
func (k Keeper) EligibleBalance(ctx sdk.Context, addr sdk.AccAddress) math.Int {
	bal := k.bankKeeper.GetBalance(ctx, addr, k.baseDenom).Amount
	acct := k.GetEnergyAccount(ctx, addr)
	if acct.LockedAtos.IsNil() || !acct.LockedAtos.IsPositive() {
		return bal
	}
	return bal.Add(acct.LockedAtos)
}

// EnsureLockedPoolExists is called once at genesis init; the SDK auth
// module account auto-creates if registered, but we sanity-check.
func (k Keeper) EnsureLockedPoolExists(ctx sdk.Context) {
	if k.accountKeeper.GetModuleAccount(ctx, types.LockedEnergyPoolName) == nil {
		panic(fmt.Errorf("module account %q is not registered in app", types.LockedEnergyPoolName))
	}
}