// Copyright Tharsis Labs Ltd.(Evmos)
// SPDX-License-Identifier:ENCL-1.0(https://github.com/evmos/evmos/blob/main/LICENSE)

package keeper

import (
	addresscodec "cosmossdk.io/core/address"
	storetypes "cosmossdk.io/core/store"
	"cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
	"github.com/cosmos/cosmos-sdk/x/staking/types"
)

// Keeper is a wrapper around the Cosmos SDK staking keeper.
type Keeper struct {
	*stakingkeeper.Keeper
	ak types.AccountKeeper
	bk types.BankKeeper

	// minSelfDelegation is shared by pointer so that every copy of the keeper
	// (precompiles hold one by value) sees the source set after construction.
	minSelfDelegation *minSelfDelegationSource
}

// minSelfDelegationSource holds the chain-wide validator self-stake floor
// getter. It lives in x/tokenomics, which is built after x/staking.
type minSelfDelegationSource struct {
	get func(ctx sdk.Context) math.Int
}

// SetMinSelfDelegationSource wires the chain-wide validator self-stake floor
// into the msg server. Until it is set, no floor is applied.
func (k *Keeper) SetMinSelfDelegationSource(get func(ctx sdk.Context) math.Int) {
	k.minSelfDelegation.get = get
}

// minSelfDelegationFloor returns the floor, or a nil Int when none applies.
func (k Keeper) minSelfDelegationFloor(ctx sdk.Context) math.Int {
	if k.minSelfDelegation == nil || k.minSelfDelegation.get == nil {
		return math.Int{}
	}
	return k.minSelfDelegation.get(ctx)
}

// NewKeeper creates a new staking Keeper wrapper instance.
func NewKeeper(
	cdc codec.BinaryCodec,
	storeService storetypes.KVStoreService,
	ak types.AccountKeeper,
	bk types.BankKeeper,
	authority string,
	validatorAddressCodec addresscodec.Codec,
	consensusAddressCodec addresscodec.Codec,
) *Keeper {
	return &Keeper{
		stakingkeeper.NewKeeper(cdc, storeService, ak, bk, authority, validatorAddressCodec, consensusAddressCodec),
		ak,
		bk,
		&minSelfDelegationSource{},
	}
}
