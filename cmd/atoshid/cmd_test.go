package main_test

import (
	"fmt"
	"testing"

	"github.com/cosmos/cosmos-sdk/client/flags"
	svrcmd "github.com/cosmos/cosmos-sdk/server/cmd"
	"github.com/cosmos/cosmos-sdk/x/genutil/client/cli"
	"github.com/stretchr/testify/require"

	"github.com/atoshi-chain/atoshi/v20/app"
	atoshid "github.com/atoshi-chain/atoshi/v20/cmd/atoshid"
	"github.com/atoshi-chain/atoshi/v20/utils"
)

func TestInitCmd(t *testing.T) {
	target := t.TempDir()

	rootCmd, _ := atoshid.NewRootCmd()
	rootCmd.SetArgs([]string{
		"init",       // Test the init cmd
		"evmos-test", // Moniker
		fmt.Sprintf("--home=%s", target),
		fmt.Sprintf("--%s=%s", cli.FlagOverwrite, "true"), // Overwrite genesis.json, in case it already exists
		fmt.Sprintf("--%s=%s", flags.FlagChainID, utils.TestnetChainID+"-1"),
	})

	err := svrcmd.Execute(rootCmd, "atoshid", app.DefaultNodeHome)
	require.NoError(t, err)
}

func TestAddKeyLedgerCmd(t *testing.T) {
	rootCmd, _ := atoshid.NewRootCmd()
	rootCmd.SetArgs([]string{
		"keys",
		"add",
		"dev0",
		fmt.Sprintf("--%s", flags.FlagUseLedger),
	})

	err := svrcmd.Execute(rootCmd, "ATOSHID", app.DefaultNodeHome)
	require.Error(t, err)
}
