package cosmos_test

import (
	"reflect"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/authz"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	ibctransfertypes "github.com/cosmos/ibc-go/v8/modules/apps/transfer/types"
	"github.com/stretchr/testify/require"
	protov2 "google.golang.org/protobuf/proto"

	cosmosante "github.com/atoshi-chain/atoshi/v20/app/ante/cosmos"
)

const atoxDenom = "aatox"

// stubTx carries messages and nothing else; the decorator reads no other part
// of a transaction.
type stubTx struct{ msgs []sdk.Msg }

func (t stubTx) GetMsgs() []sdk.Msg { return t.msgs }

// GetMsgsV2 is never called by this decorator, which reads GetMsgs only.
func (t stubTx) GetMsgsV2() ([]protov2.Message, error) { return nil, nil }

func transferMsg(denom string) *ibctransfertypes.MsgTransfer {
	return &ibctransfertypes.MsgTransfer{
		SourcePort:    "transfer",
		SourceChannel: "channel-0",
		Token:         sdk.NewInt64Coin(denom, 100),
		Sender:        "atoshi1sender",
		Receiver:      "cosmos1receiver",
	}
}

func run(t *testing.T, msgs ...sdk.Msg) error {
	t.Helper()
	d := cosmosante.RejectAtoxIBCDecorator{AtoxDenom: atoxDenom}
	reached := false
	next := func(ctx sdk.Context, _ sdk.Tx, _ bool) (sdk.Context, error) {
		reached = true
		return ctx, nil
	}
	_, err := d.AnteHandle(sdk.Context{}, stubTx{msgs: msgs}, false, next)
	if err == nil {
		require.True(t, reached, "an accepted tx must continue down the chain")
	}
	return err
}

// TestRejectAtoxIBC_BlocksAtox is the whole point: escrowing ATOX would burn
// 10% on the way in and leave the escrow holding a conversion claim nobody can
// exercise.
func TestRejectAtoxIBC_BlocksAtox(t *testing.T) {
	err := run(t, transferMsg(atoxDenom))
	require.ErrorContains(t, err, "cannot be transferred over IBC")
}

// TestRejectAtoxIBC_AllowsEverythingElse — ATOS and any other denom are
// unaffected. This decorator sits in front of every Cosmos tx, so a false
// positive here would be a chain-wide outage.
func TestRejectAtoxIBC_AllowsEverythingElse(t *testing.T) {
	require.NoError(t, run(t, transferMsg("liao")))
	require.NoError(t, run(t, transferMsg("ibc/27394FB092D2ECCD56123C74F36E4C1F926001CEADA9CA97EA622B25F41E5EB2")))
	require.NoError(t, run(t, &banktypes.MsgSend{
		Amount: sdk.NewCoins(sdk.NewInt64Coin(atoxDenom, 100)),
	}), "an ordinary ATOX bank send is fine; only IBC escrow is the problem")
	require.NoError(t, run(t))
}

// TestRejectAtoxIBC_CatchesAtoxInABundle — a tx carries many messages, and
// checking only the first would let ATOX ride along behind a harmless one.
func TestRejectAtoxIBC_CatchesAtoxInABundle(t *testing.T) {
	err := run(t, transferMsg("liao"), transferMsg(atoxDenom))
	require.ErrorContains(t, err, "cannot be transferred over IBC")
}

// execMsg wraps messages the way authz does on the wire: packed into Any, which
// is the only form MsgExec.GetMessages can unpack.
func execMsg(t *testing.T, inner ...sdk.Msg) *authz.MsgExec {
	t.Helper()
	e := authz.NewMsgExec(sdk.AccAddress("grantee-------------"), inner)
	return &e
}

// TestRejectAtoxIBC_SeesThroughAuthz is the bypass this decorator shipped with:
// tx.GetMsgs() returns the MsgExec, not what it carries, so a flat scan misses
// an ATOX transfer that anyone can wrap in one. Grants are permissionless.
func TestRejectAtoxIBC_SeesThroughAuthz(t *testing.T) {
	err := run(t, execMsg(t, transferMsg(atoxDenom)))
	require.ErrorContains(t, err, "cannot be transferred over IBC")
}

// TestRejectAtoxIBC_SeesThroughNestedAuthz — a MsgExec may carry another, so one
// level of unwrapping is not enough.
func TestRejectAtoxIBC_SeesThroughNestedAuthz(t *testing.T) {
	err := run(t, execMsg(t, execMsg(t, execMsg(t, transferMsg(atoxDenom)))))
	require.ErrorContains(t, err, "cannot be transferred over IBC")
}

// TestRejectAtoxIBC_AuthzWithoutAtoxPasses guards the other direction: authz is
// an ordinary feature and must keep working for every other denom.
func TestRejectAtoxIBC_AuthzWithoutAtoxPasses(t *testing.T) {
	require.NoError(t, run(t, execMsg(t, transferMsg("liao"))))
}

// TestRejectAtoxIBC_RejectsRunawayNesting — the walk is recursive, so an
// attacker-chosen depth must be bounded before it reaches the stack. This runs
// before any gas is charged.
func TestRejectAtoxIBC_RejectsRunawayNesting(t *testing.T) {
	msg := sdk.Msg(transferMsg("liao"))
	for i := 0; i < 10; i++ {
		msg = execMsg(t, msg)
	}
	require.ErrorContains(t, run(t, msg), "too many nested authz messages")
}

// TestRejectAtoxIBC_UnconfiguredDenomIsANoOp keeps a bare ante chain in unit
// tests from depending on this.
func TestRejectAtoxIBC_UnconfiguredDenomIsANoOp(t *testing.T) {
	d := cosmosante.RejectAtoxIBCDecorator{}
	reached := false
	_, err := d.AnteHandle(sdk.Context{}, stubTx{msgs: []sdk.Msg{transferMsg(atoxDenom)}}, false,
		func(ctx sdk.Context, _ sdk.Tx, _ bool) (sdk.Context, error) {
			reached = true
			return ctx, nil
		})
	require.NoError(t, err)
	require.True(t, reached)
}

// TestMsgTransferStillCarriesOneToken pins the shape this decorator relies on.
// ibc-go v8.5's MsgTransfer has a single Token field; if a later version adds a
// multi-denom list, checking Token alone would silently stop catching ATOX.
// This fails on that upgrade, which is the point.
func TestMsgTransferStillCarriesOneToken(t *testing.T) {
	ty := reflect.TypeOf(ibctransfertypes.MsgTransfer{})
	_, hasToken := ty.FieldByName("Token")
	require.True(t, hasToken, "MsgTransfer.Token is gone; update the decorator")

	if _, hasTokens := ty.FieldByName("Tokens"); hasTokens {
		t.Fatal("MsgTransfer now carries a Tokens slice: the ATOX check must " +
			"cover it, or ATOX escapes over IBC through the multi-denom path")
	}
}
