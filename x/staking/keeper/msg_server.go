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

	// max_rate is writable exactly once, here. MsgEditValidator carries no such
	// field and UpdateValidatorCommission assigns only Rate and UpdateTime, so
	// whatever is declared now is permanent for the life of the validator. This
	// is the only opportunity to bound it.
	//
	// Only max_rate is checked. The SDK's own Commission.Validate already
	// refuses a rate above max_rate (ErrCommissionGTMaxRate), so bounding the
	// ceiling bounds the rate with it.
	if err := k.checkCommissionCeiling(
		sdk.UnwrapSDKContext(goCtx), msg.Commission.MaxRate, "commission max_rate",
	); err != nil {
		return nil, err
	}

	return k.MsgServer.CreateValidator(goCtx, msg)
}

// EditValidator bounds a commission increase by the chain-wide ceiling before
// relaying to the SDK.
//
// This is what holds the seven validators created with max_rate 100%. That
// field cannot be lowered by anything, so the ceiling is applied to the rate
// they can actually reach instead: the 100% remains on record and becomes
// unreachable.
func (k msgServer) EditValidator(goCtx context.Context, msg *types.MsgEditValidator) (*types.MsgEditValidatorResponse, error) {
	if msg.CommissionRate != nil {
		ctx := sdk.UnwrapSDKContext(goCtx)
		if err := k.checkCommissionIncrease(ctx, msg.ValidatorAddress, *msg.CommissionRate); err != nil {
			return nil, err
		}
	}

	return k.MsgServer.EditValidator(goCtx, msg)
}

// checkCommissionCeiling refuses a rate above the chain-wide ceiling.
//
// A non-positive ceiling disables the check, which covers both an unset
// parameter and a chain whose keeper has no tokenomics source wired.
func (k msgServer) checkCommissionCeiling(ctx sdk.Context, rate math.LegacyDec, what string) error {
	ceiling := k.MaxCommissionRate(ctx)
	if !ceiling.IsPositive() {
		return nil
	}
	if rate.IsNil() {
		return nil
	}
	if rate.GT(ceiling) {
		return errorsmod.Wrapf(errortypes.ErrInvalidRequest,
			"%s %s exceeds the chain maximum commission rate of %s", what, rate, ceiling)
	}
	return nil
}

// checkCommissionIncrease refuses raising a commission above the ceiling, while
// always allowing a decrease.
//
// The exemption is not a loophole, it is the only way the ceiling stays
// enforceable. Governance can lower max_commission_rate below a validator's
// current rate -- and the validator complying with that is a decrease from above
// the ceiling, which an unconditional check would refuse. The result would be a
// validator permanently unable to obey the rule it is being held to.
//
// A rate that cannot be compared against the validator's current one is treated
// as an increase: if the validator cannot be read, the SDK is about to reject
// the message anyway, and the strict reading is the safe one.
func (k msgServer) checkCommissionIncrease(ctx sdk.Context, valAddr string, newRate math.LegacyDec) error {
	ceiling := k.MaxCommissionRate(ctx)
	if !ceiling.IsPositive() || newRate.IsNil() || newRate.LTE(ceiling) {
		return nil
	}

	bz, err := k.ValidatorAddressCodec().StringToBytes(valAddr)
	if err == nil {
		if validator, vErr := k.GetValidator(ctx, bz); vErr == nil &&
			!validator.Commission.Rate.IsNil() &&
			newRate.LT(validator.Commission.Rate) {
			// Moving towards the ceiling from above it. Allowed.
			return nil
		}
	}

	return errorsmod.Wrapf(errortypes.ErrInvalidRequest,
		"commission rate %s exceeds the chain maximum commission rate of %s; "+
			"lowering a rate is always permitted, raising it above the maximum is not",
		newRate, ceiling)
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
