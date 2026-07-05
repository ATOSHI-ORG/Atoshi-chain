package keeper

import (
	"encoding/json"
	"fmt"

	"cosmossdk.io/log"
	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	gogoproto "github.com/cosmos/gogoproto/proto"

	"github.com/atoshi-chain/atoshi/v20/types"
	tokenomicstypes "github.com/atoshi-chain/atoshi/v20/x/tokenomics/types"
)

// unmarshalCompat reads a KV value written either with the module's
// binary codec (current path) or with encoding/json (legacy path
// before audit Recommendation 4). Tries proto first; falls back to
// JSON so devnet/state written before the migration still loads
// without a hard fork or genesis dump/restore.
func unmarshalCompat(cdc codec.BinaryCodec, bz []byte, pb gogoproto.Message, jsonTarget any) error {
	if err := cdc.Unmarshal(bz, pb); err == nil {
		return nil
	}
	return json.Unmarshal(bz, jsonTarget)
}

type Keeper struct {
	storeKey         storetypes.StoreKey
	cdc              codec.BinaryCodec
	authority        sdk.AccAddress
	feeCollectorName string
	accountKeeper    tokenomicstypes.AccountKeeper
	bankKeeper       tokenomicstypes.BankKeeper
	stakingKeeper    tokenomicstypes.StakingKeeper
	distrKeeper      tokenomicstypes.DistrKeeper
	oracleKeeper     tokenomicstypes.OracleKeeper
}

func NewKeeper(
	storeKey storetypes.StoreKey,
	cdc codec.BinaryCodec,
	authority sdk.AccAddress,
	feeCollectorName string,
	ak tokenomicstypes.AccountKeeper,
	bk tokenomicstypes.BankKeeper,
	sk tokenomicstypes.StakingKeeper,
	dk tokenomicstypes.DistrKeeper,
	ok tokenomicstypes.OracleKeeper,
) Keeper {
	if err := sdk.VerifyAddressFormat(authority); err != nil {
		panic(err)
	}
	return Keeper{
		storeKey:         storeKey,
		cdc:              cdc,
		authority:        authority,
		feeCollectorName: feeCollectorName,
		accountKeeper:    ak,
		bankKeeper:       bk,
		stakingKeeper:    sk,
		distrKeeper:      dk,
		oracleKeeper:     ok,
	}
}

func (k Keeper) Logger(ctx sdk.Context) log.Logger {
	return ctx.Logger().With("module", "x/"+tokenomicstypes.ModuleName)
}

func (k Keeper) GetAuthority() string {
	return k.authority.String()
}

func (k Keeper) GetParams(ctx sdk.Context) tokenomicstypes.Params {
	store := ctx.KVStore(k.storeKey)
	bz := store.Get(tokenomicstypes.KeyPrefixParams)
	if bz == nil {
		return tokenomicstypes.DefaultParams()
	}
	var params tokenomicstypes.Params
	if err := unmarshalCompat(k.cdc, bz, &params, &params); err != nil {
		panic(fmt.Errorf("failed to unmarshal tokenomics params: %w", err))
	}
	return params
}

func (k Keeper) SetParams(ctx sdk.Context, params tokenomicstypes.Params) error {
	if err := params.Validate(); err != nil {
		return err
	}
	store := ctx.KVStore(k.storeKey)
	bz, err := k.cdc.Marshal(&params)
	if err != nil {
		return err
	}
	store.Set(tokenomicstypes.KeyPrefixParams, bz)
	return nil
}

func (k Keeper) GetReleaseState(ctx sdk.Context) tokenomicstypes.ReleaseState {
	store := ctx.KVStore(k.storeKey)
	bz := store.Get(tokenomicstypes.KeyPrefixReleaseState)
	if bz == nil {
		return tokenomicstypes.DefaultReleaseState()
	}
	var state tokenomicstypes.ReleaseState
	if err := unmarshalCompat(k.cdc, bz, &state, &state); err != nil {
		panic(fmt.Errorf("failed to unmarshal release state: %w", err))
	}
	return state
}

func (k Keeper) SetReleaseState(ctx sdk.Context, state tokenomicstypes.ReleaseState) error {
	store := ctx.KVStore(k.storeKey)
	bz, err := k.cdc.Marshal(&state)
	if err != nil {
		return err
	}
	store.Set(tokenomicstypes.KeyPrefixReleaseState, bz)
	return nil
}

func (k Keeper) GetBlockRewardState(ctx sdk.Context) tokenomicstypes.BlockRewardState {
	store := ctx.KVStore(k.storeKey)
	bz := store.Get(tokenomicstypes.KeyPrefixBlockRewardState)
	if bz == nil {
		return tokenomicstypes.DefaultBlockRewardState()
	}
	var state tokenomicstypes.BlockRewardState
	if err := unmarshalCompat(k.cdc, bz, &state, &state); err != nil {
		panic(fmt.Errorf("failed to unmarshal block reward state: %w", err))
	}
	return state
}

func (k Keeper) SetBlockRewardState(ctx sdk.Context, state tokenomicstypes.BlockRewardState) error {
	store := ctx.KVStore(k.storeKey)
	bz, err := k.cdc.Marshal(&state)
	if err != nil {
		return err
	}
	store.Set(tokenomicstypes.KeyPrefixBlockRewardState, bz)
	return nil
}

func (k Keeper) GetMinerLockedBalance(ctx sdk.Context, valAddr string) tokenomicstypes.MinerLockedBalance {
	store := ctx.KVStore(k.storeKey)
	bz := store.Get(tokenomicstypes.MinerLockedKey(valAddr))
	if bz == nil {
		return tokenomicstypes.NewMinerLockedBalance(valAddr)
	}
	var bal tokenomicstypes.MinerLockedBalance
	if err := unmarshalCompat(k.cdc, bz, &bal, &bal); err != nil {
		panic(fmt.Errorf("failed to unmarshal miner locked balance: %w", err))
	}
	return bal
}

func (k Keeper) SetMinerLockedBalance(ctx sdk.Context, bal tokenomicstypes.MinerLockedBalance) error {
	store := ctx.KVStore(k.storeKey)
	bz, err := k.cdc.Marshal(&bal)
	if err != nil {
		return err
	}
	store.Set(tokenomicstypes.MinerLockedKey(bal.ValidatorAddress), bz)
	return nil
}

func (k Keeper) IterateMinerLockedBalances(ctx sdk.Context, fn func(balance tokenomicstypes.MinerLockedBalance) bool) {
	store := ctx.KVStore(k.storeKey)
	iter := storetypes.KVStorePrefixIterator(store, tokenomicstypes.KeyPrefixMinerLocked)
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		var bal tokenomicstypes.MinerLockedBalance
		if err := unmarshalCompat(k.cdc, iter.Value(), &bal, &bal); err != nil {
			continue
		}
		if fn(bal) {
			return
		}
	}
}

func (k Keeper) GetProjectClaimable(ctx sdk.Context) math.Int {
	store := ctx.KVStore(k.storeKey)
	bz := store.Get(tokenomicstypes.KeyPrefixProjectClaimable)
	if bz == nil {
		return math.ZeroInt()
	}
	var amount math.Int
	if err := amount.Unmarshal(bz); err != nil {
		panic(fmt.Errorf("failed to unmarshal project claimable: %w", err))
	}
	return amount
}

func (k Keeper) SetProjectClaimable(ctx sdk.Context, amount math.Int) {
	store := ctx.KVStore(k.storeKey)
	bz, _ := amount.Marshal()
	store.Set(tokenomicstypes.KeyPrefixProjectClaimable, bz)
}

func (k Keeper) HasMigrationClaimed(ctx sdk.Context, addr string) bool {
	store := ctx.KVStore(k.storeKey)
	return store.Has(tokenomicstypes.MigrationClaimedKey(addr))
}

func (k Keeper) SetMigrationClaimed(ctx sdk.Context, addr string) {
	store := ctx.KVStore(k.storeKey)
	store.Set(tokenomicstypes.MigrationClaimedKey(addr), []byte{1})
}

func (k Keeper) baseDenom() string {
	return types.BaseDenom
}

func (k Keeper) validatorToAccAddress(valAddr string) (sdk.AccAddress, error) {
	v, err := sdk.ValAddressFromBech32(valAddr)
	if err != nil {
		return nil, err
	}
	return sdk.AccAddress(v), nil
}

func (k Keeper) getValidatorVotingPower(ctx sdk.Context, validator stakingtypes.ValidatorI) math.Int {
	return validator.GetTokens()
}
