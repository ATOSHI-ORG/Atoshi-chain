package cli

import (
	"encoding/hex"
	"fmt"
	"strings"

	"cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/client/tx"
	"github.com/spf13/cobra"

	"github.com/atoshi-chain/atoshi/v20/x/tokenomics/types"
)

// NewTxCmd returns the root tx command for x/tokenomics.
func NewTxCmd() *cobra.Command {
	txCmd := &cobra.Command{
		Use:                        types.ModuleName,
		Short:                      "Tokenomics transactions",
		DisableFlagParsing:         true,
		SuggestionsMinimumDistance: 2,
		RunE:                       client.ValidateCmd,
	}
	txCmd.AddCommand(
		NewClaimProjectTreasuryRewardCmd(),
		NewClaimMigrationTokensCmd(),
	)
	return txCmd
}

// NewClaimProjectTreasuryRewardCmd lets the configured project treasury claim
// unlocked project-pool funds.
func NewClaimProjectTreasuryRewardCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "claim-project-treasury-reward",
		Short: "Claim unlocked project-pool funds (signer must be params.ProjectTreasuryAddress)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cliCtx, err := client.GetClientTxContext(cmd)
			if err != nil {
				return err
			}
			msg := &types.MsgClaimProjectTreasuryReward{
				Authority: cliCtx.GetFromAddress().String(),
			}
			return tx.GenerateOrBroadcastTxCLI(cliCtx, cmd.Flags(), msg)
		},
	}
	flags.AddTxFlagsToCmd(cmd)
	return cmd
}

// NewClaimMigrationTokensCmd redeems pre-mine migration ATOS via Merkle proof.
func NewClaimMigrationTokensCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "claim-migration-tokens AMOUNT PROOF_HEX[,PROOF_HEX...]",
		Short: "Redeem pre-mine migration ATOS using a Merkle proof",
		Long: `AMOUNT is the integer liao amount allocated to your address in the snapshot.
PROOF_HEX is a comma-separated list of sibling node hashes (hex, no 0x), in
order from leaf to root. The leaf hash is computed on-chain from the signer
address and amount.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cliCtx, err := client.GetClientTxContext(cmd)
			if err != nil {
				return err
			}
			amount, ok := math.NewIntFromString(args[0])
			if !ok {
				return fmt.Errorf("invalid amount: %s", args[0])
			}
			parts := strings.Split(args[1], ",")
			proof := make([][]byte, 0, len(parts))
			for i, p := range parts {
				p = strings.TrimSpace(strings.TrimPrefix(p, "0x"))
				bz, err := hex.DecodeString(p)
				if err != nil {
					return fmt.Errorf("invalid proof element %d (%q): %w", i, parts[i], err)
				}
				proof = append(proof, bz)
			}
			msg := &types.MsgClaimMigrationTokens{
				Claimer:     cliCtx.GetFromAddress().String(),
				Amount:      amount,
				MerkleProof: proof,
			}
			return tx.GenerateOrBroadcastTxCLI(cliCtx, cmd.Flags(), msg)
		},
	}
	flags.AddTxFlagsToCmd(cmd)
	return cmd
}
