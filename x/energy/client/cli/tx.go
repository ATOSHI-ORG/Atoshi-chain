package cli

import (
	"fmt"
	"strconv"
	"time"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/client/tx"
	"github.com/spf13/cobra"

	"github.com/atoshi-chain/atoshi/v20/x/energy/types"
)

// NewTxCmd is the root for `atoshid tx energy ...`.
func NewTxCmd() *cobra.Command {
	txCmd := &cobra.Command{
		Use:                        types.ModuleName,
		Short:                      "Energy module transactions (delegate / undelegate)",
		DisableFlagParsing:         true,
		SuggestionsMinimumDistance: 2,
		RunE:                       client.ValidateCmd,
	}
	txCmd.AddCommand(
		NewDelegateEnergyCmd(),
		NewUndelegateEnergyCmd(),
	)
	return txCmd
}

// NewDelegateEnergyCmd lends TxEnergy from --from to DELEGATEE for DURATION.
//
// Usage:
//
//	atoshid tx energy delegate <delegatee> <amount> <duration>
//
// where:
//
//	amount   = TxEnergy in gas units (e.g. 200000)
//	duration = Go duration (e.g. 24h, 7d, 720h)
func NewDelegateEnergyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delegate DELEGATEE AMOUNT DURATION",
		Short: "Lend TxEnergy to another address for a fixed duration; backing ATOS is frozen until expiry",
		Long: `Lend TxEnergy from --from to DELEGATEE.
The corresponding ATOS in --from's bank balance is frozen for DURATION
and cannot be transferred. AMOUNT is gas units (e.g. 200000 covers
about 4 ERC20 transfers); DURATION is a Go duration string (24h, 720h).`,
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			cliCtx, err := client.GetClientTxContext(cmd)
			if err != nil {
				return err
			}
			amount, err := strconv.ParseUint(args[1], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid amount: %w", err)
			}
			dur, err := time.ParseDuration(args[2])
			if err != nil {
				return fmt.Errorf("invalid duration: %w", err)
			}
			msg := &types.MsgDelegateEnergy{
				Delegator:       cliCtx.GetFromAddress().String(),
				Delegatee:       args[0],
				Amount:          amount,
				DurationSeconds: int64(dur.Seconds()),
			}
			return tx.GenerateOrBroadcastTxCLI(cliCtx, cmd.Flags(), msg)
		},
	}
	flags.AddTxFlagsToCmd(cmd)
	return cmd
}

// NewUndelegateEnergyCmd cancels an outbound delegation. Only the
// original delegator may call. Frees the locked ATOS, removes any
// unused energy from the delegatee.
func NewUndelegateEnergyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "undelegate DELEGATION_ID",
		Short: "Cancel one of your outbound energy delegations and reclaim the frozen ATOS",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cliCtx, err := client.GetClientTxContext(cmd)
			if err != nil {
				return err
			}
			id, err := strconv.ParseUint(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid delegation id: %w", err)
			}
			msg := &types.MsgUndelegateEnergy{
				Delegator:    cliCtx.GetFromAddress().String(),
				DelegationId: id,
			}
			return tx.GenerateOrBroadcastTxCLI(cliCtx, cmd.Flags(), msg)
		},
	}
	flags.AddTxFlagsToCmd(cmd)
	return cmd
}
