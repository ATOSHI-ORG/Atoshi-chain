package contracts

import (
	contractutils "github.com/atoshi-chain/atoshi/v20/contracts/utils"
	evmtypes "github.com/atoshi-chain/atoshi/v20/x/evm/types"
)

// LoadEvmStateRegressionContract loads the contract that reproduces the
// StateDB and precompile issues found in the audit after the 2026-11-22 mint:
// a partial pre-precompile flush, a stale module-account balance burning what
// a precompile put there, and the precompile call limit.
func LoadEvmStateRegressionContract() (evmtypes.CompiledContract, error) {
	return contractutils.LoadContractFromJSONFile("EvmStateRegression.json")
}
