package bridgeadapter

import (
	"math/big"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"

	cmn "github.com/atoshi-chain/atoshi/v20/precompiles/common"
	"github.com/atoshi-chain/atoshi/v20/x/evm/core/vm"
)

const (
	// EventTypeBridgeOut is the event emitted when ATOS is locked for Ethereum.
	EventTypeBridgeOut = "BridgeOut"
)

// EmitBridgeOutEvent emits the BridgeOut log.
//
// The module already emits a Cosmos event, but an EVM wallet cannot see those.
// A frontend that sends the transaction through the precompile gets its receipt
// back through eth_getTransactionReceipt and has nothing to read without this.
func (p Precompile) EmitBridgeOutEvent(
	ctx sdk.Context,
	stateDB vm.StateDB,
	sender common.Address,
	recipient [32]byte,
	amount *big.Int,
	erc20Amount *big.Int,
	messageID [32]byte,
) error {
	event := p.ABI.Events[EventTypeBridgeOut]

	// sender and recipient are indexed, so they become topics; the amounts and
	// the message id travel in the data.
	topics := make([]common.Hash, 3)
	topics[0] = event.ID

	var err error
	topics[1], err = cmn.MakeTopic(sender)
	if err != nil {
		return err
	}
	topics[2] = common.BytesToHash(recipient[:])

	// Pack only the non-indexed inputs, in ABI order.
	packed, err := event.Inputs.NonIndexed().Pack(amount, erc20Amount, messageID)
	if err != nil {
		return err
	}

	stateDB.AddLog(&ethtypes.Log{
		Address:     p.Address(),
		Topics:      topics,
		Data:        packed,
		BlockNumber: uint64(ctx.BlockHeight()), //nolint:gosec // G115
	})

	return nil
}
