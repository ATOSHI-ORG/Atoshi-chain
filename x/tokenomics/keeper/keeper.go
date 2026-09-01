package keeper

import (
	"fmt"

	"cosmossdk.io/log"
	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/atoshi-chain/atoshi/v20/types"
	tokenomicstypes "github.com/atoshi-chain/atoshi/v20/x/tokenomics/types"
)

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
	// atoxKeeper is optional so unit tests can exercise the tier engine
	// without the ATOX module; block rewards are skipped when it is nil.
	atoxKeeper tokenomicstypes.AtoxKeeper
	// tierDispatcher sends the forward leg of a tier release to Ethereum. Set
	// after construction because x/bridgeadapter already depends on this keeper,
	// so taking it as a constructor argument would be an import cycle. Optional:
	// nil means the round trip is not wired, which is what unit tests want.
	tierDispatcher tokenomicstypes.TierDispatcher
}

// SetTierDispatcher wires the Ethereum-facing dispatcher. Called once from
// app.go after both keepers exist.
func (k *Keeper) SetTierDispatcher(d tokenomicstypes.TierDispatcher) {
	k.tierDispatcher = d
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
	xk tokenomicstypes.AtoxKeeper,
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
		atoxKeeper:       xk,
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
	if err := k.cdc.Unmarshal(bz, &params); err != nil {
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
	if err := k.cdc.Unmarshal(bz, &state); err != nil {
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
	if err := k.cdc.Unmarshal(bz, &state); err != nil {
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

// GetValidatorMinSelfDelegation returns the chain-wide floor on a validator's own
// stake, in the base denom. Zero means the requirement is disabled.
func (k Keeper) GetValidatorMinSelfDelegation(ctx sdk.Context) math.Int {
	return k.GetParams(ctx).ValidatorMinSelfDelegation
}
