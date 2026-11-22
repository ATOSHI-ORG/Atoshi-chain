package contracts

import (
	contractutils "github.com/atoshi-chain/atoshi/v20/contracts/utils"
	evmtypes "github.com/atoshi-chain/atoshi/v20/x/evm/types"
)

// LoadPrecompileRevertMinterContract loads the contract that reproduces the
// 2026-11-22 mint: value sent into a frame that calls a precompile with
// undecodable calldata and reverts.
func LoadPrecompileRevertMinterContract() (evmtypes.CompiledContract, error) {
	return contractutils.LoadContractFromJSONFile("PrecompileRevertMinter.json")
}

// LoadPrecompileRevertInnerContract loads the inner half of the reproduction,
// for its sweep() ABI.
func LoadPrecompileRevertInnerContract() (evmtypes.CompiledContract, error) {
	return contractutils.LoadContractFromJSONFile("PrecompileRevertInner.json")
}
