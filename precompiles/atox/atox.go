package atox

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
	atoxkeeper "github.com/atoshi-chain/atoshi/v20/x/atox/keeper"
	"github.com/atoshi-chain/atoshi/v20/x/evm/core/vm"
	evmtypes "github.com/atoshi-chain/atoshi/v20/x/evm/types"
)

var _ vm.PrecompiledContract = &Precompile{}

// Embed abi json file to the executable binary. Needed when importing as dependency.
//
//go:embed abi.json
var f embed.FS

// Precompile exposes ATOX conversion to the EVM.
//
// # Why this exists
//
// Collecting ATOS for mined ATOX is MsgClaimAtos, a Cosmos message, and an EVM
// wallet cannot sign one. Until the EndBlocker sweep was removed, nobody noticed:
// conversion happened on its own. Now that collection is something the holder
// asks for, every EVM client -- the staking dapp, the wallet -- needs a way to
// ask, and this is it.
//
// # Why its own precompile rather than a method on distribution
//
// Conversion is not part of claiming staking rewards. It applies to any ATOX
// balance however it was acquired, and the wallet needs it with no staking
// involved at all. Hanging it off the distribution precompile would tie an
// independent capability to an unrelated module and force every future caller
// through a staking-shaped door.
//
// # Read methods included deliberately
//
// claimable, burnOnSettle and globalIndex are all served over REST too, so a
// dapp does not need them here. A wallet does: it may have an EVM RPC and
// nothing else, and asking it to also reach a Cosmos REST endpoint to decide
// whether to show a button is how that button ends up wrong. They are cheap
// reads with no state change.
type Precompile struct {
	cmn.Precompile
	atoxKeeper atoxkeeper.Keeper
}

// LoadABI loads the atox ABI from the embedded abi.json file.
func LoadABI() (abi.ABI, error) {
	return cmn.LoadABI(f, "abi.json")
}

// NewPrecompile creates a new atox Precompile instance.
func NewPrecompile(
	atoxKeeper atoxkeeper.Keeper,
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
		atoxKeeper: atoxKeeper,
	}

	p.SetAddress(common.HexToAddress(evmtypes.AtoxPrecompileAddress))

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

// Run executes the atox methods defined in the ABI.
func (p Precompile) Run(evm *vm.EVM, contract *vm.Contract, readOnly bool) (bz []byte, err error) {
	ctx, stateDB, snapshot, method, initialGas, args, err := p.RunSetup(evm, contract, readOnly, p.IsTransaction)
	if err != nil {
		return nil, err
	}

	// Turn out-of-gas panics into errors so the EVM can unwind gracefully.
	defer cmn.HandleGasError(ctx, contract, initialGas, &err)()

	switch method.Name {
	case ClaimMethod:
		bz, err = p.Claim(ctx, evm.Origin, contract, stateDB, method, args)
	case ClaimableMethod:
		bz, err = p.Claimable(ctx, method, args)
	case BurnOnSettleMethod:
		bz, err = p.BurnOnSettle(ctx, method, args)
	case GlobalIndexMethod:
		bz, err = p.GlobalIndex(ctx, method, args)
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
func (Precompile) IsTransaction(method *abi.Method) bool {
	switch method.Name {
	case ClaimMethod:
		return true
	default:
		return false
	}
}

// Logger returns a precompile-specific logger.
func (p Precompile) Logger(ctx sdk.Context) log.Logger {
	return ctx.Logger().With("evm extension", "atox")
}
