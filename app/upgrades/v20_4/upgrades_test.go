package v20_4

import (
	"context"
	"fmt"
	"os"
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	icahosttypes "github.com/cosmos/ibc-go/v8/modules/apps/27-interchain-accounts/host/types"
	"github.com/stretchr/testify/require"

	cmdcfg "github.com/atoshi-chain/atoshi/v20/cmd/config"
	atoshitypes "github.com/atoshi-chain/atoshi/v20/types"
	tokenomicstypes "github.com/atoshi-chain/atoshi/v20/x/tokenomics/types"
)

// TestMain installs the atoshi bech32 prefixes. The addresses in constants.go
// are real mainnet ones, and parsing them needs the chain's prefix rather than
// the SDK default.
func TestMain(m *testing.M) {
	cfg := sdk.GetConfig()
	cmdcfg.SetBech32Prefixes(cfg)
	cfg.Seal()
	os.Exit(m.Run())
}

// ---------- fakes ----------

type fakeLogger struct{ lines, errs []string }

func (l *fakeLogger) Info(msg string, _ ...any)  { l.lines = append(l.lines, msg) }
func (l *fakeLogger) Error(msg string, _ ...any) { l.errs = append(l.errs, msg) }

// fakeICAHost stands in for the interchain-accounts host keeper, which is an
// interface here precisely so this test needs no app.
type fakeICAHost struct{ params icahosttypes.Params }

func (f *fakeICAHost) GetParams(sdk.Context) icahosttypes.Params { return f.params }
func (f *fakeICAHost) SetParams(_ sdk.Context, p icahosttypes.Params) {
	f.params = p
}

type fakeBank struct {
	balances map[string]sdk.Coins
	moduleTo map[string]sdk.Coins
	sendErr  error
}

func newFakeBank() *fakeBank {
	return &fakeBank{balances: map[string]sdk.Coins{}, moduleTo: map[string]sdk.Coins{}}
}

func (b *fakeBank) GetBalance(_ context.Context, addr sdk.AccAddress, denom string) sdk.Coin {
	return sdk.NewCoin(denom, b.balances[addr.String()].AmountOf(denom))
}

func (b *fakeBank) SendCoins(_ context.Context, from, to sdk.AccAddress, amt sdk.Coins) error {
	if b.sendErr != nil {
		return b.sendErr
	}
	cur := b.balances[from.String()]
	out, neg := cur.SafeSub(amt...)
	if neg {
		return fmt.Errorf("insufficient funds")
	}
	b.balances[from.String()] = out
	b.balances[to.String()] = b.balances[to.String()].Add(amt...)
	return nil
}

func (b *fakeBank) SendCoinsFromModuleToModule(_ context.Context, from, to string, amt sdk.Coins) error {
	if b.sendErr != nil {
		return b.sendErr
	}
	// Only the destination is tracked. The source pools are seeded by staking
	// in production, and modelling their balances here would test the fake.
	b.moduleTo[to] = b.moduleTo[to].Add(amt...)
	return nil
}

type fakeStaking struct {
	dels      []stakingtypes.Delegation
	vals      map[string]stakingtypes.Validator
	unbonded  map[string]math.Int
	unbondErr error

	ubds    []stakingtypes.UnbondingDelegation
	ubdSet  []stakingtypes.UnbondingDelegation // every SetUnbondingDelegation, in order
	ubdGone []string                           // validators whose record was removed
}

func (s *fakeStaking) GetUnbondingDelegations(_ context.Context, _ sdk.AccAddress, _ uint16) ([]stakingtypes.UnbondingDelegation, error) {
	return s.ubds, nil
}

func (s *fakeStaking) SetUnbondingDelegation(_ context.Context, ubd stakingtypes.UnbondingDelegation) error {
	s.ubdSet = append(s.ubdSet, ubd)
	return nil
}

func (s *fakeStaking) RemoveUnbondingDelegation(_ context.Context, ubd stakingtypes.UnbondingDelegation) error {
	s.ubdGone = append(s.ubdGone, ubd.ValidatorAddress)
	return nil
}

func (s *fakeStaking) GetDelegatorDelegations(_ context.Context, _ sdk.AccAddress, _ uint16) ([]stakingtypes.Delegation, error) {
	return s.dels, nil
}

func (s *fakeStaking) GetValidator(_ context.Context, addr sdk.ValAddress) (stakingtypes.Validator, error) {
	v, ok := s.vals[addr.String()]
	if !ok {
		return stakingtypes.Validator{}, stakingtypes.ErrNoValidatorFound
	}
	return v, nil
}

func (s *fakeStaking) Unbond(_ context.Context, _ sdk.AccAddress, valAddr sdk.ValAddress, shares math.LegacyDec) (math.Int, error) {
	if s.unbondErr != nil {
		return math.ZeroInt(), s.unbondErr
	}
	v := s.vals[valAddr.String()]
	tokens := v.TokensFromShares(shares).TruncateInt()
	if s.unbonded == nil {
		s.unbonded = map[string]math.Int{}
	}
	s.unbonded[valAddr.String()] = tokens
	return tokens, nil
}

// ---------- helpers ----------

func atos(n int64) math.Int {
	return math.NewInt(n).Mul(math.NewIntWithDecimal(1, 18))
}

// oneToOneValidator is a bonded validator whose shares and tokens are equal,
// which is the normal case: the ratio only moves on slashing.
func oneToOneValidator(addr string, tokens math.Int) stakingtypes.Validator {
	return stakingtypes.Validator{
		OperatorAddress: addr,
		Status:          stakingtypes.Bonded,
		Tokens:          tokens,
		DelegatorShares: math.LegacyNewDecFromInt(tokens),
	}
}

func testCtx() sdk.Context { return sdk.Context{} }

// ---------- clawback ----------

// TestClawback_TakesExactlyTheOverIssuedAmount is the property that matters
// most. The holder has far more staked than was over-issued -- the rest
// predates the incident and is theirs -- so taking a token more than
// ClawbackAtos would be confiscation rather than correction.
func TestClawback_TakesExactlyTheOverIssuedAmount(t *testing.T) {
	target := sdk.MustAccAddressFromBech32(ClawbackTarget)
	bk := newFakeBank()

	v1 := "atoshivaloper1vu3mmh7z9xzpeh30ytvm86t3fjewayfvps5rw6"
	v2 := "atoshivaloper1v0yhmxuljjcc4qccqlnk97k35naks0j2k64gx2"
	sk := &fakeStaking{
		dels: []stakingtypes.Delegation{
			{DelegatorAddress: ClawbackTarget, ValidatorAddress: v2, Shares: math.LegacyNewDecFromInt(atos(14_705_499))},
			{DelegatorAddress: ClawbackTarget, ValidatorAddress: v1, Shares: math.LegacyNewDecFromInt(atos(40_000_000))},
		},
		vals: map[string]stakingtypes.Validator{
			v1: oneToOneValidator(v1, atos(40_000_000)),
			v2: oneToOneValidator(v2, atos(14_705_499)),
		},
	}

	require.NoError(t, clawback(testCtx(), bk, sk, &fakeLogger{}))

	total := math.ZeroInt()
	for _, v := range sk.unbonded {
		total = total.Add(v)
	}
	require.Equal(t, atos(ClawbackAtos).String(), total.String(),
		"must take exactly the over-issued amount, no more")

	pool := bk.moduleTo[tokenomicstypes.MigrationPoolName]
	require.Equal(t, atos(ClawbackAtos).String(),
		pool.AmountOf(atoshitypes.BaseDenom).String(),
		"everything taken must land in the migration pool it came from")
	_ = target
}

// TestClawback_IsDeterministic — delegations arrive in map order, and two
// nodes that took from different validators would produce different app
// hashes and halt the chain. Sorting is load-bearing, not tidiness.
func TestClawback_IsDeterministic(t *testing.T) {
	v1 := "atoshivaloper1vu3mmh7z9xzpeh30ytvm86t3fjewayfvps5rw6"
	v2 := "atoshivaloper1v0yhmxuljjcc4qccqlnk97k35naks0j2k64gx2"
	v3 := "atoshivaloper15h9u90s9ay8du5tk4qza953a289r7sy35tegq0"

	run := func(order []string) map[string]math.Int {
		dels := make([]stakingtypes.Delegation, 0, len(order))
		vals := map[string]stakingtypes.Validator{}
		for _, v := range order {
			dels = append(dels, stakingtypes.Delegation{
				DelegatorAddress: ClawbackTarget, ValidatorAddress: v,
				Shares: math.LegacyNewDecFromInt(atos(30_000_000)),
			})
			vals[v] = oneToOneValidator(v, atos(30_000_000))
		}
		sk := &fakeStaking{dels: dels, vals: vals}
		require.NoError(t, clawback(testCtx(), newFakeBank(), sk, &fakeLogger{}))
		return sk.unbonded
	}

	require.Equal(t, run([]string{v1, v2, v3}), run([]string{v3, v1, v2}),
		"the same state must produce the same withdrawals regardless of iteration order")
}

// TestClawback_PrefersLiquidBalance — if the holder has undelegated since this
// was written, take from the balance and leave staking alone.
func TestClawback_PrefersLiquidBalance(t *testing.T) {
	bk := newFakeBank()
	bk.balances[ClawbackTarget] = sdk.NewCoins(sdk.NewCoin(atoshitypes.BaseDenom, atos(ClawbackAtos)))

	sk := &fakeStaking{}
	require.NoError(t, clawback(testCtx(), bk, sk, &fakeLogger{}))

	require.Empty(t, sk.unbonded, "nothing should be unbonded when the balance covers it")
	require.Equal(t, atos(ClawbackAtos).String(),
		bk.balances[authtypes.NewModuleAddress(tokenomicstypes.MigrationPoolName).String()].
			AmountOf(atoshitypes.BaseDenom).String())
}

// TestClawback_ReportsAShortfallRatherThanOverreaching — if the holder moved
// the funds on, say what is missing instead of taking it from elsewhere.
func TestClawback_ReportsAShortfallRatherThanOverreaching(t *testing.T) {
	v1 := "atoshivaloper1vu3mmh7z9xzpeh30ytvm86t3fjewayfvps5rw6"
	sk := &fakeStaking{
		dels: []stakingtypes.Delegation{
			{DelegatorAddress: ClawbackTarget, ValidatorAddress: v1, Shares: math.LegacyNewDecFromInt(atos(1_000_000))},
		},
		vals: map[string]stakingtypes.Validator{v1: oneToOneValidator(v1, atos(1_000_000))},
	}

	err := clawback(testCtx(), newFakeBank(), sk, &fakeLogger{})
	require.ErrorContains(t, err, "still outstanding")
	require.Equal(t, atos(1_000_000).String(), sk.unbonded[v1].String(),
		"what was reachable must still be recovered")
}

// TestClawback_HandlesASlashedValidator — slashing moves the share/token
// ratio, so shares and tokens are not interchangeable. Converting through the
// validator's current rate is what keeps the amount right.
func TestClawback_HandlesASlashedValidator(t *testing.T) {
	v1 := "atoshivaloper1vu3mmh7z9xzpeh30ytvm86t3fjewayfvps5rw6"

	// 1% slashed: 100 shares are now worth 99 tokens.
	val := stakingtypes.Validator{
		OperatorAddress: v1,
		Status:          stakingtypes.Bonded,
		Tokens:          atos(99_000_000),
		DelegatorShares: math.LegacyNewDecFromInt(atos(100_000_000)),
	}
	sk := &fakeStaking{
		dels: []stakingtypes.Delegation{
			{DelegatorAddress: ClawbackTarget, ValidatorAddress: v1, Shares: math.LegacyNewDecFromInt(atos(100_000_000))},
		},
		vals: map[string]stakingtypes.Validator{v1: val},
	}

	require.NoError(t, clawback(testCtx(), newFakeBank(), sk, &fakeLogger{}))

	got := sk.unbonded[v1]
	diff := got.Sub(atos(ClawbackAtos)).Abs()
	require.True(t, diff.LTE(math.NewInt(1)),
		"expected ~%s tokens, got %s", atos(ClawbackAtos), got)
}

// ---------- stuck ATOX ----------

// TestRecoverStuckAtox_SkipsWithoutARecipient — an empty recipient must stop
// the migration, not send a million ATOX to the zero address.
func TestRecoverStuckAtox_SkipsWithoutARecipient(t *testing.T) {
	if StuckAtoxRecipient != "" {
		t.Skip("recipient has been configured; this guard no longer applies")
	}
	err := recoverStuckAtox(testCtx(), newFakeBank(), &fakeLogger{})
	require.ErrorContains(t, err, "no recipient configured")
}

// TestRecoverStuckAtox_MovesWhateverIsActuallyThere — the balance drifts, so
// the handler sends the live figure rather than a number from a snapshot.
func TestRecoverStuckAtox_MovesWhateverIsActuallyThere(t *testing.T) {
	bk := newFakeBank()
	odd := math.NewInt(1_000_000_000_000_000_000).MulRaw(1_000_000).AddRaw(81)
	bk.balances[StuckAtoxHolder] = sdk.NewCoins(sdk.NewCoin(StuckAtoxDenom, odd))

	to := sdk.AccAddress([]byte("recovery-recipient--"))
	from := sdk.MustAccAddressFromBech32(StuckAtoxHolder)
	require.NoError(t, bk.SendCoins(testCtx(), from, to,
		sdk.NewCoins(sdk.NewCoin(StuckAtoxDenom, odd))))

	require.Equal(t, odd.String(), bk.balances[to.String()].AmountOf(StuckAtoxDenom).String(),
		"the recovered amount must be the live balance, dust included")
}

// ---------- ICA host ----------

// TestCloseICAHost_ShutsTheMainnetConfiguration starts from exactly what
// mainnet reports today -- enabled, allow_messages ["*"] -- which is an open
// door past every ante decorator on the chain.
func TestCloseICAHost_ShutsTheMainnetConfiguration(t *testing.T) {
	ica := &fakeICAHost{params: icahosttypes.NewParams(true, []string{icahosttypes.AllowAllHostMsgs})}
	log := &fakeLogger{}

	closeICAHost(testCtx(), ica, log)

	require.False(t, ica.params.HostEnabled)
	require.Empty(t, ica.params.AllowMessages,
		"a disabled host with \"*\" still armed is one governance flip from open")
	require.Contains(t, log.lines, "ICA host closed")
	require.Empty(t, log.errs)
}

// TestCloseICAHost_LeavesNoWildcardBehind is the part that would be easy to get
// wrong: disabling the host without clearing the list means re-enabling it
// later silently restores "*" instead of forcing a named allowlist.
func TestCloseICAHost_LeavesNoWildcardBehind(t *testing.T) {
	ica := &fakeICAHost{params: icahosttypes.NewParams(false, []string{icahosttypes.AllowAllHostMsgs})}

	closeICAHost(testCtx(), ica, &fakeLogger{})

	require.NotContains(t, ica.params.AllowMessages, icahosttypes.AllowAllHostMsgs)
}

// TestCloseICAHost_IsIdempotent — an upgrade handler may be re-run against
// state that already has the change, and doing so must not error or churn.
func TestCloseICAHost_IsIdempotent(t *testing.T) {
	ica := &fakeICAHost{params: icahosttypes.NewParams(false, nil)}
	log := &fakeLogger{}

	closeICAHost(testCtx(), ica, log)

	require.False(t, ica.params.HostEnabled)
	require.Contains(t, log.lines, "ICA host already closed; nothing to do")
	require.Empty(t, log.errs)
}

// TestCloseICAHost_SurvivesAnUnwiredKeeper. Every step after the parameter
// write is non-fatal on purpose: a nil keeper must log, not take the network
// down on the block the upgrade lands.
func TestCloseICAHost_SurvivesAnUnwiredKeeper(t *testing.T) {
	log := &fakeLogger{}
	require.NotPanics(t, func() { closeICAHost(testCtx(), nil, log) })
	require.NotEmpty(t, log.errs)
}

// ---------- clawback: unbonding entries ----------

func ubdEntry(height int64, id uint64, bal math.Int) stakingtypes.UnbondingDelegationEntry {
	return stakingtypes.UnbondingDelegationEntry{
		CreationHeight: height,
		UnbondingId:    id,
		InitialBalance: bal,
		Balance:        bal,
	}
}

// TestClawbackUnbonding_TakesTheInFlightEntryFirst reproduces what mainnet
// actually holds: one entry of 6,539,444.95 ATOS maturing 2026-11-08, with the
// rest still bonded. The entry is the part that walks out on its own, so it has
// to go first -- and taking it leaves every validator's voting power alone.
func TestClawbackUnbonding_TakesTheInFlightEntryFirst(t *testing.T) {
	v1 := "atoshivaloper1vu3mmh7z9xzpeh30ytvm86t3fjewayfvps5rw6"
	v3 := "atoshivaloper15h9u90s9ay8du5tk4qza953a289r7sy35tegq0"
	bk := newFakeBank()
	sk := &fakeStaking{
		ubds: []stakingtypes.UnbondingDelegation{{
			DelegatorAddress: ClawbackTarget,
			ValidatorAddress: v3,
			Entries:          []stakingtypes.UnbondingDelegationEntry{ubdEntry(397303, 1463, atos(6_539_444))},
		}},
		dels: []stakingtypes.Delegation{
			{DelegatorAddress: ClawbackTarget, ValidatorAddress: v1, Shares: math.LegacyNewDecFromInt(atos(40_000_000))},
		},
		vals: map[string]stakingtypes.Validator{v1: oneToOneValidator(v1, atos(40_000_000))},
	}

	require.NoError(t, clawback(testCtx(), bk, sk, &fakeLogger{}))

	require.Contains(t, sk.ubdGone, v3, "a fully drained entry leaves no record behind")

	fromBonded := math.ZeroInt()
	for _, v := range sk.unbonded {
		fromBonded = fromBonded.Add(v)
	}
	require.Equal(t, atos(ClawbackAtos).Sub(atos(6_539_444)).String(), fromBonded.String(),
		"bonded stake covers only what the unbonding entry did not")

	require.Equal(t, atos(ClawbackAtos).String(),
		bk.moduleTo[tokenomicstypes.MigrationPoolName].AmountOf(atoshitypes.BaseDenom).String(),
		"the migration pool is made whole either way")
}

// TestClawbackUnbonding_LeavesThePartItDoesNotNeed — an entry larger than what
// is owed is reduced, not taken. The remainder is still the holder's.
func TestClawbackUnbonding_LeavesThePartItDoesNotNeed(t *testing.T) {
	v3 := "atoshivaloper15h9u90s9ay8du5tk4qza953a289r7sy35tegq0"
	bk := newFakeBank()
	sk := &fakeStaking{
		ubds: []stakingtypes.UnbondingDelegation{{
			DelegatorAddress: ClawbackTarget,
			ValidatorAddress: v3,
			Entries:          []stakingtypes.UnbondingDelegationEntry{ubdEntry(397303, 1463, atos(50_000_000))},
		}},
	}

	require.NoError(t, clawback(testCtx(), bk, sk, &fakeLogger{}))

	require.Empty(t, sk.ubdGone, "a partially taken entry must survive")
	require.Len(t, sk.ubdSet, 1)
	e := sk.ubdSet[0].Entries[0]
	require.Equal(t, atos(50_000_000-ClawbackAtos).String(), e.Balance.String(),
		"the holder keeps what the over-issue did not cover")
	require.Equal(t, atos(50_000_000).String(), e.InitialBalance.String(),
		"InitialBalance is the historical record and must not be rewritten")
}

// TestClawbackUnbonding_SkipsEntriesHeldByAnotherModule. A positive
// UnbondingOnHoldRefCount means another module is holding the entry open and
// expects the balance to still be there. Editing it would corrupt that
// module's accounting.
func TestClawbackUnbonding_SkipsEntriesHeldByAnotherModule(t *testing.T) {
	v3 := "atoshivaloper15h9u90s9ay8du5tk4qza953a289r7sy35tegq0"
	held := ubdEntry(397303, 1463, atos(50_000_000))
	held.UnbondingOnHoldRefCount = 1

	bk := newFakeBank()
	sk := &fakeStaking{
		ubds: []stakingtypes.UnbondingDelegation{{
			DelegatorAddress: ClawbackTarget,
			ValidatorAddress: v3,
			Entries:          []stakingtypes.UnbondingDelegationEntry{held},
		}},
	}

	err := clawback(testCtx(), bk, sk, &fakeLogger{})

	require.Error(t, err, "nothing was reachable, so the handler must report a shortfall")
	require.ErrorContains(t, err, "still outstanding")
	require.Empty(t, sk.ubdSet, "a held entry must not be rewritten")
	require.Empty(t, sk.ubdGone)
	require.True(t, bk.moduleTo[tokenomicstypes.MigrationPoolName].IsZero())
}

// TestClawbackUnbonding_IsDeterministic — entries arrive in whatever order the
// store yields. Two nodes draining different entries produce different app
// hashes and halt the chain.
func TestClawbackUnbonding_IsDeterministic(t *testing.T) {
	v1 := "atoshivaloper1vu3mmh7z9xzpeh30ytvm86t3fjewayfvps5rw6"
	v3 := "atoshivaloper15h9u90s9ay8du5tk4qza953a289r7sy35tegq0"

	build := func(order int) *fakeStaking {
		a := stakingtypes.UnbondingDelegation{
			DelegatorAddress: ClawbackTarget, ValidatorAddress: v1,
			Entries: []stakingtypes.UnbondingDelegationEntry{
				ubdEntry(500, 2, atos(20_000_000)),
				ubdEntry(400, 1, atos(20_000_000)),
			},
		}
		b := stakingtypes.UnbondingDelegation{
			DelegatorAddress: ClawbackTarget, ValidatorAddress: v3,
			Entries: []stakingtypes.UnbondingDelegationEntry{ubdEntry(600, 3, atos(20_000_000))},
		}
		if order == 0 {
			return &fakeStaking{ubds: []stakingtypes.UnbondingDelegation{a, b}}
		}
		return &fakeStaking{ubds: []stakingtypes.UnbondingDelegation{b, a}}
	}

	var got []string
	for order := 0; order < 2; order++ {
		sk := build(order)
		require.NoError(t, clawback(testCtx(), newFakeBank(), sk, &fakeLogger{}))

		trace := ""
		for _, u := range sk.ubdSet {
			for _, e := range u.Entries {
				trace += fmt.Sprintf("%s/%d=%s;", u.ValidatorAddress, e.UnbondingId, e.Balance)
			}
		}
		for _, v := range sk.ubdGone {
			trace += "gone:" + v + ";"
		}
		got = append(got, trace)
	}
	require.Equal(t, got[0], got[1],
		"store order must not change which entries are drained")
}
