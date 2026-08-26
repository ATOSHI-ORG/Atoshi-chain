package cli

import (
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/gogoproto/proto"
	"github.com/spf13/cobra"

	"github.com/atoshi-chain/atoshi/v20/x/atox/types"
)

// GetQueryCmd returns the root query command for x/atox.
func GetQueryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:                        types.ModuleName,
		Short:                      "Querying commands for the atox module",
		DisableFlagParsing:         true,
		SuggestionsMinimumDistance: 2,
		RunE:                       client.ValidateCmd,
	}
	cmd.AddCommand(
		GetCmdQueryParams(),
		GetCmdQueryGlobalState(),
		GetCmdQueryAccount(),
		GetCmdQueryExchangePool(),
	)
	return cmd
}

func runQuery[Resp proto.Message](cmd *cobra.Command, fn func(types.QueryClient) (Resp, error)) error {
	cliCtx, err := client.GetClientQueryContext(cmd)
	if err != nil {
		return err
	}
	res, err := fn(types.NewQueryClient(cliCtx))
	if err != nil {
		return err
	}
	return cliCtx.PrintProto(res)
}

func GetCmdQueryParams() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "params",
		Short: "Query the atox module parameters",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runQuery(cmd, func(q types.QueryClient) (*types.QueryParamsResponse, error) {
				return q.Params(cmd.Context(), &types.QueryParamsRequest{})
			})
		},
	}
	flags.AddQueryFlagsToCmd(cmd)
	return cmd
}

func GetCmdQueryGlobalState() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "global-state",
		Short: "Query the ATOX conversion accumulator",
		Long: `Query the ATOX conversion accumulator.

global_index is the ATOX -> ATOS conversion rate so far: one ATOX has accrued
that much ATOS. It starts at 0 and rises to 1.0 as tier releases fund the
exchange pool, since the ATOX cap and the pool are both 1 trillion tokens.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runQuery(cmd, func(q types.QueryClient) (*types.QueryGlobalStateResponse, error) {
				return q.GlobalState(cmd.Context(), &types.QueryGlobalStateRequest{})
			})
		},
	}
	flags.AddQueryFlagsToCmd(cmd)
	return cmd
}

func GetCmdQueryAccount() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "account [address]",
		Short: "Query an account's ATOX position and convertible ATOS",
		Long: `Query an account's ATOX position.

Wallets should display "claimable" as the convertible-to-ATOS figure. "pending"
alone understates it: the span since the account was last settled has real value
that simply has not been booked yet.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runQuery(cmd, func(q types.QueryClient) (*types.QueryAccountResponse, error) {
				return q.Account(cmd.Context(), &types.QueryAccountRequest{Address: args[0]})
			})
		},
	}
	flags.AddQueryFlagsToCmd(cmd)
	return cmd
}

func GetCmdQueryExchangePool() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exchange-pool",
		Short: "Query the ATOS pool backing outstanding ATOX",
		Long: `Query the ATOS pool backing outstanding ATOX.

"balance" is the real bank balance and "outstanding" is what has been settled but
not yet paid, so anyone can check solvency directly rather than trusting the
module's running totals: balance must always cover outstanding.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runQuery(cmd, func(q types.QueryClient) (*types.QueryExchangePoolResponse, error) {
				return q.ExchangePool(cmd.Context(), &types.QueryExchangePoolRequest{})
			})
		},
	}
	flags.AddQueryFlagsToCmd(cmd)
	return cmd
}
