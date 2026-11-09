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

// CommissionCeilingKeeper is the subset of x/tokenomics this keeper reads.
//
// Declared here as an interface rather than imported so x/staking does not
// depend on x/tokenomics, which already depends on staking's types.
type CommissionCeilingKeeper interface {
	GetMaxCommissionRate(ctx sdk.Context) math.LegacyDec
}

// Keeper is a wrapper around the Cosmos SDK staking keeper.
type Keeper struct {
	*stakingkeeper.Keeper
	ak types.AccountKeeper
	bk types.BankKeeper

	// ck supplies the chain-wide commission ceiling. Set after construction
	// rather than passed in, because app.go builds this keeper before
	// x/tokenomics and hands this keeper to it -- the dependency only runs in
	// one direction at construction time.
	ck CommissionCeilingKeeper
}

// SetCommissionCeilingKeeper wires the source of the chain-wide commission
// ceiling. Call it on the keeper POINTER before app.StakingKeeper is assigned
// from it: that assignment copies the struct, and a copy taken first would
// carry a nil here.
//
// Until it is called the ceiling is unenforced. That is deliberate -- test
// fixtures construct this keeper directly and have no tokenomics module -- and
// it is why app_test asserts the wiring: a silent nil here is a silently
// unenforced ceiling on a live chain.
func (k *Keeper) SetCommissionCeilingKeeper(ck CommissionCeilingKeeper) {
	k.ck = ck
}

// MaxCommissionRate returns the chain-wide ceiling, or zero when there is no
// source wired or the parameter disables it.
func (k Keeper) MaxCommissionRate(ctx sdk.Context) math.LegacyDec {
	if k.ck == nil {
		return math.LegacyZeroDec()
	}
	rate := k.ck.GetMaxCommissionRate(ctx)
	if rate.IsNil() {
		return math.LegacyZeroDec()
	}
	return rate
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
		Keeper: stakingkeeper.NewKeeper(cdc, storeService, ak, bk, authority, validatorAddressCodec, consensusAddressCodec),
		ak:     ak,
		bk:     bk,
		// ck is wired by SetCommissionCeilingKeeper once x/tokenomics exists.
	}
}
