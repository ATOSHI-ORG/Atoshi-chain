package bridgeadapter

import (
	"embed"
	"fmt"

	"cosmossdk.io/log"
	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authzkeeper "github.com/cosmos/cosmos-sdk/x/authz/keeper"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"

	cmn "github.com/atoshi-chain/atoshi/v20/precompiles/common"
	bakeeper "github.com/atoshi-chain/atoshi/v20/x/bridgeadapter/keeper"
	"github.com/atoshi-chain/atoshi/v20/x/evm/core/vm"
	evmtypes "github.com/atoshi-chain/atoshi/v20/x/evm/types"
)

var _ vm.PrecompiledContract = &Precompile{}

// Embed abi json file to the executable binary. Needed when importing as dependency.
//
//go:embed abi.json
var f embed.FS

// Precompile exposes the bridge adapter to the EVM.
//
// Why this exists: MsgBridgeOut is a Cosmos message, and MetaMask and every
// other EVM wallet only sign EVM transactions. Without a precompile, bridging
// out is unreachable from a wallet no matter what the frontend does. Staking is
// usable from a wallet for exactly this reason -- it has one already.
//
// Only bridgeOut is exposed. Params and receipt state are already served over
// REST, and a precompile that moves money should present the smallest surface
// that removes the blockage.
type Precompile struct {
	cmn.Precompile
	bridgeKeeper bakeeper.Keeper
}

// LoadABI loads the bridge adapter ABI from the embedded abi.json file.
func LoadABI() (abi.ABI, error) {
	return cmn.LoadABI(f, "abi.json")
}

// NewPrecompile creates a new bridge adapter Precompile instance.
func NewPrecompile(
	bridgeKeeper bakeeper.Keeper,
	authzKeeper authzkeeper.Keeper,
) (*Precompile, error) {
	newAbi, err := LoadABI()
	if err != nil {
		return nil, err
	}

	p := &Precompile{
		Precompile: cmn.Precompile{
			ABI:                  newAbi,
			AuthzKeeper:          authzKeeper,
			KvGasConfig:          storetypes.KVGasConfig(),
			TransientKVGasConfig: storetypes.TransientGasConfig(),
			ApprovalExpiration:   cmn.DefaultExpirationDuration,
		},
		bridgeKeeper: bridgeKeeper,
	}

	p.SetAddress(common.HexToAddress(evmtypes.BridgeAdapterPrecompileAddress))

	return p, nil
}

// RequiredGas calculates the precompiled contract's base gas rate.
func (p Precompile) RequiredGas(input []byte) uint64 {
	// Avoid panicking when the input is too short to hold a method ID.
	if len(input) < 4 {
		return 0
	}
	methodID := input[:4]

	method, err := p.MethodById(methodID)
	if err != nil {
		// Never reached: Run fails on the same lookup.
		return 0
	}

	return p.Precompile.RequiredGas(input, p.IsTransaction(method))
}

// Run executes the bridge adapter methods defined in the ABI.
func (p Precompile) Run(evm *vm.EVM, contract *vm.Contract, readOnly bool) (bz []byte, err error) {
	ctx, stateDB, snapshot, method, initialGas, args, err := p.RunSetup(evm, contract, readOnly, p.IsTransaction)
	if err != nil {
		return nil, err
	}

	// Turn out-of-gas panics into errors so the EVM can unwind gracefully.
	defer cmn.HandleGasError(ctx, contract, initialGas, &err)()

	switch method.Name {
	case BridgeOutMethod:
		bz, err = p.BridgeOut(ctx, evm.Origin, contract, stateDB, method, args)
	default:
		return nil, fmt.Errorf(cmn.ErrUnknownMethod, method.Name)
	}

	if err != nil {
		return nil, err
	}

	cost := ctx.GasMeter().GasConsumed() - initialGas

	if !contract.UseGas(cost) {
		return nil, vm.ErrOutOfGas
	}

	if err := p.AddJournalEntries(stateDB, snapshot); err != nil {
		return nil, err
	}

	return bz, nil
}

// IsTransaction reports whether the method writes state.
//
// bridgeOut moves funds and dispatches a cross-chain message, so it can never
// be served in a read-only context.
func (Precompile) IsTransaction(method *abi.Method) bool {
	switch method.Name {
	case BridgeOutMethod:
		return true
	default:
		return false
	}
}

// Logger returns a precompile-specific logger.
func (p Precompile) Logger(ctx sdk.Context) log.Logger {
	return ctx.Logger().With("evm extension", "bridgeadapter")
}
