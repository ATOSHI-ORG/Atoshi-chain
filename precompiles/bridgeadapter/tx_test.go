package bridgeadapter_test

import (
	"math/big"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/atoshi-chain/atoshi/v20/precompiles/bridgeadapter"
	testkeyring "github.com/atoshi-chain/atoshi/v20/testutil/integration/evmos/keyring"
	"github.com/atoshi-chain/atoshi/v20/testutil/integration/evmos/network"
	"github.com/atoshi-chain/atoshi/v20/x/evm/core/vm"
	evmtypes "github.com/atoshi-chain/atoshi/v20/x/evm/types"
)

type PrecompileTestSuite struct {
	suite.Suite

	network *network.UnitTestNetwork
	keyring testkeyring.Keyring

	precompile *bridgeadapter.Precompile
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
	s.precompile, err = bridgeadapter.NewPrecompile(
		s.network.App.BridgeAdapterKeeper,
		s.network.App.AuthzKeeper,
	)
	s.Require().NoError(err)
}

// contractFor builds a vm.Contract with the given caller, so the caller-equals-
// signer rule can be exercised without a full EVM run.
func contractFor(caller common.Address) *vm.Contract {
	return &vm.Contract{CallerAddress: caller}
}

func (s *PrecompileTestSuite) ctx() sdk.Context { return s.network.GetContext() }

func (s *PrecompileTestSuite) method() *abi.Method {
	m := s.precompile.ABI.Methods[bridgeadapter.BridgeOutMethod]
	return &m
}

// TestAddressIsPinned guards the one value a wallet and a frontend both
// hardcode. Changing it silently would send every bridgeOut call to an address
// with no code, which reverts with nothing useful.
func (s *PrecompileTestSuite) TestAddressIsPinned() {
	s.Require().Equal(
		"0x0000000000000000000000000000000000000808",
		s.precompile.Address().String(),
	)
	s.Require().Contains(
		evmtypes.AvailableStaticPrecompiles,
		evmtypes.BridgeAdapterPrecompileAddress,
		"the address must be in AvailableStaticPrecompiles or the chain will not enable it",
	)
}

// TestBridgeOutIsATransaction: a read-only call must never reach a method that
// moves funds and dispatches a cross-chain message.
func (s *PrecompileTestSuite) TestBridgeOutIsATransaction() {
	m := s.precompile.ABI.Methods[bridgeadapter.BridgeOutMethod]
	s.Require().True(s.precompile.IsTransaction(&m))
}

// TestContractCallerRefused is the authorization model in one test.
//
// If a contract could bridge out with origin as the sender, any contract a user
// merely interacts with could move their ATOS across a chain boundary, which
// cannot be undone. There is no authz path, so the caller must be the signer.
func (s *PrecompileTestSuite) TestContractCallerRefused() {
	origin := s.keyring.GetAddr(0)
	someContract := common.HexToAddress("0x00000000000000000000000000000000000c0de0")

	_, err := s.precompile.BridgeOut(
		s.ctx(),
		origin,
		contractFor(someContract),
		s.network.GetStateDB(),
		s.method(),
		[]interface{}{
			recipient32(),
			big.NewInt(100),
			big.NewInt(0),
		},
	)
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "called directly")
}

// TestZeroRecipientRefused: a zero recipient locks ATOS against an Ethereum
// address nobody holds, and nothing downstream rejects it.
func (s *PrecompileTestSuite) TestZeroRecipientRefused() {
	origin := s.keyring.GetAddr(0)

	_, err := s.precompile.BridgeOut(
		s.ctx(), origin, contractFor(origin), s.network.GetStateDB(), s.method(),
		[]interface{}{[32]byte{}, big.NewInt(100), big.NewInt(0)},
	)
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "recipient cannot be zero")
}

// TestNegativeAmountRefused covers the ABI's unsigned/signed mismatch: uint256
// decodes into a signed big.Int, so a value above 2^255 arrives negative and
// would otherwise be carried into the keeper as a negative Int.
func (s *PrecompileTestSuite) TestNegativeAmountRefused() {
	origin := s.keyring.GetAddr(0)

	for _, tc := range []struct {
		name   string
		amount *big.Int
		maxFee *big.Int
	}{
		{"negative amount", big.NewInt(-1), big.NewInt(0)},
		{"negative maxFee", big.NewInt(100), big.NewInt(-1)},
	} {
		s.Run(tc.name, func() {
			_, err := s.precompile.BridgeOut(
				s.ctx(), origin, contractFor(origin), s.network.GetStateDB(), s.method(),
				[]interface{}{recipient32(), tc.amount, tc.maxFee},
			)
			s.Require().Error(err)
			s.Require().Contains(err.Error(), "signed 256-bit")
		})
	}
}

// TestWrongArgCountRefused: a mismatched ABI on the frontend should fail here
// rather than be interpreted with whatever happens to be in the slots.
func (s *PrecompileTestSuite) TestWrongArgCountRefused() {
	origin := s.keyring.GetAddr(0)

	_, err := s.precompile.BridgeOut(
		s.ctx(), origin, contractFor(origin), s.network.GetStateDB(), s.method(),
		[]interface{}{recipient32(), big.NewInt(100)},
	)
	s.Require().Error(err)
}

// TestDisabledBridgeRefused: with bridge_enabled false -- the current testnet
// state, since HypERC20Collateral is not deployed -- the call must fail rather
// than take the ATOS and produce nothing.
func (s *PrecompileTestSuite) TestDisabledBridgeRefused() {
	origin := s.keyring.GetAddr(0)

	params := s.network.App.BridgeAdapterKeeper.GetParams(s.ctx())
	s.Require().False(params.BridgeEnabled, "default genesis should have the asset bridge off")

	_, err := s.precompile.BridgeOut(
		s.ctx(), origin, contractFor(origin), s.network.GetStateDB(), s.method(),
		[]interface{}{recipient32(), big.NewInt(1000), big.NewInt(0)},
	)
	s.Require().Error(err)
}

// TestBaseDenomReachable: the precompile builds the max_fee coin from the
// keeper's denom. An empty denom would make sdk.NewCoin panic.
func (s *PrecompileTestSuite) TestBaseDenomReachable() {
	denom := s.network.App.BridgeAdapterKeeper.BaseDenom()
	s.Require().NotEmpty(denom)
}

func recipient32() [32]byte {
	var r [32]byte
	// An Ethereum address left-padded to 32 bytes, which is what the frontend
	// sends.
	copy(r[12:], common.HexToAddress("0xcfaE4Ed9268A83761cd5A2D1f36838c8A4fb8760").Bytes())
	return r
}

func TestRecipientPaddingMatchesHyperlane(t *testing.T) {
	r := recipient32()
	require.Equal(t, make([]byte, 12), r[:12], "the high 12 bytes must be zero")
	require.Equal(t,
		common.HexToAddress("0xcfaE4Ed9268A83761cd5A2D1f36838c8A4fb8760").Bytes(),
		r[12:],
	)
}
