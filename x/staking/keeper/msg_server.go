// Copyright Tharsis Labs Ltd.(Evmos)
// SPDX-License-Identifier:ENCL-1.0(https://github.com/evmos/evmos/blob/main/LICENSE)

package keeper

import (
	"context"
	"fmt"

	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	errortypes "github.com/cosmos/cosmos-sdk/types/errors"
	sdkstakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
	"github.com/cosmos/cosmos-sdk/x/staking/types"

	vestingtypes "github.com/atoshi-chain/atoshi/v20/x/vesting/types"
)

// msgServer is a wrapper around the Cosmos SDK message server.
type msgServer struct {
	types.MsgServer
	*Keeper
}

var _ types.MsgServer = msgServer{}

// NewMsgServerImpl returns an implementation of the staking MsgServer interface
// for the provided Keeper.
func NewMsgServerImpl(keeper *Keeper) types.MsgServer {
	baseMsgServer := sdkstakingkeeper.NewMsgServerImpl(keeper.Keeper)
	return &msgServer{baseMsgServer, keeper}
}

// Delegate defines a method for performing a delegation of coins from a delegator to a validator.
// The method performs some checks if the sender of the tx is a clawback vesting account and then
// relay the message to the Cosmos SDK staking method.
func (k msgServer) Delegate(goCtx context.Context, msg *types.MsgDelegate) (*types.MsgDelegateResponse, error) {
	if err := k.validateDelegationAmountNotUnvested(goCtx, msg.DelegatorAddress, msg.Amount.Amount); err != nil {
		return nil, err
	}

	return k.MsgServer.Delegate(goCtx, msg)
}

// CreateValidator defines a method to create a validator. The method performs some checks if the
// sender of the tx is a clawback vesting account and then relay the message to the Cosmos SDK staking
// method.
func (k msgServer) CreateValidator(goCtx context.Context, msg *types.MsgCreateValidator) (*types.MsgCreateValidatorResponse, error) {
	bz, err := k.ValidatorAddressCodec().StringToBytes(msg.ValidatorAddress)
	if err != nil {
		return nil, errorsmod.Wrap(errortypes.ErrInvalidAddress, fmt.Sprintf("invalid validator address. %s", err))
	}

	if err := k.validateDelegationAmountNotUnvested(goCtx, sdk.AccAddress(bz).String(), msg.Value.Amount); err != nil {
		return nil, err
	}

	// The chain-wide self-stake floor. The AnteHandler checks it too, to keep
	// doomed txs out of the mempool, but only Cosmos txs pass through that
	// decorator: the staking precompile reached this msg server directly from
	// an EVM tx, so a validator could be created with 1 wei of self-stake.
	if floor := k.minSelfDelegationFloor(sdk.UnwrapSDKContext(goCtx)); !floor.IsNil() && floor.IsPositive() {
		if msg.MinSelfDelegation.IsNil() || msg.MinSelfDelegation.LT(floor) {
			return nil, errorsmod.Wrapf(errortypes.ErrInvalidRequest,
				"validator min_self_delegation must be at least %s, got %s", floor, msg.MinSelfDelegation)
		}
		if msg.Value.Amount.LT(floor) {
			return nil, errorsmod.Wrapf(errortypes.ErrInvalidRequest,
				"validator must self-delegate at least %s, got %s", floor, msg.Value.Amount)
		}
	}

	return k.MsgServer.CreateValidator(goCtx, msg)
}

// EditValidator refuses to lower min_self_delegation below the chain-wide
// floor, then relays to the Cosmos SDK. See CreateValidator for why this is
// enforced here and not only in the AnteHandler.
func (k msgServer) EditValidator(goCtx context.Context, msg *types.MsgEditValidator) (*types.MsgEditValidatorResponse, error) {
	if msg.MinSelfDelegation != nil {
		if floor := k.minSelfDelegationFloor(sdk.UnwrapSDKContext(goCtx)); !floor.IsNil() && floor.IsPositive() && msg.MinSelfDelegation.LT(floor) {
			return nil, errorsmod.Wrapf(errortypes.ErrInvalidRequest,
				"validator min_self_delegation must be at least %s, got %s", floor, *msg.MinSelfDelegation)
		}
	}
	return k.MsgServer.EditValidator(goCtx, msg)
}

// validateDelegationAmountNotUnvested checks if the delegator is a clawback vesting account.
// In such case, checks that the provided delegation amount is available according
// to the current vesting schedule (unvested coins cannot be delegated).
func (k msgServer) validateDelegationAmountNotUnvested(goCtx context.Context, delegatorAddress string, amount math.Int) error {
	ctx := sdk.UnwrapSDKContext(goCtx)
	addr, err := sdk.AccAddressFromBech32(delegatorAddress)
	if err != nil {
		return err
	}

	acc := k.ak.GetAccount(ctx, addr)
	if acc == nil {
		return errorsmod.Wrap(
			errortypes.ErrUnknownAddress,
			fmt.Sprintf("account %s does not exist", addr),
		)
	}
	// check if delegator address is a clawback vesting account. If not, no check
	// is required.
	clawbackAccount, isClawback := acc.(*vestingtypes.ClawbackVestingAccount)
	if !isClawback {
		return nil
	}

	// vesting account can only delegate
	// if enough free balance (coins not in vesting schedule)
	// plus the vested coins (locked/unlocked)
	bondDenom, err := k.BondDenom(ctx)
	if err != nil {
		return err
	}
	// GetBalance returns entire account balance
	// balance = free coins + all coins in vesting schedule
	balance := k.bk.GetBalance(ctx, addr, bondDenom)
	unvestedOnly := clawbackAccount.GetVestingCoins(ctx.BlockTime())
	// delegatable coins are going to be all the free coins + vested coins
	// Can only delegate bondable coins
	unvestedBondableAmt := unvestedOnly.AmountOf(bondDenom)
	// A ClawbackVestingAccount can delegate coins from the vesting schedule
	// when having vested locked coins or unlocked vested coins.
	// It CANNOT delegate unvested coins
	delegatableAmt := balance.Amount.Sub(unvestedBondableAmt)
	if delegatableAmt.IsNegative() {
		delegatableAmt = math.ZeroInt()
	}

	if delegatableAmt.LT(amount) {
		return errorsmod.Wrapf(
			vestingtypes.ErrInsufficientVestedCoins,
			"cannot delegate unvested coins. coins available for delegation < delegation amount (%s < %s)",
			delegatableAmt, amount,
		)
	}
	return nil
}
