package ante_test

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
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/stretchr/testify/require"
	protov2 "google.golang.org/protobuf/proto"

	energyante "github.com/atoshi-chain/atoshi/v20/x/energy/ante"
	"github.com/atoshi-chain/atoshi/v20/x/energy/keeper"
	"github.com/atoshi-chain/atoshi/v20/x/energy/types"
)

// ----- minimal fakes (copied from keeper/settle_test pattern) -----

type fakeBank struct {
	balances map[string]math.Int
	denom    string
	// records the last SendCoinsFromAccountToModule call so we can assert
	lastFrom   sdk.AccAddress
	lastModule string
	lastAmt    sdk.Coins
}

func newFakeBank(denom string) *fakeBank {
	return &fakeBank{balances: map[string]math.Int{}, denom: denom}
}

func (b *fakeBank) GetBalance(_ context.Context, addr sdk.AccAddress, denom string) sdk.Coin {
	v, ok := b.balances[addr.String()]
	if !ok {
		return sdk.NewCoin(denom, math.ZeroInt())
	}
	return sdk.NewCoin(denom, v)
}
func (b *fakeBank) SendCoinsFromAccountToModule(_ context.Context, sender sdk.AccAddress, module string, amt sdk.Coins) error {
	b.lastFrom = sender
	b.lastModule = module
	b.lastAmt = amt
	cur := b.balances[sender.String()]
	if cur.IsNil() {
		cur = math.ZeroInt()
	}
	b.balances[sender.String()] = cur.Sub(amt.AmountOf(b.denom))
	return nil
}
func (b *fakeBank) SendCoinsFromModuleToAccount(_ context.Context, _ string, recipient sdk.AccAddress, amt sdk.Coins) error {
	cur := b.balances[recipient.String()]
	if cur.IsNil() {
		cur = math.ZeroInt()
	}
	b.balances[recipient.String()] = cur.Add(amt.AmountOf(b.denom))
	return nil
}

type fakeAccountKeeper struct {
	exists map[string]bool
}

func (f fakeAccountKeeper) GetModuleAddress(name string) sdk.AccAddress {
	return sdk.AccAddress([]byte("module/" + name))
}
func (f fakeAccountKeeper) GetModuleAccount(_ context.Context, _ string) sdk.ModuleAccountI { return nil }
func (f fakeAccountKeeper) GetAccount(_ context.Context, addr sdk.AccAddress) sdk.AccountI {
	if f.exists[addr.String()] {
		// return a non-nil minimal account; the decorator only checks != nil
		return authtypes.NewBaseAccountWithAddress(addr)
	}
	return nil
}

// fakeFeeTx implements sdk.Tx + sdk.FeeTx for the decorator path.
type fakeFeeTx struct {
	gas        uint64
	fee        sdk.Coins
	feePayer   sdk.AccAddress
	feeGranter sdk.AccAddress
	msgs       []sdk.Msg
}

func (t fakeFeeTx) GetMsgs() []sdk.Msg                       { return t.msgs }
func (t fakeFeeTx) GetMsgsV2() ([]protov2.Message, error)    { return nil, nil }
func (t fakeFeeTx) ValidateBasic() error                     { return nil }
func (t fakeFeeTx) GetGas() uint64                           { return t.gas }
func (t fakeFeeTx) GetFee() sdk.Coins                        { return t.fee }
func (t fakeFeeTx) FeePayer() []byte                         { return t.feePayer }
func (t fakeFeeTx) FeeGranter() []byte                       { return t.feeGranter }

// ----- harness -----

func newTestEnv(t *testing.T) (keeper.Keeper, *fakeBank, fakeAccountKeeper, sdk.Context) {
	t.Helper()
	storeKey := storetypes.NewKVStoreKey(types.StoreKey)
	db := dbm.NewMemDB()
	cms := store.NewCommitMultiStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	cms.MountStoreWithDB(storeKey, storetypes.StoreTypeIAVL, db)
	require.NoError(t, cms.LoadLatestVersion())

	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)

	bank := newFakeBank("liao")
	ak := fakeAccountKeeper{exists: map[string]bool{}}
	k := keeper.NewKeeper(cdc, storeKey, fakeAccKeeperShim{fakeAccountKeeper: ak}, bank, nil, nil,
		sdk.AccAddress([]byte("authority")).String(), "liao")

	header := tmproto.Header{Time: time.Unix(1_700_000_000, 0)}
	ctx := sdk.NewContext(cms, header, false, log.NewNopLogger()).
		WithGasMeter(storetypes.NewGasMeter(1_000_000))
	require.NoError(t, k.SetParams(ctx, types.DefaultParams()))
	return k, bank, ak, ctx
}

// fakeAccKeeperShim adapts fakeAccountKeeper to the keeper.types.AccountKeeper
// interface (which uses GetModuleAccount).
type fakeAccKeeperShim struct{ fakeAccountKeeper }

// terminalAnte returns ctx unchanged; the decorator chain ends here.
func terminalAnte(ctx sdk.Context, _ sdk.Tx, _ bool) (sdk.Context, error) { return ctx, nil }

func dummyTxFeeChecker(ctx sdk.Context, tx sdk.Tx) (sdk.Coins, int64, error) {
	feeTx, _ := tx.(sdk.FeeTx)
	return feeTx.GetFee(), 0, nil
}

// ----- tests -----

func TestDecorator_SubsidizedMsg_NoFeeCharged(t *testing.T) {
	k, bank, ak, ctx := newTestEnv(t)
	signer := sdk.AccAddress([]byte("alice___________________"))
	ak.exists[signer.String()] = true

	d := energyante.NewEnergyDeductDecorator(k, ak, bank, nil, dummyTxFeeChecker)
	tx := fakeFeeTx{
		gas:      21_000,
		fee:      sdk.NewCoins(sdk.NewCoin("liao", math.NewInt(1_000))),
		feePayer: signer,
		msgs:     nil,
	}
	// fake msg URL on the subsidized list
	subsidizedMsg := mockMsg{typeURL: "/atoshi.oracle.v1.MsgReportPrice"}
	tx.msgs = []sdk.Msg{subsidizedMsg}

	_, err := d.AnteHandle(ctx, tx, false, terminalAnte)
	require.NoError(t, err)
	require.Nil(t, bank.lastAmt, "no fee should have been charged for subsidized msg")
}

func TestDecorator_EnergyCoversAllGas_ZeroFee(t *testing.T) {
	k, bank, ak, ctx := newTestEnv(t)
	signer := sdk.AccAddress([]byte("alice___________________"))
	ak.exists[signer.String()] = true
	bank.balances[signer.String()] = math.NewIntWithDecimal(60_000, 18) // capacity 100k
	k.Settle(ctx, signer)
	// Advance 24h so accrued = capacity = 100k
	ctx = ctx.WithBlockTime(ctx.BlockTime().Add(24 * time.Hour))

	d := energyante.NewEnergyDeductDecorator(k, ak, bank, nil, dummyTxFeeChecker)
	tx := fakeFeeTx{
		gas:      21_000,
		fee:      sdk.NewCoins(sdk.NewCoin("liao", math.NewInt(1_000))),
		feePayer: signer,
		msgs:     []sdk.Msg{mockMsg{typeURL: "/cosmos.bank.v1beta1.MsgSend"}},
	}
	_, err := d.AnteHandle(ctx, tx, false, terminalAnte)
	require.NoError(t, err)
	require.Nil(t, bank.lastAmt, "fully covered by energy → no ATOS fee deducted")

	// Verify energy was actually consumed.
	acct := k.GetEnergyAccount(ctx, signer)
	require.EqualValues(t, 100_000-21_000, acct.TxEnergyAccrued)
}

func TestDecorator_PartialEnergy_DeductsShortfallProrated(t *testing.T) {
	k, bank, ak, ctx := newTestEnv(t)
	signer := sdk.AccAddress([]byte("alice___________________"))
	ak.exists[signer.String()] = true
	bank.balances[signer.String()] = math.NewIntWithDecimal(30_000, 18) // capacity 50k
	k.Settle(ctx, signer)
	ctx = ctx.WithBlockTime(ctx.BlockTime().Add(24 * time.Hour))

	d := energyante.NewEnergyDeductDecorator(k, ak, bank, nil, dummyTxFeeChecker)
	// gas_limit 100k, energy covers 50k → shortfall = 50k. Fee offered:
	// 100,000 liao for 100k gas = 1 liao/gas. Charge = 1 * 50000 = 50000.
	tx := fakeFeeTx{
		gas:      100_000,
		fee:      sdk.NewCoins(sdk.NewCoin("liao", math.NewInt(100_000))),
		feePayer: signer,
		msgs:     []sdk.Msg{mockMsg{typeURL: "/cosmos.bank.v1beta1.MsgSend"}},
	}
	_, err := d.AnteHandle(ctx, tx, false, terminalAnte)
	require.NoError(t, err)
	require.NotNil(t, bank.lastAmt)
	require.Equal(t, "liao", bank.lastAmt[0].Denom)
	require.True(t, bank.lastAmt[0].Amount.Equal(math.NewInt(50_000)),
		"expected 50000 shortfall, got %s", bank.lastAmt[0].Amount)
	require.Equal(t, authtypes.FeeCollectorName, bank.lastModule)
}

func TestDecorator_EnergyDisabled_StillChargesFullFee(t *testing.T) {
	k, bank, ak, ctx := newTestEnv(t)
	signer := sdk.AccAddress([]byte("alice___________________"))
	ak.exists[signer.String()] = true
	p := k.GetParams(ctx)
	p.EnergyEnabled = false
	require.NoError(t, k.SetParams(ctx, p))

	bank.balances[signer.String()] = math.NewIntWithDecimal(1_000_000, 18)

	d := energyante.NewEnergyDeductDecorator(k, ak, bank, nil, dummyTxFeeChecker)
	tx := fakeFeeTx{
		gas:      21_000,
		fee:      sdk.NewCoins(sdk.NewCoin("liao", math.NewInt(21_000))),
		feePayer: signer,
		msgs:     []sdk.Msg{mockMsg{typeURL: "/cosmos.bank.v1beta1.MsgSend"}},
	}
	_, err := d.AnteHandle(ctx, tx, false, terminalAnte)
	require.NoError(t, err)
	// Disabled → entire 21k gas is shortfall, full fee charged.
	require.NotNil(t, bank.lastAmt)
	require.True(t, bank.lastAmt[0].Amount.Equal(math.NewInt(21_000)))
}

// ----- mock msg satisfying sdk.Msg -----

type mockMsg struct{ typeURL string }

func (m mockMsg) Reset()         {}
func (m mockMsg) String() string { return m.typeURL }
func (m mockMsg) ProtoMessage()  {}

// XXX_MessageName lets MsgTypeURL pick up the URL. The SDK uses
// proto.MessageName() to format type URLs as "/<name>", so we make our
// mock report whatever URL the test wants.
func (m mockMsg) XXX_MessageName() string { return m.typeURL[1:] }
