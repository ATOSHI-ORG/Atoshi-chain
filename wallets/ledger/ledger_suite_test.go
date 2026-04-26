// Copyright Tharsis Labs Ltd.(Evmos)
// SPDX-License-Identifier:ENCL-1.0(https://github.com/evmos/evmos/blob/main/LICENSE)

package ledger_test

import (
	"encoding/hex"
	"fmt"
	"regexp"
	"testing"

	"cosmossdk.io/math"
	"github.com/stretchr/testify/suite"

	"github.com/cosmos/cosmos-sdk/codec"
	codecTypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	cryptoTypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	txTypes "github.com/cosmos/cosmos-sdk/types/tx"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	auxTx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	bankTypes "github.com/cosmos/cosmos-sdk/x/bank/types"

	"github.com/atoshi-chain/atoshi/v20/testutil/integration/evmos/network"
	"github.com/atoshi-chain/atoshi/v20/wallets/ledger"
	"github.com/atoshi-chain/atoshi/v20/wallets/ledger/mocks"
	"github.com/atoshi-chain/atoshi/v20/wallets/usbwallet"
)

type LedgerTestSuite struct {
	suite.Suite
	txAmino    []byte
	txProtobuf []byte
	ledger     ledger.EvmosSECP256K1
	mockWallet *mocks.Wallet
	hrp        string
}

// Load encoding config for sign doc encoding/decoding
// This is done on app instantiation.
// We use the testutil network to load the encoding config
func init() {
	network.New()
}

func TestLedgerTestSuite(t *testing.T) {
	suite.Run(t, new(LedgerTestSuite))
}

func (suite *LedgerTestSuite) SetupTest() {
	suite.hrp = "evmos"

	suite.txAmino = suite.getMockTxAmino()
	suite.txProtobuf = suite.getMockTxProtobuf()

	hub, err := usbwallet.NewLedgerHub()
	suite.Require().NoError(err)

	mockWallet := new(mocks.Wallet)
	suite.mockWallet = mockWallet
	suite.ledger = ledger.EvmosSECP256K1{Hub: hub, PrimaryWallet: mockWallet}
}

func (suite *LedgerTestSuite) newPubKey(pk string) (res cryptoTypes.PubKey) {
	pkBytes, err := hex.DecodeString(pk)
	suite.Require().NoError(err)

	pubkey := &ed25519.PubKey{Key: pkBytes}

	return pubkey
}

func (suite *LedgerTestSuite) convertToCurrentPrefix(address string) string {
	bz, err := sdk.GetFromBech32(address, "evmos")
	suite.Require().NoError(err)
	return sdk.AccAddress(bz).String()
}

func (suite *LedgerTestSuite) getMockTxAmino() []byte {
	from := suite.convertToCurrentPrefix("evmos10jmp6sgh4cc6zt3e8gw05wavvejgr5pwjnpcky")
	to := suite.convertToCurrentPrefix("evmos1fx944mzagwdhx0wz7k9tfztc8g3lkfk6rrgv6l")
	whitespaceRegex := regexp.MustCompile(`\s+`)
	tmp := whitespaceRegex.ReplaceAllString(
		fmt.Sprintf(`{
			"account_number": "0",
			"chain_id":"atoshi_88388-1",
			"fee":{
				"amount":[{"amount":"150","denom":"atom"}],
				"gas":"20000"
			},
			"memo":"memo",
			"msgs":[{
				"type":"cosmos-sdk/MsgSend",
				"value":{
					"amount":[{"amount":"150","denom":"atom"}],
					"from_address":"%s",
					"to_address":"%s"
				}
			}],
			"sequence":"6"
		}`,
			from,
			to,
		),
		"",
	)

	return []byte(tmp)
}

func (suite *LedgerTestSuite) getMockTxProtobuf() []byte {
	marshaler := codec.NewProtoCodec(codecTypes.NewInterfaceRegistry())

	memo := "memo"
	msg := bankTypes.NewMsgSend(
		sdk.MustAccAddressFromBech32(suite.convertToCurrentPrefix("evmos10jmp6sgh4cc6zt3e8gw05wavvejgr5pwjnpcky")),
		sdk.MustAccAddressFromBech32(suite.convertToCurrentPrefix("evmos1fx944mzagwdhx0wz7k9tfztc8g3lkfk6rrgv6l")),
		[]sdk.Coin{
			{
				Denom:  "atom",
				Amount: math.NewIntFromUint64(150),
			},
		},
	)

	msgAsAny, err := codecTypes.NewAnyWithValue(msg)
	suite.Require().NoError(err)

	body := &txTypes.TxBody{
		Messages: []*codecTypes.Any{
			msgAsAny,
		},
		Memo: memo,
	}

	pubKey := suite.newPubKey("0B485CFC0EECC619440448436F8FC9DF40566F2369E72400281454CB552AFB50")

	pubKeyAsAny, err := codecTypes.NewAnyWithValue(pubKey)
	suite.Require().NoError(err)

	signingMode := txTypes.ModeInfo_Single_{
		Single: &txTypes.ModeInfo_Single{
			Mode: signing.SignMode_SIGN_MODE_DIRECT,
		},
	}

	signerInfo := &txTypes.SignerInfo{
		PublicKey: pubKeyAsAny,
		ModeInfo: &txTypes.ModeInfo{
			Sum: &signingMode,
		},
		Sequence: 6,
	}

	fee := txTypes.Fee{Amount: sdk.NewCoins(sdk.NewInt64Coin("atom", 150)), GasLimit: 20000}

	authInfo := &txTypes.AuthInfo{
		SignerInfos: []*txTypes.SignerInfo{signerInfo},
		Fee:         &fee,
	}

	bodyBytes := marshaler.MustMarshal(body)
	authInfoBytes := marshaler.MustMarshal(authInfo)

	signBytes, err := auxTx.DirectSignBytes(
		bodyBytes,
		authInfoBytes,
		"atoshi_88388-1",
		0,
	)
	suite.Require().NoError(err)

	return signBytes
}
