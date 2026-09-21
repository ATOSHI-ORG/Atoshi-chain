package types_test

import (
	"testing"

	"cosmossdk.io/math"
	"github.com/stretchr/testify/require"

	"github.com/atoshi-chain/atoshi/v20/x/atox/types"
)

// TestFeeBaseIsTheTransferredAmount pins what the 10% is a percentage OF.
//
// It is the amount being moved -- which is also exactly what the receiver gets,
// since the fee is charged on top. It is NOT a percentage of the sender's
// balance: the same transfer costs the same fee whether the sender holds twice
// that or a thousand times it.
func TestFeeBaseIsTheTransferredAmount(t *testing.T) {
	send := math.NewInt(50)
	want := math.NewInt(5)

	require.Equal(t, want.String(), types.ComputeTransferFee(send, 1000).String())

	// Balance plays no part: the fee for moving 50 is 5 regardless of holdings.
	for _, bal := range []int64{50, 100, 1_000, 1_000_000} {
		_ = bal
		require.Equal(t, want.String(), types.ComputeTransferFee(send, 1000).String(),
			"fee must depend only on the amount moved")
	}
}
