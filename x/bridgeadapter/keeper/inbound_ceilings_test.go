package keeper_test

import (
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

	"github.com/atoshi-chain/atoshi/v20/x/bridgeadapter/keeper"
	"github.com/atoshi-chain/atoshi/v20/x/bridgeadapter/types"
)

// The pool these tests run against, and the ceilings it implies.
//
//	pool                              150,000,000,000 ATOS
//	pool x inbound_daily_cap_bps_of_pool (0.9%)   1,350,000,000
//	inbound_daily_cap                             2,500,000,000  <- not binding
//	per-address allowance (x10%)                    135,000,000
//
// The pool term binds and inbound_daily_cap does not, which is the only shape
// where Params.Validate and the handler disagree: Validate sees 2,500,000,000 x
// 10% = 250,000,000 of allowance, the chain enforces 135,000,000. Every
// max_transfer_in below is inside that band on purpose.
var (
	ceilingPool      = atosInt(150_000_000_000)
	ceilingAllowance = atosInt(135_000_000)
)

func atosInt(n int64) math.Int { return math.NewIntWithDecimal(n, 18) }

const ceilingAuthority = "authority-----------"

// setupCeilings builds a keeper whose migration pool is small enough for the
// pool term to bind, and seeds it with maxTransferIn as the current parameters.
func setupCeilings(t *testing.T, maxTransferIn math.Int) (types.MsgServer, sdk.Context) {
	t.Helper()

	storeKey := storetypes.NewKVStoreKey(types.StoreKey)
	db := dbm.NewMemDB()
	cms := store.NewCommitMultiStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	cms.MountStoreWithDB(storeKey, storetypes.StoreTypeIAVL, db)
	require.NoError(t, cms.LoadLatestVersion())

	tk := newFakeTokenomics(math.ZeroInt(), math.ZeroInt())
	tk.poolBalance = ceilingPool

	k := keeper.NewKeeper(
		codec.NewProtoCodec(codectypes.NewInterfaceRegistry()),
		storeKey,
		sdk.AccAddress([]byte(ceilingAuthority)).String(),
		newFakeAtox(), tk, &fakeCore{}, &fakeBank{},
	)

	ctx := sdk.NewContext(cms, tmproto.Header{Time: time.Unix(1_700_000_000, 0)}, false, log.NewNopLogger())
	require.NoError(t, k.SetParams(ctx, ceilingParams(maxTransferIn)))

	return keeper.NewMsgServerImpl(k), ctx
}

// ceilingParams is the fixture both sides of every comparison are built from.
// Both switches stay off so the ISM and vault requirements in Validate are out
// of the way; none of them interact with the inbound ceilings.
func ceilingParams(maxTransferIn math.Int) types.Params {
	p := types.DefaultParams()
	p.InboundDailyCap = atosInt(2_500_000_000)
	p.InboundDailyCapBpsOfPool = 90
	p.InboundPerAddressDailyBps = 1000
	p.MaxTransferIn = maxTransferIn
	return p
}

func update(srv types.MsgServer, ctx sdk.Context, maxTransferIn math.Int) error {
	_, err := srv.UpdateParams(ctx, &types.MsgUpdateParams{
		Authority: sdk.AccAddress([]byte(ceilingAuthority)).String(),
		Params:    ceilingParams(maxTransferIn),
	})
	return err
}

func strandingEvents(ctx sdk.Context) int {
	n := 0
	for _, e := range ctx.EventManager().Events() {
		if e.Type == types.EventTypeInboundCeilingStranding {
			n++
		}
	}
	return n
}

// TestUpdateParams_RejectsPoolDrivenStranding is the case Params.Validate
// cannot see. The proposal passes the parameter-only check -- 200,000,000 is
// below inbound_daily_cap x 10% -- and is still wrong, because the live pool
// puts the enforced allowance at 135,000,000. Going from a safe configuration
// to this one has no deadlock to excuse it, so it is refused.
func TestUpdateParams_RejectsPoolDrivenStranding(t *testing.T) {
	safe := atosInt(100_000_000) // below the 135,000,000 the pool allows
	srv, ctx := setupCeilings(t, safe)

	// Established first: the parameter-only check really does pass, so a
	// failure below is the handler's doing and not Validate's.
	require.NoError(t, ceilingParams(atosInt(200_000_000)).Validate())

	err := update(srv, ctx, atosInt(200_000_000))
	require.ErrorIs(t, err, types.ErrStrandableInboundCeiling)
	require.Contains(t, err.Error(), ceilingAllowance.String(),
		"the error must quote the allowance the pool actually enforces")
	require.Zero(t, strandingEvents(ctx), "a refused proposal records nothing")
}

// TestUpdateParams_AcceptsAPartialRepair is the deadlock escape. The pool can
// break this invariant with no proposal involved, so when it is already broken
// a proposal that narrows the gap has to be accepted -- otherwise the only
// remaining fix is a chain upgrade. It is still wrong afterwards, and the event
// is what says so.
func TestUpdateParams_AcceptsAPartialRepair(t *testing.T) {
	srv, ctx := setupCeilings(t, atosInt(200_000_000)) // gap -65,000,000

	require.NoError(t, update(srv, ctx, atosInt(150_000_000))) // gap -15,000,000
	require.Equal(t, 1, strandingEvents(ctx),
		"partial progress must still be announced; the next proposal has to finish it")
}

// TestUpdateParams_RejectsWideningAnExistingGap keeps the escape hatch from
// becoming a way in. Already-broken does not mean anything goes.
func TestUpdateParams_RejectsWideningAnExistingGap(t *testing.T) {
	srv, ctx := setupCeilings(t, atosInt(150_000_000)) // gap -15,000,000

	err := update(srv, ctx, atosInt(200_000_000)) // gap -65,000,000
	require.ErrorIs(t, err, types.ErrStrandableInboundCeiling)
	require.Zero(t, strandingEvents(ctx))
}

// TestUpdateParams_AcceptsAFullRepairSilently pins that a proposal which
// actually closes the gap emits nothing. The event means "better, still wrong";
// firing it on a correct outcome would train operators to ignore it.
func TestUpdateParams_AcceptsAFullRepairSilently(t *testing.T) {
	srv, ctx := setupCeilings(t, atosInt(200_000_000))

	require.NoError(t, update(srv, ctx, ceilingAllowance)) // exactly the allowance
	require.Zero(t, strandingEvents(ctx))
}

// TestUpdateParams_IgnoresUnconfiguredCeilings covers the shape every chain
// starts in. With max_transfer_in unset there is no per-transfer refusal to be
// permanent, so the pool is irrelevant and nothing should fire -- including on
// a pool far too small to support the configured cap.
func TestUpdateParams_IgnoresUnconfiguredCeilings(t *testing.T) {
	srv, ctx := setupCeilings(t, math.ZeroInt())

	require.NoError(t, update(srv, ctx, math.ZeroInt()))
	require.Zero(t, strandingEvents(ctx))
}
