package cli

import (
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/gogoproto/proto"
	"github.com/spf13/cobra"

	"github.com/atoshi-chain/atoshi/v20/x/tokenomics/types"
)

// GetQueryCmd returns the root query command for x/tokenomics.
func GetQueryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:                        types.ModuleName,
		Short:                      "Querying commands for the tokenomics module",
		DisableFlagParsing:         true,
		SuggestionsMinimumDistance: 2,
		RunE:                       client.ValidateCmd,
	}
	cmd.AddCommand(
		GetCmdQueryParams(),
		GetCmdQueryReleaseStatus(),
		GetCmdQueryCirculatingSupply(),
		GetCmdQueryBlockReward(),
		GetCmdQueryProjectClaimable(),
		GetCmdQueryMinerLockedBalance(),
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
		Short: "Query tokenomics parameters",
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

func GetCmdQueryReleaseStatus() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "release-status",
		Short: "Query the price-unlock state machine (current tier, consecutive days, totals)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runQuery(cmd, func(qc types.QueryClient) (*types.QueryReleaseStatusResponse, error) {
				return qc.ReleaseStatus(cmd.Context(), &types.QueryReleaseStatusRequest{})
			})
		},
	}
	flags.AddQueryFlagsToCmd(cmd)
	return cmd
}

func GetCmdQueryCirculatingSupply() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "circulating-supply",
		Short: "Query the live circulating ATOS supply",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runQuery(cmd, func(qc types.QueryClient) (*types.QueryCirculatingSupplyResponse, error) {
				return qc.CirculatingSupply(cmd.Context(), &types.QueryCirculatingSupplyRequest{})
			})
		},
	}
	flags.AddQueryFlagsToCmd(cmd)
	return cmd
}

func GetCmdQueryBlockReward() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "block-reward",
		Short: "Query the current per-block miner reward and halving period",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runQuery(cmd, func(qc types.QueryClient) (*types.QueryBlockRewardResponse, error) {
				return qc.BlockReward(cmd.Context(), &types.QueryBlockRewardRequest{})
			})
		},
	}
	flags.AddQueryFlagsToCmd(cmd)
	return cmd
}

func GetCmdQueryProjectClaimable() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project-claimable",
		Short: "Query unclaimed project-pool funds",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runQuery(cmd, func(qc types.QueryClient) (*types.QueryProjectClaimableResponse, error) {
				return qc.ProjectClaimable(cmd.Context(), &types.QueryProjectClaimableRequest{})
			})
		},
	}
	flags.AddQueryFlagsToCmd(cmd)
	return cmd
}

func GetCmdQueryMinerLockedBalance() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "miner-locked-balance VALIDATOR_ADDR",
		Short: "Query a validator's locked-pool accrued / claimable / claimed amounts",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runQuery(cmd, func(qc types.QueryClient) (*types.QueryMinerLockedBalanceResponse, error) {
				return qc.MinerLockedBalance(cmd.Context(), &types.QueryMinerLockedBalanceRequest{ValidatorAddress: args[0]})
			})
		},
	}
	flags.AddQueryFlagsToCmd(cmd)
	return cmd
}
