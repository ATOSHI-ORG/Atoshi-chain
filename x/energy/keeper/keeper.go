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
	stakingKeeper   types.StakingKeeper   // optional; nil-safe (EligibleBalance then omits staked ATOS)
	feemarketKeeper types.FeemarketKeeper // optional; nil-safe (EstimateFee falls back to InsufficientGasPrice)
	authority       string
	// baseDenomFn resolves the denom energy is measured in. It is a getter, not
	// a captured string, because the value it must agree with -- the EVM fee
	// denom -- lives in a global that is only populated once the app's chain id
	// is known. NewAtoshi is also constructed with that global deliberately
	// unset: cmd/atoshid's NewRootCmd builds a throwaway app with
	// NoOpAtoshiOptions purely to read the encoding config, so reading the
	// global at construction time panicked on every atoshid invocation.
	// Deferring the read to first use keeps the two denoms equal by
	// construction without depending on keeper wiring order.
	baseDenomFn func() string
}

func NewKeeper(
	cdc codec.BinaryCodec,
	storeKey storetypes.StoreKey,
	ak types.AccountKeeper,
	bk types.BankKeeper,
	sk types.StakingKeeper,
	fk types.FeemarketKeeper,
	authority string,
	baseDenomFn func() string,
) Keeper {
	if baseDenomFn == nil {
		panic("energy keeper: baseDenomFn must not be nil")
	}
	return Keeper{
		cdc:             cdc,
		storeKey:        storeKey,
		accountKeeper:   ak,
		bankKeeper:      bk,
		stakingKeeper:   sk,
		feemarketKeeper: fk,
		authority:       authority,
		baseDenomFn:     baseDenomFn,
	}
}

func (k Keeper) Logger(ctx sdk.Context) log.Logger {
	return ctx.Logger().With("module", "x/"+types.ModuleName)
}

func (k Keeper) GetAuthority() string { return k.authority }
func (k Keeper) BaseDenom() string    { return k.baseDenomFn() }

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

// EligibleBalance returns the ATOS backing a holder's TxEnergyCapacity:
//
//	bank balance + LockedAtos + bonded + unbonding
//
// The principle behind all four terms is that energy is earned by OWNING ATOS,
// not by leaving it liquid. Every mechanism that "removes" ATOS from the bank
// balance below merely parks it in a module account the holder can recover from,
// so none of them should reduce capacity.
//
// LockedAtos (audit Question 2, round2) is ATOS moved into LockedEnergyPool by
// an outbound energy delegation. Counting only the bank balance made Delegate
// shrink the delegator's cap — a second penalty on top of DelegatedOut, which
// already stops them spending energy they lent out. Including it makes Delegate
// cap-neutral: the bank balance falls and the counter rises by the same amount.
//
// bonded / unbonding are the staking equivalents. Delegate moves coins to
// bonded_tokens_pool and Undelegate parks them in not_bonded_tokens_pool for the
// unbonding period; both remain the delegator's. Omitting bonded would zero the
// energy of anyone staking to mine ATOX, forcing a choice between mining and
// free transfers. Omitting unbonding would do the same for the 21 days after
// they start undelegating.
//
// The four terms are disjoint — a given coin sits in exactly one of the bank
// balance, LockedEnergyPool, bonded_tokens_pool or not_bonded_tokens_pool — so
// nothing is double-counted.
//
// A staking read failure means a corrupt delegation record. We log and omit that
// term rather than panic: panicking here would halt the chain from inside a bank
// SendRestriction, whereas omitting only understates capacity, which can never
// hand out gas that was not earned. Reads are deterministic, so every node
// computes the same value either way and there is no fork risk.
func (k Keeper) EligibleBalance(ctx sdk.Context, addr sdk.AccAddress) math.Int {
	total := k.bankKeeper.GetBalance(ctx, addr, k.BaseDenom()).Amount

	acct := k.GetEnergyAccount(ctx, addr)
	if !acct.LockedAtos.IsNil() && acct.LockedAtos.IsPositive() {
		total = total.Add(acct.LockedAtos)
	}

	return total.Add(k.stakedAtos(ctx, addr))
}

// stakedAtos returns bonded + unbonding ATOS for addr, or zero when no staking
// keeper is wired (unit tests that exercise energy in isolation).
func (k Keeper) stakedAtos(ctx sdk.Context, addr sdk.AccAddress) math.Int {
	if k.stakingKeeper == nil {
		return math.ZeroInt()
	}

	staked := math.ZeroInt()

	bonded, err := k.stakingKeeper.GetDelegatorBonded(ctx, addr)
	switch {
	case err != nil:
		k.Logger(ctx).Error("failed to read bonded delegations for energy eligibility",
			"address", addr.String(), "err", err)
	case !bonded.IsNil() && bonded.IsPositive():
		staked = staked.Add(bonded)
	}

	unbonding, err := k.stakingKeeper.GetDelegatorUnbonding(ctx, addr)
	switch {
	case err != nil:
		k.Logger(ctx).Error("failed to read unbonding delegations for energy eligibility",
			"address", addr.String(), "err", err)
	case !unbonding.IsNil() && unbonding.IsPositive():
		staked = staked.Add(unbonding)
	}

	return staked
}

// EnsureLockedPoolExists is called once at genesis init; the SDK auth
// module account auto-creates if registered, but we sanity-check.
func (k Keeper) EnsureLockedPoolExists(ctx sdk.Context) {
	if k.accountKeeper.GetModuleAccount(ctx, types.LockedEnergyPoolName) == nil {
		panic(fmt.Errorf("module account %q is not registered in app", types.LockedEnergyPoolName))
	}
}
