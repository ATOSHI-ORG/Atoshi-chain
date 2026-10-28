package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/atoshi-chain/atoshi/v20/testutil/integration/evmos/network"
	atoshitypes "github.com/atoshi-chain/atoshi/v20/types"
)

// TestGenesisRoundTrip_DoesNotRemintTheSupply reproduces what an operator does
// during disaster recovery or a hard fork: `atoshid export`, then start a chain
// from the file that came out.
//
// Module init order puts bank at position 3 and tokenomics at 25 (app.go). So
// bank restores every balance and recomputes the supply from them -- its own
// "genesis supply is incorrect" panic has already passed by the time tokenomics
// runs. If tokenomics then mints its pool totals unconditionally, the three
// pools are the entire 10 trillion ATOS supply, and the chain comes back up
// with 20 trillion against 100 billion ERC20 on Ethereum.
//
// Exporting and re-importing the module in place is the smallest faithful
// reproduction: same two calls, same order, same keeper.
func TestGenesisRoundTrip_DoesNotRemintTheSupply(t *testing.T) {
	nw := network.NewUnitTestNetwork()
	ctx := nw.GetContext()
	k := nw.App.TokenomicsKeeper
	bk := nw.App.BankKeeper

	denom := atoshitypes.BaseDenom

	before := bk.GetSupply(ctx, denom).Amount
	require.True(t, before.IsPositive(), "fixture should have minted the pools once")

	exported := k.ExportGenesis(ctx)
	k.InitGenesis(ctx, *exported)

	after := bk.GetSupply(ctx, denom).Amount
	require.Equal(t, before.String(), after.String(),
		"export/import must not change the supply")
}

// TestGenesisRoundTrip_KeepsTheMigrationClaimSet pins that a claimed migration
// airdrop stays claimed across export/import. The set lives under its own store
// prefix and nothing else records it, so if genesis drops it every address in
// the Merkle tree can claim a second time and drain the migration pool.
func TestGenesisRoundTrip_KeepsTheMigrationClaimSet(t *testing.T) {
	nw := network.NewUnitTestNetwork()
	ctx := nw.GetContext()
	k := nw.App.TokenomicsKeeper

	const claimer = "atoshi1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq"
	k.SetMigrationClaimed(ctx, claimer)
	require.True(t, k.HasMigrationClaimed(ctx, claimer))

	exported := k.ExportGenesis(ctx)

	// Fresh chain, same genesis file.
	nw2 := network.NewUnitTestNetwork()
	ctx2 := nw2.GetContext()
	k2 := nw2.App.TokenomicsKeeper
	k2.InitGenesis(ctx2, *exported)

	require.True(t, k2.HasMigrationClaimed(ctx2, claimer),
		"a claimed address must not be able to claim again after a restore")
}
