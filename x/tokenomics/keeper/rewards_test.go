package keeper

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"cosmossdk.io/log"
	"cosmossdk.io/math"
	"cosmossdk.io/store/metrics"
	"cosmossdk.io/store/rootmulti"
	storetypes "cosmossdk.io/store/types"
	dbm "github.com/cosmos/cosmos-db"
	tmproto "github.com/cometbft/cometbft/proto/tendermint/types"
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

func (testAccountKeeper) GetModuleAddress(name string) sdk.AccAddress { return authtypes.NewModuleAddress(name) }
func (testAccountKeeper) GetModuleAccount(ctx context.Context, moduleName string) sdk.ModuleAccountI {
	return nil
}
func (testAccountKeeper) GetAccount(ctx context.Context, addr sdk.AccAddress) sdk.AccountI { return nil }
func (testAccountKeeper) SetAccount(ctx context.Context, acc sdk.AccountI)               {}

type testStakingKeeper struct {
	totalBonded math.Int
	validators  []stakingtypes.Validator
}

func (sk testStakingKeeper) TotalBondedTokens(ctx context.Context) (math.Int, error) { return sk.totalBonded, nil }
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
	price oracletypes.PriceData
	err   error
}

func (ok testOracleKeeper) GetCurrentPrice(ctx sdk.Context) (oracletypes.PriceData, error) {
	return ok.price, ok.err
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
	k := NewKeeper(key, cdc, authtypes.NewModuleAddress("gov"), authtypes.FeeCollectorName, testAccountKeeper{}, bk, sk, testDistrKeeper{}, ok)
	require.NoError(t, k.SetParams(ctx, tokenomicstypes.DefaultParams()))
	require.NoError(t, k.SetReleaseState(ctx, tokenomicstypes.DefaultReleaseState()))
	require.NoError(t, k.SetBlockRewardState(ctx, tokenomicstypes.DefaultBlockRewardState()))
	return k, ctx
}

func TestBeginBlockerDistributesImmediateAndLockedRewards(t *testing.T) {
	bk := newTestBankKeeper()
	bk.balances[authtypes.NewModuleAddress(tokenomicstypes.MinerPoolName).String()] = sdk.NewCoins(sdk.NewCoin(atoshitypes.BaseDenom, math.NewIntWithDecimal(1, 20)))

	v1 := stakingtypes.Validator{OperatorAddress: "atoshivaloper1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Tokens: math.NewInt(100)}
	v2 := stakingtypes.Validator{OperatorAddress: "atoshivaloper1bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Tokens: math.NewInt(300)}
	k, ctx := newKeeperForTest(t, bk, testStakingKeeper{totalBonded: math.NewInt(400), validators: []stakingtypes.Validator{v1, v2}}, testOracleKeeper{})

	params := k.GetParams(ctx)
	params.InitialBlockReward = math.NewInt(100)
	params.HalvingIntervalBlocks = 100
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, k.BeginBlocker(ctx.WithBlockHeight(1)))

	feeCollectorBalance := bk.GetBalance(ctx, authtypes.NewModuleAddress(authtypes.FeeCollectorName), atoshitypes.BaseDenom)
	require.Equal(t, math.NewInt(20), feeCollectorBalance.Amount)

	balA := k.GetMinerLockedBalance(ctx, v1.OperatorAddress)
	balB := k.GetMinerLockedBalance(ctx, v2.OperatorAddress)
	require.Equal(t, math.NewInt(20), balA.LockedAccrued)
	require.Equal(t, math.NewInt(60), balB.LockedAccrued)
}

func TestReleaseMinerLockedRewards(t *testing.T) {
	bk := newTestBankKeeper()
	k, ctx := newKeeperForTest(t, bk, testStakingKeeper{}, testOracleKeeper{})

	require.NoError(t, k.SetMinerLockedBalance(ctx, tokenomicstypes.MinerLockedBalance{ValidatorAddress: "atoshivaloper1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", LockedAccrued: math.NewInt(100)}))
	require.NoError(t, k.SetMinerLockedBalance(ctx, tokenomicstypes.MinerLockedBalance{ValidatorAddress: "atoshivaloper1bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", LockedAccrued: math.NewInt(300)}))

	released := k.ReleaseMinerLockedRewards(ctx, math.NewInt(200))
	require.Equal(t, math.NewInt(200), released)

	balA := k.GetMinerLockedBalance(ctx, "atoshivaloper1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	balB := k.GetMinerLockedBalance(ctx, "atoshivaloper1bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	require.Equal(t, math.NewInt(50), balA.LockedClaimable)
	require.Equal(t, math.NewInt(150), balB.LockedClaimable)
}

func TestTriggerReleaseCapsProjectClaimableToPoolBalance(t *testing.T) {
	bk := newTestBankKeeper()
	projectAddr := authtypes.NewModuleAddress(tokenomicstypes.ProjectPoolName)
	bk.balances[projectAddr.String()] = sdk.NewCoins(sdk.NewCoin(atoshitypes.BaseDenom, math.NewInt(10)))

	k, ctx := newKeeperForTest(t, bk, testStakingKeeper{}, testOracleKeeper{})
	state := tokenomicstypes.DefaultReleaseState()
	state.TotalImmediateDistributed = math.NewInt(1000)
	params := tokenomicstypes.DefaultParams()
	params.ReleasePercentageBps = 1000
	params.MinerReleaseShareBps = 0
	params.ProjectReleaseShareBps = 10000

	require.NoError(t, k.TriggerRelease(ctx, &state, params))
	require.Equal(t, math.NewInt(10), k.GetProjectClaimable(ctx))
}

func TestClaimMinerLockedReward(t *testing.T) {
	bk := newTestBankKeeper()
	poolAddr := authtypes.NewModuleAddress(tokenomicstypes.MinerLockedPoolName)
	bk.balances[poolAddr.String()] = sdk.NewCoins(sdk.NewCoin(atoshitypes.BaseDenom, math.NewInt(50)))

	k, ctx := newKeeperForTest(t, bk, testStakingKeeper{}, testOracleKeeper{})
	msgSrv := NewMsgServerImpl(k)
	valAddr := sdk.ValAddress([]byte("validator_addr_test__")).String()
	require.NoError(t, k.SetMinerLockedBalance(ctx, tokenomicstypes.MinerLockedBalance{ValidatorAddress: valAddr, LockedClaimable: math.NewInt(50)}))

	_, err := msgSrv.ClaimMinerLockedReward(sdk.WrapSDKContext(ctx), &tokenomicstypes.MsgClaimMinerLockedReward{ValidatorAddress: valAddr})
	require.NoError(t, err)

	bal := k.GetMinerLockedBalance(ctx, valAddr)
	require.True(t, bal.LockedClaimable.IsZero())
	require.Equal(t, math.NewInt(50), bal.LockedClaimed)
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
