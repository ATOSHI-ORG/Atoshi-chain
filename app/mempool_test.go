package app_test

import (
	"testing"

	"github.com/cosmos/cosmos-sdk/types/mempool"
	"github.com/stretchr/testify/require"
)

// Audit Issue-13 (round2) — POST-REVERT note:
//
// Round 2 of the audit recommended switching from mempool.NoOpMempool
// to mempool.DefaultPriorityMempool() (which returns PriorityNonceMempool).
// The intent was to let the priority that EnergyDeductDecorator writes
// in the ante chain affect mempool ordering — pure-Cosmos chains
// benefit from this immediately.
//
// On a live EVM chain the change broke MetaMask. PriorityNonceMempool.
// Insert calls SignerExtractor.GetSigners(tx); the default adapter
// only implements Cosmos-style signature recovery. EVM transactions
// arrive wrapped as MsgEthereumTx with the eth-style (r,s,v) signature
// inside the message body — the default extractor sees zero Cosmos
// signers and Insert returns "tx must have at least one signer",
// causing every eth_sendRawTransaction call from MetaMask / dApps to
// fail with no tx hash. (Evmos historically used NoOpMempool by design
// for this exact reason.)
//
// We revert to NoOpMempool. The audit Issue-13 finding will be carried
// forward to round-3 with the proper fix: a custom SignerExtractor
// that handles MsgEthereumTx alongside Cosmos signatures, then re-wire
// PriorityNonceMempool with that extractor. Until then this test pins
// the choice so any silent re-introduction of PriorityNonceMempool
// without a custom extractor (which would break MetaMask again) fails
// at CI.
func TestMempool_DefaultIsNoOpMempool(t *testing.T) {
	mp := mempool.NoOpMempool{}
	require.IsType(t, mempool.NoOpMempool{}, mp,
		"audit Issue-13 round-2 revert: NoOpMempool keeps MetaMask working until a custom SignerExtractor lands")
}
