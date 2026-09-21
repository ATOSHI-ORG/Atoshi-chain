package cosmos_test

import (
	"testing"

	"cosmossdk.io/math"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/authz"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"
	protov2 "google.golang.org/protobuf/proto"

	cosmosante "github.com/atoshi-chain/atoshi/v20/app/ante/cosmos"
)

// floor is 100 million ATOS, the production default.
var floor = math.NewIntWithDecimal(1, 26)

type stubTokenomics struct{ floor math.Int }

func (s stubTokenomics) GetValidatorMinSelfDelegation(_ sdk.Context) math.Int { return s.floor }

// selfDelegationTx is the minimum sdk.Tx the decorator needs: it only reads
// GetMsgs.
type selfDelegationTx struct{ msgs []sdk.Msg }

func (t selfDelegationTx) GetMsgs() []sdk.Msg { return t.msgs }
func (t selfDelegationTx) GetMsgsV2() ([]protov2.Message, error) {
	return nil, nil
}

func createValidatorMsg(minSelf, value math.Int) *stakingtypes.MsgCreateValidator {
	return &stakingtypes.MsgCreateValidator{
		Description:       stakingtypes.Description{Moniker: "v"},
		MinSelfDelegation: minSelf,
		ValidatorAddress:  sdk.ValAddress([]byte("validator___________")).String(),
		Value:             sdk.NewCoin("liao", value),
	}
}

func editValidatorMsg(minSelf *math.Int) *stakingtypes.MsgEditValidator {
	return &stakingtypes.MsgEditValidator{
		Description:       stakingtypes.Description{Moniker: "v"},
		ValidatorAddress:  sdk.ValAddress([]byte("validator___________")).String(),
		MinSelfDelegation: minSelf,
	}
}

func runDecorator(t *testing.T, tk cosmosante.TokenomicsKeeper, msgs ...sdk.Msg) error {
	t.Helper()
	d := cosmosante.NewMinSelfDelegationDecorator(tk)
	reached := false
	terminal := func(ctx sdk.Context, _ sdk.Tx, _ bool) (sdk.Context, error) {
		reached = true
		return ctx, nil
	}
	_, err := d.AnteHandle(sdk.Context{}, selfDelegationTx{msgs: msgs}, false, terminal)
	if err == nil {
		require.True(t, reached, "decorator must call next when it accepts the tx")
	}
	return err
}

func TestMinSelfDelegation_CreateValidator(t *testing.T) {
	tk := stubTokenomics{floor: floor}
	below := floor.Sub(math.OneInt())

	t.Run("accepts exactly the floor", func(t *testing.T) {
		require.NoError(t, runDecorator(t, tk, createValidatorMsg(floor, floor)))
	})

	t.Run("accepts above the floor", func(t *testing.T) {
		above := floor.MulRaw(2)
		require.NoError(t, runDecorator(t, tk, createValidatorMsg(above, above)))
	})

	t.Run("rejects a declared minimum below the floor", func(t *testing.T) {
		// This is the case the SDK cannot catch on its own: a default gentx
		// declares min_self_delegation = 1 and x/staking is satisfied by it.
		err := runDecorator(t, tk, createValidatorMsg(math.OneInt(), floor))
		require.Error(t, err)
		require.Contains(t, err.Error(), "min_self_delegation must be at least")
	})

	t.Run("rejects self-stake below the floor even when the declared minimum is fine", func(t *testing.T) {
		// Declaring the floor without funding it would create a validator the
		// SDK unbonds immediately, so both halves have to be checked.
		err := runDecorator(t, tk, createValidatorMsg(floor, below))
		require.Error(t, err)
		require.Contains(t, err.Error(), "must self-delegate at least")
	})
}

func TestMinSelfDelegation_EditValidator(t *testing.T) {
	tk := stubTokenomics{floor: floor}

	t.Run("nil minimum means unchanged and is allowed", func(t *testing.T) {
		require.NoError(t, runDecorator(t, tk, editValidatorMsg(nil)))
	})

	t.Run("rejects lowering below the floor", func(t *testing.T) {
		one := math.OneInt()
		err := runDecorator(t, tk, editValidatorMsg(&one))
		require.Error(t, err)
		require.Contains(t, err.Error(), "min_self_delegation must be at least")
	})

	t.Run("allows raising", func(t *testing.T) {
		high := floor.MulRaw(3)
		require.NoError(t, runDecorator(t, tk, editValidatorMsg(&high)))
	})
}

func TestMinSelfDelegation_ZeroFloorDisablesTheCheck(t *testing.T) {
	tk := stubTokenomics{floor: math.ZeroInt()}
	require.NoError(t, runDecorator(t, tk, createValidatorMsg(math.OneInt(), math.OneInt())))
}

func TestMinSelfDelegation_NilFloorDisablesTheCheck(t *testing.T) {
	// A param read on a chain that has not migrated the field yet yields a nil
	// Int; comparing against it would panic.
	tk := stubTokenomics{floor: math.Int{}}
	require.NoError(t, runDecorator(t, tk, createValidatorMsg(math.OneInt(), math.OneInt())))
}

func TestMinSelfDelegation_SeesThroughAuthzExec(t *testing.T) {
	// Without descending into MsgExec the floor would be trivially bypassable by
	// wrapping the create in an authz grant.
	tk := stubTokenomics{floor: floor}

	inner := createValidatorMsg(math.OneInt(), floor)
	anyMsg, err := codectypes.NewAnyWithValue(inner)
	require.NoError(t, err)

	exec := &authz.MsgExec{
		Grantee: sdk.AccAddress([]byte("grantee_____________")).String(),
		Msgs:    []*codectypes.Any{anyMsg},
	}

	err = runDecorator(t, tk, exec)
	require.Error(t, err)
	require.Contains(t, err.Error(), "min_self_delegation must be at least")
}

func TestMinSelfDelegation_IgnoresUnrelatedMsgs(t *testing.T) {
	tk := stubTokenomics{floor: floor}
	require.NoError(t, runDecorator(t, tk, editValidatorMsg(nil), &stakingtypes.MsgDelegate{
		DelegatorAddress: sdk.AccAddress([]byte("delegator___________")).String(),
		ValidatorAddress: sdk.ValAddress([]byte("validator___________")).String(),
		Amount:           sdk.NewCoin("liao", math.OneInt()),
	}))
}
