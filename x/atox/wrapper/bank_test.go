package wrapper_test

import (
	"context"
	"errors"
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	bankkeeper "github.com/cosmos/cosmos-sdk/x/bank/keeper"
	"github.com/stretchr/testify/require"

	"github.com/atoshi-chain/atoshi/v20/x/atox/wrapper"
)

const denom = "aatox"

var errShort = errors.New("insufficient funds")

type stubBank struct {
	bal    map[string]math.Int
	burned math.Int
}

func newStub() *stubBank { return &stubBank{bal: map[string]math.Int{}, burned: math.ZeroInt()} }

func (s *stubBank) SendCoins(_ context.Context, from, to sdk.AccAddress, amt sdk.Coins) error {
	a := amt.AmountOf(denom)
	cur := s.get(from)
	if cur.LT(a) {
		return errShort
	}
	s.bal[from.String()] = cur.Sub(a)
	s.bal[to.String()] = s.get(to).Add(a)
	return nil
}

func (s *stubBank) SendCoinsFromAccountToModule(_ context.Context, from sdk.AccAddress, _ string, amt sdk.Coins) error {
	a := amt.AmountOf(denom)
	cur := s.get(from)
	if cur.LT(a) {
		return errShort
	}
	s.bal[from.String()] = cur.Sub(a)
	return nil
}

func (s *stubBank) BurnCoins(_ context.Context, _ string, amt sdk.Coins) error {
	s.burned = s.burned.Add(amt.AmountOf(denom))
	return nil
}

func (s *stubBank) get(a sdk.AccAddress) math.Int {
	v, ok := s.bal[a.String()]
	if !ok {
		return math.ZeroInt()
	}
	return v
}

type stubAtox struct {
	bps     uint32
	modules map[string]bool
}

func (s stubAtox) AtoxDenom() string                 { return denom }
func (s stubAtox) TransferFeeBps(sdk.Context) uint32 { return s.bps }
func (s stubAtox) IsModuleAccount(_ sdk.Context, a sdk.AccAddress) bool {
	return s.modules[a.String()]
}

func addr(s string) sdk.AccAddress { return sdk.AccAddress([]byte(s + "-------------------")[:20]) }

// TestInclusiveFee_SenderLosesExactlyTheAmount is the whole point of the model.
func TestInclusiveFee_SenderLosesExactlyTheAmount(t *testing.T) {
	ctx := sdk.Context{}.WithContext(context.Background())
	bank := newStub()
	alice, bob := addr("alice"), addr("bob")
	bank.bal[alice.String()] = math.NewInt(100)

	err := wrapper.ChargeInclusiveFee(ctx, bank, stubAtox{bps: 1000, modules: map[string]bool{}},
		alice, bob, sdk.NewCoins(sdk.NewCoin(denom, math.NewInt(100))))
	require.NoError(t, err)

	require.Equal(t, "0", bank.get(alice).String(), "exactly the requested 100 left the sender")
	require.Equal(t, "90", bank.get(bob).String(), "recipient got the amount minus the fee")
	require.Equal(t, "10", bank.burned.String(), "the fee was burned")
}

// TestInclusiveFee_ModuleLegsAreExempt: ATOX reaches holders through module
// accounts, so taxing those paths would take a cut of every holder's mining
// income before they ever saw it.
func TestInclusiveFee_ModuleLegsAreExempt(t *testing.T) {
	ctx := sdk.Context{}.WithContext(context.Background())
	alice, mod := addr("alice"), addr("module")
	mods := map[string]bool{mod.String(): true}

	for _, tc := range []struct {
		name     string
		from, to sdk.AccAddress
	}{
		{"module sends", mod, alice},
		{"module receives", alice, mod},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bank := newStub()
			bank.bal[tc.from.String()] = math.NewInt(100)
			require.NoError(t, wrapper.ChargeInclusiveFee(ctx, bank,
				stubAtox{bps: 1000, modules: mods}, tc.from, tc.to,
				sdk.NewCoins(sdk.NewCoin(denom, math.NewInt(100)))))
			require.Equal(t, "100", bank.get(tc.to).String(), "full amount, untaxed")
			require.Equal(t, "0", bank.burned.String())
		})
	}
}

// TestInclusiveFee_SelfTransferIsFree: a self-transfer nets to zero, so charging
// it would be a pure toll on a no-op.
func TestInclusiveFee_SelfTransferIsFree(t *testing.T) {
	ctx := sdk.Context{}.WithContext(context.Background())
	bank := newStub()
	alice := addr("alice")
	bank.bal[alice.String()] = math.NewInt(100)

	require.NoError(t, wrapper.ChargeInclusiveFee(ctx, bank,
		stubAtox{bps: 1000, modules: map[string]bool{}}, alice, alice,
		sdk.NewCoins(sdk.NewCoin(denom, math.NewInt(100)))))
	require.Equal(t, "100", bank.get(alice).String())
	require.Equal(t, "0", bank.burned.String())
}

// TestInclusiveFee_NonAtoxUntouched: ATOS and every other denom must not be
// taxed, and must not pay the cost of the lookup either.
func TestInclusiveFee_NonAtoxUntouched(t *testing.T) {
	ctx := sdk.Context{}.WithContext(context.Background())
	bank := newStub()
	alice, bob := addr("alice"), addr("bob")
	bank.bal[alice.String()] = math.NewInt(100)

	require.NoError(t, wrapper.ChargeInclusiveFee(ctx, bank,
		stubAtox{bps: 1000, modules: map[string]bool{}}, alice, bob,
		sdk.NewCoins(sdk.NewCoin("liao", math.NewInt(100)))))
	require.Equal(t, "0", bank.burned.String())
}

// TestFeeInclusiveBank_SatisfiesBankKeeper pins that the wrapper can stand in
// wherever a bankkeeper.Keeper is expected -- the property the whole
// interception depends on.
func TestFeeInclusiveBank_SatisfiesBankKeeper(_ *testing.T) {
	var _ bankkeeper.Keeper = wrapper.FeeInclusiveBank{}
}
