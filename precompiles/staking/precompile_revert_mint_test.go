package staking_test

import (
	"math/big"

	//nolint:revive // dot imports are fine for Ginkgo
	. "github.com/onsi/ginkgo/v2"
	//nolint:revive // dot imports are fine for Ginkgo
	. "github.com/onsi/gomega"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/atoshi-chain/atoshi/v20/precompiles/testutil"
	"github.com/atoshi-chain/atoshi/v20/precompiles/testutil/contracts"
	"github.com/atoshi-chain/atoshi/v20/testutil/integration/evmos/factory"
	testutils "github.com/atoshi-chain/atoshi/v20/testutil/integration/evmos/utils"
	evmtypes "github.com/atoshi-chain/atoshi/v20/x/evm/types"
)

// The 2026-11-22 mainnet mint, end to end. The minter holds `amount`, sends it
// into a frame that calls the staking precompile with calldata it cannot
// decode, and swallows that frame's revert. Nothing moved, so the minter must
// still hold `amount`, the inner contract nothing, and the supply must not
// change. Before the fix the inner contract also held `amount` and the evm
// module had minted it.
var _ = Describe("Reverted value transfer around a failing precompile call", func() {
	var (
		s           *PrecompileTestSuite
		minterAddr  common.Address
		innerAddr   common.Address
		minter      evmtypes.CompiledContract
		inner       evmtypes.CompiledContract
		amount      = math.NewInt(1e18)
		stakingAddr = common.HexToAddress(evmtypes.StakingPrecompileAddress)
	)

	BeforeEach(func() {
		var err error
		s = new(PrecompileTestSuite)
		s.SetupTest()

		minter, err = contracts.LoadPrecompileRevertMinterContract()
		Expect(err).To(BeNil())
		inner, err = contracts.LoadPrecompileRevertInnerContract()
		Expect(err).To(BeNil())

		minterAddr, err = s.factory.DeployContract(
			s.keyring.GetPrivKey(0),
			evmtypes.EvmTxArgs{},
			factory.ContractDeploymentData{Contract: minter},
		)
		Expect(err).To(BeNil())
		Expect(s.network.NextBlock()).To(BeNil())
		// The constructor's CREATE is the minter's first, at contract nonce 1.
		innerAddr = crypto.CreateAddress(minterAddr, 1)

		Expect(testutils.FundAccountWithBaseDenom(
			s.factory, s.network, s.keyring.GetKey(0), minterAddr.Bytes(), amount,
		)).To(BeNil())
		Expect(s.network.NextBlock()).To(BeNil())
	})

	balanceOf := func(addr common.Address) math.Int {
		res, err := s.grpcHandler.GetBalanceFromBank(sdk.AccAddress(addr.Bytes()), s.bondDenom)
		Expect(err).To(BeNil())
		return res.Balance.Amount
	}
	supply := func() math.Int {
		return s.network.App.BankKeeper.GetSupply(s.network.GetContext(), s.bondDenom).Amount
	}

	It("moves nothing and mints nothing", func() {
		Expect(balanceOf(minterAddr)).To(Equal(amount))
		Expect(balanceOf(innerAddr)).To(Equal(math.ZeroInt()))
		supplyBefore := supply()

		passCheck := testutil.LogCheckArgs{ABIEvents: s.precompile.ABI.Events}.WithExpPass(true)
		_, _, err := s.factory.CallContractAndCheckLogs(
			s.keyring.GetPrivKey(0),
			evmtypes.EvmTxArgs{To: &minterAddr, GasPrice: big.NewInt(1e9)},
			factory.CallArgs{
				ContractABI: minter.ABI,
				MethodName:  "attack",
				Args:        []interface{}{stakingAddr, amount.BigInt()},
			},
			passCheck,
		)
		Expect(err).To(BeNil(), "the outer call swallows the revert, so the tx must succeed")
		Expect(s.network.NextBlock()).To(BeNil())

		Expect(balanceOf(innerAddr)).To(Equal(math.ZeroInt()),
			"the reverted transfer must not leave the value with the inner contract")
		Expect(balanceOf(minterAddr)).To(Equal(amount),
			"the reverted transfer must leave the value with the minter")

		// Fees go to the fee collector, not to a burn, so the supply is flat.
		Expect(supply()).To(Equal(supplyBefore), "any rise in supply is the mint")

		// The attacker's next step: sweep whatever the inner contract holds.
		// After the fix there is nothing there to take.
		_, _, err = s.factory.CallContractAndCheckLogs(
			s.keyring.GetPrivKey(0),
			evmtypes.EvmTxArgs{To: &innerAddr, GasPrice: big.NewInt(1e9)},
			factory.CallArgs{
				ContractABI: inner.ABI,
				MethodName:  "sweep",
				Args:        []interface{}{s.keyring.GetAddr(0)},
			},
			passCheck,
		)
		Expect(err).To(BeNil())
		Expect(s.network.NextBlock()).To(BeNil())
		Expect(balanceOf(minterAddr)).To(Equal(amount))
	})
})
