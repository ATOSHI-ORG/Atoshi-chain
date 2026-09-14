package atox_test

import (
	"math/big"
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/suite"

	"github.com/atoshi-chain/atoshi/v20/precompiles/atox"
	testkeyring "github.com/atoshi-chain/atoshi/v20/testutil/integration/evmos/keyring"
	"github.com/atoshi-chain/atoshi/v20/testutil/integration/evmos/network"
	atoxtypes "github.com/atoshi-chain/atoshi/v20/x/atox/types"
	"github.com/atoshi-chain/atoshi/v20/x/evm/core/vm"
	evmtypes "github.com/atoshi-chain/atoshi/v20/x/evm/types"
)

type PrecompileTestSuite struct {
	suite.Suite

	network *network.UnitTestNetwork
	keyring testkeyring.Keyring

	precompile *atox.Precompile
}

func TestPrecompileTestSuite(t *testing.T) {
	suite.Run(t, new(PrecompileTestSuite))
}

func (s *PrecompileTestSuite) SetupTest() {
	keyring := testkeyring.New(2)
	nw := network.NewUnitTestNetwork(
		network.WithPreFundedAccounts(keyring.GetAllAccAddrs()...),
	)

	s.network = nw
	s.keyring = keyring

	var err error
	s.precompile, err = atox.NewPrecompile(
		s.network.App.AtoxKeeper,
		s.network.App.AuthzKeeper,
	)
	s.Require().NoError(err)
}

func (s *PrecompileTestSuite) ctx() sdk.Context { return s.network.GetContext() }

func contractFor(caller common.Address) *vm.Contract {
	return &vm.Contract{CallerAddress: caller}
}

func (s *PrecompileTestSuite) method(name string) *abi.Method {
	m := s.precompile.ABI.Methods[name]
	return &m
}

// TestAddressIsRegistered pins the precompile to 0x…0809. A wallet hardcodes
// this address, so moving it silently breaks every client already shipped.
func (s *PrecompileTestSuite) TestAddressIsRegistered() {
	s.Require().Equal(
		common.HexToAddress(evmtypes.AtoxPrecompileAddress),
		s.precompile.Address(),
	)
	s.Require().Contains(evmtypes.AvailableStaticPrecompiles, evmtypes.AtoxPrecompileAddress)
}

// TestOnlyClaimWritesState: everything else must stay callable from eth_call,
// or a wallet cannot read them without sending a transaction.
func (s *PrecompileTestSuite) TestOnlyClaimWritesState() {
	for name, want := range map[string]bool{
		atox.ClaimMethod:        true,
		atox.ClaimableMethod:    false,
		atox.BurnOnSettleMethod: false,
		atox.GlobalIndexMethod:  false,
	} {
		s.Require().Equal(want, s.precompile.IsTransaction(s.method(name)), "method %s", name)
	}
}

// TestContractCannotClaimForSigner is the authorization model: a contract the
// user merely interacts with must not be able to burn their ATOX.
func (s *PrecompileTestSuite) TestContractCannotClaimForSigner() {
	signer := s.keyring.GetAddr(0)
	attacker := s.keyring.GetAddr(1)

	_, err := s.precompile.Claim(
		s.ctx(), signer, contractFor(attacker), s.network.GetStateDB(),
		s.method(atox.ClaimMethod), []interface{}{},
	)
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "must be called directly")
}

// TestClaimRevertsWithNothingToClaim: succeeding with zero would let a batched
// caller pay gas for a no-op, and would make a UI that offers the button
// unconditionally look like it worked.
func (s *PrecompileTestSuite) TestClaimRevertsWithNothingToClaim() {
	addr := s.keyring.GetAddr(0)

	_, err := s.precompile.Claim(
		s.ctx(), addr, contractFor(addr), s.network.GetStateDB(),
		s.method(atox.ClaimMethod), []interface{}{},
	)
	s.Require().ErrorIs(err, atoxtypes.ErrNothingToClaim)
}

// TestReadsAreZeroForAFreshAccount pins that the read methods answer rather
// than error for an account the module has never seen -- a wallet calls these
// before the holder has any ATOX at all.
func (s *PrecompileTestSuite) TestReadsAreZeroForAFreshAccount() {
	addr := s.keyring.GetAddr(0)
	ctx := s.ctx()

	for _, name := range []string{atox.ClaimableMethod, atox.BurnOnSettleMethod} {
		var (
			bz  []byte
			err error
		)
		switch name {
		case atox.ClaimableMethod:
			bz, err = s.precompile.Claimable(ctx, s.method(name), []interface{}{addr})
		case atox.BurnOnSettleMethod:
			bz, err = s.precompile.BurnOnSettle(ctx, s.method(name), []interface{}{addr})
		}
		s.Require().NoError(err, "method %s", name)

		out, err := s.method(name).Outputs.Unpack(bz)
		s.Require().NoError(err)
		// 比字符串而不是比对象：big.Int 的零有两种内部表示（abs 为 nil
		// 或长度为 0 的切片），深比较会把它们判成不等。
		s.Require().Equal("0", out[0].(*big.Int).String(), "method %s", name)
	}
}

// TestGlobalIndexIsScaledBy1e18 pins the unit the ABI promises. A client that
// assumed a different scale would misreport every holder's entitlement.
func (s *PrecompileTestSuite) TestGlobalIndexIsScaledBy1e18() {
	ctx := s.ctx()

	// Push the index to exactly 1% by funding the pool with 1% of the cap.
	// The ATOS has to exist first: AddToExchangePool moves coins between module
	// accounts, it does not create them.
	cap := s.network.App.AtoxKeeper.AtoxSupplyCap(ctx)
	onePercent := cap.Quo(math.NewInt(100))
	funding := sdk.NewCoins(sdk.NewCoin(s.network.App.AtoxKeeper.BaseDenom(), onePercent))
	s.Require().NoError(s.network.App.BankKeeper.MintCoins(ctx, atoxtypes.ModuleName, funding))
	s.Require().NoError(
		s.network.App.AtoxKeeper.AddToExchangePool(ctx, atoxtypes.ModuleName, onePercent),
	)

	bz, err := s.precompile.GlobalIndex(ctx, s.method(atox.GlobalIndexMethod), []interface{}{})
	s.Require().NoError(err)
	out, err := s.method(atox.GlobalIndexMethod).Outputs.Unpack(bz)
	s.Require().NoError(err)

	// 1% scaled by 1e18.
	want := new(big.Int).Div(new(big.Int).SetUint64(1e18), big.NewInt(100))
	s.Require().Equal(want.String(), out[0].(*big.Int).String())
}
