package distribution_test

import (
	"math/big"

	"cosmossdk.io/math"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	distrtypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/ethereum/go-ethereum/common"
	//nolint:revive // dot imports are fine for Ginkgo
	. "github.com/onsi/gomega"

	"github.com/atoshi-chain/atoshi/v20/precompiles/authorization"
	"github.com/atoshi-chain/atoshi/v20/precompiles/testutil"
	"github.com/atoshi-chain/atoshi/v20/testutil/integration/evmos/factory"
	evmtypes "github.com/atoshi-chain/atoshi/v20/x/evm/types"

	"github.com/atoshi-chain/atoshi/v20/precompiles/staking"
	"github.com/atoshi-chain/atoshi/v20/testutil/integration/evmos/keyring"
	inflationtypes "github.com/atoshi-chain/atoshi/v20/x/inflation/v1/types"
)

type stakingRewards struct {
	Delegator sdk.AccAddress
	Validator stakingtypes.Validator
	RewardAmt math.Int
}

var (
	testRewardsAmt, _       = math.NewIntFromString("1000000000000000000")
	validatorCommPercentage = math.LegacyNewDecWithPrec(5, 2) // 5% commission
	validatorCommAmt        = math.LegacyNewDecFromInt(testRewardsAmt).Mul(validatorCommPercentage).TruncateInt()
	expRewardsAmt           = testRewardsAmt.Sub(validatorCommAmt) // testRewardsAmt - commission
)

// prepareStakingRewards prepares the test suite for testing delegation rewards.
//
// Specified rewards amount are allocated to the specified validator using the distribution keeper,
// such that the given amount of tokens is outstanding as a staking reward for the account.
//
// The setup is done in the following way:
//   - Fund distribution module to pay for rewards.
//   - Allocate rewards to the validator.
func (s *PrecompileTestSuite) prepareStakingRewards(ctx sdk.Context, stkRs ...stakingRewards) (sdk.Context, error) {
	for _, r := range stkRs {
		// set distribution module account balance which pays out the rewards
		coins := sdk.NewCoins(sdk.NewCoin(s.bondDenom, r.RewardAmt))
		if err := s.mintCoinsForDistrMod(ctx, coins); err != nil {
			return ctx, err
		}

		// allocate rewards to validator
		allocatedRewards := sdk.NewDecCoins(sdk.NewDecCoin(s.bondDenom, r.RewardAmt))
		if err := s.network.App.DistrKeeper.AllocateTokensToValidator(ctx, r.Validator, allocatedRewards); err != nil {
			return ctx, err
		}
	}
	return ctx, nil
}

// mintCoinsForDistrMod is a helper function to mint a specific amount of coins from the
// distribution module to pay for staking rewards.
func (s *PrecompileTestSuite) mintCoinsForDistrMod(ctx sdk.Context, amount sdk.Coins) error {
	// Minting tokens for the FeeCollector to simulate fee accrued.
	if err := s.network.App.BankKeeper.MintCoins(
		ctx,
		inflationtypes.ModuleName,
		amount,
	); err != nil {
		return err
	}

	return s.network.App.BankKeeper.SendCoinsFromModuleToModule(
		ctx,
		inflationtypes.ModuleName,
		distrtypes.ModuleName,
		amount,
	)
}

// fundAccountWithBaseDenom is a helper function to fund a given address with the chain's
// base denomination.
func (s *PrecompileTestSuite) fundAccountWithBaseDenom(ctx sdk.Context, addr sdk.AccAddress, amount math.Int) error {
	coins := sdk.NewCoins(sdk.NewCoin(s.bondDenom, amount))
	if err := s.network.App.BankKeeper.MintCoins(ctx, inflationtypes.ModuleName, coins); err != nil {
		return err
	}
	return s.network.App.BankKeeper.SendCoinsFromModuleToAccount(ctx, inflationtypes.ModuleName, addr, coins)
}

func (s *PrecompileTestSuite) getStakingPrecompile() (*staking.Precompile, error) {
	return staking.NewPrecompile(
		s.network.App.StakingKeeper,
		s.network.App.AuthzKeeper,
	)
}

func generateKeys(count int) []keyring.Key {
	accs := make([]keyring.Key, 0, count)
	for i := 0; i < count; i++ {
		acc := keyring.NewKey()
		accs = append(accs, acc)
	}
	return accs
}

// approveDelegationDirectly grants `grantee` a DELEGATE StakeAuthorization from
// the account of `granterPriv` by calling the staking precompile directly.
// Approvals may only be made by tx.origin calling the precompile itself (a
// contract calling approve is rejected), so contracts that delegate through the
// staking precompile need this grant set up beforehand.
func (s *PrecompileTestSuite) approveDelegationDirectly(granterPriv cryptotypes.PrivKey, grantee common.Address, amount *big.Int) {
	stkPrecompile, err := s.getStakingPrecompile()
	Expect(err).To(BeNil())

	precompileAddr := stkPrecompile.Address()
	_, _, err = s.factory.CallContractAndCheckLogs(
		granterPriv,
		evmtypes.EvmTxArgs{To: &precompileAddr},
		factory.CallArgs{
			ContractABI: stkPrecompile.ABI,
			MethodName:  authorization.ApproveMethod,
			Args:        []interface{}{grantee, amount, []string{staking.DelegateMsg}},
		},
		testutil.LogCheckArgs{
			ABIEvents: stkPrecompile.ABI.Events,
			ExpEvents: []string{authorization.EventTypeApproval},
			ExpPass:   true,
		},
	)
	Expect(err).To(BeNil(), "error while approving the delegation directly: %v", err)
	Expect(s.network.NextBlock()).To(BeNil())
}
