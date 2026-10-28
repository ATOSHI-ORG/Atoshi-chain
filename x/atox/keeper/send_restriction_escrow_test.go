package keeper

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	transfertypes "github.com/cosmos/ibc-go/v8/modules/apps/transfer/types"
	channeltypes "github.com/cosmos/ibc-go/v8/modules/core/04-channel/types"
	"github.com/stretchr/testify/require"

	"github.com/atoshi-chain/atoshi/v20/x/atox/types"
)

// An internal test, deliberately: it exercises rejectIBCEscrow against a stub
// channel keeper, which is the whole of the new logic and needs none of the
// store, bank or settlement machinery around it.

type stubChannels struct {
	channels []channeltypes.IdentifiedChannel
}

func (s stubChannels) GetAllChannels(sdk.Context) []channeltypes.IdentifiedChannel {
	return s.channels
}

func openChannel(port, id string) channeltypes.IdentifiedChannel {
	return channeltypes.IdentifiedChannel{PortId: port, ChannelId: id, State: channeltypes.OPEN}
}

func escrowCtx() sdk.Context { return sdk.Context{} }

func TestRejectIBCEscrow_BlocksTheEscrowAccount(t *testing.T) {
	k := Keeper{channelKeeper: stubChannels{channels: []channeltypes.IdentifiedChannel{
		openChannel("transfer", "channel-0"),
	}}}

	escrow := transfertypes.GetEscrowAddress("transfer", "channel-0")

	err := k.rejectIBCEscrow(escrowCtx(), escrow)
	require.ErrorIs(t, err, types.ErrAtoxCannotLeave)
	require.Contains(t, err.Error(), "channel-0")
}

func TestRejectIBCEscrow_LetsAnOrdinaryAddressThrough(t *testing.T) {
	k := Keeper{channelKeeper: stubChannels{channels: []channeltypes.IdentifiedChannel{
		openChannel("transfer", "channel-0"),
	}}}

	require.NoError(t, k.rejectIBCEscrow(escrowCtx(), sdk.AccAddress("an-ordinary-account--")))
}

// Every channel is checked, not only the first. A chain with several open
// channels would otherwise leak ATOX through any but channel-0.
func TestRejectIBCEscrow_ChecksEveryChannel(t *testing.T) {
	k := Keeper{channelKeeper: stubChannels{channels: []channeltypes.IdentifiedChannel{
		openChannel("transfer", "channel-0"),
		openChannel("transfer", "channel-7"),
		openChannel("transfer", "channel-42"),
	}}}

	for _, id := range []string{"channel-0", "channel-7", "channel-42"} {
		err := k.rejectIBCEscrow(escrowCtx(), transfertypes.GetEscrowAddress("transfer", id))
		require.ErrorIsf(t, err, types.ErrAtoxCannotLeave, "escrow for %s was not blocked", id)
	}
}

// A port other than "transfer" is covered too. Only ICS20 escrows today, but
// the check costs the same and a future app on another port would otherwise be
// a fresh hole of exactly the kind this function exists to stop.
func TestRejectIBCEscrow_IsNotLimitedToTheTransferPort(t *testing.T) {
	k := Keeper{channelKeeper: stubChannels{channels: []channeltypes.IdentifiedChannel{
		openChannel("someotherport", "channel-3"),
	}}}

	err := k.rejectIBCEscrow(escrowCtx(), transfertypes.GetEscrowAddress("someotherport", "channel-3"))
	require.ErrorIs(t, err, types.ErrAtoxCannotLeave)
}

// With no channels open -- mainnet today -- nothing is blocked and the walk is
// one empty iteration.
func TestRejectIBCEscrow_NoChannelsBlocksNothing(t *testing.T) {
	k := Keeper{channelKeeper: stubChannels{}}
	require.NoError(t, k.rejectIBCEscrow(escrowCtx(), sdk.AccAddress("anybody--------------")))
}

// A keeper built without a channel keeper must not panic. Unit tests across the
// repo assemble bare keepers; app.go always wires one.
func TestRejectIBCEscrow_SurvivesAnUnwiredKeeper(t *testing.T) {
	k := Keeper{}
	require.NoError(t, k.rejectIBCEscrow(escrowCtx(), sdk.AccAddress("anybody--------------")))
}
