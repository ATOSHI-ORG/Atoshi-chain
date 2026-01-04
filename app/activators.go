// Copyright Tharsis Labs Ltd.(Evmos)
// SPDX-License-Identifier:ENCL-1.0(https://github.com/evmos/evmos/blob/main/LICENSE)
package app

import (
	"github.com/atoshi-chain/atoshi/v20/app/eips"
	"github.com/atoshi-chain/atoshi/v20/x/evm/core/vm"
)

// atoshiActivators defines a map of opcode modifiers associated
// with a key defining the corresponding EIP.
var atoshiActivators = map[string]func(*vm.JumpTable){
	"atoshi_0": eips.Enable0000,
	"atoshi_1": eips.Enable0001,
	"atoshi_2": eips.Enable0002,
}
