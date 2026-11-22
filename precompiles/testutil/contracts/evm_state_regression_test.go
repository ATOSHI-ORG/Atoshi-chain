package contracts_test

import (
	"bytes"
	"math/big"
	"testing"
	"time"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	"github.com/atoshi-chain/atoshi/v20/precompiles/authorization"
	"github.com/atoshi-chain/atoshi/v20/precompiles/staking"
	"github.com/atoshi-chain/atoshi/v20/precompiles/testutil/contracts"
	"github.com/atoshi-chain/atoshi/v20/testutil/integration/evmos/factory"
	"github.com/atoshi-chain/atoshi/v20/testutil/integration/evmos/grpc"
	testkeyring "github.com/atoshi-chain/atoshi/v20/testutil/integration/evmos/keyring"
	"github.com/atoshi-chain/atoshi/v20/testutil/integration/evmos/network"
	testutils "github.com/atoshi-chain/atoshi/v20/testutil/integration/evmos/utils"
	evmtypes "github.com/atoshi-chain/atoshi/v20/x/evm/types"
)

// These tests replay the attacks found in the audit after the 2026-11-22
// precompile mint. Each one succeeded before v20.5; each asserts it no longer
// does.

type stateEnv struct {
	nw   *network.UnitTestNetwork
	tf   factory.TxFactory
	kr   testkeyring.Keyring
	c    evmtypes.CompiledContract
	s    common.Address // attack contract; sorts after m
	x    common.Address // receives the leaked credit; sorts before m
	m    common.Address // existing module account, so the pre-precompile flush fails on it
	pool common.Address // bonded pool
	val  string
	bond string
}

func newStateEnv(t *testing.T) *stateEnv {
	t.Helper()
	kr := testkeyring.New(8)
	nw := network.NewUnitTestNetwork(network.WithPreFundedAccounts(kr.GetAllAccAddrs()...))
	tf := factory.New(nw, grpc.NewIntegrationHandler(nw))
	c, err := contracts.LoadEvmStateRegressionContract()
	require.NoError(t, err)

	e := &stateEnv{nw: nw, tf: tf, kr: kr, c: c}
	e.bond, err = nw.App.StakingKeeper.BondDenom(nw.GetContext())
	require.NoError(t, err)
	e.pool = common.BytesToAddress(authtypes.NewModuleAddress(stakingtypes.BondedPoolName))
	e.val = nw.GetValidators()[0].OperatorAddress

	var mods []common.Address
	for k := range nw.App.BlockedAddrs() {
		a := sdk.MustAccAddressFromBech32(k)
		if nw.App.AccountKeeper.GetAccount(nw.GetContext(), a) == nil {
			continue
		}
		mods = append(mods, common.BytesToAddress(a))
	}
	// The flush writes dirty accounts in address order, so the attack needs
	// X < M < S. Redeploy until the contract lands somewhere that allows it.
	for i := 0; i < 10 && e.s == (common.Address{}); i++ {
		s, err := tf.DeployContract(kr.GetPrivKey(0), evmtypes.EvmTxArgs{}, factory.ContractDeploymentData{Contract: c})
		require.NoError(t, err)
		require.NoError(t, nw.NextBlock())
		for _, m := range mods {
			if bytes.Compare(m.Bytes(), s.Bytes()) >= 0 || m == e.pool {
				continue
			}
			for j := 1; j < 8; j++ {
				if x := kr.GetAddr(j); bytes.Compare(x.Bytes(), m.Bytes()) < 0 {
					e.s, e.m, e.x = s, m, x
					break
				}
			}
			if e.s != (common.Address{}) {
				break
			}
		}
	}
	require.NotEqual(t, common.Address{}, e.s, "no X < M < S ordering found")
	require.NoError(t, testutils.FundAccountWithBaseDenom(tf, nw, kr.GetKey(0), e.s.Bytes(), math.NewInt(10).MulRaw(1e18)))
	require.NoError(t, nw.NextBlock())
	return e
}

func (e *stateEnv) bal(a common.Address) math.Int {
	return e.nw.App.BankKeeper.GetBalance(e.nw.GetContext(), a.Bytes(), e.bond).Amount
}

func (e *stateEnv) supply() math.Int {
	return e.nw.App.BankKeeper.GetSupply(e.nw.GetContext(), e.bond).Amount
}

// approve lets the attack contract delegate the signer's stake. It is sent
// directly to the precompile by the signer, which is still allowed.
func (e *stateEnv) approve(t *testing.T, amt *big.Int) {
	t.Helper()
	sp, err := staking.NewPrecompile(e.nw.App.StakingKeeper, e.nw.App.AuthzKeeper)
	require.NoError(t, err)
	addr := sp.Address()
	res, err := e.tf.ExecuteContractCall(e.kr.GetPrivKey(0), evmtypes.EvmTxArgs{To: &addr}, factory.CallArgs{
		ContractABI: sp.ABI, MethodName: authorization.ApproveMethod,
		Args: []interface{}{e.s, amt, []string{staking.DelegateMsg}},
	})
	require.NoError(t, err)
	require.True(t, res.IsOK(), res.Log)
	require.NoError(t, e.nw.NextBlock())
}

func (e *stateEnv) call(t *testing.T, gas uint64, method string, args ...interface{}) bool {
	t.Helper()
	res, err := e.tf.ExecuteContractCall(e.kr.GetPrivKey(0),
		evmtypes.EvmTxArgs{To: &e.s, GasLimit: gas, GasPrice: big.NewInt(1e9)},
		factory.CallArgs{ContractABI: e.c.ABI, MethodName: method, Args: args})
	require.NoError(t, e.nw.NextBlock())
	return err == nil && res.IsOK()
}

// The flush before a precompile call failed part-way on a module account and
// left the accounts sorted before it written, with nothing on the journal to
// undo them: X kept a credit the EVM had reverted. On its own the supply
// guard failed the whole tx; now the inner frame reverts cleanly, so the
// outer call, which catches it, succeeds and nothing leaks.
func TestPartialPrecompileFlushLeavesNoCredit(t *testing.T) {
	e := newStateEnv(t)
	xBefore, supBefore := e.bal(e.x), e.supply()

	ok := e.call(t, 3_000_000, "mintOnly", e.x, e.m, big.NewInt(1e18))

	require.True(t, ok, "the reverted inner frame should leave nothing for the supply guard to catch")

	require.True(t, e.bal(e.x).Equal(xBefore), "X was credited by a reverted frame")
	require.True(t, e.supply().LTE(supBefore), "supply rose")
}

// A delegation made through the precompile moves coins into the bonded pool
// behind the StateDB's back; a 1-wei EVM transfer to the pool then made the
// commit write the stale pool balance back, burning the delegation's coins.
// Changing a module account's balance from the EVM now fails the tx.
func TestStaleModuleBalanceCannotBurnPoolFunds(t *testing.T) {
	e := newStateEnv(t)
	d := big.NewInt(1e18)
	e.approve(t, d)
	poolBefore, supBefore := e.bal(e.pool), e.supply()

	ok := e.call(t, 3_000_000, "burnOnly", e.val, d, e.pool)

	require.False(t, ok, "tx changing the bonded pool's balance from the EVM must fail")
	require.True(t, e.bal(e.pool).Equal(poolBefore), "bonded pool balance changed")
	require.True(t, e.supply().Equal(supBefore), "supply changed")
}

// Both halves together kept the supply flat, so the supply guard let the
// attacker walk away with D-1 while keeping a delegation of D.
func TestSupplyGuardBypassIsClosed(t *testing.T) {
	e := newStateEnv(t)
	d := big.NewInt(1e18)
	e.approve(t, d)
	xBefore, poolBefore := e.bal(e.x), e.bal(e.pool)

	e.call(t, 3_000_000, "exploit", e.x, e.m, new(big.Int).Sub(d, big.NewInt(1)), e.val, d, e.pool)

	require.True(t, e.bal(e.x).Equal(xBefore), "X gained funds")
	require.True(t, e.bal(e.pool).Equal(poolBefore), "bonded pool balance changed")
}

// The call counter was a uint8 incremented before the check, so it wrapped
// and 300 attempts got 15 calls through a limit of 7.
func TestPrecompileCallLimitDoesNotWrap(t *testing.T) {
	e := newStateEnv(t)
	require.True(t, e.call(t, 30_000_000, "countOk", big.NewInt(300), e.val))

	n := e.nw.App.EvmKeeper.GetState(e.nw.GetContext(), e.s, common.Hash{}).Big()
	require.LessOrEqual(t, n.Uint64(), evmtypes.MaxPrecompileCalls)
}

// Every precompile call copied the cache multistore and re-flushed every dirty
// slot before the limit was checked; 50,000 zero-gas calls over 1,500 dirty
// slots took 63.7s. Calls past the limit now fail before any of that work.
func TestCallsPastTheLimitDoNoWork(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	e := newStateEnv(t)
	for i := int64(0); i < 4; i++ {
		require.True(t, e.call(t, 25_000_000, "spamVal", big.NewInt(5000+i*500), big.NewInt(500), big.NewInt(1), big.NewInt(0)))
	}

	start := time.Now()
	e.call(t, 25_000_000, "spamVal", big.NewInt(5000), big.NewInt(1500), big.NewInt(2), big.NewInt(50_000))
	elapsed := time.Since(start)

	// Unpatched this is about a minute; patched it is about a second. The
	// bound is loose so that slow CI machines do not flake.
	require.Less(t, elapsed, 15*time.Second, "precompile call spam is still expensive: %s", elapsed)
}
