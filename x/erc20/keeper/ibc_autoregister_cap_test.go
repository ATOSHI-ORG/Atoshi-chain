package keeper_test

import (
	"fmt"
	"math/big"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	transfertypes "github.com/cosmos/ibc-go/v8/modules/apps/transfer/types"
	clienttypes "github.com/cosmos/ibc-go/v8/modules/core/02-client/types"
	channeltypes "github.com/cosmos/ibc-go/v8/modules/core/04-channel/types"
	ibcmock "github.com/cosmos/ibc-go/v8/testing/mock"
	"github.com/ethereum/go-ethereum/common"

	"github.com/atoshi-chain/atoshi/v20/crypto/ethsecp256k1"
	"github.com/atoshi-chain/atoshi/v20/x/erc20/types"
)

// IBC clients, connections and channels are permissionless, and every new
// single-hop denom received used to register a dynamic precompile, which
// every EVM CALL then scans. Past MaxAutoRegisteredDynamicPrecompiles the
// coin is still received but no extension is registered.
func (suite *KeeperTestSuite) TestOnRecvPacketAutoRegistrationCap() {
	for _, tc := range []struct {
		name          string
		existing      int
		expRegistered bool
	}{
		{"registers below the cap", types.MaxAutoRegisteredDynamicPrecompiles - 1, true},
		{"does not register at the cap", types.MaxAutoRegisteredDynamicPrecompiles, false},
	} {
		suite.Run(tc.name, func() {
			suite.SetupTest()
			ctx := suite.network.GetContext()

			params := suite.network.App.Erc20Keeper.GetParams(ctx)
			params.DynamicPrecompiles = nil
			for i := 0; i < tc.existing; i++ {
				params.DynamicPrecompiles = append(params.DynamicPrecompiles, common.BigToAddress(big.NewInt(int64(0x10000+i))).Hex())
			}
			suite.Require().NoError(suite.network.App.Erc20Keeper.SetParams(ctx, params))

			sender := sdk.AccAddress(secp256k1.GenPrivKey().PubKey().Address()).String()
			ethPk, err := ethsecp256k1.GenerateKey()
			suite.Require().NoError(err)
			receiver := sdk.AccAddress(ethPk.PubKey().Address()).String()

			const srcChannel, dstChannel = "channel-292", "channel-3"
			data := transfertypes.NewFungibleTokenPacketData("uspam", "1", sender, receiver, "")
			packet := channeltypes.NewPacket(transfertypes.ModuleCdc.MustMarshalJSON(&data), 1,
				transfertypes.PortID, srcChannel, transfertypes.PortID, dstChannel, clienttypes.NewHeight(0, 100), 0)
			ibcDenom := transfertypes.ParseDenomTrace(fmt.Sprintf("%s/%s/uspam", transfertypes.PortID, dstChannel)).IBCDenom()

			ack := suite.network.App.Erc20Keeper.OnRecvPacket(ctx, packet, ibcmock.MockAcknowledgement)
			suite.Require().True(ack.Success(), string(ack.Acknowledgement()))

			_, found := suite.network.App.Erc20Keeper.GetTokenPair(ctx, suite.network.App.Erc20Keeper.GetTokenPairID(ctx, ibcDenom))
			suite.Require().Equal(tc.expRegistered, found)
		})
	}
}
