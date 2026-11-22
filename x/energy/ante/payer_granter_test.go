package ante_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	energyante "github.com/atoshi-chain/atoshi/v20/x/energy/ante"
	"github.com/atoshi-chain/atoshi/v20/x/energy/keeper"
	"github.com/atoshi-chain/atoshi/v20/x/energy/types"
)

// fakeFeegrant records UseGrantedFees calls and answers with err.
type fakeFeegrant struct {
	err   error
	calls []sdk.Coins
}

func (f *fakeFeegrant) UseGrantedFees(_ context.Context, _, _ sdk.AccAddress, fee sdk.Coins, _ []sdk.Msg) error {
	f.calls = append(f.calls, fee)
	return f.err
}

// captureAnte is a terminal AnteHandler that keeps the ctx it was handed,
// so a test can read the values the decorator stashed for the PostHandler.
func captureAnte(out *sdk.Context) sdk.AnteHandler {
	return func(ctx sdk.Context, _ sdk.Tx, _ bool) (sdk.Context, error) {
		*out = ctx
		return ctx, nil
	}
}

// payerAndGranter funds both accounts with 30k ATOS (50k energy capacity)
// and advances 24h so both hold a full 50k of TxEnergy.
func payerAndGranter(t *testing.T) (keeper.Keeper, *fakeBank, fakeAccountKeeper, sdk.Context, sdk.AccAddress, sdk.AccAddress) {
	t.Helper()
	k, bank, ak, ctx := newTestEnv(t)
	payer := sdk.AccAddress([]byte("payer___________________"))
	granter := sdk.AccAddress([]byte("granter_________________"))
	for _, a := range []sdk.AccAddress{payer, granter} {
		ak.exists[a.String()] = true
		bank.balances[a.String()] = math.NewIntWithDecimal(30_000, 18)
		k.Settle(ctx, a)
	}
	ctx = ctx.WithBlockTime(ctx.BlockTime().Add(24 * time.Hour))
	require.EqualValues(t, 50_000, k.Settle(ctx, payer).TxEnergyAccrued)
	require.EqualValues(t, 50_000, k.Settle(ctx, granter).TxEnergyAccrued)
	return k, bank, ak, ctx, payer, granter
}

func sendTx(payer, granter sdk.AccAddress, gas uint64) fakeFeeTx {
	return fakeFeeTx{
		gas:        gas,
		fee:        sdk.NewCoins(sdk.NewCoin("liao", math.NewInt(int64(gas)))), // 1 liao/gas
		feePayer:   payer,
		feeGranter: granter,
		msgs:       []sdk.Msg{mockMsg{typeURL: "/cosmos.bank.v1beta1.MsgSend"}},
	}
}

// H6: energy covers the whole gas limit. Before the fix the decorator
// consumed the GRANTER's energy and never asked x/feegrant (that check sat
// on the ATOS branch only), so any tx could burn any account's energy by
// naming it as granter.
func TestDecorator_GranterEnergyIsNeverConsumed(t *testing.T) {
	k, bank, ak, ctx, payer, granter := payerAndGranter(t)
	fg := &fakeFeegrant{err: errors.New("fee-grant not found")}
	d := energyante.NewEnergyDeductDecorator(k, ak, bank, fg, dummyTxFeeChecker)

	var got sdk.Context
	_, err := d.AnteHandle(ctx, sendTx(payer, granter, 21_000), false, captureAnte(&got))
	require.NoError(t, err, "energy covers the gas; the granter is not needed at all")

	require.EqualValues(t, 50_000, k.GetEnergyAccount(ctx, granter).TxEnergyAccrued,
		"granter's energy must not be touched")
	require.EqualValues(t, 50_000-21_000, k.GetEnergyAccount(ctx, payer).TxEnergyAccrued,
		"energy comes from the payer")
	require.Empty(t, fg.calls, "nothing owed in ATOS, so no allowance is spent")
	require.Nil(t, bank.lastAmt)
	require.Equal(t, payer, got.Value(energyante.CtxKeyEnergySigner),
		"PostHandler must refund the account the energy was taken from")
}

// H6: when the payer's energy falls short, the granter can cover only the
// ATOS part, and only if x/feegrant accepts. A rejected allowance aborts the
// tx; the granter's energy is untouched either way.
func TestDecorator_GranterWithoutAllowanceIsRejected(t *testing.T) {
	k, bank, ak, ctx, payer, granter := payerAndGranter(t)
	fg := &fakeFeegrant{err: errors.New("fee-grant not found")}
	d := energyante.NewEnergyDeductDecorator(k, ak, bank, fg, dummyTxFeeChecker)

	_, err := d.AnteHandle(ctx, sendTx(payer, granter, 100_000), false, terminalAnte)
	require.ErrorContains(t, err, "fee-grant not found")
	require.Len(t, fg.calls, 1)
	require.Nil(t, bank.lastAmt, "no ATOS may move")
	require.EqualValues(t, 50_000, k.GetEnergyAccount(ctx, granter).TxEnergyAccrued)
}

// H6: with an accepted allowance the granter pays the ATOS shortfall --
// exactly the shortfall, debited through UseGrantedFees -- and still none
// of its energy.
func TestDecorator_GranterPaysShortfallThroughFeegrant(t *testing.T) {
	k, bank, ak, ctx, payer, granter := payerAndGranter(t)
	fg := &fakeFeegrant{}
	d := energyante.NewEnergyDeductDecorator(k, ak, bank, fg, dummyTxFeeChecker)

	var got sdk.Context
	_, err := d.AnteHandle(ctx, sendTx(payer, granter, 100_000), false, captureAnte(&got))
	require.NoError(t, err)

	want := sdk.NewCoins(sdk.NewCoin("liao", math.NewInt(50_000))) // 50k shortfall x 1 liao/gas
	require.Len(t, fg.calls, 1)
	require.Equal(t, want, fg.calls[0], "the allowance is charged the shortfall only")
	require.Equal(t, granter, bank.lastFrom, "the granter pays the ATOS")
	require.Equal(t, want, bank.lastAmt)
	require.EqualValues(t, 50_000, k.GetEnergyAccount(ctx, granter).TxEnergyAccrued,
		"granter's energy must not be touched")
	require.EqualValues(t, 0, k.GetEnergyAccount(ctx, payer).TxEnergyAccrued)
	require.Equal(t, payer, got.Value(energyante.CtxKeyEnergySigner))
}

// H6: no feegrant keeper wired -> a tx that needs the granter is refused.
func TestDecorator_GranterWithoutFeegrantModuleIsRejected(t *testing.T) {
	k, bank, ak, ctx, payer, granter := payerAndGranter(t)
	d := energyante.NewEnergyDeductDecorator(k, ak, bank, nil, dummyTxFeeChecker)

	_, err := d.AnteHandle(ctx, sendTx(payer, granter, 100_000), false, terminalAnte)
	require.ErrorContains(t, err, "fee grants are not enabled")
	require.EqualValues(t, 50_000, k.GetEnergyAccount(ctx, granter).TxEnergyAccrued)
}

// H7: a subsidized msg over the gas cap, or batched, is charged normally.
// The zero-balance spam shape from the audit: one MsgReportPrice, gas 30M.
func TestDecorator_SubsidyNeedsOneMsgUnderTheCap(t *testing.T) {
	report := mockMsg{typeURL: "/atoshi.oracle.v1.MsgReportPrice"}
	cases := []struct {
		name string
		gas  uint64
		msgs []sdk.Msg
		free bool
	}{
		{"one msg at the cap", types.MaxSubsidizedTxGas, []sdk.Msg{report}, true},
		{"one msg over the cap", 30_000_000, []sdk.Msg{report}, false},
		{"two subsidized msgs", 21_000, []sdk.Msg{report, report}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k, bank, ak, ctx := newTestEnv(t)
			spammer := sdk.AccAddress([]byte("spammer_________________"))
			ak.exists[spammer.String()] = true // exists, holds nothing

			d := energyante.NewEnergyDeductDecorator(k, ak, bank, nil, dummyTxFeeChecker)
			var got sdk.Context
			_, err := d.AnteHandle(ctx, fakeFeeTx{gas: tc.gas, feePayer: spammer, msgs: tc.msgs}, false, captureAnte(&got))
			require.NoError(t, err) // the fake bank does not enforce balances
			res := got.Value(energyante.CtxKeyEnergyReserved).(keeper.ConsumeResult)
			require.Equal(t, tc.free, res.Free)
			if tc.free {
				require.Nil(t, bank.lastAmt)
			} else {
				require.NotNil(t, bank.lastAmt, "a non-free tx with no energy must be charged ATOS")
				require.Equal(t, spammer, bank.lastFrom)
				require.EqualValues(t, tc.gas, res.ShortfallGas)
			}
		})
	}
}
