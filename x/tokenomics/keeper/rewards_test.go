package keeper

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"cosmossdk.io/log"
	"cosmossdk.io/math"
	"cosmossdk.io/store/metrics"
	"cosmossdk.io/store/rootmulti"
	storetypes "cosmossdk.io/store/types"
	tmproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"

	atoshitypes "github.com/atoshi-chain/atoshi/v20/types"
	oracletypes "github.com/atoshi-chain/atoshi/v20/x/oracle/types"
	tokenomicstypes "github.com/atoshi-chain/atoshi/v20/x/tokenomics/types"
)

type testBankKeeper struct {
	balances map[string]sdk.Coins
}

func newTestBankKeeper() *testBankKeeper {
	return &testBankKeeper{balances: map[string]sdk.Coins{}}
}

func (bk *testBankKeeper) key(addr sdk.AccAddress) string { return addr.String() }

func (bk *testBankKeeper) GetBalance(_ context.Context, addr sdk.AccAddress, denom string) sdk.Coin {
	coins := bk.balances[bk.key(addr)]
	return sdk.NewCoin(denom, coins.AmountOf(denom))
}

func (bk *testBankKeeper) GetAllBalances(_ context.Context, addr sdk.AccAddress) sdk.Coins {
	return bk.balances[bk.key(addr)]
}

func (bk *testBankKeeper) SendCoinsFromModuleToAccount(_ context.Context, senderModule string, recipientAddr sdk.AccAddress, amt sdk.Coins) error {
	moduleAddr := authtypes.NewModuleAddress(senderModule)
	return bk.transfer(moduleAddr, recipientAddr, amt)
}

func (bk *testBankKeeper) SendCoinsFromModuleToModule(_ context.Context, senderModule, recipientModule string, amt sdk.Coins) error {
	from := authtypes.NewModuleAddress(senderModule)
	to := authtypes.NewModuleAddress(recipientModule)
	return bk.transfer(from, to, amt)
}

func (bk *testBankKeeper) MintCoins(_ context.Context, name string, amt sdk.Coins) error {
	addr := authtypes.NewModuleAddress(name)
	bk.balances[bk.key(addr)] = bk.balances[bk.key(addr)].Add(amt...)
	return nil
}

func (bk *testBankKeeper) GetSupply(_ context.Context, denom string) sdk.Coin {
	total := math.ZeroInt()
	for _, coins := range bk.balances {
		total = total.Add(coins.AmountOf(denom))
	}
	return sdk.NewCoin(denom, total)
}

func (bk *testBankKeeper) transfer(from, to sdk.AccAddress, amt sdk.Coins) error {
	fromCoins := bk.balances[bk.key(from)]
	for _, coin := range amt {
		if fromCoins.AmountOf(coin.Denom).LT(coin.Amount) {
			return tokenomicstypes.ErrInsufficientClaimable
		}
	}
	bk.balances[bk.key(from)] = fromCoins.Sub(amt...)
	bk.balances[bk.key(to)] = bk.balances[bk.key(to)].Add(amt...)
	return nil
}

type testAccountKeeper struct{}

func (testAccountKeeper) GetModuleAddress(name string) sdk.AccAddress {
	return authtypes.NewModuleAddress(name)
}
func (testAccountKeeper) GetModuleAccount(ctx context.Context, moduleName string) sdk.ModuleAccountI {
	return nil
}
func (testAccountKeeper) GetAccount(ctx context.Context, addr sdk.AccAddress) sdk.AccountI {
	return nil
}
func (testAccountKeeper) SetAccount(ctx context.Context, acc sdk.AccountI) {}

type testStakingKeeper struct {
	totalBonded math.Int
	validators  []stakingtypes.Validator
}

func (sk testStakingKeeper) TotalBondedTokens(ctx context.Context) (math.Int, error) {
	return sk.totalBonded, nil
}
func (sk testStakingKeeper) IterateBondedValidatorsByPower(ctx context.Context, fn func(index int64, validator stakingtypes.ValidatorI) bool) error {
	for i, v := range sk.validators {
		if fn(int64(i), v) {
			break
		}
	}
	return nil
}
func (sk testStakingKeeper) GetValidator(ctx context.Context, addr sdk.ValAddress) (stakingtypes.Validator, error) {
	return stakingtypes.Validator{}, nil
}
func (sk testStakingKeeper) Validator(ctx context.Context, addr sdk.ValAddress) (stakingtypes.ValidatorI, error) {
	return nil, nil
}

type testDistrKeeper struct{}

func (testDistrKeeper) FundCommunityPool(ctx context.Context, amount sdk.Coins, sender sdk.AccAddress) error {
	return nil
}

type testOracleKeeper struct {
	price  oracletypes.PriceData
	err    error
	params *oracletypes.Params // optional override; nil → DefaultParams
}

func (ok testOracleKeeper) GetCurrentPrice(ctx sdk.Context) (oracletypes.PriceData, error) {
	return ok.price, ok.err
}

func (ok testOracleKeeper) GetParams(ctx sdk.Context) oracletypes.Params {
	if ok.params != nil {
		return *ok.params
	}
	return oracletypes.DefaultParams()
}

func newKeeperForTest(t *testing.T, bk *testBankKeeper, sk testStakingKeeper, ok testOracleKeeper) (Keeper, sdk.Context) {
	t.Helper()

	key := storetypes.NewKVStoreKey(tokenomicstypes.StoreKey)
	db := dbm.NewMemDB()
	cms := rootmulti.NewStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	cms.MountStoreWithDB(key, storetypes.StoreTypeIAVL, db)
	require.NoError(t, cms.LoadLatestVersion())

	ctx := sdk.NewContext(cms, tmproto.Header{}, false, log.NewNopLogger())
	ctx = ctx.WithEventManager(sdk.NewEventManager())

	interfaceRegistry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(interfaceRegistry)
	xk := newTestAtoxKeeper()
	k := NewKeeper(key, cdc, authtypes.NewModuleAddress("gov"), authtypes.FeeCollectorName, testAccountKeeper{}, bk, sk, testDistrKeeper{}, ok, xk)
	require.NoError(t, k.SetParams(ctx, tokenomicstypes.DefaultParams()))
	require.NoError(t, k.SetReleaseState(ctx, tokenomicstypes.DefaultReleaseState()))
	require.NoError(t, k.SetBlockRewardState(ctx, tokenomicstypes.DefaultBlockRewardState()))
	return k, ctx
}

// testAtoxKeeper records what tokenomics asks x/atox to do, so the tier engine
// can be tested without wiring the real module.
type testAtoxKeeper struct {
	minted      map[string]math.Int
	pooled      map[string]math.Int
	supply      math.Int
	cap         math.Int
	mintErr     error
	addPoolErr  error
	totalMinted math.Int
	totalPooled math.Int
}

func newTestAtoxKeeper() *testAtoxKeeper {
	return &testAtoxKeeper{
		minted:      map[string]math.Int{},
		pooled:      map[string]math.Int{},
		supply:      math.ZeroInt(),
		cap:         math.NewIntWithDecimal(1, 30),
		totalMinted: math.ZeroInt(),
		totalPooled: math.ZeroInt(),
	}
}

func (a *testAtoxKeeper) MintAtoxToModule(_ sdk.Context, module string, amount math.Int) error {
	if a.mintErr != nil {
		return a.mintErr
	}
	cur, ok := a.minted[module]
	if !ok {
		cur = math.ZeroInt()
	}
	a.minted[module] = cur.Add(amount)
	a.supply = a.supply.Add(amount)
	a.totalMinted = a.totalMinted.Add(amount)
	return nil
}

func (a *testAtoxKeeper) AddToExchangePool(_ sdk.Context, fromModule string, amount math.Int) error {
	if a.addPoolErr != nil {
		return a.addPoolErr
	}
	cur, ok := a.pooled[fromModule]
	if !ok {
		cur = math.ZeroInt()
	}
	a.pooled[fromModule] = cur.Add(amount)
	a.totalPooled = a.totalPooled.Add(amount)
	return nil
}

func (a *testAtoxKeeper) AtoxSupply(_ sdk.Context) math.Int    { return a.supply }
func (a *testAtoxKeeper) AtoxSupplyCap(_ sdk.Context) math.Int { return a.cap }

// TestBeginBlocker_MintsAtoxToFeeCollector replaces the old immediate/locked ATOS
// split test. Block rewards are ATOX now, minted into the fee collector so
// x/distribution apportions them across the active set and their delegators by
// commission — no ATOS leaves the miner pool.
func TestBeginBlocker_MintsAtoxToFeeCollector(t *testing.T) {
	bk := newTestBankKeeper()
	minerAddr := authtypes.NewModuleAddress(tokenomicstypes.MinerPoolName)
	bk.balances[minerAddr.String()] = sdk.NewCoins(sdk.NewCoin(atoshitypes.BaseDenom, math.NewIntWithDecimal(1, 20)))

	v1 := stakingtypes.Validator{OperatorAddress: "atoshivaloper1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Tokens: math.NewInt(100)}
	k, ctx := newKeeperForTest(t, bk, testStakingKeeper{totalBonded: math.NewInt(400), validators: []stakingtypes.Validator{v1}}, testOracleKeeper{})
	xk := k.atoxKeeper.(*testAtoxKeeper)

	params := k.GetParams(ctx)
	params.InitialBlockReward = math.NewInt(100)
	params.HalvingIntervalBlocks = 100
	require.NoError(t, k.SetParams(ctx, params))

	minerBefore := bk.GetBalance(ctx, minerAddr, atoshitypes.BaseDenom).Amount
	require.NoError(t, k.BeginBlocker(ctx.WithBlockHeight(1)))

	require.Equal(t, math.NewInt(100).String(), xk.minted[authtypes.FeeCollectorName].String(),
		"the whole reward is ATOX and goes to the fee collector")
	require.Equal(t, minerBefore.String(), bk.GetBalance(ctx, minerAddr, atoshitypes.BaseDenom).Amount.String(),
		"no ATOS may leave the miner pool for a block reward")
	require.Equal(t, math.NewInt(100).String(), k.GetBlockRewardState(ctx).TotalDistributed.String())
}

// TestBeginBlocker_DoesNotInflateCirculatingSupply is the correctness point
// behind the change: circulating supply is an ATOS figure and sets the tier
// release quota, so ATOX emission must not touch it.
func TestBeginBlocker_DoesNotInflateCirculatingSupply(t *testing.T) {
	bk := newTestBankKeeper()
	v1 := stakingtypes.Validator{OperatorAddress: "atoshivaloper1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Tokens: math.NewInt(100)}
	k, ctx := newKeeperForTest(t, bk, testStakingKeeper{totalBonded: math.NewInt(100), validators: []stakingtypes.Validator{v1}}, testOracleKeeper{})

	params := k.GetParams(ctx)
	params.InitialBlockReward = math.NewIntWithDecimal(1, 20)
	params.HalvingIntervalBlocks = 1_000_000
	require.NoError(t, k.SetParams(ctx, params))

	before := k.GetCirculatingSupply(ctx)
	for h := int64(1); h <= 10; h++ {
		require.NoError(t, k.BeginBlocker(ctx.WithBlockHeight(h)))
	}
	require.Equal(t, before.String(), k.GetCirculatingSupply(ctx).String(),
		"ATOX emission must leave the ATOS circulating figure untouched")
}

// TestBeginBlocker_StopsAtAtoxCapWithoutErroring — an error here propagates into
// FinalizeBlock, so once the cap is reached every block on every node would fail
// and the chain would halt. Emission has to just stop.
func TestBeginBlocker_StopsAtAtoxCapWithoutErroring(t *testing.T) {
	bk := newTestBankKeeper()
	v1 := stakingtypes.Validator{OperatorAddress: "atoshivaloper1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Tokens: math.NewInt(100)}
	k, ctx := newKeeperForTest(t, bk, testStakingKeeper{totalBonded: math.NewInt(100), validators: []stakingtypes.Validator{v1}}, testOracleKeeper{})
	xk := k.atoxKeeper.(*testAtoxKeeper)

	params := k.GetParams(ctx)
	params.InitialBlockReward = math.NewInt(100)
	params.HalvingIntervalBlocks = 1_000_000
	require.NoError(t, k.SetParams(ctx, params))

	// Only 30 of headroom left against a reward of 100.
	xk.cap = math.NewInt(1000)
	xk.supply = math.NewInt(970)

	require.NoError(t, k.BeginBlocker(ctx.WithBlockHeight(1)))
	require.Equal(t, math.NewInt(30).String(), xk.minted[authtypes.FeeCollectorName].String(),
		"the last block must emit only the remaining headroom")

	// Cap now reached: further blocks are quiet no-ops, not errors.
	for h := int64(2); h <= 5; h++ {
		require.NoError(t, k.BeginBlocker(ctx.WithBlockHeight(h)))
	}
	require.Equal(t, math.NewInt(30).String(), xk.minted[authtypes.FeeCollectorName].String())
}

// TestTriggerRelease_RecordsWithoutMovingAtos pins the half of the round trip
// that lives here: a tier judgment only authorises. The ATOS stays in the miner
// pool until x/bridgeadapter sees Ethereum confirm the matching ERC20, because
// releasing first would let holders convert ATOX into ATOS nothing backs.
func TestTriggerRelease_RecordsWithoutMovingAtos(t *testing.T) {
	bk := newTestBankKeeper()
	minerAddr := authtypes.NewModuleAddress(tokenomicstypes.MinerPoolName)
	projectAddr := authtypes.NewModuleAddress(tokenomicstypes.ProjectPoolName)
	// Fund both pools at their real genesis sizes so the release is not clamped;
	// clamping is covered separately below.
	bk.balances[minerAddr.String()] = sdk.NewCoins(sdk.NewCoin(atoshitypes.BaseDenom, math.NewIntWithDecimal(1, 30)))
	bk.balances[projectAddr.String()] = sdk.NewCoins(sdk.NewCoin(atoshitypes.BaseDenom, math.NewIntWithDecimal(87, 29)))

	k, ctx := newKeeperForTest(t, bk, testStakingKeeper{}, testOracleKeeper{})
	xk := k.atoxKeeper.(*testAtoxKeeper)

	state := k.GetReleaseState(ctx)
	params := k.GetParams(ctx)
	require.NoError(t, k.TriggerRelease(ctx, &state, params))

	// 5% of circulating, split 50/50 between miner and project.
	quota := k.GetCirculatingSupply(ctx).MulRaw(int64(params.ReleasePercentageBps)).QuoRaw(10000)
	expectMiner := quota.MulRaw(int64(params.MinerReleaseShareBps)).QuoRaw(10000)

	require.Equal(t, expectMiner.String(), state.TotalMinerReleased.String(),
		"the authorised figure is recorded")
	require.True(t, state.TotalProjectReleased.IsPositive())

	require.Empty(t, xk.pooled,
		"no ATOS may reach the conversion pool until Ethereum confirms")
	require.Equal(t, math.NewIntWithDecimal(1, 30).String(),
		bk.GetBalance(ctx, minerAddr, atoshitypes.BaseDenom).Amount.String(),
		"the miner pool balance is untouched by a tier judgment")

	// What bridgeadapter will read to bound an incoming receipt. It reads
	// COMMITTED state, which is why the caller must persist first: TriggerRelease
	// only mutates the state it was handed, and EndBlocker is what writes it. The
	// asymmetry is correct — receipts arrive in later blocks, by which time the
	// authorisation is committed — but it means a reader in the same block as the
	// judgment would see the pre-release figure.
	require.NoError(t, k.SetReleaseState(ctx, state))
	authMiner, authProject := k.AuthorizedReleases(ctx)
	require.Equal(t, state.TotalMinerReleased.String(), authMiner.String())
	require.Equal(t, state.TotalProjectReleased.String(), authProject.String())
}

// TestTriggerRelease_AuthorizationCappedByPoolBalance — the quota is derived from
// circulating supply, so authorising more than the pool holds would let a
// receipt later demand ATOS that does not exist.
func TestTriggerRelease_AuthorizationCappedByPoolBalance(t *testing.T) {
	bk := newTestBankKeeper()
	minerAddr := authtypes.NewModuleAddress(tokenomicstypes.MinerPoolName)
	bk.balances[minerAddr.String()] = sdk.NewCoins(sdk.NewCoin(atoshitypes.BaseDenom, math.NewInt(7)))

	k, ctx := newKeeperForTest(t, bk, testStakingKeeper{}, testOracleKeeper{})

	state := k.GetReleaseState(ctx)
	require.NoError(t, k.TriggerRelease(ctx, &state, k.GetParams(ctx)))

	require.Equal(t, "7", state.TotalMinerReleased.String(),
		"cannot authorise more ATOS than the miner pool holds")
}

// TestTriggerReleaseCapsProjectAuthorisationToPoolBalance checks the cap on how
// much a tier judgment may authorise.
//
// It used to assert on ProjectClaimable, which TriggerRelease credited directly.
// It no longer does: per the design doc the forward leg only books the
// authorisation and ProjectClaimable is credited from the Ethereum receipt in
// step 4. Crediting both places double-counted every release. The cap itself is
// unchanged, so the assertion moves to the authorisation counter.
func TestTriggerReleaseCapsProjectAuthorisationToPoolBalance(t *testing.T) {
	bk := newTestBankKeeper()
	projectAddr := authtypes.NewModuleAddress(tokenomicstypes.ProjectPoolName)
	bk.balances[projectAddr.String()] = sdk.NewCoins(sdk.NewCoin(atoshitypes.BaseDenom, math.NewInt(10)))

	k, ctx := newKeeperForTest(t, bk, testStakingKeeper{}, testOracleKeeper{})
	state := tokenomicstypes.DefaultReleaseState()
	params := tokenomicstypes.DefaultParams()
	params.ReleasePercentageBps = 1000
	params.MinerReleaseShareBps = 0
	params.ProjectReleaseShareBps = 10000

	require.NoError(t, k.TriggerRelease(ctx, &state, params))
	require.Equal(t, math.NewInt(10), state.TotalProjectReleased,
		"cannot authorise more ATOS than the project pool holds")
	require.True(t, k.GetProjectClaimable(ctx).IsZero(),
		"ProjectClaimable must stay untouched until the Ethereum receipt arrives")
}

func TestClaimMigrationTokensWithValidMerkleProof(t *testing.T) {
	bk := newTestBankKeeper()
	poolAddr := authtypes.NewModuleAddress(tokenomicstypes.MigrationPoolName)
	bk.balances[poolAddr.String()] = sdk.NewCoins(sdk.NewCoin(atoshitypes.BaseDenom, math.NewInt(100)))

	k, ctx := newKeeperForTest(t, bk, testStakingKeeper{}, testOracleKeeper{})
	msgSrv := NewMsgServerImpl(k)
	claimer := sdk.AccAddress([]byte("claimer_______________")).String()
	otherClaimer := sdk.AccAddress([]byte("other_________________")).String()
	amount := math.NewInt(25)
	otherAmount := math.NewInt(10)

	leafA := migrationLeafHash(claimer, amount)
	leafB := migrationLeafHash(otherClaimer, otherAmount)
	// Sorted-pair hashing: parent = sha256(min(a,b) || max(a,b)).
	left, right := leafA, leafB
	if bytes.Compare(leafA, leafB) > 0 {
		left, right = leafB, leafA
	}
	combined := append([]byte{}, left...)
	combined = append(combined, right...)
	root := sha256.Sum256(combined)
	rootHex := hex.EncodeToString(root[:])

	params := k.GetParams(ctx)
	params.MigrationMerkleRoot = rootHex
	require.NoError(t, k.SetParams(ctx, params))

	_, err := msgSrv.ClaimMigrationTokens(sdk.WrapSDKContext(ctx), &tokenomicstypes.MsgClaimMigrationTokens{
		Claimer:     claimer,
		Amount:      amount,
		MerkleProof: [][]byte{leafB},
	})
	require.NoError(t, err)
	require.True(t, k.HasMigrationClaimed(ctx, claimer))
}

// Audit Issue 3 regression: EndBlocker's tier check previously consumed
// whatever GetCurrentPrice returned without checking the price's
// freshness. A stale high-tier price could persistently drive the
// ConsecutiveDays counter upward and eventually trigger an undeserved
// miner/project release. The fixed code rejects price data older than
// oracle.params.MaxPriceAgeSeconds and treats staleness as "no signal"
// (pauses the streak; does not reset it).

// helper to make a fresh keeper with a controllable oracle.
func newKeeperWithOracle(t *testing.T, ok testOracleKeeper) (Keeper, sdk.Context) {
	bk := newTestBankKeeper()
	bk.balances[authtypes.NewModuleAddress("tokenomics_miner_pool").String()] =
		sdk.NewCoins(sdk.NewCoin("liao", math.NewIntWithDecimal(1, 24)))
	bk.balances[authtypes.NewModuleAddress("tokenomics_project_pool").String()] =
		sdk.NewCoins(sdk.NewCoin("liao", math.NewIntWithDecimal(1, 24)))
	sk := testStakingKeeper{totalBonded: math.NewInt(100)}
	return newKeeperForTest(t, bk, sk, ok)
}

// Set release state so the next EndBlocker tick is forced to evaluate.
func forceTierEvaluation(t *testing.T, k Keeper, ctx sdk.Context) {
	st := k.GetReleaseState(ctx)
	st.LastCheckBlock = 0 // force evaluation regardless of epoch
	require.NoError(t, k.SetReleaseState(ctx, st))
}

func TestEndBlocker_RejectsStaleOraclePrice(t *testing.T) {
	// Price timestamp is older than MaxPriceAgeSeconds (default 3600).
	now := int64(1_700_000_000)
	stalePrice := oracletypes.PriceData{
		Price:     math.LegacyMustNewDecFromStr("1000.00"), // way above any tier
		Volume24h: math.LegacyMustNewDecFromStr("1000000"),
		Timestamp: now - 7200, // 2 hours stale
	}
	k, ctx := newKeeperWithOracle(t, testOracleKeeper{price: stalePrice})
	ctx = ctx.WithBlockTime(time.Unix(now, 0)).WithBlockHeight(100)
	forceTierEvaluation(t, k, ctx)

	require.NoError(t, k.EndBlocker(ctx))

	// ConsecutiveDays must NOT have been incremented despite the
	// high-tier price, because the price is stale.
	got := k.GetReleaseState(ctx)
	require.EqualValues(t, 0, got.ConsecutiveDays,
		"stale oracle data must not advance the tier streak; got %d", got.ConsecutiveDays)
}

func TestEndBlocker_FreshPriceAdvancesStreak(t *testing.T) {
	// Price timestamp is fresh (within MaxPriceAgeSeconds), price+volume
	// above tier 0 thresholds → ConsecutiveDays should increment.
	now := int64(1_700_000_000)
	freshPrice := oracletypes.PriceData{
		Price:     math.LegacyMustNewDecFromStr("1000.00"),
		Volume24h: math.LegacyMustNewDecFromStr("1000000"),
		Timestamp: now - 60, // 1 minute old
	}
	k, ctx := newKeeperWithOracle(t, testOracleKeeper{price: freshPrice})
	ctx = ctx.WithBlockTime(time.Unix(now, 0)).WithBlockHeight(100)
	forceTierEvaluation(t, k, ctx)

	require.NoError(t, k.EndBlocker(ctx))

	got := k.GetReleaseState(ctx)
	require.EqualValues(t, 1, got.ConsecutiveDays,
		"fresh high-tier price should advance the streak; got %d", got.ConsecutiveDays)
}

func TestEndBlocker_StalenessPausesNotResetsStreak(t *testing.T) {
	// Build state with an existing streak; then go stale. Expectation:
	// streak holds (doesn't reset to 0), but doesn't grow either.
	now := int64(1_700_000_000)
	stalePrice := oracletypes.PriceData{
		Price:     math.LegacyMustNewDecFromStr("1000.00"),
		Volume24h: math.LegacyMustNewDecFromStr("1000000"),
		Timestamp: now - 7200, // stale
	}
	k, ctx := newKeeperWithOracle(t, testOracleKeeper{price: stalePrice})
	ctx = ctx.WithBlockTime(time.Unix(now, 0)).WithBlockHeight(100)

	// Pre-load a 5-day streak.
	st := k.GetReleaseState(ctx)
	st.ConsecutiveDays = 5
	st.LastCheckBlock = 0
	require.NoError(t, k.SetReleaseState(ctx, st))

	require.NoError(t, k.EndBlocker(ctx))

	got := k.GetReleaseState(ctx)
	require.EqualValues(t, 5, got.ConsecutiveDays,
		"staleness must pause (not reset) an existing streak; got %d", got.ConsecutiveDays)
}

func TestEndBlocker_ZeroTimestampIsTreatedAsStale(t *testing.T) {
	// Timestamp=0 is the zero-value default for an unset oracle entry.
	// Should be treated as stale, not as "1970-01-01 timestamp".
	now := int64(1_700_000_000)
	zeroTs := oracletypes.PriceData{
		Price:     math.LegacyMustNewDecFromStr("1000.00"),
		Volume24h: math.LegacyMustNewDecFromStr("1000000"),
		Timestamp: 0,
	}
	k, ctx := newKeeperWithOracle(t, testOracleKeeper{price: zeroTs})
	ctx = ctx.WithBlockTime(time.Unix(now, 0)).WithBlockHeight(100)
	forceTierEvaluation(t, k, ctx)

	require.NoError(t, k.EndBlocker(ctx))

	got := k.GetReleaseState(ctx)
	require.EqualValues(t, 0, got.ConsecutiveDays)
}

// Audit Issue-18 (round2) regression: when totalBonded is zero,
// BeginBlocker must NOT mutate any state. Pre-fix the function
// transferred the immediate share from MinerPool to FeeCollector and
// updated releaseState IN MEMORY before checking totalBonded, then
// returned early without persisting the in-memory updates. Result:
//   - bank state: FeeCollector got the liao (committed),
//   - blockRewardState.TotalDistributed: not persisted.
//
// On the next block, the same currentReward would be computed again
// (TotalDistributed unchanged) and another chunk would land in the
// FeeCollector — silently over-distributing while tokenomics
// accounting diverged from bank reality.
//
// Post-fix: the totalBonded zero-check runs BEFORE any bank send or
// in-memory state mutation. With zero validators, BeginBlocker is a
// clean no-op: bank balances and persisted state both unchanged.
func TestBeginBlocker_NoStateChangeWhenNoBondedValidators(t *testing.T) {
	bk := newTestBankKeeper()
	minerPoolAddr := authtypes.NewModuleAddress(tokenomicstypes.MinerPoolName)
	feeCollectorAddr := authtypes.NewModuleAddress(authtypes.FeeCollectorName)
	bk.balances[minerPoolAddr.String()] = sdk.NewCoins(sdk.NewCoin(atoshitypes.BaseDenom, math.NewIntWithDecimal(1, 20)))

	// Zero bonded validators — the audit's failing scenario.
	k, ctx := newKeeperForTest(t, bk, testStakingKeeper{totalBonded: math.ZeroInt()}, testOracleKeeper{})

	params := k.GetParams(ctx)
	params.InitialBlockReward = math.NewInt(100)
	params.HalvingIntervalBlocks = 100
	require.NoError(t, k.SetParams(ctx, params))

	preBlockRewardState := k.GetBlockRewardState(ctx)
	preReleaseState := k.GetReleaseState(ctx)
	preMinerPool := bk.balances[minerPoolAddr.String()].AmountOf(atoshitypes.BaseDenom)
	preFeeCollector := bk.balances[feeCollectorAddr.String()].AmountOf(atoshitypes.BaseDenom)

	require.NoError(t, k.BeginBlocker(ctx.WithBlockHeight(1)))

	postBlockRewardState := k.GetBlockRewardState(ctx)
	postReleaseState := k.GetReleaseState(ctx)
	postMinerPool := bk.balances[minerPoolAddr.String()].AmountOf(atoshitypes.BaseDenom)
	postFeeCollector := bk.balances[feeCollectorAddr.String()].AmountOf(atoshitypes.BaseDenom)

	// Bank state: NO immediate transfer should have happened.
	require.True(t, preMinerPool.Equal(postMinerPool),
		"audit Issue-18: MinerPool must NOT lose coins when no validators are bonded; pre=%s post=%s",
		preMinerPool, postMinerPool)
	require.True(t, preFeeCollector.Equal(postFeeCollector),
		"audit Issue-18: FeeCollector must NOT receive immediate share when no validators are bonded; pre=%s post=%s",
		preFeeCollector, postFeeCollector)

	// Tokenomics accounting: counters must NOT advance.
	require.True(t, preBlockRewardState.TotalDistributed.Equal(postBlockRewardState.TotalDistributed),
		"audit Issue-18: TotalDistributed must not move")
	require.True(t, preReleaseState.TotalMinerReleased.Equal(postReleaseState.TotalMinerReleased),
		"audit Issue-18: release state must not move when nothing is bonded")
}
