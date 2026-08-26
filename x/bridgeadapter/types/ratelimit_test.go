package types_test

import (
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/atoshi-chain/atoshi/v20/x/bridgeadapter/types"
)

func atos(n int64) math.Int { return math.NewIntWithDecimal(n, 18) }

// poolTotal is the configured migration pool: 300 billion ATOS.
var poolTotal = math.NewIntWithDecimal(3, 29)

func limitParams() types.Params {
	p := types.DefaultParams()
	p.GlobalDailyCap = atos(1_000_000) // fixed leg well below the percentage leg
	p.GlobalDailyCapBpsOfPool = 500    // 5%
	p.PerAddressDailyBps = 200         // 2% of global
	p.MinTransferOut = atos(1_000)     //
	p.SmallTransferThreshold = atos(10_000)
	p.SmallQuotaBps = 2000 // 20% reserved
	p.CrisisPoolBps = 1000 // 10%
	return p
}

// TestResolveLimits_TakesTheSmallerLeg is what makes the cap self-tightening:
// as the pool drains the percentage leg falls below the fixed one and starts
// binding, with no proposal needed.
func TestResolveLimits_TakesTheSmallerLeg(t *testing.T) {
	p := limitParams()

	// Full pool: 5% is 15 billion, far above the 1M fixed cap, so the fixed leg
	// binds.
	l := types.ResolveLimits(p, poolTotal, poolTotal)
	require.Equal(t, atos(1_000_000).String(), l.Global.String())

	// Nearly empty pool: 5% of 10M ATOS is 500k, which now binds instead.
	l = types.ResolveLimits(p, atos(10_000_000), poolTotal)
	require.Equal(t, atos(500_000).String(), l.Global.String())
}

func TestResolveLimits_DerivedFigures(t *testing.T) {
	l := types.ResolveLimits(limitParams(), poolTotal, poolTotal)

	require.Equal(t, atos(1_000_000).String(), l.Global.String())
	require.Equal(t, atos(800_000).String(), l.LargeBudget.String(),
		"20%% of the cap is reserved for small transfers")
	require.Equal(t, atos(20_000).String(), l.PerAddress.String(), "2%% of the cap per address")
	require.False(t, l.CrisisMode, "a full pool is not in crisis")
}

// TestSmallTransferReserve_CannotBeEatenByWhales is the layer that matters most
// for ordinary holders. Large exits are capped at the global cap minus the
// reserve, so the reserve is still there after they have taken everything they
// can — which is precisely when small holders want out.
func TestSmallTransferReserve_CannotBeEatenByWhales(t *testing.T) {
	l := types.ResolveLimits(limitParams(), poolTotal, poolTotal)

	// Whales exhaust the entire large budget.
	usedLarge := l.LargeBudget
	used := l.LargeBudget

	// One more large transfer is refused even though the global cap has room.
	err := types.CheckOutbound(l, atos(50_000), used, usedLarge, math.ZeroInt())
	require.ErrorIs(t, err, types.ErrLargeQuotaReached)

	// A small transfer still goes through, drawing on the reserve.
	require.NoError(t, types.CheckOutbound(l, atos(10_000), used, usedLarge, math.ZeroInt()))

	// And the reserve is exactly the configured 20%: small transfers can consume
	// the remainder up to the global cap, then stop.
	used = l.Global.Sub(atos(10_000))
	require.NoError(t, types.CheckOutbound(l, atos(10_000), used, usedLarge, math.ZeroInt()))
	require.ErrorIs(t,
		types.CheckOutbound(l, atos(10_001), used, usedLarge, math.ZeroInt()),
		types.ErrDailyCapReached)
}

func TestCheckOutbound_GlobalCap(t *testing.T) {
	l := types.ResolveLimits(limitParams(), poolTotal, poolTotal)

	require.NoError(t, types.CheckOutbound(l, atos(10_000), l.Global.Sub(atos(10_000)), math.ZeroInt(), math.ZeroInt()))
	require.ErrorIs(t,
		types.CheckOutbound(l, atos(10_001), l.Global.Sub(atos(10_000)), math.ZeroInt(), math.ZeroInt()),
		types.ErrDailyCapReached)
}

func TestCheckOutbound_PerAddressCap(t *testing.T) {
	l := types.ResolveLimits(limitParams(), poolTotal, poolTotal)

	// The address has used its whole 2% allowance; the global cap is untouched.
	require.ErrorIs(t,
		types.CheckOutbound(l, atos(1_000), math.ZeroInt(), math.ZeroInt(), l.PerAddress),
		types.ErrAddressCapReached)

	// A different address with no usage is unaffected.
	require.NoError(t, types.CheckOutbound(l, atos(1_000), math.ZeroInt(), math.ZeroInt(), math.ZeroInt()))
}

func TestCheckOutbound_MinimumTransfer(t *testing.T) {
	l := types.ResolveLimits(limitParams(), poolTotal, poolTotal)

	require.ErrorIs(t,
		types.CheckOutbound(l, atos(999), math.ZeroInt(), math.ZeroInt(), math.ZeroInt()),
		types.ErrBelowMinimum)
	require.NoError(t, types.CheckOutbound(l, atos(1_000), math.ZeroInt(), math.ZeroInt(), math.ZeroInt()))
}

// TestCrisisMode_OnlySmallTransfers — below the floor the remaining liquidity
// should serve many small holders rather than one large exit.
func TestCrisisMode_OnlySmallTransfers(t *testing.T) {
	p := limitParams()

	// 9% of the pool: below the 10% crisis floor.
	poolBalance := poolTotal.MulRaw(9).QuoRaw(100)
	l := types.ResolveLimits(p, poolBalance, poolTotal)
	require.True(t, l.CrisisMode)

	require.ErrorIs(t,
		types.CheckOutbound(l, atos(50_000), math.ZeroInt(), math.ZeroInt(), math.ZeroInt()),
		types.ErrCrisisMode)
	require.NoError(t, types.CheckOutbound(l, atos(10_000), math.ZeroInt(), math.ZeroInt(), math.ZeroInt()),
		"small transfers must keep working in crisis mode")

	// Exactly at the floor is not crisis.
	l = types.ResolveLimits(p, poolTotal.MulRaw(10).QuoRaw(100), poolTotal)
	require.False(t, l.CrisisMode)
}

func TestCheckOutbound_ZeroGlobalCapThrottlesEverything(t *testing.T) {
	p := limitParams()
	p.GlobalDailyCap = math.ZeroInt()
	p.GlobalDailyCapBpsOfPool = 0
	l := types.ResolveLimits(p, poolTotal, poolTotal)

	require.ErrorIs(t,
		types.CheckOutbound(l, atos(1_000), math.ZeroInt(), math.ZeroInt(), math.ZeroInt()),
		types.ErrDailyCapReached)
}

func TestCheckOutbound_RejectsNonPositive(t *testing.T) {
	l := types.ResolveLimits(limitParams(), poolTotal, poolTotal)
	for _, a := range []math.Int{math.ZeroInt(), math.NewInt(-1), {}} {
		require.ErrorIs(t, types.CheckOutbound(l, a, math.ZeroInt(), math.ZeroInt(), math.ZeroInt()),
			types.ErrInvalidAmount)
	}
}

// ----- day rollover -----

func TestDayOf(t *testing.T) {
	require.Equal(t, int64(0), types.DayOf(0))
	require.Equal(t, int64(0), types.DayOf(types.SecondsPerDay-1))
	require.Equal(t, int64(1), types.DayOf(types.SecondsPerDay))
	require.Equal(t, int64(19_675), types.DayOf(1_700_000_000))
	require.Equal(t, int64(0), types.DayOf(-1), "a negative clock must not produce a negative day key")
}

// ----- peg -----

// TestAtosToErc20_RefusesRemainder — truncating would lock the full ATOS here
// while asking Ethereum for less, quietly confiscating the difference.
func TestAtosToErc20_RefusesRemainder(t *testing.T) {
	got, err := types.AtosToErc20(math.NewInt(500), 100)
	require.NoError(t, err)
	require.Equal(t, "5", got.String())

	_, err = types.AtosToErc20(math.NewInt(501), 100)
	require.ErrorIs(t, err, types.ErrIndivisibleAmount)

	_, err = types.AtosToErc20(math.NewInt(99), 100)
	require.ErrorIs(t, err, types.ErrIndivisibleAmount,
		"an amount below the peg has no ERC20 representation at all")
}

func TestPegRoundTrip(t *testing.T) {
	for _, n := range []int64{100, 1_000, 1_000_000} {
		a := atos(n)
		e, err := types.AtosToErc20(a, types.DefaultAtosPerErc20)
		require.NoError(t, err)
		require.Equal(t, a.String(), types.Erc20ToAtos(e, types.DefaultAtosPerErc20).String())
	}
}

// ----- asset payload -----

func TestAssetPayloadRoundTrip(t *testing.T) {
	recipient := make([]byte, 32)
	copy(recipient[12:], []byte("twenty-byte-account!"))

	body, err := types.BuildAssetPayload(recipient, math.NewInt(12_345))
	require.NoError(t, err)
	require.Len(t, body, types.AssetPayloadLen)

	gotRecipient, gotAmount, err := types.ParseAssetPayload(body)
	require.NoError(t, err)
	require.Equal(t, recipient, gotRecipient)
	require.Equal(t, "12345", gotAmount.String())
}

func TestParseAssetPayload_RejectsWrongLength(t *testing.T) {
	for _, size := range []int{0, 32, 63, 65} {
		_, _, err := types.ParseAssetPayload(make([]byte, size))
		require.ErrorIs(t, err, types.ErrInvalidPayload)
	}
}

// TestCosmosAddressFromHyperlane_RequiresZeroPadding is the guard against
// releasing funds to a coerced address. A 32-byte address in some other chain's
// format would otherwise be silently truncated into a valid-looking Cosmos
// account, and the ATOS would go there unrecoverably.
func TestCosmosAddressFromHyperlane_RequiresZeroPadding(t *testing.T) {
	good := make([]byte, 32)
	copy(good[12:], []byte("twenty-byte-account!"))
	got, err := types.CosmosAddressFromHyperlane(good)
	require.NoError(t, err)
	require.Equal(t, sdk.AccAddress([]byte("twenty-byte-account!")).String(), got.String())

	// Non-zero high bytes: not a Cosmos account.
	bad := make([]byte, 32)
	copy(bad, []byte("0123456789abcdef0123456789abcdef"))
	_, err = types.CosmosAddressFromHyperlane(bad)
	require.ErrorIs(t, err, types.ErrInvalidPayload)

	// All zeros: the zero address.
	_, err = types.CosmosAddressFromHyperlane(make([]byte, 32))
	require.ErrorIs(t, err, types.ErrInvalidPayload)

	// Wrong width.
	_, err = types.CosmosAddressFromHyperlane(make([]byte, 20))
	require.ErrorIs(t, err, types.ErrInvalidPayload)
}

// ----- params -----

func TestParams_BridgeEnablingRequiresConfiguration(t *testing.T) {
	p := types.DefaultParams()
	require.False(t, p.BridgeEnabled, "the bridge must not be live before the Ethereum side exists")
	require.NoError(t, p.Validate())

	p.BridgeEnabled = true
	require.ErrorContains(t, p.Validate(), "mailbox_id and remote_bridge_vault must be set",
		"with no counterparty the inbound sender check has nothing to compare against")

	p.MailboxId = make([]byte, types.HexAddressLen)
	p.RemoteBridgeVault = make([]byte, types.HexAddressLen)
	require.NoError(t, p.Validate())
}

// TestParams_FullSmallReserveIsRejected — a 100% reserve leaves large transfers
// no budget at all, which is a silent ban rather than a limit.
func TestParams_FullSmallReserveIsRejected(t *testing.T) {
	p := types.DefaultParams()
	p.SmallQuotaBps = types.BpsDenominator
	require.ErrorContains(t, p.Validate(), "large transfers are banned outright")

	p.SmallQuotaBps = types.BpsDenominator - 1
	require.NoError(t, p.Validate())
}

func TestParams_BpsFieldsBounded(t *testing.T) {
	for _, name := range []string{"global", "address", "small", "crisis"} {
		p := types.DefaultParams()
		switch name {
		case "global":
			p.GlobalDailyCapBpsOfPool = types.BpsDenominator + 1
		case "address":
			p.PerAddressDailyBps = types.BpsDenominator + 1
		case "small":
			p.SmallQuotaBps = types.BpsDenominator + 1
		case "crisis":
			p.CrisisPoolBps = types.BpsDenominator + 1
		}
		require.Error(t, p.Validate(), "%s bps above 100%% must be rejected", name)
	}
}

// TestDefaultParams_LimitsAreSane checks the shipped defaults resolve to
// something usable against the real pool size.
func TestDefaultParams_LimitsAreSane(t *testing.T) {
	p := types.DefaultParams()
	require.NoError(t, p.Validate())

	l := types.ResolveLimits(p, poolTotal, poolTotal)
	require.True(t, l.Global.IsPositive())
	require.True(t, l.LargeBudget.LT(l.Global), "some of the cap must be reserved")
	require.True(t, l.PerAddress.LT(l.Global), "one address must not be able to take the day")
	require.True(t, l.MinTransfer.IsPositive())
	require.True(t, l.SmallThreshold.IsPositive())
	require.False(t, l.CrisisMode)

	// The fixed leg binds at full pool: 5 billion, against 5% of 300 billion.
	require.Equal(t, math.NewIntWithDecimal(5, 27).String(), l.Global.String())
}
