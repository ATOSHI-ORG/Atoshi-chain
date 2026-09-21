package types_test

import (
	"testing"

	"cosmossdk.io/math"
	"github.com/stretchr/testify/require"

	"github.com/atoshi-chain/atoshi/v20/x/bridgeadapter/types"
)

// TestSolidityProducedBytesParseOnChain feeds this package the exact bytes the
// Solidity contract emits, captured from `forge test` output rather than
// hand-written.
//
// The Go golden vectors in tier_message_test.go prove the encoder matches
// `cast abi-encode`. This proves the decoder accepts what UnlockReserve itself
// produces, which is the direction that actually runs in production. Both hand
// transcriptions of these hex strings were wrong on the first attempt, which is
// why they are now generated.
func TestSolidityProducedBytesParseOnChain(t *testing.T) {
	t.Run("forward leg from Solidity", func(t *testing.T) {
		body := mustHex(t,
			"0000000000000000000000000000000000000000000000000000000000000001"+
				"000000000000000000000000000000000000000000661efdf12d1653cf340000"+
				"00000000000000000000000000000000000000000330f7f0064f1eef5c240000")

		miner, project, err := types.ParseTierRelease(body)
		require.NoError(t, err, "the chain must accept the forward body the contract encodes")
		require.Equal(t, math.NewIntWithDecimal(123456789, 18), miner)
		require.Equal(t, math.NewIntWithDecimal(987654321, 18), project)
	})

	t.Run("reverse leg from Solidity", func(t *testing.T) {
		body := mustHex(t,
			"0000000000000000000000000000000000000000000000000000000000000002"+
				"000000000000000000000000000000000000000000000000000000000012d687"+
				"000000000000000000000000000000000000000000000000000000000096b43f")

		toBridge, toProject, err := types.ParseReceipt(body)
		require.NoError(t, err, "the chain must accept the receipt body the contract encodes")
		require.Equal(t, math.NewInt(1234567), toBridge)
		require.Equal(t, math.NewInt(9876543), toProject)
	})
}

// The reverse body must not be accepted on the forward channel, and vice versa,
// even though both come from the same encoder.
func TestSolidityLegsAreNotInterchangeable(t *testing.T) {
	fwd := mustHex(t, "0000000000000000000000000000000000000000000000000000000000000001"+"000000000000000000000000000000000000000000661efdf12d1653cf340000"+"00000000000000000000000000000000000000000330f7f0064f1eef5c240000")
	rev := mustHex(t, "0000000000000000000000000000000000000000000000000000000000000002"+"000000000000000000000000000000000000000000000000000000000012d687"+"000000000000000000000000000000000000000000000000000000000096b43f")

	_, _, err := types.ParseReceipt(fwd)
	require.ErrorIs(t, err, types.ErrInvalidPayload)

	_, _, err = types.ParseTierRelease(rev)
	require.ErrorIs(t, err, types.ErrInvalidPayload)
}
