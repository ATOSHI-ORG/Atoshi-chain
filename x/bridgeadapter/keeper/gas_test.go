package keeper_test

import (
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
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/stretchr/testify/require"

	"github.com/atoshi-chain/atoshi/v20/x/bridgeadapter/keeper"
	"github.com/atoshi-chain/atoshi/v20/x/bridgeadapter/types"
)

// setupGas is setup() with the fakes handed back, so a test can set a balance
// and read what was actually passed to DispatchMessage.
func setupGas(t *testing.T, params types.Params) (keeper.Keeper, sdk.Context, *fakeCore, *fakeBank) {
	t.Helper()

	storeKey := storetypes.NewKVStoreKey(types.StoreKey)
	db := dbm.NewMemDB()
	cms := store.NewCommitMultiStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	cms.MountStoreWithDB(storeKey, storetypes.StoreTypeIAVL, db)
	require.NoError(t, cms.LoadLatestVersion())

	core := &fakeCore{}
	bank := &fakeBank{}
	k := keeper.NewKeeper(
		codec.NewProtoCodec(codectypes.NewInterfaceRegistry()),
		storeKey,
		sdk.AccAddress([]byte("authority-----------")).String(),
		newFakeAtox(), newFakeTokenomics(math.ZeroInt(), math.ZeroInt()),
		core, bank,
	)

	ctx := sdk.NewContext(cms, tmproto.Header{Time: time.Unix(1_700_000_000, 0)}, false, log.NewNopLogger())
	require.NoError(t, k.SetParams(ctx, params))

	st := types.DefaultReceiptState()
	appID := make([]byte, types.HexAddressLen)
	copy(appID, []byte("atoshi-bridge-adapter-app------"))
	st.AppId = appID
	require.NoError(t, k.SetReceiptState(ctx, st))

	return k, ctx, core, bank
}

func dispatchableParams(t *testing.T) types.Params {
	t.Helper()
	p := types.DefaultParams()
	p.Enabled = true
	p.EthereumDomain = ethDomain
	v := vaultAddr()
	p.TierReleaseVault = v[:]
	mb := configuredMailbox()
	p.MailboxId = mb[:]
	ism := configuredIsm()
	p.IsmId = ism[:]
	return p
}

// TestEffectiveDestinationGas_IsNeverNilOrZero is the guard against a panic
// rather than an error.
//
// GasLimit reaches hyperlane as a math.Int, and the interchain gas paymaster's
// first act is gasLimit.Add(gas_overhead) (hook_igp.go:118). math.Int's zero
// value wraps a nil big.Int, so that addition panics. A chain upgraded from
// before this parameter existed has destination_gas == 0, which is exactly the
// case that has to resolve to something usable.
func TestEffectiveDestinationGas_IsNeverNilOrZero(t *testing.T) {
	for name, p := range map[string]types.Params{
		"unset (upgraded chain)": {DestinationGas: 0},
		"default params":         types.DefaultParams(),
		"explicitly set":         {DestinationGas: 350_000},
	} {
		t.Run(name, func(t *testing.T) {
			got := p.EffectiveDestinationGas()
			require.False(t, got.IsNil(), "a nil Int panics inside the gas paymaster")
			require.True(t, got.IsPositive(), "zero gas would quote a transfer that cannot execute")
		})
	}

	require.Equal(t, math.NewInt(types.DefaultDestinationGas).String(),
		types.Params{DestinationGas: 0}.EffectiveDestinationGas().String(),
		"an unset field must fall back to the default, not to zero")
	require.Equal(t, math.NewInt(350_000).String(),
		types.Params{DestinationGas: 350_000}.EffectiveDestinationGas().String(),
		"an explicit figure must win over the default")
}

// TestBridgeOut_CarriesGasLimit pins that the outbound path actually fills the
// field in. Without it the value would default to nil at the one call site that
// matters, and nothing would notice until a paying hook was attached.
func TestBridgeOut_CarriesGasLimit(t *testing.T) {
	p := dispatchableParams(t)
	p.BridgeEnabled = true
	rv := vaultAddr()
	p.RemoteBridgeVault = rv[:]
	p.DestinationGas = 250_000

	k, ctx, core, _ := setupGas(t, p)
	st := k.GetReceiptState(ctx)
	assetApp := make([]byte, types.HexAddressLen)
	copy(assetApp, []byte("atoshi-bridge-adapter-asset----"))
	st.AssetAppId = assetApp
	require.NoError(t, k.SetReceiptState(ctx, st))

	sender := sdk.AccAddress([]byte("bridge-out-sender---"))
	recipient := make([]byte, types.HexAddressLen)
	copy(recipient[12:], []byte("ethereum-recipient--")[:20])

	_, _, err := k.ExecuteBridgeOut(ctx, sender, recipient, atosFor(10), sdk.NewCoins())
	require.NoError(t, err)

	require.False(t, core.lastMetadata.GasLimit.IsNil(),
		"GasLimit must be set, or the gas paymaster panics on nil")
	require.Equal(t, "250000", core.lastMetadata.GasLimit.String())
	require.Equal(t, sender.String(), core.lastMetadata.Address.String(),
		"the fee payer must be the user bridging out, not the module")
}

// TestDispatchTierRelease_PaysFromTheModuleAccount covers the half of this that
// has no user to charge. A tier release is triggered by the chain, so the fee
// comes out of this module's own address -- and the hook metadata has to name
// it, or the paymaster debits nobody.
func TestDispatchTierRelease_PaysFromTheModuleAccount(t *testing.T) {
	p := dispatchableParams(t)
	p.TierDispatchMaxFee = math.NewInt(5_000)

	k, ctx, core, bank := setupGas(t, p)
	bank.balance = math.NewInt(1_000_000)

	_, err := k.DispatchTierRelease(ctx, math.NewInt(100), math.NewInt(200))
	require.NoError(t, err)

	want := authtypes.NewModuleAddress(types.ModuleName)
	require.Equal(t, want.String(), core.lastMetadata.Address.String(),
		"the module's own address pays for a chain-triggered release")
	require.False(t, core.lastMetadata.GasLimit.IsNil())
	require.Equal(t, "5000", core.lastMaxFee.AmountOf("liao").String(),
		"maxFee must carry the configured ceiling")
}

// TestDispatchTierRelease_RefusesWhenUnderfunded is about making a silent
// failure loud. Tier releases run from EndBlock: a bank overdraft there
// produces no transaction result and no query error, so the release simply
// stops happening and the first symptom is a counter that stopped moving.
func TestDispatchTierRelease_RefusesWhenUnderfunded(t *testing.T) {
	p := dispatchableParams(t)
	p.TierDispatchMaxFee = math.NewInt(5_000)

	k, ctx, core, bank := setupGas(t, p)
	bank.balance = math.NewInt(4_999) // one short

	_, err := k.DispatchTierRelease(ctx, math.NewInt(100), math.NewInt(200))
	require.ErrorIs(t, err, types.ErrInsufficientDispatchFee)
	require.Empty(t, core.dispatched, "nothing should be dispatched when the fee cannot be covered")

	found := false
	for _, e := range ctx.EventManager().Events() {
		if e.Type == types.EventTypeTierDispatchUnderfunded {
			found = true
		}
	}
	require.True(t, found,
		"an underfunded dispatch must emit an event; nothing else surfaces it from EndBlock")
}

// TestDispatchTierRelease_FreeWhileNoHookCharges keeps the change backwards
// compatible. With tier_dispatch_max_fee at its default of zero -- which is
// every chain upgrading into this version -- the release must still go out, and
// must not consult a balance that is irrelevant.
func TestDispatchTierRelease_FreeWhileNoHookCharges(t *testing.T) {
	k, ctx, core, bank := setupGas(t, dispatchableParams(t))
	bank.balance = math.ZeroInt()

	_, err := k.DispatchTierRelease(ctx, math.NewInt(100), math.NewInt(200))
	require.NoError(t, err)
	require.Len(t, core.dispatched, 1)
	require.True(t, core.lastMaxFee.IsZero(), "no hook charges, so nothing is offered")
	require.False(t, core.lastMetadata.GasLimit.IsNil(),
		"GasLimit is declared even when the fee is zero: it is what a hook would price against")
}
