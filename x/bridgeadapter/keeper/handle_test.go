package keeper_test

import (
	"context"
	"testing"
	"time"

	"cosmossdk.io/log"
	"cosmossdk.io/math"
	"cosmossdk.io/store"
	"cosmossdk.io/store/metrics"
	storetypes "cosmossdk.io/store/types"
	tmproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/bcp-innovations/hyperlane-cosmos/util"

	"github.com/atoshi-chain/atoshi/v20/x/bridgeadapter/keeper"
	"github.com/atoshi-chain/atoshi/v20/x/bridgeadapter/types"
)

const (
	ethDomain   = uint32(1)
	otherDomain = uint32(8453) // a cheap L2 also connected to Hyperlane
	minerPool   = "miner_pool"
)

// ---------- doubles ----------

type fakeAtox struct {
	pooled  map[string]math.Int
	failNow error
}

func newFakeAtox() *fakeAtox { return &fakeAtox{pooled: map[string]math.Int{}} }

func (a *fakeAtox) AddToExchangePool(_ sdk.Context, fromModule string, amount math.Int) error {
	if a.failNow != nil {
		return a.failNow
	}
	cur, ok := a.pooled[fromModule]
	if !ok {
		cur = math.ZeroInt()
	}
	a.pooled[fromModule] = cur.Add(amount)
	return nil
}

type fakeTokenomics struct {
	authMiner   math.Int
	authProject math.Int
	claimable   math.Int
}

func newFakeTokenomics(miner, project math.Int) *fakeTokenomics {
	return &fakeTokenomics{authMiner: miner, authProject: project, claimable: math.ZeroInt()}
}

func (t *fakeTokenomics) AuthorizedReleases(_ sdk.Context) (math.Int, math.Int) {
	return t.authMiner, t.authProject
}
func (t *fakeTokenomics) GetProjectClaimable(_ sdk.Context) math.Int    { return t.claimable }
func (t *fakeTokenomics) SetProjectClaimable(_ sdk.Context, a math.Int) { t.claimable = a }
func (t *fakeTokenomics) MinerPoolName() string                         { return minerPool }
func (t *fakeTokenomics) MigrationPoolName() string                     { return "migration_pool" }
func (t *fakeTokenomics) MigrationPoolBalance(_ sdk.Context) math.Int {
	return math.NewIntWithDecimal(3, 29)
}
func (t *fakeTokenomics) MigrationPoolTotal(_ sdk.Context) math.Int {
	return math.NewIntWithDecimal(3, 29)
}
func (t *fakeTokenomics) BaseDenom() string { return "liao" }

// fakeBank records ATOS movement for the asset-bridge paths.
type fakeBank struct{}

func (fakeBank) SendCoinsFromAccountToModule(_ context.Context, _ sdk.AccAddress, _ string, _ sdk.Coins) error {
	return nil
}
func (fakeBank) SendCoinsFromModuleToAccount(_ context.Context, _ string, _ sdk.AccAddress, _ sdk.Coins) error {
	return nil
}

// fakeCore stands in for the app router. Genesis only needs an address back.
type fakeCore struct {
	router      *util.Router[util.HyperlaneApp]
	dispatched  [][]byte
	dispatchErr error
}

func (c *fakeCore) AppRouter() *util.Router[util.HyperlaneApp] { return c.router }

func (c *fakeCore) DispatchMessage(
	_ sdk.Context,
	_ util.HexAddress,
	_ util.HexAddress,
	_ sdk.Coins,
	_ uint32,
	_ util.HexAddress,
	body []byte,
	_ util.StandardHookMetadata,
	_ *util.HexAddress,
) (util.HexAddress, error) {
	if c.dispatchErr != nil {
		return util.HexAddress{}, c.dispatchErr
	}
	c.dispatched = append(c.dispatched, body)
	var id util.HexAddress
	copy(id[:], []byte("dispatched-message-id----------"))
	return id, nil
}

// ---------- fixtures ----------

// vaultAddr is the Ethereum contract allowed to send receipts.
func vaultAddr() util.HexAddress {
	var a util.HexAddress
	copy(a[:], []byte("tier-release-vault--------------"))
	return a
}

// impostorAddr is some other contract on the same chain.
func impostorAddr() util.HexAddress {
	var a util.HexAddress
	copy(a[:], []byte("some-other-contract-------------"))
	return a
}

func erc20(n int64) math.Int { return math.NewIntWithDecimal(n, 18) }

// atosFor mirrors the peg the keeper applies.
func atosFor(n int64) math.Int { return erc20(n).MulRaw(types.DefaultAtosPerErc20) }

func setup(t *testing.T, authMiner, authProject math.Int) (keeper.Keeper, sdk.Context, *fakeAtox, *fakeTokenomics) {
	t.Helper()

	storeKey := storetypes.NewKVStoreKey(types.StoreKey)
	db := dbm.NewMemDB()
	cms := store.NewCommitMultiStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	cms.MountStoreWithDB(storeKey, storetypes.StoreTypeIAVL, db)
	require.NoError(t, cms.LoadLatestVersion())

	xk := newFakeAtox()
	tk := newFakeTokenomics(authMiner, authProject)

	k := keeper.NewKeeper(
		codec.NewProtoCodec(codectypes.NewInterfaceRegistry()),
		storeKey,
		sdk.AccAddress([]byte("authority-----------")).String(),
		xk, tk, &fakeCore{}, &fakeBank{},
	)

	ctx := sdk.NewContext(cms, tmproto.Header{Time: time.Unix(1_700_000_000, 0)}, false, log.NewNopLogger())

	p := types.DefaultParams()
	p.Enabled = true
	p.EthereumDomain = ethDomain
	v := vaultAddr()
	p.TierReleaseVault = v[:]
	require.NoError(t, k.SetParams(ctx, p))

	// Assign an app id directly; genesis assignment is covered separately.
	st := types.DefaultReceiptState()
	appID := make([]byte, types.HexAddressLen)
	copy(appID, []byte("atoshi-bridge-adapter-app------"))
	st.AppId = appID
	require.NoError(t, k.SetReceiptState(ctx, st))

	return k, ctx, xk, tk
}

// tierAppID is the recipient the tier-release channel listens on. Must match
// what setup writes into state.
func tierAppID() util.HexAddress {
	var a util.HexAddress
	copy(a[:], []byte("atoshi-bridge-adapter-app------"))
	return a
}

// receipt builds a message as the Ethereum vault would send it.
func receipt(t *testing.T, origin uint32, sender util.HexAddress, toBridge, toProject math.Int) util.HyperlaneMessage {
	t.Helper()
	body, err := types.BuildReceipt(toBridge, toProject)
	require.NoError(t, err)
	return util.HyperlaneMessage{
		Version:     3,
		Nonce:       1,
		Origin:      origin,
		Sender:      sender,
		Destination: 88388,
		Recipient:   tierAppID(),
		Body:        body,
	}
}

// ---------- provenance: the three checks ----------

// TestHandle_RejectsForeignOrigin is the attack the origin check exists for.
// Hyperlane connects dozens of chains, so without it a contract deployed on any
// cheap L2 could address a message here and claim a release.
func TestHandle_RejectsForeignOrigin(t *testing.T) {
	k, ctx, xk, _ := setup(t, atosFor(1_000), atosFor(1_000))

	msg := receipt(t, otherDomain, vaultAddr(), erc20(500), erc20(500))
	require.ErrorIs(t, k.Handle(ctx, util.HexAddress{}, msg), types.ErrWrongOrigin)

	require.Empty(t, xk.pooled, "nothing may be released on a foreign-origin message")
	require.True(t, k.GetReceiptState(ctx).AppliedToBridge.IsZero())
}

// TestHandle_RejectsImpostorSender is the check the ISM structurally cannot make.
// A multisig ISM proves a message came from Ethereum; it says nothing about which
// contract sent it, so validators would sign for an impostor just as readily.
func TestHandle_RejectsImpostorSender(t *testing.T) {
	k, ctx, xk, _ := setup(t, atosFor(1_000), atosFor(1_000))

	msg := receipt(t, ethDomain, impostorAddr(), erc20(500), erc20(500))
	require.ErrorIs(t, k.Handle(ctx, util.HexAddress{}, msg), types.ErrWrongSender)

	require.Empty(t, xk.pooled, "nothing may be released for a sender that is not the vault")
}

// TestHandle_RejectsWhenDisabled — rejecting rather than dropping matters:
// Hyperlane keeps an undelivered message deliverable, so a paused adapter defers
// the release instead of losing it.
func TestHandle_RejectsWhenDisabled(t *testing.T) {
	k, ctx, xk, _ := setup(t, atosFor(1_000), atosFor(1_000))
	p := k.GetParams(ctx)
	p.Enabled = false
	require.NoError(t, k.SetParams(ctx, p))

	msg := receipt(t, ethDomain, vaultAddr(), erc20(500), erc20(500))
	require.ErrorIs(t, k.Handle(ctx, util.HexAddress{}, msg), types.ErrDisabled)
	require.Empty(t, xk.pooled)
}

// ---------- cumulative-target semantics ----------

func TestHandle_AppliesDeltaAtThePeg(t *testing.T) {
	k, ctx, xk, tk := setup(t, atosFor(10_000), atosFor(10_000))

	require.NoError(t, k.Handle(ctx, util.HexAddress{},
		receipt(t, ethDomain, vaultAddr(), erc20(300), erc20(300))))

	require.Equal(t, atosFor(300).String(), xk.pooled[minerPool].String(),
		"300 ERC20 confirmed must release 300*100 ATOS into the conversion pool")
	require.Equal(t, atosFor(300).String(), tk.claimable.String(),
		"the project share raises ProjectClaimable at the same peg")

	st := k.GetReceiptState(ctx)
	require.Equal(t, erc20(300).String(), st.AppliedToBridge.String())
	require.Equal(t, erc20(300).String(), st.AppliedToProject.String())
	require.NotEmpty(t, st.LastMessageId)
}

// TestHandle_DuplicateIsANoOp is what removes the need for a dedup table: the
// second delivery computes a zero delta. It must also SUCCEED, so Hyperlane
// marks the message delivered rather than retrying it forever.
func TestHandle_DuplicateIsANoOp(t *testing.T) {
	k, ctx, xk, tk := setup(t, atosFor(10_000), atosFor(10_000))
	msg := receipt(t, ethDomain, vaultAddr(), erc20(300), erc20(300))

	require.NoError(t, k.Handle(ctx, util.HexAddress{}, msg))
	require.NoError(t, k.Handle(ctx, util.HexAddress{}, msg), "a duplicate must not error")
	require.NoError(t, k.Handle(ctx, util.HexAddress{}, msg))

	require.Equal(t, atosFor(300).String(), xk.pooled[minerPool].String(),
		"three deliveries of the same total release once")
	require.Equal(t, atosFor(300).String(), tk.claimable.String())
}

// TestHandle_LostReceiptIsRepairedByTheNext — cumulative totals mean a dropped
// message needs no retry: the following one carries the full figure.
func TestHandle_LostReceiptIsRepairedByTheNext(t *testing.T) {
	k, ctx, xk, _ := setup(t, atosFor(10_000), atosFor(10_000))

	// The receipt for 300 never arrives; the next one reports 900 cumulative.
	require.NoError(t, k.Handle(ctx, util.HexAddress{},
		receipt(t, ethDomain, vaultAddr(), erc20(900), erc20(900))))

	require.Equal(t, atosFor(900).String(), xk.pooled[minerPool].String(),
		"the gap is closed in one step, with nothing lost")
}

// TestHandle_RejectsBackwardsTarget catches a reordered or stale receipt.
func TestHandle_RejectsBackwardsTarget(t *testing.T) {
	k, ctx, xk, _ := setup(t, atosFor(10_000), atosFor(10_000))

	require.NoError(t, k.Handle(ctx, util.HexAddress{},
		receipt(t, ethDomain, vaultAddr(), erc20(900), erc20(900))))

	err := k.Handle(ctx, util.HexAddress{}, receipt(t, ethDomain, vaultAddr(), erc20(400), erc20(900)))
	require.ErrorIs(t, err, types.ErrTargetWentBackward)

	require.Equal(t, atosFor(900).String(), xk.pooled[minerPool].String(),
		"a backwards receipt must not move anything")
}

// TestHandle_MonotonicSequenceAccumulates walks a normal sequence.
func TestHandle_MonotonicSequenceAccumulates(t *testing.T) {
	k, ctx, xk, tk := setup(t, atosFor(10_000), atosFor(10_000))

	for _, total := range []int64{100, 250, 250, 700, 1_000} {
		require.NoError(t, k.Handle(ctx, util.HexAddress{},
			receipt(t, ethDomain, vaultAddr(), erc20(total), erc20(total))))
	}

	require.Equal(t, atosFor(1_000).String(), xk.pooled[minerPool].String(),
		"the total released must equal the last cumulative figure, not the sum of deltas")
	require.Equal(t, atosFor(1_000).String(), tk.claimable.String())
}

// ---------- authorization bound ----------

// TestHandle_RejectsMoreThanAuthorized is the last line of defence. Ethereum can
// only release what Atoshi's tier engine authorised, so a larger figure is a bug
// or a forgery either way — and acting on it would put ATOS into the conversion
// pool with no ERC20 behind it.
func TestHandle_RejectsMoreThanAuthorized(t *testing.T) {
	k, ctx, xk, _ := setup(t, atosFor(500), atosFor(500))

	err := k.Handle(ctx, util.HexAddress{}, receipt(t, ethDomain, vaultAddr(), erc20(501), erc20(500)))
	require.ErrorIs(t, err, types.ErrExceedsAuthorized)
	require.Empty(t, xk.pooled)

	// Exactly at the authorised figure is fine.
	require.NoError(t, k.Handle(ctx, util.HexAddress{},
		receipt(t, ethDomain, vaultAddr(), erc20(500), erc20(500))))
	require.Equal(t, atosFor(500).String(), xk.pooled[minerPool].String())
}

func TestHandle_RejectsProjectOverAuthorization(t *testing.T) {
	k, ctx, xk, _ := setup(t, atosFor(1_000), atosFor(100))

	err := k.Handle(ctx, util.HexAddress{}, receipt(t, ethDomain, vaultAddr(), erc20(500), erc20(101)))
	require.ErrorIs(t, err, types.ErrExceedsAuthorized)
	require.Empty(t, xk.pooled, "the bridge share must not be released when the project share is invalid")
}

// ---------- payload ----------

func TestHandle_RejectsMalformedPayload(t *testing.T) {
	k, ctx, _, _ := setup(t, atosFor(1_000), atosFor(1_000))

	for _, size := range []int{0, 32, 63, 65, 128} {
		msg := receipt(t, ethDomain, vaultAddr(), erc20(1), erc20(1))
		msg.Body = make([]byte, size)
		require.ErrorIs(t, k.Handle(ctx, util.HexAddress{}, msg), types.ErrInvalidPayload,
			"a %d-byte body must be rejected", size)
	}
}

func TestReceiptRoundTrip(t *testing.T) {
	cases := []struct{ bridge, project math.Int }{
		{math.ZeroInt(), math.ZeroInt()},
		{erc20(1), erc20(999_999)},
		{math.NewIntWithDecimal(1, 26), math.NewIntWithDecimal(87, 26)},
	}
	for _, c := range cases {
		body, err := types.BuildReceipt(c.bridge, c.project)
		require.NoError(t, err)
		require.Len(t, body, types.TierMessagePayloadLen)

		gotB, gotP, err := types.ParseReceipt(body)
		require.NoError(t, err)
		require.Equal(t, c.bridge.String(), gotB.String())
		require.Equal(t, c.project.String(), gotP.String())
	}
}

// ---------- ordering ----------

// TestHandle_NothingIsAppliedWhenTheReleaseFails — the receipt state must not
// advance if the ATOS transfer failed, or a retry would compute a zero delta and
// the release would be silently skipped forever.
func TestHandle_NothingIsAppliedWhenTheReleaseFails(t *testing.T) {
	k, ctx, xk, tk := setup(t, atosFor(1_000), atosFor(1_000))
	xk.failNow = types.ErrDisabled // stand-in for any pool-side failure

	err := k.Handle(ctx, util.HexAddress{}, receipt(t, ethDomain, vaultAddr(), erc20(300), erc20(300)))
	require.Error(t, err)

	require.True(t, k.GetReceiptState(ctx).AppliedToBridge.IsZero(),
		"applied totals must not advance past a failed release")
	require.True(t, tk.claimable.IsZero(),
		"the project share must not be authorised when the bridge share failed")
}

// ---------- app identity ----------

func TestExists_OnlyMatchesOurAppId(t *testing.T) {
	k, ctx, _, _ := setup(t, atosFor(1), atosFor(1))

	var ours util.HexAddress
	copy(ours[:], k.GetReceiptState(ctx).AppId)
	ok, err := k.Exists(ctx, ours)
	require.NoError(t, err)
	require.True(t, ok)

	ok, err = k.Exists(ctx, impostorAddr())
	require.NoError(t, err)
	require.False(t, ok, "an address we do not own must not be claimed")
}

func TestReceiverIsmId_PinnedOrDefault(t *testing.T) {
	k, ctx, _, _ := setup(t, atosFor(1), atosFor(1))
	var ours util.HexAddress
	copy(ours[:], k.GetReceiptState(ctx).AppId)

	// Unset: fall back to the mailbox default.
	got, err := k.ReceiverIsmId(ctx, ours)
	require.NoError(t, err)
	require.Nil(t, got)

	// Pinned: a later change to the mailbox default cannot weaken verification
	// for tier releases.
	p := k.GetParams(ctx)
	ism := make([]byte, types.HexAddressLen)
	copy(ism, []byte("five-of-seven-multisig-ism-----"))
	p.IsmId = ism
	require.NoError(t, k.SetParams(ctx, p))

	got, err = k.ReceiverIsmId(ctx, ours)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, ism, got[:])

	_, err = k.ReceiverIsmId(ctx, impostorAddr())
	require.ErrorIs(t, err, types.ErrUnknownRecipient)
}

// ---------- params ----------

func TestParams_EnablingRequiresAVault(t *testing.T) {
	p := types.DefaultParams()
	require.False(t, p.Enabled, "genesis must not enable the adapter before the vault is known")
	require.NoError(t, p.Validate())

	p.Enabled = true
	require.ErrorContains(t, p.Validate(), "tier_release_vault must be set",
		"enabling with no vault would accept a receipt from any contract on the origin chain")

	v := vaultAddr()
	p.TierReleaseVault = v[:]
	require.NoError(t, p.Validate())
}

func TestParams_RejectsWrongWidthAddresses(t *testing.T) {
	p := types.DefaultParams()
	p.TierReleaseVault = make([]byte, 20)
	require.ErrorContains(t, p.Validate(), "tier_release_vault must be 32 bytes")

	p = types.DefaultParams()
	p.IsmId = make([]byte, 31)
	require.ErrorContains(t, p.Validate(), "ism_id must be 32 bytes")
}

func TestPendingConfirmation(t *testing.T) {
	k, ctx, _, _ := setup(t, atosFor(1_000), atosFor(400))

	bridge, project := k.PendingConfirmation(ctx)
	require.Equal(t, erc20(1_000).String(), bridge.String(),
		"everything authorised is pending until a receipt arrives")
	require.Equal(t, erc20(400).String(), project.String())

	require.NoError(t, k.Handle(ctx, util.HexAddress{},
		receipt(t, ethDomain, vaultAddr(), erc20(600), erc20(400))))

	bridge, project = k.PendingConfirmation(ctx)
	require.Equal(t, erc20(400).String(), bridge.String())
	require.True(t, project.IsZero(), "fully confirmed shares report nothing pending")
}
