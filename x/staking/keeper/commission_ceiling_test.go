package keeper_test

import (
	"testing"
	"time"

	"cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"

	"github.com/atoshi-chain/atoshi/v20/testutil"
	"github.com/atoshi-chain/atoshi/v20/testutil/integration/evmos/network"
	utiltx "github.com/atoshi-chain/atoshi/v20/testutil/tx"
	atoshitypes "github.com/atoshi-chain/atoshi/v20/types"
	"github.com/atoshi-chain/atoshi/v20/x/staking/keeper"
	tokenomicstypes "github.com/atoshi-chain/atoshi/v20/x/tokenomics/types"
)

func pct(n int64) math.LegacyDec { return math.LegacyNewDecWithPrec(n, 2) }

// setCeiling writes the chain-wide ceiling through tokenomics params, the same
// way a governance proposal would.
func setCeiling(t *testing.T, nw *network.UnitTestNetwork, ctx sdk.Context, rate math.LegacyDec) {
	t.Helper()
	p := nw.App.TokenomicsKeeper.GetParams(ctx)
	p.MaxCommissionRate = rate
	require.NoError(t, nw.App.TokenomicsKeeper.SetParams(ctx, p))
}

// createValidator submits a validator creation with the given commission rates.
func createValidator(
	t *testing.T, nw *network.UnitTestNetwork, ctx sdk.Context,
	rate, maxRate, maxChangeRate math.LegacyDec,
) error {
	t.Helper()

	addr, _ := utiltx.NewAccAddressAndKey()
	selfBond := sdk.NewCoin(atoshitypes.BaseDenom, math.NewIntWithDecimal(2, 26))
	require.NoError(t, testutil.FundAccount(ctx, nw.App.BankKeeper, addr, sdk.NewCoins(selfBond)))

	msg, err := types.NewMsgCreateValidator(
		sdk.ValAddress(addr).String(),
		ed25519.GenPrivKey().PubKey(),
		selfBond,
		types.NewDescription("T", "E", "S", "T", "Z"),
		types.NewCommissionRates(rate, maxRate, maxChangeRate),
		selfBond.Amount,
	)
	require.NoError(t, err)

	_, err = keeper.NewMsgServerImpl(&nw.App.StakingKeeper).CreateValidator(ctx, msg)
	return err
}

// TestCreateValidator_RefusesMaxRateAboveTheCeiling is the check that matters
// for every validator that does not exist yet.
//
// max_rate is writable exactly once, in this message. MsgEditValidator has no
// such field and UpdateValidatorCommission assigns only Rate and UpdateTime, so
// a validator admitted with 100% carries it for life -- which is how the seven
// on mainnet came to have a ceiling nothing can lower. This is the only moment
// it can be bounded.
func TestCreateValidator_RefusesMaxRateAboveTheCeiling(t *testing.T) {
	nw := network.NewUnitTestNetwork()
	ctx := nw.GetContext()
	setCeiling(t, nw, ctx, pct(30))

	err := createValidator(t, nw, ctx, pct(10), math.LegacyOneDec(), pct(1))
	require.ErrorContains(t, err, "exceeds the chain maximum commission rate")
	require.ErrorContains(t, err, "0.300000000000000000",
		"the error has to name the ceiling, or the operator cannot tell what to change it to")
}

// TestCreateValidator_AcceptsMaxRateAtTheCeiling pins the boundary as
// inclusive. A validator declaring exactly the maximum is complying with it.
func TestCreateValidator_AcceptsMaxRateAtTheCeiling(t *testing.T) {
	nw := network.NewUnitTestNetwork()
	ctx := nw.GetContext()
	setCeiling(t, nw, ctx, pct(30))

	require.NoError(t, createValidator(t, nw, ctx, pct(10), pct(30), pct(1)))
}

// TestCreateValidator_RateIsBoundedByMaxRate explains why only max_rate is
// checked here. The SDK's own Commission.Validate already refuses a rate above
// max_rate, so bounding the ceiling bounds the rate underneath it and a second
// check would be dead code.
func TestCreateValidator_RateIsBoundedByMaxRate(t *testing.T) {
	nw := network.NewUnitTestNetwork()
	ctx := nw.GetContext()
	setCeiling(t, nw, ctx, pct(30))

	err := createValidator(t, nw, ctx, pct(50), pct(30), pct(1))
	require.Error(t, err, "a rate above max_rate must be refused by the SDK itself")
}

// TestCreateValidator_CeilingOfZeroIsDisabled covers an upgraded chain. Params
// written before this field existed unmarshal it as nil, which the keeper reads
// as zero, and zero has to mean "no ceiling" rather than "ceiling of nothing".
func TestCreateValidator_CeilingOfZeroIsDisabled(t *testing.T) {
	nw := network.NewUnitTestNetwork()
	ctx := nw.GetContext()
	setCeiling(t, nw, ctx, math.LegacyZeroDec())

	require.NoError(t, createValidator(t, nw, ctx, pct(10), math.LegacyOneDec(), pct(1)),
		"with the ceiling disabled a 100%% max_rate is the SDK's business again")
}

// TestCreateValidator_NilCeilingIsDisabled is the same case arriving as nil
// rather than zero. LegacyDec's zero value wraps a nil pointer and panics on
// comparison, so this would be a panic rather than a rejection.
func TestCreateValidator_NilCeilingIsDisabled(t *testing.T) {
	nw := network.NewUnitTestNetwork()
	ctx := nw.GetContext()

	p := nw.App.TokenomicsKeeper.GetParams(ctx)
	p.MaxCommissionRate = math.LegacyDec{}
	require.NoError(t, nw.App.TokenomicsKeeper.SetParams(ctx, p))

	require.NotPanics(t, func() {
		_ = createValidator(t, nw, ctx, pct(10), math.LegacyOneDec(), pct(1))
	})
	require.True(t, nw.App.StakingKeeper.MaxCommissionRate(ctx).IsZero())
}

// ----- EditValidator -----

// afterCooldown returns a context a day later.
//
// Commission.ValidateNewRate refuses a second change within 24h of the last
// one, and creating a validator stamps UpdateTime with the creation block. Any
// edit a fixture makes is therefore inside the cooldown unless the clock is
// moved, which has nothing to do with the ceiling under test.
func afterCooldown(ctx sdk.Context) sdk.Context {
	return ctx.WithBlockTime(ctx.BlockTime().Add(25 * time.Hour))
}

// editCommission submits a commission change for an existing validator.
func editCommission(
	t *testing.T, nw *network.UnitTestNetwork, ctx sdk.Context,
	valAddr sdk.ValAddress, newRate math.LegacyDec,
) error {
	t.Helper()
	msg := types.NewMsgEditValidator(
		valAddr.String(), types.NewDescription("T", "E", "S", "T", "Z"), &newRate, nil,
	)
	_, err := keeper.NewMsgServerImpl(&nw.App.StakingKeeper).EditValidator(ctx, msg)
	return err
}

// permissiveValidator returns a validator created the way mainnet's seven were:
// max_rate 100%, max_change_rate 10% a day. Nothing can lower those two fields
// afterwards, which is the situation EditValidator has to contain.
func permissiveValidator(t *testing.T, nw *network.UnitTestNetwork, ctx sdk.Context, rate math.LegacyDec) sdk.ValAddress {
	t.Helper()

	addr, _ := utiltx.NewAccAddressAndKey()
	selfBond := sdk.NewCoin(atoshitypes.BaseDenom, math.NewIntWithDecimal(2, 26))
	require.NoError(t, testutil.FundAccount(ctx, nw.App.BankKeeper, addr, sdk.NewCoins(selfBond)))

	msg, err := types.NewMsgCreateValidator(
		sdk.ValAddress(addr).String(),
		ed25519.GenPrivKey().PubKey(),
		selfBond,
		types.NewDescription("T", "E", "S", "T", "Z"),
		types.NewCommissionRates(rate, math.LegacyOneDec(), pct(10)),
		selfBond.Amount,
	)
	require.NoError(t, err)

	// Created with the ceiling off, because the point of the fixture is a
	// validator that already has max_rate 100% -- exactly what the ceiling now
	// forbids and cannot retroactively fix.
	setCeiling(t, nw, ctx, math.LegacyZeroDec())
	_, err = keeper.NewMsgServerImpl(&nw.App.StakingKeeper).CreateValidator(ctx, msg)
	require.NoError(t, err)

	return sdk.ValAddress(addr)
}

// TestEditValidator_RefusesRaisingAboveTheCeiling is what actually protects
// delegators of the seven existing validators.
//
// Their max_rate is 100% and no message can change it. The ceiling therefore
// applies to the rate they can reach: the 100% stays on record and becomes
// unreachable. Without this, max_change_rate of 10% a day puts 100% nine days
// away, against a 21-day unbonding period.
func TestEditValidator_RefusesRaisingAboveTheCeiling(t *testing.T) {
	nw := network.NewUnitTestNetwork()
	ctx := nw.GetContext()

	valAddr := permissiveValidator(t, nw, ctx, pct(10))
	setCeiling(t, nw, ctx, pct(30))

	err := editCommission(t, nw, ctx, valAddr, pct(50))
	require.ErrorContains(t, err, "exceeds the chain maximum commission rate")
	require.ErrorContains(t, err, "lowering a rate is always permitted")
}

// TestEditValidator_AllowsRaisingUpToTheCeiling confirms the ceiling is a
// ceiling and not a freeze. A validator may still set its commission anywhere
// up to the maximum.
func TestEditValidator_AllowsRaisingUpToTheCeiling(t *testing.T) {
	nw := network.NewUnitTestNetwork()
	ctx := nw.GetContext()

	valAddr := permissiveValidator(t, nw, ctx, pct(10))
	setCeiling(t, nw, ctx, pct(30))

	// max_change_rate on this fixture is 10% a day, so 10% -> 20% is the largest
	// step the SDK permits. What matters here is that our check does not refuse
	// a rate at or below the ceiling.
	err := editCommission(t, nw, afterCooldown(ctx), valAddr, pct(20))
	require.NoError(t, err)
}

// TestEditValidator_AllowsLoweringFromAboveTheCeiling is the exemption, and it
// is load-bearing rather than lenient.
//
// Governance can lower max_commission_rate below a validator's current rate.
// Complying with that is a decrease that starts above the ceiling, and an
// unconditional check would refuse it -- leaving the validator permanently
// unable to obey the rule it is being held to.
func TestEditValidator_AllowsLoweringFromAboveTheCeiling(t *testing.T) {
	nw := network.NewUnitTestNetwork()
	ctx := nw.GetContext()

	// Starts at 50%, which was legal when it was set.
	valAddr := permissiveValidator(t, nw, ctx, pct(50))

	// Governance then tightens the ceiling to 30%, below where this validator
	// already sits.
	setCeiling(t, nw, ctx, pct(30))

	// 40% is still above the ceiling, and is a step towards it. Accepted.
	// A decrease is also uncapped by max_change_rate -- ValidateNewRate compares
	// newRate.Sub(rate) against it, and that difference is negative here.
	require.NoError(t, editCommission(t, nw, afterCooldown(ctx), valAddr, pct(40)),
		"a validator above the ceiling must be able to move towards it")
}

// TestEditValidator_RefusesRaisingWhileAlreadyAboveTheCeiling keeps the
// exemption from becoming a way in. Already over the line is not a licence to
// go further over it.
func TestEditValidator_RefusesRaisingWhileAlreadyAboveTheCeiling(t *testing.T) {
	nw := network.NewUnitTestNetwork()
	ctx := nw.GetContext()

	valAddr := permissiveValidator(t, nw, ctx, pct(40))
	setCeiling(t, nw, ctx, pct(30))

	require.Error(t, editCommission(t, nw, ctx, valAddr, pct(45)))
}

// TestMaxCommissionRate_DefaultsTo30Percent states the shipped figure, and that
// the band a validator may choose from is bounded on both sides: x/staking's
// min_commission_rate below, this above.
func TestMaxCommissionRate_DefaultsTo30Percent(t *testing.T) {
	require.Equal(t, "0.300000000000000000",
		tokenomicstypes.DefaultParams().MaxCommissionRate.String())
	require.Equal(t, "0.300000000000000000",
		tokenomicstypes.DefaultMaxCommissionRate.String())
}
