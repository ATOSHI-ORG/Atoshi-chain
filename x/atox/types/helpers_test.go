package types_test

import (
	"bytes"
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/atoshi-chain/atoshi/v20/x/atox/types"
)

// cap1T is the production ATOX supply cap: 1 trillion ATOX in aatox.
var cap1T = math.NewIntWithDecimal(1, 30)

func TestComputeIndexDelta_TierRelease(t *testing.T) {
	// A tier release of 1 billion ATOS against a 1-trillion-ATOX cap must give
	// every ATOX a claim on 1e9/1e12 = 0.001 ATOS.
	released := math.NewIntWithDecimal(1, 27) // 1e9 ATOS in liao

	delta, rem, err := types.ComputeIndexDelta(released, math.ZeroInt(), cap1T)
	require.NoError(t, err)
	require.Equal(t, "0.001000000000000000", delta.String())
	require.True(t, rem.IsZero(), "exact division should leave no remainder")

	// One ATOX (1e18 aatox) is therefore owed 0.001 ATOS (1e15 liao).
	owed := types.ComputeOwed(math.NewIntWithDecimal(1, 18), delta)
	require.Equal(t, math.NewIntWithDecimal(1, 15).String(), owed.String())
}

// TestComputeIndexDelta_RemainderIsExact is the regression test for the leak the
// remainder exists to prevent: three releases that each truncate must still sum
// to the full amount released.
func TestComputeIndexDelta_RemainderIsExact(t *testing.T) {
	// cap=3 forces 1/3 per release, which is not representable at 18 decimals.
	smallCap := math.NewInt(3)
	one := math.NewInt(1)

	total := math.LegacyZeroDec()
	rem := math.ZeroInt()
	for i := 0; i < 3; i++ {
		var delta math.LegacyDec
		var err error
		delta, rem, err = types.ComputeIndexDelta(one, rem, smallCap)
		require.NoError(t, err)
		total = total.Add(delta)
	}

	// 3 units released against a cap of 3 must move the index by exactly 1.0.
	// Without the carried remainder this lands on 0.999999999999999999.
	require.Equal(t, "1.000000000000000000", total.String())
	require.True(t, rem.IsZero(), "remainder should be consumed exactly")
}

func TestComputeIndexDelta_SubTickReleaseIsNotLost(t *testing.T) {
	// An amount too small to move the index by one tick yields delta 0, but the
	// value must survive in the remainder rather than vanish.
	delta, rem, err := types.ComputeIndexDelta(math.NewInt(1), math.ZeroInt(), cap1T)
	require.NoError(t, err)
	require.True(t, delta.IsZero())
	require.Equal(t, types.IndexPrecision.String(), rem.String())
}

func TestComputeOwed_TruncatesDown(t *testing.T) {
	// delta small enough that balance*delta is fractional; must floor, never
	// round up, or the sum of payouts can exceed what was released.
	delta := math.LegacyMustNewDecFromStr("0.000000000000000001") // 1 tick
	require.Equal(t, "0", types.ComputeOwed(math.NewInt(999_999_999_999_999_999), delta).String())
	require.Equal(t, "1", types.ComputeOwed(math.NewIntWithDecimal(1, 18), delta).String())
}

func TestComputeOwed_ZeroAndNegativeInputs(t *testing.T) {
	delta := math.LegacyMustNewDecFromStr("0.5")
	require.True(t, types.ComputeOwed(math.ZeroInt(), delta).IsZero())
	require.True(t, types.ComputeOwed(math.Int{}, delta).IsZero())
	require.True(t, types.ComputeOwed(math.NewInt(100), math.LegacyZeroDec()).IsZero())
	require.True(t, types.ComputeOwed(math.NewInt(100), math.LegacyDec{}).IsZero())
}

func TestComputeIndexDelta_RejectsBadInput(t *testing.T) {
	_, _, err := types.ComputeIndexDelta(math.NewInt(-1), math.ZeroInt(), cap1T)
	require.ErrorIs(t, err, types.ErrInvalidAmount)

	_, _, err = types.ComputeIndexDelta(math.NewInt(1), math.ZeroInt(), math.ZeroInt())
	require.Error(t, err, "zero supply cap would divide by zero")

	_, _, err = types.ComputeIndexDelta(math.NewInt(1), math.NewInt(-1), cap1T)
	require.Error(t, err, "negative remainder would inflate the index")
}

// TestSolvency_TotalOwedNeverExceedsReleased is the core safety property, at the
// arithmetic level: however ATOX is distributed among holders, the sum of what
// they are owed cannot exceed what was released into the pool.
//
// This is what makes the fixed-cap denominator safe. Dividing by live supply
// instead would let holders during an early release claim far more than the
// release, which is the over-issuance failure this design exists to avoid.
func TestSolvency_TotalOwedNeverExceedsReleased(t *testing.T) {
	released := math.NewIntWithDecimal(5, 28) // 50 billion ATOS
	delta, _, err := types.ComputeIndexDelta(released, math.ZeroInt(), cap1T)
	require.NoError(t, err)

	// Live supply is only 40% of the cap, split unevenly across holders.
	liveSupply := cap1T.MulRaw(40).QuoRaw(100)
	balances := []math.Int{
		liveSupply.MulRaw(50).QuoRaw(100),
		liveSupply.MulRaw(30).QuoRaw(100),
		liveSupply.MulRaw(19).QuoRaw(100),
		liveSupply.MulRaw(1).QuoRaw(100),
	}

	totalOwed := math.ZeroInt()
	for _, b := range balances {
		totalOwed = totalOwed.Add(types.ComputeOwed(b, delta))
	}

	require.True(t, totalOwed.LTE(released),
		"total owed %s must not exceed released %s", totalOwed, released)

	// With 40% of the cap minted, holders collectively claim 40% of the release
	// and the rest stays in the pool for ATOX not yet mined.
	require.Equal(t, released.MulRaw(40).QuoRaw(100).String(), totalOwed.String())
}

// TestSolvency_RepeatedTransfersCannotInflate is the adversarial case: a fixed
// pot of ATOX passed through many hands, settling at every hop, must never yield
// more ATOS in total than a single holder sitting still would have received.
//
// This is the failure mode a naive "balance * ratio at claim time" scheme has:
// there, each new holder claims the full ratio again, so 90 hops extract ~20x
// the pool. Settling both sides against the pre-transfer balance is what caps it.
func TestSolvency_RepeatedTransfersCannotInflate(t *testing.T) {
	released := math.NewIntWithDecimal(1, 28) // 10 billion ATOS
	delta, _, err := types.ComputeIndexDelta(released, math.ZeroInt(), cap1T)
	require.NoError(t, err)

	pot := cap1T.QuoRaw(100) // 1% of the cap changes hands

	// Baseline: one holder never moves, settles once over the whole span.
	baseline := types.ComputeOwed(pot, delta)

	// Adversarial: the pot moves 90 times. Each hop settles the sender over the
	// span accrued so far and hands the receiver a fresh index, so the receiver
	// earns only over the remaining span. Model that by splitting the span.
	hops := 90
	perHop := delta.QuoInt64(int64(hops))
	totalExtracted := math.ZeroInt()
	for i := 0; i < hops; i++ {
		totalExtracted = totalExtracted.Add(types.ComputeOwed(pot, perHop))
	}

	require.True(t, totalExtracted.LTE(baseline),
		"90 hops extracted %s but a stationary holder gets %s", totalExtracted, baseline)
	require.True(t, totalExtracted.LTE(released))
}

func TestDefaultParams_Valid(t *testing.T) {
	p := types.DefaultParams()
	require.NoError(t, p.Validate())
	require.Equal(t, cap1T.String(), p.SupplyCap.String())

	// The cap and the exchange pool are both 1 trillion tokens, so a fully
	// released pool converts one ATOX to exactly one ATOS: index tops out at 1.0.
	fullPool := math.NewIntWithDecimal(1, 30)
	delta, rem, err := types.ComputeIndexDelta(fullPool, math.ZeroInt(), p.SupplyCap)
	require.NoError(t, err)
	require.Equal(t, "1.000000000000000000", delta.String())
	require.True(t, rem.IsZero())
	require.Equal(t,
		math.NewIntWithDecimal(1, 18).String(),
		types.ComputeOwed(math.NewIntWithDecimal(1, 18), delta).String(),
		"1 ATOX should convert to exactly 1 ATOS at full release")
}

func TestParams_Validate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*types.Params)
		errStr string
	}{
		{"nil supply cap", func(p *types.Params) { p.SupplyCap = math.Int{} }, "supply cap must be positive"},
		{"zero supply cap", func(p *types.Params) { p.SupplyCap = math.ZeroInt() }, "supply cap must be positive"},
		{"negative supply cap", func(p *types.Params) { p.SupplyCap = math.NewInt(-1) }, "supply cap must be positive"},
		{"sweep too large", func(p *types.Params) { p.AutoSettlePerBlock = types.MaxAutoSettlePerBlock + 1 }, "auto settle per block"},
		{"nil min payout", func(p *types.Params) { p.MinAutoPayout = math.Int{} }, "min auto payout"},
		{"negative min payout", func(p *types.Params) { p.MinAutoPayout = math.NewInt(-1) }, "min auto payout"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := types.DefaultParams()
			tc.mutate(&p)
			require.ErrorContains(t, p.Validate(), tc.errStr)
		})
	}

	// Zero is legal and means "sweep disabled"; holders then rely on MsgClaimAtos.
	p := types.DefaultParams()
	p.AutoSettlePerBlock = 0
	require.NoError(t, p.Validate())
}

func TestGlobalState_ValidateSolvency(t *testing.T) {
	s := types.DefaultGlobalState()
	require.NoError(t, s.Validate())

	s.TotalReleasedToPool = math.NewInt(100)
	s.TotalPending = math.NewInt(60)
	s.TotalPaidOut = math.NewInt(40)
	require.NoError(t, s.Validate(), "booked exactly equal to released is fine")

	s.TotalPaidOut = math.NewInt(41)
	require.ErrorContains(t, s.Validate(), "exceeds total_released_to_pool")
}

func TestGenesis_Validate(t *testing.T) {
	require.NoError(t, types.DefaultGenesisState().Validate())

	// Derive rather than hardcode so the bech32 HRP tracks the SDK config.
	addr := sdk.AccAddress(bytes.Repeat([]byte{1}, 20)).String()

	t.Run("total_pending must match account sum", func(t *testing.T) {
		gs := types.DefaultGenesisState()
		gs.GlobalState.TotalReleasedToPool = math.NewInt(1000)
		gs.GlobalState.TotalPending = math.NewInt(500)
		gs.Accounts = []types.AtoxAccount{{
			Address:      addr,
			Index:        math.LegacyZeroDec(),
			Pending:      math.NewInt(499), // off by one
			TotalClaimed: math.ZeroInt(),
		}}
		require.ErrorContains(t, gs.Validate(), "does not match the sum of account pending")

		gs.Accounts[0].Pending = math.NewInt(500)
		require.NoError(t, gs.Validate())
	})

	t.Run("account index cannot exceed global index", func(t *testing.T) {
		gs := types.DefaultGenesisState()
		gs.GlobalState.GlobalIndex = math.LegacyMustNewDecFromStr("0.5")
		gs.Accounts = []types.AtoxAccount{{
			Address:      addr,
			Index:        math.LegacyMustNewDecFromStr("0.6"),
			Pending:      math.ZeroInt(),
			TotalClaimed: math.ZeroInt(),
		}}
		require.ErrorContains(t, gs.Validate(), "exceeds global_index")
	})

	t.Run("duplicate accounts rejected", func(t *testing.T) {
		gs := types.DefaultGenesisState()
		acct := types.NewAtoxAccount(addr, math.LegacyZeroDec())
		gs.Accounts = []types.AtoxAccount{acct, acct}
		require.ErrorContains(t, gs.Validate(), "duplicate address")
	})
}
