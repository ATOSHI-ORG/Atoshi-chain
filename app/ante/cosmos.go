// Copyright Tharsis Labs Ltd.(Evmos)
// SPDX-License-Identifier:ENCL-1.0(https://github.com/evmos/evmos/blob/main/LICENSE)

package ante

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/auth/ante"
	sdkvesting "github.com/cosmos/cosmos-sdk/x/auth/vesting/types"
	ibcante "github.com/cosmos/ibc-go/v8/modules/core/ante"
	cosmosante "github.com/atoshi-chain/atoshi/v20/app/ante/cosmos"
	evmante "github.com/atoshi-chain/atoshi/v20/app/ante/evm"
	energyante "github.com/atoshi-chain/atoshi/v20/x/energy/ante"
	evmtypes "github.com/atoshi-chain/atoshi/v20/x/evm/types"
)

// newCosmosAnteHandler creates the default ante handler for Cosmos transactions.
//
// Energy integration:
//   - When options.EnergyKeeper is non-nil, the standard DeductFeeDecorator is
//     replaced by EnergyDeductDecorator, which consults the energy module
//     before charging ATOS for the gas not covered by accrued energy.
//   - When the EnergyKeeper is nil (e.g. tests that want stock behavior),
//     we fall back to the SDK's DeductFeeDecorator.
func newCosmosAnteHandler(options HandlerOptions) sdk.AnteHandler {
	var feeDecorator sdk.AnteDecorator
	if options.EnergyKeeper != nil {
		feeDecorator = energyante.NewEnergyDeductDecorator(
			*options.EnergyKeeper,
			options.AccountKeeper,
			options.BankKeeper,
			options.FeegrantKeeper,
			options.TxFeeChecker,
		)
	} else {
		feeDecorator = ante.NewDeductFeeDecorator(options.AccountKeeper, options.BankKeeper, options.FeegrantKeeper, options.TxFeeChecker)
	}

	return sdk.ChainAnteDecorators(
		cosmosante.RejectMessagesDecorator{}, // reject MsgEthereumTxs
		cosmosante.NewAuthzLimiterDecorator( // disable the Msg types that cannot be included on an authz.MsgExec msgs field
			sdk.MsgTypeURL(&evmtypes.MsgEthereumTx{}),
			sdk.MsgTypeURL(&sdkvesting.MsgCreateVestingAccount{}),
		),
		ante.NewSetUpContextDecorator(),
		ante.NewExtensionOptionsDecorator(options.ExtensionOptionChecker),
		ante.NewValidateBasicDecorator(),
		ante.NewTxTimeoutHeightDecorator(),
		ante.NewValidateMemoDecorator(options.AccountKeeper),
		cosmosante.NewMinGasPriceDecorator(options.FeeMarketKeeper, options.EvmKeeper),
		ante.NewConsumeGasForTxSizeDecorator(options.AccountKeeper),
		feeDecorator,
		// SetPubKeyDecorator must be called before all signature verification decorators
		ante.NewSetPubKeyDecorator(options.AccountKeeper),
		ante.NewValidateSigCountDecorator(options.AccountKeeper),
		ante.NewSigGasConsumeDecorator(options.AccountKeeper, options.SigGasConsumer),
		ante.NewSigVerificationDecorator(options.AccountKeeper, options.SignModeHandler),
		ante.NewIncrementSequenceDecorator(options.AccountKeeper),
		ibcante.NewRedundantRelayDecorator(options.IBCKeeper),
		evmante.NewGasWantedDecorator(options.EvmKeeper, options.FeeMarketKeeper),
	)
}
