package keeper

import (
	"context"
	"errors"
	"testing"
	"time"

	"cosmossdk.io/log"
	"cosmossdk.io/math"
	"cosmossdk.io/store"
	"cosmossdk.io/store/metrics"
	storetypes "cosmossdk.io/store/types"
	tmproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/atoshi-chain/atoshi/v20/x/energy/types"
)

// fakeStaking stands in for x/staking's two aggregate delegation reads.
type fakeStaking struct {
	bonded       map[string]math.Int
	unbonding    map[string]math.Int
	bondedErr    error
	unbondingErr error
}

func newFakeStaking() *fakeStaking {
	return &fakeStaking{bonded: map[string]math.Int{}, unbonding: map[string]math.Int{}}
}

func (s *fakeStaking) GetDelegatorBonded(_ context.Context, addr sdk.AccAddress) (math.Int, error) {
	if s.bondedErr != nil {
		return math.Int{}, s.bondedErr
	}
	if v, ok := s.bonded[addr.String()]; ok {
		return v, nil
	}
	return math.ZeroInt(), nil
}

func (s *fakeStaking) GetDelegatorUnbonding(_ context.Context, addr sdk.AccAddress) (math.Int, error) {
	if s.unbondingErr != nil {
		return math.Int{}, s.unbondingErr
	}
	if v, ok := s.unbonding[addr.String()]; ok {
		return v, nil
	}
	return math.ZeroInt(), nil
}

// newKeeperWithStaking mirrors newKeeperForTest but wires a staking keeper.
func newKeeperWithStaking(t *testing.T) (Keeper, sdk.Context, *fakeBank, *fakeStaking) {
	t.Helper()

	storeKey := storetypes.NewKVStoreKey(types.StoreKey)
	db := dbm.NewMemDB()
	cms := store.NewCommitMultiStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	cms.MountStoreWithDB(storeKey, storetypes.StoreTypeIAVL, db)
	require.NoError(t, cms.LoadLatestVersion())

	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)

	bank := newFakeBank("liao")
	staking := newFakeStaking()
	k := NewKeeper(cdc, storeKey, fakeAccountKeeper{}, bank, staking, nil,
		sdk.AccAddress([]byte("authority")).String(), "liao")

	header := tmproto.Header{Time: time.Unix(1_700_000_000, 0)}
	ctx := sdk.NewContext(cms, header, false, log.NewNopLogger())
	require.NoError(t, k.SetParams(ctx, types.DefaultParams()))
	return k, ctx, bank, staking
}

func atos(n int64) math.Int {
	return math.NewIntWithDecimal(1, 18).MulRaw(n)
}

// TestEligibleBalance_SumsAllFourTerms pins the definition: energy eligibility
// is everything the holder owns, wherever the coins are parked.
func TestEligibleBalance_SumsAllFourTerms(t *testing.T) {
	k, ctx, bank, staking := newKeeperWithStaking(t)
	addr := sdk.AccAddress([]byte("holder--------------"))

	bank.balances[addr.String()] = atos(1_000)
	staking.bonded[addr.String()] = atos(2_000)
	staking.unbonding[addr.String()] = atos(4_000)

	acct := k.GetEnergyAccount(ctx, addr)
	acct.LockedAtos = atos(8_000)
	k.SetEnergyAccount(ctx, acct)

	require.Equal(t, atos(15_000).String(), k.EligibleBalance(ctx, addr).String())
}

// TestEligibleBalance_StakingIsCapNeutral is the behaviour the user asked for:
// staked ATOS still belongs to the account, so moving it into the bonded pool
// must not change energy capacity.
//
// Before this change a holder who staked their entire balance saw bank drop to
// zero, capacity drop to zero, and their accrued energy clamped away — forcing a
// choice between mining ATOX and keeping subsidised transfers.
func TestEligibleBalance_StakingIsCapNeutral(t *testing.T) {
	k, ctx, bank, staking := newKeeperWithStaking(t)
	addr := sdk.AccAddress([]byte("staker--------------"))
	params := k.GetParams(ctx)

	// Liquid: exactly one holding threshold.
	bank.balances[addr.String()] = atos(30_000)
	liquidCap := types.TxEnergyCapacity(k.EligibleBalance(ctx, addr), params)
	require.Equal(t, uint64(50_000), liquidCap)

	// Stake all of it: coins move to bonded_tokens_pool, bank goes to zero.
	bank.balances[addr.String()] = math.ZeroInt()
	staking.bonded[addr.String()] = atos(30_000)

	require.Equal(t, atos(30_000).String(), k.EligibleBalance(ctx, addr).String(),
		"staked ATOS is still the holder's, so eligibility must not move")
	require.Equal(t, liquidCap, types.TxEnergyCapacity(k.EligibleBalance(ctx, addr), params),
		"capacity must be unchanged by staking")
}

// TestEligibleBalance_NoUnbondingBlackout covers the 21-day gap: unbonding coins
// are in not_bonded_tokens_pool, so they are in neither the bank balance nor the
// bonded total. Counting only bonded would zero the holder's energy for the whole
// unbonding period.
func TestEligibleBalance_NoUnbondingBlackout(t *testing.T) {
	k, ctx, bank, staking := newKeeperWithStaking(t)
	addr := sdk.AccAddress([]byte("unbonder------------"))
	params := k.GetParams(ctx)

	bank.balances[addr.String()] = math.ZeroInt()
	staking.bonded[addr.String()] = atos(30_000)
	bondedCap := types.TxEnergyCapacity(k.EligibleBalance(ctx, addr), params)

	// Begin undelegating everything: bonded -> unbonding.
	staking.bonded[addr.String()] = math.ZeroInt()
	staking.unbonding[addr.String()] = atos(30_000)

	require.Equal(t, bondedCap, types.TxEnergyCapacity(k.EligibleBalance(ctx, addr), params),
		"starting to unbond must not zero capacity for the unbonding period")

	// Unbonding completes: coins land back in the bank.
	staking.unbonding[addr.String()] = math.ZeroInt()
	bank.balances[addr.String()] = atos(30_000)
	require.Equal(t, bondedCap, types.TxEnergyCapacity(k.EligibleBalance(ctx, addr), params),
		"the full stake -> unbond -> liquid round trip is capacity-neutral")
}

func TestEligibleBalance_NilStakingKeeperIsSafe(t *testing.T) {
	// newKeeperForTest passes nil for the staking keeper; energy must still work
	// for unit tests and any deployment that has not wired staking.
	k, ctx, bank := newKeeperForTest(t)
	addr := sdk.AccAddress([]byte("nostaking-----------"))

	bank.balances[addr.String()] = atos(500)
	acct := k.GetEnergyAccount(ctx, addr)
	acct.LockedAtos = atos(250)
	k.SetEnergyAccount(ctx, acct)

	require.Equal(t, atos(750).String(), k.EligibleBalance(ctx, addr).String())
}

// TestEligibleBalance_StakingReadErrorDegradesQuietly documents the failure
// policy: a corrupt delegation record must not halt the chain from inside a bank
// SendRestriction. The term is dropped, which can only understate capacity and so
// can never hand out gas that was not earned.
func TestEligibleBalance_StakingReadErrorDegradesQuietly(t *testing.T) {
	k, ctx, bank, staking := newKeeperWithStaking(t)
	addr := sdk.AccAddress([]byte("corrupt-------------"))

	bank.balances[addr.String()] = atos(1_000)
	staking.bonded[addr.String()] = atos(9_000)
	staking.unbonding[addr.String()] = atos(500)

	staking.bondedErr = errors.New("corrupt delegation record")
	require.NotPanics(t, func() {
		got := k.EligibleBalance(ctx, addr)
		// bonded dropped, unbonding still counted.
		require.Equal(t, atos(1_500).String(), got.String())
	})

	staking.unbondingErr = errors.New("corrupt unbonding record")
	require.NotPanics(t, func() {
		require.Equal(t, atos(1_000).String(), k.EligibleBalance(ctx, addr).String(),
			"both staking terms dropped, bank balance still honoured")
	})
}

// TestEligibleBalance_NilAndZeroStakingValues guards against a staking keeper
// returning an uninitialised math.Int, which would panic on Add.
func TestEligibleBalance_NilAndZeroStakingValues(t *testing.T) {
	k, ctx, bank, staking := newKeeperWithStaking(t)
	addr := sdk.AccAddress([]byte("nilvalues-----------"))

	bank.balances[addr.String()] = atos(100)
	staking.bonded[addr.String()] = math.Int{}    // nil
	staking.unbonding[addr.String()] = math.Int{} // nil

	require.NotPanics(t, func() {
		require.Equal(t, atos(100).String(), k.EligibleBalance(ctx, addr).String())
	})
}

// TestEligibleBalance_TransfersStillMoveEligibility makes sure widening the
// definition did not make eligibility insensitive to what it must track: a plain
// bank transfer out still reduces it.
func TestEligibleBalance_TransfersStillMoveEligibility(t *testing.T) {
	k, ctx, bank, staking := newKeeperWithStaking(t)
	addr := sdk.AccAddress([]byte("mover---------------"))

	bank.balances[addr.String()] = atos(60_000)
	staking.bonded[addr.String()] = atos(30_000)
	require.Equal(t, atos(90_000).String(), k.EligibleBalance(ctx, addr).String())

	// Send 60k away; the staked portion is untouched.
	bank.balances[addr.String()] = math.ZeroInt()
	require.Equal(t, atos(30_000).String(), k.EligibleBalance(ctx, addr).String())
}
