package cli

import (
	"strconv"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/gogoproto/proto"
	"github.com/spf13/cobra"

	"github.com/atoshi-chain/atoshi/v20/x/energy/types"
)

// GetQueryCmd is the root for `atoshid query energy ...`.
func GetQueryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:                        types.ModuleName,
		Short:                      "Querying commands for the energy module",
		DisableFlagParsing:         true,
		SuggestionsMinimumDistance: 2,
		RunE:                       client.ValidateCmd,
	}
	cmd.AddCommand(
		GetCmdQueryParams(),
		GetCmdQueryAccount(),
		GetCmdQueryDelegations(),
		GetCmdQueryEstimateFee(),
	)
	return cmd
}

func runQuery[Resp proto.Message](
	cmd *cobra.Command, fn func(types.QueryClient) (Resp, error),
) error {
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
		Short: "Query the energy module parameters",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runQuery(cmd, func(qc types.QueryClient) (*types.QueryParamsResponse, error) {
				return qc.Params(cmd.Context(), &types.QueryParamsRequest{})
			})
		},
	}
	flags.AddQueryFlagsToCmd(cmd)
	return cmd
}

func GetCmdQueryAccount() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "account ADDRESS",
		Short: "Query an account's settled energy state and current capacity ceilings",
		Long: `Returns:
- settled.tx_energy_accrued       current spendable TxEnergy
- settled.deploy_energy_accrued   current spendable DeployEnergy
- settled.delegated_in_usable     energy lent to me by others
- settled.delegated_out           energy I lent to others
- settled.locked_atos             ATOS frozen due to outbound delegation
- tx_energy_capacity              upper bound at current bank balance
- deploy_energy_capacity          constant DeployEnergy ceiling`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runQuery(cmd, func(qc types.QueryClient) (*types.QueryAccountResponse, error) {
				return qc.Account(cmd.Context(), &types.QueryAccountRequest{Address: args[0]})
			})
		},
	}
	flags.AddQueryFlagsToCmd(cmd)
	return cmd
}

func GetCmdQueryDelegations() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delegations ADDRESS [direction]",
		Short: "List active energy delegations for ADDRESS (direction: out | in | all, default all)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			direction := "all"
			if len(args) == 2 {
				direction = args[1]
			}
			return runQuery(cmd, func(qc types.QueryClient) (*types.QueryDelegationsResponse, error) {
				return qc.Delegations(cmd.Context(), &types.QueryDelegationsRequest{
					Address:   args[0],
					Direction: direction,
				})
			})
		},
	}
	flags.AddQueryFlagsToCmd(cmd)
	return cmd
}

func GetCmdQueryEstimateFee() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "estimate-fee SIGNER GAS_LIMIT [--deploy]",
		Short: "Predict how much TxEnergy / DeployEnergy / ATOS a tx would cost the signer right now",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			gasLimit, err := strconv.ParseUint(args[1], 10, 64)
			if err != nil {
				return err
			}
			isDeploy, err := cmd.Flags().GetBool("deploy")
			if err != nil {
				return err
			}
			return runQuery(cmd, func(qc types.QueryClient) (*types.QueryEstimateFeeResponse, error) {
				return qc.EstimateFee(cmd.Context(), &types.QueryEstimateFeeRequest{
					Signer:   args[0],
					GasLimit: gasLimit,
					IsDeploy: isDeploy,
				})
			})
		},
	}
	cmd.Flags().Bool("deploy", false, "treat as a contract deployment (draws DeployEnergy first)")
	flags.AddQueryFlagsToCmd(cmd)
	return cmd
}
