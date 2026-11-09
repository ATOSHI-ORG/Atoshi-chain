package types_test

import (
	"testing"

	"cosmossdk.io/math"
	"github.com/stretchr/testify/require"

	"github.com/atoshi-chain/atoshi/v20/x/bridgeadapter/types"
)

// mainnetParams is the parameter set live on atoshi_88188-1, read from the
// chain on 2026-11-09. Only the inbound fields this file is about are filled;
// the rest come from DefaultParams.
//
// Pinned as a fixture because the check added here can refuse a proposal, and
// the proposal it must never refuse is the one that re-states production as it
// already is -- the emergency re-open template does exactly that.
func mainnetParams() types.Params {
	p := types.DefaultParams()
	p.InboundDailyCap = atos(2_500_000_000)
	p.InboundDailyCapBpsOfPool = 90
	p.InboundPerAddressDailyBps = 1000
	p.MaxTransferIn = atos(200_000_000)
	p.MaxTransferOut = atos(60_000_000)
	p.GlobalDailyCap = atos(100_000_000)
	p.GlobalDailyCapBpsOfPool = 4
	p.PerAddressDailyBps = 3000
	return p
}

// TestValidate_AcceptsLiveMainnetParams is the regression guard that matters
// most here. A new rule in Validate that rejects the running configuration
// would not be caught by any other test -- every other fixture is derived from
// DefaultParams, which leaves both inbound ceilings unset and so skips this
// check entirely.
func TestValidate_AcceptsLiveMainnetParams(t *testing.T) {
	require.NoError(t, mainnetParams().Validate())

	// And with the headroom stated, so a later tuning that eats it shows up
	// here as a changed number rather than as a failed proposal.
	require.Equal(t, atos(250_000_000).String(),
		mainnetParams().ParamsOnlyInboundPerAddressCap().String())
}

// TestValidate_AcceptsGenesis covers the unset case. DefaultParams ships
// max_transfer_in and inbound_per_address_daily_bps at zero on purpose, and
// CheckInbound reads a resolved zero as "no limit", so neither ceiling exists
// and nothing can be stranded by this route.
func TestValidate_AcceptsGenesis(t *testing.T) {
	require.NoError(t, types.DefaultParams().Validate())
	require.True(t, types.DefaultParams().ParamsOnlyInboundPerAddressCap().IsZero())
}

// TestValidate_RejectsStrandableCeilings walks the three shapes a proposal can
// take. Two of them tighten a limit, which is why they are the dangerous ones:
// they read as "make the bridge safer" to everyone drafting and voting, and
// what they actually do is create a band of amounts that Ethereum accepts and
// this chain can never deliver.
func TestValidate_RejectsStrandableCeilings(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*types.Params)
		cap    math.Int // the per-address allowance the proposal leaves
	}{
		"raising max_transfer_in above the allowance": {
			mutate: func(p *types.Params) { p.MaxTransferIn = atos(300_000_000) },
			cap:    atos(250_000_000),
		},
		"halving the per-address share": {
			mutate: func(p *types.Params) { p.InboundPerAddressDailyBps = 500 },
			cap:    atos(125_000_000),
		},
		"lowering the inbound daily cap": {
			mutate: func(p *types.Params) { p.InboundDailyCap = atos(1_000_000_000) },
			cap:    atos(100_000_000),
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := mainnetParams()
			tc.mutate(&p)

			err := p.Validate()
			require.ErrorIs(t, err, types.ErrStrandableInboundCeiling)
			// Both figures have to be in the message: the author has to know
			// which of the two to move, and by how much.
			require.Contains(t, err.Error(), p.MaxTransferIn.String())
			require.Contains(t, err.Error(), tc.cap.String())
			require.Equal(t, tc.cap.String(), p.ParamsOnlyInboundPerAddressCap().String())
		})
	}
}

// TestValidate_AcceptsEqualCeilings pins the boundary. Equal is correct and
// must pass: an amount above the per-transfer ceiling is then also above the
// per-address allowance, so it meets the retryable refusal first. Rejecting
// equality would force headroom nobody asked for.
func TestValidate_AcceptsEqualCeilings(t *testing.T) {
	p := mainnetParams()
	p.MaxTransferIn = p.ParamsOnlyInboundPerAddressCap()
	require.NoError(t, p.Validate())
}

// TestValidate_InboundBpsBounds covers the two fields that were missing from
// the bps range check. Zero stays legal -- it is the genesis value and means
// "no limit" -- so only the upper bound is enforced.
func TestValidate_InboundBpsBounds(t *testing.T) {
	t.Run("above the denominator is refused", func(t *testing.T) {
		for _, f := range []func(*types.Params){
			func(p *types.Params) { p.InboundDailyCapBpsOfPool = types.BpsDenominator + 1 },
			func(p *types.Params) { p.InboundPerAddressDailyBps = types.BpsDenominator + 1 },
		} {
			p := types.DefaultParams()
			f(&p)
			require.ErrorContains(t, p.Validate(), "must be <= 10000")
		}
	})

	t.Run("the denominator itself is fine", func(t *testing.T) {
		p := mainnetParams()
		p.InboundPerAddressDailyBps = types.BpsDenominator // 100% of the daily cap
		require.NoError(t, p.Validate())
	})

	t.Run("zero stays legal", func(t *testing.T) {
		p := mainnetParams()
		p.InboundPerAddressDailyBps = 0
		require.NoError(t, p.Validate(),
			"zero means no per-address limit, which is the genesis state")
	})
}

// TestValidate_ToleratesNilAmountFields is the emergency-proposal case.
// MsgUpdateParams replaces the whole struct, and the re-open template an
// operator reaches for during a halt predates several of these fields, so they
// arrive nil. Nil must validate; only an explicitly negative figure is refused.
func TestValidate_ToleratesNilAmountFields(t *testing.T) {
	p := types.DefaultParams()
	p.InboundDailyCap = math.Int{}
	p.InboundSmallThreshold = math.Int{}
	p.MaxTransferIn = math.Int{}
	p.MaxTransferOut = math.Int{}
	require.NoError(t, p.Validate())

	// ParamsOnlyInboundPerAddressCap has to survive the nil too: it is reached
	// from Validate before anything else normalises the field.
	require.True(t, p.ParamsOnlyInboundPerAddressCap().IsZero())
}

// TestCheckInboundCeilings_UnconfiguredCeilingsCannotStrand states the rule the
// bool in inboundGap depends on. A zero on either side is "no limit", not "a
// limit of zero", and neither shape can produce a permanently undeliverable
// transfer.
func TestCheckInboundCeilings_UnconfiguredCeilingsCannotStrand(t *testing.T) {
	big, small := atos(300_000_000), atos(1)

	require.NoError(t, types.CheckInboundCeilings(math.ZeroInt(), small, "test"),
		"no per-transfer ceiling: nothing is refused on every retry")
	require.NoError(t, types.CheckInboundCeilings(big, math.ZeroInt(), "test"),
		"no per-address allowance: there is no retryable refusal to shelter behind")
	require.Error(t, types.CheckInboundCeilings(big, small, "test"),
		"both configured and the wrong way round is the one case that strands")
}
