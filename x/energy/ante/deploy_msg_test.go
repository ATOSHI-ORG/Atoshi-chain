package ante

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	evmtypes "github.com/atoshi-chain/atoshi/v20/x/evm/types"
	"github.com/stretchr/testify/require"
)

// Audit Issue-3 (round1-issue1) regression: MsgEthereumTx must NOT be
// classified as a deploy message by isContractDeployMsg. The Cosmos
// ante chain installs RejectMessagesDecorator before the energy fee
// step (see app/ante/cosmos.go), which rejects MsgEthereumTx outright;
// EVM txs are handled by the EVM ante chain (MonoDecorator) instead.
// The previous code's MsgEthereumTx branch was unreachable in
// production AND misleading — it implied a code path that does not
// exist. Removing it makes the function honest about the chain's
// actual transaction routing.
//
// The fact that any change to ante routing (re-enabling EVM through
// the Cosmos chain, etc.) would need to flip this test serves as an
// active assertion of the dual-ante architecture.
func TestIsContractDeployMsg_DoesNotMatchMsgEthereumTx(t *testing.T) {
	tx := &evmtypes.MsgEthereumTx{}
	require.False(t, isContractDeployMsg([]sdk.Msg{tx}),
		"audit Issue-3: MsgEthereumTx never reaches this decorator (rejected by Cosmos ante upstream); the dead branch must stay removed")
}

// Audit Issue-3 (round1-issue1) companion: a bare MsgSend must not be
// treated as a deploy message either — that would mis-charge the
// DeployEnergy bucket on every regular transfer.
func TestIsContractDeployMsg_DoesNotMatchOrdinaryMsg(t *testing.T) {
	// Use a typed nil so MsgTypeURL still returns the proper URL.
	var msg sdk.Msg = (*evmtypes.MsgUpdateParams)(nil)
	require.False(t, isContractDeployMsg([]sdk.Msg{msg}),
		"non-deploy msg types must return false")
	require.False(t, isContractDeployMsg(nil),
		"nil slice must return false")
	require.False(t, isContractDeployMsg([]sdk.Msg{}),
		"empty slice must return false")
}
