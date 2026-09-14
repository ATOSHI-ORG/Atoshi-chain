package atox

import (
	"math/big"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"

	cmn "github.com/atoshi-chain/atoshi/v20/precompiles/common"
	"github.com/atoshi-chain/atoshi/v20/x/evm/core/vm"
)

const (
	// EventTypeClaimAtos is emitted when ATOX is converted into ATOS.
	EventTypeClaimAtos = "ClaimAtos"
)

// EmitClaimAtosEvent emits the ClaimAtos log.
//
// x/atox already emits a Cosmos event for the same settlement, but an EVM wallet
// cannot see those: it gets a receipt from eth_getTransactionReceipt and reads
// logs. Without this, a wallet that just converted has no way to show how much
// arrived except by diffing balances, which races with anything else in the
// block.
func (p Precompile) EmitClaimAtosEvent(
	ctx sdk.Context,
	stateDB vm.StateDB,
	claimer common.Address,
	atosPaid *big.Int,
	atoxBurned *big.Int,
) error {
	event := p.ABI.Events[EventTypeClaimAtos]

	// claimer is indexed, so it becomes a topic; the amounts travel in the data.
	topics := make([]common.Hash, 2)
	topics[0] = event.ID

	var err error
	topics[1], err = cmn.MakeTopic(claimer)
	if err != nil {
		return err
	}

	packed, err := event.Inputs.NonIndexed().Pack(atosPaid, atoxBurned)
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
