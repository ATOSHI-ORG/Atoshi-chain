package cli

import (
	"fmt"

	"cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/client/tx"
	"github.com/spf13/cobra"

	"github.com/atoshi-chain/atoshi/v20/x/oracle/types"
)

// NewTxCmd returns the root tx command for x/oracle.
func NewTxCmd() *cobra.Command {
	txCmd := &cobra.Command{
		Use:                        types.ModuleName,
		Short:                      "Oracle transactions",
		DisableFlagParsing:         true,
		SuggestionsMinimumDistance: 2,
		RunE:                       client.ValidateCmd,
	}
	txCmd.AddCommand(NewReportPriceCmd())
	return txCmd
}

// NewReportPriceCmd returns a CLI command that submits a MsgReportPrice tx.
// Restricted to whitelisted feeders by the keeper.
func NewReportPriceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "report-price PRICE VOLUME_24H SOURCE",
		Short: "Submit a price report. PRICE/VOLUME_24H are decimals; SOURCE is e.g. 'uniswap_v3'.",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			cliCtx, err := client.GetClientTxContext(cmd)
			if err != nil {
				return err
			}
			price, err := math.LegacyNewDecFromStr(args[0])
			if err != nil {
				return fmt.Errorf("invalid price: %w", err)
			}
			volume, err := math.LegacyNewDecFromStr(args[1])
			if err != nil {
				return fmt.Errorf("invalid volume: %w", err)
			}
			msg := &types.MsgReportPrice{
				Feeder:    cliCtx.GetFromAddress().String(),
				Price:     price,
				Volume24h: volume,
				Source:    args[2],
			}
			return tx.GenerateOrBroadcastTxCLI(cliCtx, cmd.Flags(), msg)
		},
	}
	flags.AddTxFlagsToCmd(cmd)
	return cmd
}
