package ante_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"testing"

	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	"cosmossdk.io/x/feegrant"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/stretchr/testify/require"

	"github.com/atoshi-chain/atoshi/v20/app/post"
	commonfactory "github.com/atoshi-chain/atoshi/v20/testutil/integration/common/factory"
	"github.com/atoshi-chain/atoshi/v20/testutil/integration/evmos/factory"
	"github.com/atoshi-chain/atoshi/v20/testutil/integration/evmos/grpc"
	testkeyring "github.com/atoshi-chain/atoshi/v20/testutil/integration/evmos/keyring"
	"github.com/atoshi-chain/atoshi/v20/testutil/integration/evmos/network"
	energyante "github.com/atoshi-chain/atoshi/v20/x/energy/ante"
	energykeeper "github.com/atoshi-chain/atoshi/v20/x/energy/keeper"
	energytypes "github.com/atoshi-chain/atoshi/v20/x/energy/types"
	inflationtypes "github.com/atoshi-chain/atoshi/v20/x/inflation/v1/types"
	oracletypes "github.com/atoshi-chain/atoshi/v20/x/oracle/types"
	tokenomicstypes "github.com/atoshi-chain/atoshi/v20/x/tokenomics/types"
)

// energyFeeSuite drives the app's real AnteHandler / msg router /
// PostHandler against the unit network's context. State prepared through
// GetContext() is visible to the tx, which a committed block would not see
// (the integration network discards GetContext() writes on NextBlock), and
// the gas meter is the one SetUpContextDecorator installs -- the same meter
// BaseApp.runTx reads GasUsed from.
type energyFeeSuite struct {
	nw      *network.UnitTestNetwork
	factory factory.TxFactory
	keyring testkeyring.Keyring
}

func newEnergyFeeSuite(t *testing.T, nAccs int) *energyFeeSuite {
	t.Helper()
	kr := testkeyring.New(nAccs)
	nw := network.NewUnitTestNetwork(network.WithPreFundedAccounts(kr.GetAllAccAddrs()...))
	gh := grpc.NewIntegrationHandler(nw)
	return &energyFeeSuite{nw: nw, factory: factory.New(nw, gh), keyring: kr}
}

// txOutcome mirrors what BaseApp.runTx reports for a delivered tx.
type txOutcome struct {
	anteErr error
	msgErr  error
	gasUsed uint64
	free    bool // the energy decorator took the subsidized path
}

// deliver runs one tx the way BaseApp.runTx does in FinalizeBlock: ante
// state is kept once the AnteHandler succeeds even if the msgs then fail,
// msg state and the PostHandler are kept only on success.
func (s *energyFeeSuite) deliver(t *testing.T, priv cryptotypes.PrivKey, gas uint64, granter sdk.AccAddress, msgs ...sdk.Msg) txOutcome {
	t.Helper()
	fee := sdk.NewCoins(sdk.NewCoin(s.nw.GetBaseDenom(), math.NewIntWithDecimal(1, 9).MulRaw(int64(gas))))
	tx, err := s.factory.BuildCosmosTx(priv, commonfactory.CosmosTxArgs{
		Msgs:       msgs,
		Gas:        &gas,
		Fees:       fee,
		FeeGranter: granter,
	})
	require.NoError(t, err)
	txBytes, err := s.factory.EncodeTx(tx)
	require.NoError(t, err)

	app := s.nw.App
	base := s.nw.GetContext().WithTxBytes(txBytes).WithIsCheckTx(false)

	anteCtx, writeAnte := base.CacheContext()
	newCtx, err := app.AnteHandler()(anteCtx, tx, false)
	if err != nil {
		return txOutcome{anteErr: err, gasUsed: newCtx.GasMeter().GasConsumed()}
	}
	writeAnte()
	reserved, _ := newCtx.Value(energyante.CtxKeyEnergyReserved).(energykeeper.ConsumeResult)

	msgCtx, writeMsgs := newCtx.CacheContext()
	for _, m := range msgs {
		h := app.MsgServiceRouter().Handler(m)
		require.NotNil(t, h, "no handler for %s", sdk.MsgTypeURL(m))
		if err := recoverOutOfGas(func() error { _, err := h(msgCtx, m); return err }); err != nil {
			return txOutcome{msgErr: err, gasUsed: msgCtx.GasMeter().GasConsumed(), free: reserved.Free}
		}
	}
	postHandler := post.NewPostHandler(post.HandlerOptions{
		FeeCollectorName: authtypes.FeeCollectorName,
		BankKeeper:       app.BankKeeper,
		EnergyKeeper:     &app.EnergyKeeper,
	})
	var postCtx sdk.Context
	if err := recoverOutOfGas(func() (err error) {
		postCtx, err = postHandler(msgCtx, tx, false, true)
		return err
	}); err != nil {
		// BaseApp drops msg and post state when the PostHandler fails
		return txOutcome{msgErr: err, gasUsed: msgCtx.GasMeter().GasConsumed(), free: reserved.Free}
	}
	writeMsgs()
	writeAnte() // msgCtx sits on anteCtx's cache: flush it down again
	return txOutcome{gasUsed: postCtx.GasMeter().GasConsumed(), free: reserved.Free}
}

func (s *energyFeeSuite) fundModule(t *testing.T, module string, amount math.Int) {
	t.Helper()
	ctx := s.nw.GetContext()
	coins := sdk.NewCoins(sdk.NewCoin(s.nw.GetBaseDenom(), amount))
	require.NoError(t, s.nw.App.BankKeeper.MintCoins(ctx, inflationtypes.ModuleName, coins))
	require.NoError(t, s.nw.App.BankKeeper.SendCoinsFromModuleToModule(ctx, inflationtypes.ModuleName, module, coins))
}

// recoverOutOfGas turns an out-of-gas panic into an error, as
// BaseApp.runTx's deferred recovery does: the tx fails, ante state stays.
func recoverOutOfGas(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			oog, ok := r.(storetypes.ErrorOutOfGas)
			if !ok {
				panic(r)
			}
			err = fmt.Errorf("out of gas in %s", oog.Descriptor)
		}
	}()
	return fn()
}

// migrationLeaf reproduces x/tokenomics/keeper.migrationLeafHash.
func migrationLeaf(claimer string, amount math.Int) []byte {
	amountBytes := []byte(amount.String())
	var buf []byte
	var lenBuf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(lenBuf[:], uint64(len(claimer)))
	buf = append(buf, lenBuf[:n]...)
	buf = append(buf, claimer...)
	n = binary.PutUvarint(lenBuf[:], uint64(len(amountBytes)))
	buf = append(buf, lenBuf[:n]...)
	buf = append(buf, amountBytes...)
	inner := sha256.Sum256(buf)
	leaf := sha256.Sum256(inner[:])
	return leaf[:]
}

// merkleProofOfDepth builds a valid proof of the given depth for one leaf
// and returns it with the matching root (sorted-pair hashing).
func merkleProofOfDepth(leaf []byte, depth int) ([][]byte, string) {
	proof := make([][]byte, depth)
	cur := leaf
	for i := 0; i < depth; i++ {
		sib := sha256.Sum256([]byte(fmt.Sprintf("sibling-%d", i)))
		proof[i] = sib[:]
		var combined []byte
		if bytes.Compare(cur, sib[:]) <= 0 {
			combined = append(append(combined, cur...), sib[:]...)
		} else {
			combined = append(append(combined, sib[:]...), cur...)
		}
		h := sha256.Sum256(combined)
		cur = h[:]
	}
	return proof, hex.EncodeToString(cur)
}

// TestSubsidizedMsgs_FitUnderTheCap is both the measurement behind
// energytypes.MaxSubsidizedTxGas and its guard: every default subsidized msg,
// on its success path and declared at exactly the cap, must stay free and
// succeed. If a handler grows past the cap this fails, and the cap (or the
// handler) has to be revisited.
//
// Run with -v to see the figures quoted on MaxSubsidizedTxGas.
func TestSubsidizedMsgs_FitUnderTheCap(t *testing.T) {
	gasLimit := energytypes.MaxSubsidizedTxGas
	s := newEnergyFeeSuite(t, 6)
	ctx := s.nw.GetContext()
	app := s.nw.App
	worst := uint64(0)
	check := func(label string, out txOutcome) {
		t.Helper()
		require.NoError(t, out.anteErr, label)
		require.NoError(t, out.msgErr, label)
		require.True(t, out.free, "%s must take the free path at gas limit %d", label, gasLimit)
		require.LessOrEqual(t, out.gasUsed, gasLimit, label)
		if out.gasUsed > worst {
			worst = out.gasUsed
		}
		t.Logf("%-45s gas=%d", label, out.gasUsed)
	}
	resetSequence := func(addr sdk.AccAddress) {
		// the factory signs with the committed sequence; our txs only
		// advance it in the working context
		acc := app.AccountKeeper.GetAccount(ctx, addr)
		require.NoError(t, acc.SetSequence(0))
		app.AccountKeeper.SetAccount(ctx, acc)
	}

	// MsgClaimMigrationTokens with realistic proof depths. 2^20..2^24
	// leaves covers any plausible migration snapshot; 32 is a ceiling.
	for i, depth := range []int{20, 24, 32} {
		key := s.keyring.GetKey(i)
		amount := math.NewIntWithDecimal(12_345, 18)
		proof, root := merkleProofOfDepth(migrationLeaf(key.AccAddr.String(), amount), depth)
		tp := app.TokenomicsKeeper.GetParams(ctx)
		tp.MigrationMerkleRoot = root
		require.NoError(t, app.TokenomicsKeeper.SetParams(ctx, tp))
		s.fundModule(t, tokenomicstypes.MigrationPoolName, amount)

		check(fmt.Sprintf("MsgClaimMigrationTokens depth=%d", depth),
			s.deliver(t, key.Priv, gasLimit, nil, &tokenomicstypes.MsgClaimMigrationTokens{
				Claimer: key.AccAddr.String(), Amount: amount, MerkleProof: proof,
			}))
	}

	// MsgReportPrice re-reads every report inside MaxPriceAgeSeconds, so its
	// cost grows with the window's population. Feeders report once a minute
	// (atoshi-docs oracle module), so 60 reports per feeder per hour; 180 =
	// three feeders.
	feeder := s.keyring.GetKey(3)
	op := app.OracleKeeper.GetParams(ctx)
	op.AllowedFeeders = []string{feeder.AccAddr.String()}
	require.NoError(t, app.OracleKeeper.SetParams(ctx, op))
	now := ctx.BlockTime().Unix()
	seeded := 0
	for _, n := range []int{0, 60, 180} {
		for ; seeded < n; seeded++ {
			require.NoError(t, app.OracleKeeper.AppendPriceHistory(ctx, oracletypes.PriceData{
				Price: math.LegacyMustNewDecFromStr("0.15"), Volume24h: math.LegacyNewDec(150_000),
				Timestamp: now - 3000 + int64(seeded), Feeder: feeder.AccAddr.String(), Source: "itest",
			}))
		}
		check(fmt.Sprintf("MsgReportPrice fresh-reports=%d", n),
			s.deliver(t, feeder.Priv, gasLimit, nil, &oracletypes.MsgReportPrice{
				Feeder: feeder.AccAddr.String(), Price: math.LegacyMustNewDecFromStr("0.15"),
				Volume24h: math.LegacyNewDec(150_000), Source: "itest",
			}))
		resetSequence(feeder.AccAddr)
	}

	// MsgDelegateEnergy / MsgUndelegateEnergy.
	del := s.keyring.GetKey(4)
	delegatee := s.keyring.GetAccAddr(5)
	acct := app.EnergyKeeper.Settle(ctx, del.AccAddr)
	acct.TxEnergyAccrued = 100_000
	app.EnergyKeeper.SetEnergyAccount(ctx, acct)
	check("MsgDelegateEnergy", s.deliver(t, del.Priv, gasLimit, nil, &energytypes.MsgDelegateEnergy{
		Delegator: del.AccAddr.String(), Delegatee: delegatee.String(), Amount: 50_000,
	}))
	resetSequence(del.AccAddr)

	var id uint64
	app.EnergyKeeper.IterateDelegationsByDelegatee(ctx, delegatee.String(), func(d energytypes.EnergyDelegation) bool {
		id = d.Id
		return true
	})
	check("MsgUndelegateEnergy", s.deliver(t, del.Priv, gasLimit, nil, &energytypes.MsgUndelegateEnergy{
		Delegator: del.AccAddr.String(), DelegationId: id,
	}))

	// The cap was set at roughly twice the worst realistic figure. Keep at
	// least 1.5x headroom so a modest handler change trips this test before
	// it silently starts charging real users.
	require.LessOrEqual(t, worst*3/2, gasLimit,
		"worst subsidized msg (%d gas) is too close to MaxSubsidizedTxGas (%d)", worst, gasLimit)
}

func (s *energyFeeSuite) setEnergy(t *testing.T, addr sdk.AccAddress, amount uint64) {
	t.Helper()
	ctx := s.nw.GetContext()
	acct := s.nw.App.EnergyKeeper.Settle(ctx, addr)
	acct.TxEnergyAccrued = amount
	s.nw.App.EnergyKeeper.SetEnergyAccount(ctx, acct)
}

func (s *energyFeeSuite) energyOf(addr sdk.AccAddress) uint64 {
	return s.nw.App.EnergyKeeper.Settle(s.nw.GetContext(), addr).TxEnergyAccrued
}

func (s *energyFeeSuite) balanceOf(addr sdk.AccAddress) math.Int {
	return s.nw.App.BankKeeper.GetBalance(s.nw.GetContext(), addr, s.nw.GetBaseDenom()).Amount
}

// H6: naming someone as Fee.Granter must not spend their energy.
//
// Before the fix the decorator charged energy to the granter and only
// consulted x/feegrant on the ATOS branch. With the victim's energy covering
// the whole gas limit that branch never ran, so the attacker's tx went
// through on the victim's energy with no grant in existence.
func TestEnergyFee_GranterEnergyIsNotSpentWithoutAGrant(t *testing.T) {
	s := newEnergyFeeSuite(t, 2)
	attacker, victim := s.keyring.GetKey(0), s.keyring.GetKey(1)
	s.setEnergy(t, attacker.AccAddr, 0)
	s.setEnergy(t, victim.AccAddr, 100_000)
	victimBal := s.balanceOf(victim.AccAddr)

	out := s.deliver(t, attacker.Priv, 100_000, victim.AccAddr, &banktypes.MsgSend{
		FromAddress: attacker.AccAddr.String(), ToAddress: attacker.AccAddr.String(),
		Amount: sdk.NewCoins(sdk.NewCoin(s.nw.GetBaseDenom(), math.OneInt())),
	})
	require.Error(t, out.anteErr, "without a fee grant the tx must be rejected")
	require.EqualValues(t, 100_000, s.energyOf(victim.AccAddr), "victim's energy must be untouched")
	require.True(t, victimBal.Equal(s.balanceOf(victim.AccAddr)), "victim's ATOS must be untouched")
}

// H6 companion: with a real grant, the granter pays only the ATOS shortfall
// (through x/feegrant), and energy still comes from the payer only.
func TestEnergyFee_GranterPaysOnlyTheATOSShortfall(t *testing.T) {
	s := newEnergyFeeSuite(t, 2)
	ctx, app := s.nw.GetContext(), s.nw.App
	payer, granter := s.keyring.GetKey(0), s.keyring.GetKey(1)
	s.setEnergy(t, payer.AccAddr, 30_000)
	s.setEnergy(t, granter.AccAddr, 100_000)
	spendLimit := sdk.NewCoins(sdk.NewCoin(s.nw.GetBaseDenom(), math.NewIntWithDecimal(1, 18)))
	require.NoError(t, app.FeeGrantKeeper.GrantAllowance(ctx, granter.AccAddr, payer.AccAddr,
		&feegrant.BasicAllowance{SpendLimit: spendLimit}))
	granterBal := s.balanceOf(granter.AccAddr)
	payerBal := s.balanceOf(payer.AccAddr)

	const gas = 200_000
	out := s.deliver(t, payer.Priv, gas, granter.AccAddr, &banktypes.MsgSend{
		FromAddress: payer.AccAddr.String(), ToAddress: granter.AccAddr.String(),
		Amount: sdk.NewCoins(sdk.NewCoin(s.nw.GetBaseDenom(), math.OneInt())),
	})
	require.NoError(t, out.anteErr)
	require.NoError(t, out.msgErr)

	require.EqualValues(t, 100_000, s.energyOf(granter.AccAddr), "granter's energy must never be spent")
	require.EqualValues(t, 0, s.energyOf(payer.AccAddr), "payer's 30k energy covers the first 30k gas")

	// Shortfall = 170k gas at the offered 1e9 liao/gas, paid by the granter
	// and debited from the allowance. The granter also received the 1-liao
	// MsgSend.
	shortfallFee := math.NewIntWithDecimal(1, 9).MulRaw(gas - 30_000)
	require.True(t, granterBal.Sub(shortfallFee).AddRaw(1).Equal(s.balanceOf(granter.AccAddr)),
		"granter must pay exactly the shortfall fee")
	require.True(t, payerBal.SubRaw(1).Equal(s.balanceOf(payer.AccAddr)), "payer pays no ATOS fee")
	grant, err := app.FeeGrantKeeper.GetAllowance(ctx, granter.AccAddr, payer.AccAddr)
	require.NoError(t, err)
	require.True(t, grant.(*feegrant.BasicAllowance).SpendLimit.Equal(spendLimit.Sub(sdk.NewCoin(s.nw.GetBaseDenom(), shortfallFee))),
		"the shortfall must be drawn from the allowance")
}

// H7: a subsidized msg declared with a huge gas limit is not free. Before the
// fix a zero-balance, zero-energy account could submit MsgReportPrice with
// gas = 30M, have it fail (not a feeder), and still land a block-filling tx
// at no cost.
func TestEnergyFee_OversizedSubsidizedTxIsNotFree(t *testing.T) {
	s := newEnergyFeeSuite(t, 2)
	ctx, app := s.nw.GetContext(), s.nw.App
	spammer := s.keyring.GetKey(0)
	// empty the spammer's wallet
	require.NoError(t, app.BankKeeper.SendCoins(ctx, spammer.AccAddr, s.keyring.GetAccAddr(1),
		sdk.NewCoins(sdk.NewCoin(s.nw.GetBaseDenom(), s.balanceOf(spammer.AccAddr)))))
	s.setEnergy(t, spammer.AccAddr, 0)

	out := s.deliver(t, spammer.Priv, 30_000_000, nil, &oracletypes.MsgReportPrice{
		Feeder: spammer.AccAddr.String(), Price: math.LegacyMustNewDecFromStr("0.15"),
		Volume24h: math.LegacyNewDec(150_000), Source: "spam",
	})
	require.Error(t, out.anteErr, "an over-cap subsidized tx must pay, and a zero-balance account cannot")
	require.False(t, out.free)
}

// M3: a failed tx's energy stays spent, like a fee. Before the fix the
// AnteHandler left a pending-reservation marker for the failed tx and
// EndBlocker refunded the whole reservation, so failing txs were free and
// an account could reuse its full energy every block.
func TestEnergyFee_FailedTxKeepsItsEnergySpent(t *testing.T) {
	s := newEnergyFeeSuite(t, 2)
	ctx, app := s.nw.GetContext(), s.nw.App
	sender := s.keyring.GetKey(0)
	s.setEnergy(t, sender.AccAddr, 100_000)

	const gas = 100_000
	tooMuch := s.balanceOf(sender.AccAddr).AddRaw(1)
	out := s.deliver(t, sender.Priv, gas, nil, &banktypes.MsgSend{
		FromAddress: sender.AccAddr.String(), ToAddress: s.keyring.GetAccAddr(1).String(),
		Amount: sdk.NewCoins(sdk.NewCoin(s.nw.GetBaseDenom(), tooMuch)),
	})
	require.NoError(t, out.anteErr)
	require.Error(t, out.msgErr, "the msg must fail")
	require.EqualValues(t, 0, s.energyOf(sender.AccAddr), "the whole gas limit is reserved in ante")

	app.EnergyKeeper.EndBlocker(ctx)
	require.EqualValues(t, 0, s.energyOf(sender.AccAddr),
		"EndBlocker must not refund a failed tx's energy")
}

// M3 companion: the success path is unchanged -- the PostHandler refunds
// the reserved energy the tx did not use.
func TestEnergyFee_SuccessfulTxIsRefundedItsUnusedEnergy(t *testing.T) {
	s := newEnergyFeeSuite(t, 2)
	ctx, app := s.nw.GetContext(), s.nw.App
	sender := s.keyring.GetKey(0)
	// Top the sender up to 300k ATOS (500k energy capacity) so a 300k gas
	// limit leaves real room above the gas used, and the refund -- capped at
	// capacity -- is not clipped.
	require.NoError(t, s.nw.FundAccount(sender.AccAddr,
		sdk.NewCoins(sdk.NewCoin(s.nw.GetBaseDenom(), math.NewIntWithDecimal(200_000, 18)))))
	s.setEnergy(t, sender.AccAddr, 300_000)

	const gas = 300_000
	out := s.deliver(t, sender.Priv, gas, nil, &banktypes.MsgSend{
		FromAddress: sender.AccAddr.String(), ToAddress: s.keyring.GetAccAddr(1).String(),
		Amount: sdk.NewCoins(sdk.NewCoin(s.nw.GetBaseDenom(), math.OneInt())),
	})
	require.NoError(t, out.anteErr)
	require.NoError(t, out.msgErr)
	app.EnergyKeeper.EndBlocker(ctx)

	left := s.energyOf(sender.AccAddr)
	require.Less(t, left, uint64(300_000), "the gas actually used is paid in energy")
	require.GreaterOrEqual(t, left, 300_000-out.gasUsed, "the unused part of the reservation is refunded")
}
