package keeper_test

import (
	"testing"

	"cosmossdk.io/math"
	"github.com/stretchr/testify/require"

	sdk "github.com/cosmos/cosmos-sdk/types"

	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/atoshi-chain/atoshi/v20/testutil/integration/evmos/network"
	atoshitypes "github.com/atoshi-chain/atoshi/v20/types"
	stakingkeeper "github.com/atoshi-chain/atoshi/v20/x/staking/keeper"
	tokenomicstypes "github.com/atoshi-chain/atoshi/v20/x/tokenomics/types"
)

// slashFixture stands up a network with one third-party delegation against the
// first validator and returns everything the assertions need.
type slashFixture struct {
	nw        *network.UnitTestNetwork
	ctx       sdk.Context
	valAddr   sdk.ValAddress
	consAddr  sdk.ConsAddress
	delegator sdk.AccAddress
	power     int64
}

func setupSlashFixture(t *testing.T, delegation math.Int) slashFixture {
	t.Helper()

	nw := network.NewUnitTestNetwork()
	ctx := nw.GetContext()

	validator := nw.GetValidators()[0]
	valAddr, err := sdk.ValAddressFromBech32(validator.OperatorAddress)
	require.NoError(t, err)
	consAddr, err := validator.GetConsAddr()
	require.NoError(t, err)

	srv := stakingkeeper.NewMsgServerImpl(&nw.App.StakingKeeper)
	delegate := func(from sdk.AccAddress, amount math.Int) {
		coin := sdk.NewCoin(atoshitypes.BaseDenom, amount)
		require.NoError(t, nw.FundAccount(from, sdk.NewCoins(coin)))
		_, err := srv.Delegate(ctx, &stakingtypes.MsgDelegate{
			DelegatorAddress: from.String(),
			ValidatorAddress: validator.OperatorAddress,
			Amount:           coin,
		})
		require.NoError(t, err)
	}

	// The fixture's validators have no self-delegation: createDelegations
	// (testutil/.../network/setup.go) stakes the first genesis account against
	// every validator and nothing else. Mainnet validators always have one --
	// MinSelfDelegationDecorator refuses MsgCreateValidator without 1e26 of it --
	// so the fixture has to be given one or these tests exercise the degenerate
	// no-self-stake branch instead of the real path.
	delegate(sdk.AccAddress(valAddr), delegation.MulRaw(4))

	// A delegator that is not the validator. This is the account the whole
	// exercise is about: it must be worth the same before and after.
	delegator := sdk.AccAddress([]byte("slash-test-delegator"))
	delegate(delegator, delegation)

	v, err := nw.App.StakingKeeper.GetValidator(ctx, valAddr)
	require.NoError(t, err)

	return slashFixture{
		nw:        nw,
		ctx:       ctx,
		valAddr:   valAddr,
		consAddr:  sdk.ConsAddress(consAddr),
		delegator: delegator,
		power:     v.GetConsensusPower(nw.App.StakingKeeper.PowerReduction(ctx)),
	}
}

// delegationValue is what a delegation is worth in ATOS right now: shares priced
// at the validator's current tokens-per-share. This is the number a user sees in
// a wallet, and the number the SDK's slash silently moves.
func delegationValue(t *testing.T, f slashFixture, who sdk.AccAddress) math.Int {
	t.Helper()
	v, err := f.nw.App.StakingKeeper.GetValidator(f.ctx, f.valAddr)
	require.NoError(t, err)
	del, err := f.nw.App.StakingKeeper.GetDelegation(f.ctx, who, f.valAddr)
	require.NoError(t, err)
	return v.TokensFromShares(del.Shares).TruncateInt()
}

func slashPoolBalance(t *testing.T, f slashFixture) math.Int {
	t.Helper()
	addr := f.nw.App.AccountKeeper.GetModuleAddress(tokenomicstypes.SlashPoolName)
	require.NotNil(t, addr, "slash_pool module account must exist")
	return f.nw.App.BankKeeper.GetBalance(f.ctx, addr, atoshitypes.BaseDenom).Amount
}

// TestSlash_LeavesOtherDelegationsUntouched is the acceptance test for the whole
// change. Under the SDK implementation this fails: a 1% slash lowers
// validator.Tokens while leaving DelegatorShares alone, so the delegator's
// shares are worth 1% less without any of its own records changing. That is
// precisely what produced the 2026-10-18 support tickets.
func TestSlash_LeavesOtherDelegationsUntouched(t *testing.T) {
	f := setupSlashFixture(t, math.NewInt(5_000_000_000_000_000_000)) // 5 ATOS

	before := delegationValue(t, f, f.delegator)
	require.True(t, before.IsPositive())

	confiscated, err := f.nw.App.StakingKeeper.Slash(
		f.ctx, f.consAddr, f.ctx.BlockHeight(), f.power, math.LegacyNewDecWithPrec(1, 2), // 1%
	)
	require.NoError(t, err)
	require.True(t, confiscated.IsPositive(), "a 1%% penalty should confiscate something")

	after := delegationValue(t, f, f.delegator)
	require.Equal(t, before.String(), after.String(),
		"a third-party delegation must be worth exactly the same after a slash")
}

// TestSlash_ConfiscatesFromTheValidatorsOwnStake checks the other half: the
// penalty is not merely spared the delegators, it is actually taken from
// somebody. The validator's self-delegation is what shrinks.
func TestSlash_ConfiscatesFromTheValidatorsOwnStake(t *testing.T) {
	f := setupSlashFixture(t, math.NewInt(5_000_000_000_000_000_000))

	selfDelegator := sdk.AccAddress(f.valAddr)
	before := delegationValue(t, f, selfDelegator)

	confiscated, err := f.nw.App.StakingKeeper.Slash(
		f.ctx, f.consAddr, f.ctx.BlockHeight(), f.power, math.LegacyNewDecWithPrec(1, 2),
	)
	require.NoError(t, err)

	after := delegationValue(t, f, selfDelegator)
	require.Equal(t, before.Sub(confiscated).String(), after.String(),
		"the validator's own stake must fall by exactly what was confiscated")
}

// TestSlash_MovesToSlashPoolAndDoesNotBurn pins the second half of the change.
// The SDK burns, which destroys supply pegged 100:1 against a fixed ERC20 total
// and leaves no way to compensate anyone. Here the supply is constant and the
// penalty is sitting in a module account that governance can spend.
func TestSlash_MovesToSlashPoolAndDoesNotBurn(t *testing.T) {
	f := setupSlashFixture(t, math.NewInt(5_000_000_000_000_000_000))

	supplyBefore := f.nw.App.BankKeeper.GetSupply(f.ctx, atoshitypes.BaseDenom).Amount
	poolBefore := slashPoolBalance(t, f)

	confiscated, err := f.nw.App.StakingKeeper.Slash(
		f.ctx, f.consAddr, f.ctx.BlockHeight(), f.power, math.LegacyNewDecWithPrec(1, 2),
	)
	require.NoError(t, err)
	require.True(t, confiscated.IsPositive())

	require.Equal(t,
		supplyBefore.String(),
		f.nw.App.BankKeeper.GetSupply(f.ctx, atoshitypes.BaseDenom).Amount.String(),
		"slashing must not change total supply -- the 100:1 ERC20 peg depends on it")
	require.Equal(t,
		poolBefore.Add(confiscated).String(),
		slashPoolBalance(t, f).String(),
		"every confiscated coin must land in slash_pool")
}

// TestSlashWithInfractionReason_UsesTheOverride guards the easiest way to get
// this change half-done. x/evidence calls SlashWithInfractionReason for double
// signs; if only Slash were overridden, the embedded SDK keeper's copy would
// still run and double-sign penalties would keep diluting delegators, with
// nothing in the behaviour to suggest an override was missing.
func TestSlashWithInfractionReason_UsesTheOverride(t *testing.T) {
	f := setupSlashFixture(t, math.NewInt(5_000_000_000_000_000_000))

	before := delegationValue(t, f, f.delegator)
	poolBefore := slashPoolBalance(t, f)

	confiscated, err := f.nw.App.StakingKeeper.SlashWithInfractionReason(
		f.ctx, f.consAddr, f.ctx.BlockHeight(), f.power,
		math.LegacyNewDecWithPrec(5, 3), // 0.5%, the double-sign factor
		stakingtypes.Infraction_INFRACTION_DOUBLE_SIGN,
	)
	require.NoError(t, err)
	require.True(t, confiscated.IsPositive())

	require.Equal(t, before.String(), delegationValue(t, f, f.delegator).String(),
		"double-sign penalties must spare delegators too")
	require.Equal(t, poolBefore.Add(confiscated).String(), slashPoolBalance(t, f).String(),
		"double-sign penalties must also land in slash_pool")
}

// TestSlash_CapsAtSelfStake covers the degenerate case: a penalty larger than
// everything the validator has staked. It must stop at the self-stake rather
// than reach into delegations, which is the behaviour being removed.
func TestSlash_CapsAtSelfStake(t *testing.T) {
	f := setupSlashFixture(t, math.NewInt(5_000_000_000_000_000_000))

	selfDelegator := sdk.AccAddress(f.valAddr)
	selfBefore := delegationValue(t, f, selfDelegator)
	delegatorBefore := delegationValue(t, f, f.delegator)

	// 100%: more than the validator's own stake once a third party is also
	// delegated, because the figure is taken against total consensus power.
	confiscated, err := f.nw.App.StakingKeeper.Slash(
		f.ctx, f.consAddr, f.ctx.BlockHeight(), f.power, math.LegacyOneDec(),
	)
	require.NoError(t, err)

	require.Equal(t, selfBefore.String(), confiscated.String(),
		"a penalty beyond the self-stake confiscates the self-stake and stops")
	require.Equal(t, delegatorBefore.String(), delegationValue(t, f, f.delegator).String(),
		"even an uncoverable penalty must not touch a delegator")
}

// TestSlash_PoolHasNoMintOrBurnPermission makes the containment explicit. The
// account that now holds every penalty must not be able to create or destroy
// what it holds; MsgSpendSlashPool is the only way out, and that is governance.
func TestSlash_PoolHasNoMintOrBurnPermission(t *testing.T) {
	nw := network.NewUnitTestNetwork()
	ctx := nw.GetContext()

	addr := nw.App.AccountKeeper.GetModuleAddress(tokenomicstypes.SlashPoolName)
	require.NotNil(t, addr)

	// GetModuleAccount, not GetAccount: module accounts are instantiated lazily
	// on first use, and nothing has paid into slash_pool in this fixture.
	macc := nw.App.AccountKeeper.GetModuleAccount(ctx, tokenomicstypes.SlashPoolName)
	require.NotNil(t, macc, "slash_pool must be a registered module account")
	require.Equal(t, addr.String(), macc.GetAddress().String())
	require.Empty(t, macc.GetPermissions(),
		"slash_pool must hold no permissions: Burner would let a penalty be destroyed, Minter conjured")
}
