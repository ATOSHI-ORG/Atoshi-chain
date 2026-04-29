// Copyright Tharsis Labs Ltd.(Evmos)
// SPDX-License-Identifier:ENCL-1.0(https://github.com/evmos/evmos/blob/main/LICENSE)

package post

import (
	"errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	bankkeeper "github.com/cosmos/cosmos-sdk/x/bank/keeper"

	energyante "github.com/atoshi-chain/atoshi/v20/x/energy/ante"
	energykeeper "github.com/atoshi-chain/atoshi/v20/x/energy/keeper"
)

// HandlerOptions are the options required for constructing a PostHandler.
type HandlerOptions struct {
	FeeCollectorName string
	BankKeeper       bankkeeper.Keeper
	// EnergyKeeper is optional; when set the energy refund decorator is
	// chained after the burn decorator so that any TxEnergy reserved
	// in excess of actual gas usage is returned to the signer.
	EnergyKeeper *energykeeper.Keeper
}

func (h HandlerOptions) Validate() error {
	if h.FeeCollectorName == "" {
		return errors.New("fee collector name cannot be empty")
	}

	if h.BankKeeper == nil {
		return errors.New("bank keeper cannot be nil")
	}

	return nil
}

// NewPostHandler returns a new PostHandler decorators chain.
func NewPostHandler(ho HandlerOptions) sdk.PostHandler {
	postDecorators := []sdk.PostDecorator{
		NewBurnDecorator(ho.FeeCollectorName, ho.BankKeeper),
	}
	if ho.EnergyKeeper != nil {
		postDecorators = append(postDecorators, energyante.NewEnergyRefundDecorator(*ho.EnergyKeeper))
	}

	return sdk.ChainPostDecorators(postDecorators...)
}
