// Copyright Tharsis Labs Ltd.(Evmos)
// SPDX-License-Identifier:ENCL-1.0(https://github.com/evmos/evmos/blob/main/LICENSE)

package ante

import (
	cosmosante "github.com/atoshi-chain/atoshi/v20/app/ante/cosmos"
	evmante "github.com/atoshi-chain/atoshi/v20/app/ante/evm"
	energyante "github.com/atoshi-chain/atoshi/v20/x/energy/ante"
	evmtypes "github.com/atoshi-chain/atoshi/v20/x/evm/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/auth/ante"
	sdkvesting "github.com/cosmos/cosmos-sdk/x/auth/vesting/types"
	ibcante "github.com/cosmos/ibc-go/v8/modules/core/ante"
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

	decorators := []sdk.AnteDecorator{
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
	}

	// Chain-wide validator self-stake floor. Only wired when a tokenomics keeper
	// is supplied -- see HandlerOptions -- so unit tests assembling a bare ante
	// chain are unaffected.
	//
	// Position matters: after ValidateBasic so the msg is known well-formed, and
	// before the fee decorator so a validator that cannot meet the floor is
	// rejected without being charged for the attempt.
	if options.TokenomicsKeeper != nil {
		decorators = append(decorators,
			cosmosante.NewMinSelfDelegationDecorator(options.TokenomicsKeeper))
	}

	decorators = append(decorators,
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

	return sdk.ChainAnteDecorators(decorators...)
}
