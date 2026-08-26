package cli

import (
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/spf13/cobra"

	"github.com/atoshi-chain/atoshi/v20/x/bridgeadapter/types"
)

func GetQueryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:                        types.ModuleName,
		Short:                      "Querying commands for the bridge adapter",
		DisableFlagParsing:         true,
		SuggestionsMinimumDistance: 2,
		RunE:                       client.ValidateCmd,
	}
	cmd.AddCommand(GetCmdQueryParams(), GetCmdQueryReceiptState())
	return cmd
}

func GetCmdQueryParams() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "params",
		Short: "Query the bridge adapter parameters",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cliCtx, err := client.GetClientQueryContext(cmd)
			if err != nil {
				return err
			}
			res, err := types.NewQueryClient(cliCtx).Params(cmd.Context(), &types.QueryParamsRequest{})
			if err != nil {
				return err
			}
			return cliCtx.PrintProto(res)
		},
	}
	flags.AddQueryFlagsToCmd(cmd)
	return cmd
}

func GetCmdQueryReceiptState() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "receipt-state",
		Short: "Query what Ethereum has confirmed, and what is still pending",
		Long: `Query what Ethereum has confirmed, and what is still pending.

The applied figures are cumulative ERC20 and should equal what the Ethereum
tier-release vault reports having released — that is the check an auditor makes.

A pending figure that stays positive means receipts are not arriving: the tier
engine has authorised a release but the ATOX conversion rate has not moved,
because no ATOS may enter the conversion pool until Ethereum confirms.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cliCtx, err := client.GetClientQueryContext(cmd)
			if err != nil {
				return err
			}
			res, err := types.NewQueryClient(cliCtx).ReceiptState(cmd.Context(), &types.QueryReceiptStateRequest{})
			if err != nil {
				return err
			}
			return cliCtx.PrintProto(res)
		},
	}
	flags.AddQueryFlagsToCmd(cmd)
	return cmd
}
