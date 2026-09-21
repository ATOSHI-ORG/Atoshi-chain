package cli

import (
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/client/tx"
	"github.com/spf13/cobra"

	"github.com/atoshi-chain/atoshi/v20/x/atox/types"
)

// NewTxCmd returns the root tx command for x/atox.
func NewTxCmd() *cobra.Command {
	txCmd := &cobra.Command{
		Use:                        types.ModuleName,
		Short:                      "ATOX transactions",
		DisableFlagParsing:         true,
		SuggestionsMinimumDistance: 2,
		RunE:                       client.ValidateCmd,
	}
	txCmd.AddCommand(
		NewClaimAtosCmd(),
	)
	return txCmd
}

// NewClaimAtosCmd converts the signer's accrued ATOX claim to ATOS immediately.
func NewClaimAtosCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "claim",
		Short: "Convert your accrued ATOX claim to ATOS now",
		Long: `Convert your accrued ATOX claim to ATOS now.

This is optional. The chain sweeps every ATOX holder automatically, so the
conversion arrives without any transaction; this command only skips the wait.
Unlike the automatic sweep it ignores the dust threshold and pays out whatever
has accrued.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cliCtx, err := client.GetClientTxContext(cmd)
			if err != nil {
				return err
			}
			msg := &types.MsgClaimAtos{Claimer: cliCtx.GetFromAddress().String()}
			return tx.GenerateOrBroadcastTxCLI(cliCtx, cmd.Flags(), msg)
		},
	}
	flags.AddTxFlagsToCmd(cmd)
	return cmd
}
