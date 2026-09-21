// Copyright Tharsis Labs Ltd.(Evmos)
// SPDX-License-Identifier:ENCL-1.0(https://github.com/evmos/evmos/blob/main/LICENSE)
package erc20

import (
	"math/big"

	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/authz"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	cmn "github.com/atoshi-chain/atoshi/v20/precompiles/common"
	"github.com/atoshi-chain/atoshi/v20/x/evm/core/vm"
	evmtypes "github.com/atoshi-chain/atoshi/v20/x/evm/types"
)

const (
	// TransferMethod defines the ABI method name for the ERC-20 transfer
	// transaction.
	TransferMethod = "transfer"
	// TransferFromMethod defines the ABI method name for the ERC-20 transferFrom
	// transaction.
	TransferFromMethod = "transferFrom"
)

// SendMsgURL defines the authorization type for MsgSend
var SendMsgURL = sdk.MsgTypeURL(&banktypes.MsgSend{})

// Transfer executes a direct transfer from the caller address to the
// destination address.
func (p *Precompile) Transfer(
	ctx sdk.Context,
	contract *vm.Contract,
	stateDB vm.StateDB,
	method *abi.Method,
	args []interface{},
) ([]byte, error) {
	from := contract.CallerAddress
	to, amount, err := ParseTransferArgs(args)
	if err != nil {
		return nil, err
	}

	return p.transfer(ctx, contract, stateDB, method, from, to, amount)
}

// TransferFrom executes a transfer on behalf of the specified from address in
// the call data to the destination address.
func (p *Precompile) TransferFrom(
	ctx sdk.Context,
	contract *vm.Contract,
	stateDB vm.StateDB,
	method *abi.Method,
	args []interface{},
) ([]byte, error) {
	from, to, amount, err := ParseTransferFromArgs(args)
	if err != nil {
		return nil, err
	}

	return p.transfer(ctx, contract, stateDB, method, from, to, amount)
}

// transfer is a common function that handles transfers for the ERC-20 Transfer
// and TransferFrom methods. It executes a bank Send message if the spender is
// the sender of the transfer, otherwise it executes an authorization.
func (p *Precompile) transfer(
	ctx sdk.Context,
	contract *vm.Contract,
	stateDB vm.StateDB,
	method *abi.Method,
	from, to common.Address,
	amount *big.Int,
) (data []byte, err error) {
	coins := sdk.Coins{{Denom: p.tokenPair.Denom, Amount: math.NewIntFromBigInt(amount)}}

	msg := banktypes.NewMsgSend(from.Bytes(), to.Bytes(), coins)

	if err := msg.Amount.Validate(); err != nil {
		return nil, err
	}

	// Recipient balance before the send, so the Transfer event can report what
	// actually arrived rather than what was asked for.
	//
	// ATOX carries a 10% inclusive transfer fee: a transfer(to, 100) moves 100
	// out of the sender and delivers 90. Emitting `amount` would tell every
	// explorer, indexer and exchange that 90 aatox arrived as 100 -- the kind of
	// mismatch that makes an exchange credit a deposit it never received.
	//
	// Measured rather than recomputed: the fee rules live in x/atox and
	// duplicating them here is how the two drift. A balance delta is exact for
	// any fee, exemption or future rule.
	recipientBefore := p.BankKeeper.GetBalance(ctx, to.Bytes(), p.tokenPair.Denom).Amount

	isTransferFrom := method.Name == TransferFromMethod
	owner := sdk.AccAddress(from.Bytes())
	spenderAddr := contract.CallerAddress
	spender := sdk.AccAddress(spenderAddr.Bytes()) // aka. grantee
	ownerIsSpender := spender.Equals(owner)

	var prevAllowance *big.Int
	if ownerIsSpender {
		_, err = p.bankMsgServerOrStock().Send(ctx, msg)
	} else {
		_, _, prevAllowance, err = GetAuthzExpirationAndAllowance(p.AuthzKeeper, ctx, spenderAddr, from, p.tokenPair.Denom)
		if err != nil {
			return nil, ConvertErrToERC20Error(errorsmod.Wrapf(authz.ErrNoAuthorizationFound, "%s", err.Error()))
		}

		_, err = p.AuthzKeeper.DispatchActions(ctx, spender, []sdk.Msg{msg})
	}

	if err != nil {
		err = ConvertErrToERC20Error(err)
		// This should return an error to avoid the contract from being executed and an event being emitted
		return nil, err
	}

	if p.tokenPair.Denom == evmtypes.GetEVMCoinDenom() {
		// add the entries to the statedb journal in 18 decimals
		convertedAmount := evmtypes.ConvertAmountTo18DecimalsBigInt(amount)
		p.SetBalanceChangeEntries(cmn.NewBalanceChangeEntry(from, convertedAmount, cmn.Sub),
			cmn.NewBalanceChangeEntry(to, convertedAmount, cmn.Add))
	}

	delivered := p.BankKeeper.GetBalance(ctx, to.Bytes(), p.tokenPair.Denom).Amount.
		Sub(recipientBefore)
	if delivered.IsNegative() {
		// Cannot happen for a send, but a negative would pack as a huge uint256.
		delivered = math.ZeroInt()
	}

	if err = p.EmitTransferEvent(ctx, stateDB, from, to, delivered.BigInt()); err != nil {
		return nil, err
	}

	// NOTE: if it's a direct transfer, we return here but if used through transferFrom,
	// we need to emit the approval event with the new allowance.
	if !isTransferFrom {
		return method.Outputs.Pack(true)
	}

	var newAllowance *big.Int
	if ownerIsSpender {
		// NOTE: in case the spender is the owner we emit an approval event with
		// the maxUint256 value.
		newAllowance = abi.MaxUint256
	} else {
		newAllowance = new(big.Int).Sub(prevAllowance, amount)
	}

	if err = p.EmitApprovalEvent(ctx, stateDB, from, spenderAddr, newAllowance); err != nil {
		return nil, err
	}

	return method.Outputs.Pack(true)
}
