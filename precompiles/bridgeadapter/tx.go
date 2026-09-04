package bridgeadapter

import (
	"fmt"
	"math/big"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"

	cmn "github.com/atoshi-chain/atoshi/v20/precompiles/common"
	bakeeper "github.com/atoshi-chain/atoshi/v20/x/bridgeadapter/keeper"
	batypes "github.com/atoshi-chain/atoshi/v20/x/bridgeadapter/types"
	"github.com/atoshi-chain/atoshi/v20/x/evm/core/vm"
)

const (
	// BridgeOutMethod is the ABI method name for locking ATOS and requesting
	// the matching ERC20 on Ethereum.
	BridgeOutMethod = "bridgeOut"
)

// ErrCallerNotOrigin is returned when a contract tries to bridge out on behalf
// of the account that signed the transaction.
const ErrCallerNotOrigin = "bridgeOut must be called directly by the account whose ATOS is being bridged: caller %s, signer %s"

// BridgeOut locks the caller's ATOS and dispatches a Hyperlane message so the
// matching ERC20 is released on Ethereum.
//
// The sender is always evm.Origin -- the account that signed this transaction.
// There is deliberately no sender argument and no authz path:
//
//   - A sender argument would have to be checked against origin anyway, so it
//     would only add a way to get the check wrong.
//   - Allowing a contract to call this for a user (as staking does, gated by an
//     authz grant) would let any contract a user merely interacts with move
//     their ATOS across a chain boundary. That is not recoverable, and no
//     current use case needs it. So a contract caller is refused outright
//     rather than half-supported.
//
// Every substantive check -- the rate limits, the peg multiple, the balance --
// lives in ExecuteBridgeOut and is not repeated here. Duplicating them would
// mean two places to keep in agreement, and the copy would eventually disagree
// with the one that actually guards the funds.
func (p Precompile) BridgeOut(
	ctx sdk.Context,
	origin common.Address,
	contract *vm.Contract,
	stateDB vm.StateDB,
	method *abi.Method,
	args []interface{},
) ([]byte, error) {
	if len(args) != 3 {
		return nil, fmt.Errorf(cmn.ErrInvalidNumberOfArgs, 3, len(args))
	}

	// A contract calling on someone else's behalf is refused. See the doc
	// comment: this is the whole authorization model.
	if contract.CallerAddress != origin {
		return nil, fmt.Errorf(ErrCallerNotOrigin, contract.CallerAddress, origin)
	}

	recipient, ok := args[0].([32]byte)
	if !ok {
		return nil, fmt.Errorf("invalid recipient: expected bytes32, got %T", args[0])
	}
	// A zero recipient would lock ATOS against an Ethereum address nobody
	// holds. Nothing downstream rejects it, and it cannot be undone.
	if recipient == ([32]byte{}) {
		return nil, fmt.Errorf("recipient cannot be zero")
	}

	amountBig, ok := args[1].(*big.Int)
	if !ok {
		return nil, fmt.Errorf("invalid amount: expected uint256, got %T", args[1])
	}
	maxFeeBig, ok := args[2].(*big.Int)
	if !ok {
		return nil, fmt.Errorf("invalid maxFeeAmount: expected uint256, got %T", args[2])
	}
	// uint256 is unsigned in the ABI, but the decoded big.Int is signed and a
	// value above 2^255 comes back negative. math.NewIntFromBigInt would then
	// carry a negative amount into the keeper.
	if amountBig.Sign() < 0 || maxFeeBig.Sign() < 0 {
		return nil, fmt.Errorf("amount and maxFeeAmount must fit in a signed 256-bit integer")
	}

	amount := math.NewIntFromBigInt(amountBig)

	// maxFee is a Coins list on the message. Zero means "no bound" and must be
	// an empty list rather than a zero coin -- sdk.Coins rejects zero amounts,
	// and a coin of zero would read as "willing to pay nothing".
	var maxFee sdk.Coins
	if maxFeeBig.Sign() > 0 {
		denom := p.bridgeKeeper.BaseDenom()
		if denom == "" {
			return nil, fmt.Errorf("base denom is not set")
		}
		maxFee = sdk.NewCoins(sdk.NewCoin(denom, math.NewIntFromBigInt(maxFeeBig)))
	}

	sender := sdk.AccAddress(origin.Bytes())

	p.Logger(ctx).Debug(
		"tx called",
		"method", method.Name,
		"args", fmt.Sprintf(
			"{ sender: %s, recipient: 0x%x, amount: %s, max_fee: %s }",
			sender, recipient, amount, maxFee,
		),
	)

	msg := &batypes.MsgBridgeOut{
		Sender:    sender.String(),
		Recipient: recipient[:],
		Amount:    amount,
		MaxFee:    maxFee,
	}

	msgSrv := bakeeper.NewMsgServerImpl(p.bridgeKeeper)
	res, err := msgSrv.BridgeOut(ctx, msg)
	if err != nil {
		return nil, err
	}

	var messageID [32]byte
	// The Hyperlane message id is 32 bytes. Copy rather than assume, so a
	// shorter value is left-padded instead of panicking.
	copy(messageID[32-min(len(res.MessageId), 32):], res.MessageId)

	if err := p.EmitBridgeOutEvent(
		ctx, stateDB, origin, recipient, amountBig, res.Erc20Amount.BigInt(), messageID,
	); err != nil {
		return nil, err
	}

	return method.Outputs.Pack(messageID, res.Erc20Amount.BigInt())
}
