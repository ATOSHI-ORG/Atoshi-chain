package cli

import (
	"strconv"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/spf13/cobra"

	"github.com/atoshi-chain/atoshi/v20/x/oracle/types"
)

// GetQueryCmd returns the root query command for x/oracle.
func GetQueryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:                        types.ModuleName,
		Short:                      "Querying commands for the oracle module",
		DisableFlagParsing:         true,
		SuggestionsMinimumDistance: 2,
		RunE:                       client.ValidateCmd,
	}
	cmd.AddCommand(
		GetCmdQueryParams(),
		GetCmdQueryCurrentPrice(),
		GetCmdQueryTWAP(),
		GetCmdQueryPriceHistory(),
	)
	return cmd
}

func GetCmdQueryParams() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "params",
		Short: "Query oracle module parameters",
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

func GetCmdQueryCurrentPrice() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "current-price",
		Short: "Query the latest reported ATOS price",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cliCtx, err := client.GetClientQueryContext(cmd)
			if err != nil {
				return err
			}
			res, err := types.NewQueryClient(cliCtx).CurrentPrice(cmd.Context(), &types.QueryCurrentPriceRequest{})
			if err != nil {
				return err
			}
			return cliCtx.PrintProto(res)
		},
	}
	flags.AddQueryFlagsToCmd(cmd)
	return cmd
}

func GetCmdQueryTWAP() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "twap [lookback-seconds]",
		Short: "Query the TWAP price (default lookback uses params.TWAPLookbackSeconds)",
		Args:  cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cliCtx, err := client.GetClientQueryContext(cmd)
			if err != nil {
				return err
			}
			req := &types.QueryTWAPRequest{}
			if len(args) == 1 {
				seconds, err := strconv.ParseUint(args[0], 10, 64)
				if err != nil {
					return err
				}
				req.LookbackSeconds = seconds
			}
			res, err := types.NewQueryClient(cliCtx).TWAP(cmd.Context(), req)
			if err != nil {
				return err
			}
			return cliCtx.PrintProto(res)
		},
	}
	flags.AddQueryFlagsToCmd(cmd)
	return cmd
}

func GetCmdQueryPriceHistory() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "price-history [limit]",
		Short: "Query recent price reports (limit defaults to all stored)",
		Args:  cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cliCtx, err := client.GetClientQueryContext(cmd)
			if err != nil {
				return err
			}
			req := &types.QueryPriceHistoryRequest{}
			if len(args) == 1 {
				limit, err := strconv.ParseUint(args[0], 10, 32)
				if err != nil {
					return err
				}
				req.Limit = uint32(limit)
			}
			res, err := types.NewQueryClient(cliCtx).PriceHistory(cmd.Context(), req)
			if err != nil {
				return err
			}
			return cliCtx.PrintProto(res)
		},
	}
	flags.AddQueryFlagsToCmd(cmd)
	return cmd
}

