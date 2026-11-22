package types

import (
	"fmt"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// DefaultParams returns the genesis energy parameters.
//
// The numbers track the design doc: 30,000 ATOS holding gives one
// 50,000-gas free transfer per 24h; 1M ATOS holding gives one 800k-gas
// deploy every 10 days. Threshold ATOS is expressed in liao
// (1 ATOS = 10^18 liao).
func DefaultParams() Params {
	atosUnit := math.NewIntWithDecimal(1, 18) // 1 ATOS in liao
	return Params{
		EnergyEnabled:            true,
		TxEnergyHoldingThreshold: atosUnit.Mul(math.NewInt(30_000)),
		TxEnergyPerThreshold:     50_000,
		TxEnergyMaxAccrueWindow:  86_400, // 24h
		DeployHoldingThreshold:   atosUnit.Mul(math.NewInt(1_000_000)),
		DeployEnergyCapacity:     800_000,
		DeployRecoverDays:        10,
		InsufficientGasPrice:     math.LegacyNewDecWithPrec(21, 4), // 0.0021
		SubsidizedMsgTypeUrls: []string{
			"/atoshi.tokenomics.v1.MsgClaimMigrationTokens",
			"/atoshi.oracle.v1.MsgReportPrice",
			// Delegate / undelegate of energy itself MUST be subsidized:
			// the AnteHandler greedily reserves up to gas_limit worth of
			// the signer's energy as fee coverage, which would empty the
			// delegator's accrued energy BEFORE the delegate msg_server
			// runs and try to lock some of it. Subsidizing these two
			// messages is safe — both already require locking ATOS as
			// collateral, which is the real anti-spam protection.
			// The subsidy only holds within MaxSubsidizedTxGas: a
			// delegate tx declared above it is charged energy first and
			// can then fail for lack of energy to lend.
			"/atoshi.energy.v1.MsgDelegateEnergy",
			"/atoshi.energy.v1.MsgUndelegateEnergy",
		},
		PrivacyRelayerWhitelist:          []string{},
		DefaultDelegationDurationSeconds: 86_400, // 24h
		MaxDelegationDurationSeconds:     86_400, // 24h, hard cap
	}
}

// Validate enforces invariants on energy params.
func (p Params) Validate() error {
	if p.TxEnergyHoldingThreshold.IsNil() || !p.TxEnergyHoldingThreshold.IsPositive() {
		return fmt.Errorf("tx_energy_holding_threshold must be positive")
	}
	if p.TxEnergyPerThreshold == 0 {
		return fmt.Errorf("tx_energy_per_threshold must be positive")
	}
	if p.TxEnergyMaxAccrueWindow <= 0 {
		return fmt.Errorf("tx_energy_max_accrue_window must be positive")
	}
	if p.DeployHoldingThreshold.IsNil() || !p.DeployHoldingThreshold.IsPositive() {
		return fmt.Errorf("deploy_holding_threshold must be positive")
	}
	if p.DeployEnergyCapacity == 0 {
		return fmt.Errorf("deploy_energy_capacity must be positive")
	}
	if p.DeployRecoverDays <= 0 {
		return fmt.Errorf("deploy_recover_days must be positive")
	}
	if p.InsufficientGasPrice.IsNil() || p.InsufficientGasPrice.IsNegative() {
		return fmt.Errorf("insufficient_gas_price cannot be negative")
	}
	for _, addr := range p.PrivacyRelayerWhitelist {
		if _, err := sdk.AccAddressFromBech32(addr); err != nil {
			return fmt.Errorf("invalid privacy relayer address %q: %w", addr, err)
		}
	}
	// Delegation-duration params: 0 means "not set in state" and code
	// falls back to compiled defaults. When both are set, default must
	// not exceed max, otherwise a client omitting duration would be
	// auto-rejected against the cap.
	if p.DefaultDelegationDurationSeconds < 0 {
		return fmt.Errorf("default_delegation_duration_seconds cannot be negative")
	}
	if p.MaxDelegationDurationSeconds < 0 {
		return fmt.Errorf("max_delegation_duration_seconds cannot be negative")
	}
	if p.MaxDelegationDurationSeconds > 0 &&
		p.DefaultDelegationDurationSeconds > p.MaxDelegationDurationSeconds {
		return fmt.Errorf(
			"default_delegation_duration_seconds (%d) exceeds max_delegation_duration_seconds (%d)",
			p.DefaultDelegationDurationSeconds, p.MaxDelegationDurationSeconds)
	}
	return nil
}

// IsSubsidizedMsg reports whether a Msg type URL is on the
// subsidized whitelist. Being on the list is necessary but not
// sufficient for a tx to be free; see IsSubsidizedTx.
func (p Params) IsSubsidizedMsg(typeUrl string) bool {
	for _, t := range p.SubsidizedMsgTypeUrls {
		if t == typeUrl {
			return true
		}
	}
	return false
}

// MaxSubsidizedTxGas is the largest gas limit a subsidized tx may declare
// and still be free. A tx above it is not rejected; it simply loses the
// subsidy and pays through the normal energy / ATOS path like any other tx.
//
// Why a cap exists at all: a free tx costs its sender nothing whether it
// succeeds or fails, and a failed tx is still included in the block with its
// full gas limit counted against the block. Without a cap, any account --
// including one holding zero ATOS -- could sign a single MsgReportPrice or
// MsgClaimMigrationTokens with gas limit = block max gas; the msg fails
// (unauthorized feeder / bad proof), the tx still lands, and every block can
// be filled for free.
//
// How the value was chosen: the real AnteHandler + msg handler + PostHandler
// were run through app/ante/energy_fee_test.go
// (TestSubsidizedMsgs_FitUnderTheCap) on a full app, measuring delivered gas
// for each default subsidized msg on its success path:
//
//	MsgClaimMigrationTokens, Merkle proof depth 20 / 24 / 32  ~142k-164k
//	  (depth barely matters, ~340 gas/level; the first claim on a chain
//	  pays a one-off ~20k more)
//	MsgDelegateEnergy (delegatee's first inbound delegation)   ~177k
//	MsgUndelegateEnergy                                        ~118k
//	MsgReportPrice, 0 / 60 / 180 fresh reports in the window   ~89k / ~107k / ~168k
//	  (180 = three feeders at the documented once-a-minute cadence over
//	  the default 1h MaxPriceAgeSeconds)
//
// MsgReportPrice grows by ~510 gas per report inside MaxPriceAgeSeconds,
// because the handler re-reads the whole window for the median. The worst
// realistic figure is ~177k; doubling it and rounding up gives 400k, which
// also leaves room for ~600 fresh reports (ten feeders at once a minute).
// A feeder deployment that outgrows that does not break -- its reports just
// stop being free and are charged energy/ATOS like any other tx.
//
// The cap also bounds tx size for free txs: ConsumeGasForTxSize charges
// 10 gas/byte before the energy decorator runs, so a free tx can carry at
// most ~40 KB.
const MaxSubsidizedTxGas uint64 = 400_000

// IsSubsidizedTx reports whether a tx with the given msg type URLs and
// declared gas limit is entirely free (no energy, no ATOS). All three must
// hold:
//
//   - exactly one msg -- batching N subsidized msgs into one tx would let a
//     single signature buy N msgs' worth of free execution, and nothing
//     legitimate needs it;
//   - that msg is on SubsidizedMsgTypeUrls;
//   - gasLimit <= MaxSubsidizedTxGas.
//
// A tx failing any of these is not rejected -- it falls through to the
// normal energy / ATOS path.
func (p Params) IsSubsidizedTx(msgTypeUrls []string, gasLimit uint64) bool {
	return len(msgTypeUrls) == 1 &&
		gasLimit <= MaxSubsidizedTxGas &&
		p.IsSubsidizedMsg(msgTypeUrls[0])
}

// NewEnergyAccount returns a fresh, zeroed energy account for an address.
func NewEnergyAccount(addr string) EnergyAccount {
	return EnergyAccount{
		Address:             addr,
		TxEnergyAccrued:     0,
		DeployEnergyAccrued: 0,
		LastBalanceSnapshot: math.ZeroInt(),
		LastUpdatedTime:     0,
		DelegatedOut:        0,
		DelegatedInUsable:   0,
		LockedAtos:          math.ZeroInt(),
		StakedSnapshot:      math.ZeroInt(),
	}
}

// TxEnergyCapacity returns how much TxEnergy a holder of `eligibleBalance`
// can accumulate at most. Rounded down by truncating threshold blocks.
//
// Formula: floor(balance / threshold) * tx_energy_per_threshold
//
// The lazy-settle code uses this when computing accrual against a stored
// last_balance_snapshot.
func TxEnergyCapacity(eligibleBalance math.Int, p Params) uint64 {
	if eligibleBalance.IsNil() || !eligibleBalance.IsPositive() {
		return 0
	}
	if p.TxEnergyHoldingThreshold.IsNil() || !p.TxEnergyHoldingThreshold.IsPositive() {
		return 0
	}
	units := eligibleBalance.Quo(p.TxEnergyHoldingThreshold)
	if !units.IsUint64() {
		// guard against absurd holdings overflowing uint64; clamp to max
		return ^uint64(0)
	}
	// Audit Issue 11: the prior `units.Uint64() * p.TxEnergyPerThreshold`
	// only protected against `units` exceeding uint64. The multiplication
	// itself could still wrap around if units × TxEnergyPerThreshold
	// crossed 2^64. After the wrap, capacity collapses to a small number
	// (or even 0), silently destroying the user's accrual ceiling.
	// Saturate instead so the cap is clamped at MaxUint64 in the
	// pathological case (which has no realistic legitimate trigger
	// under default params but becomes reachable if governance retunes
	// either parameter).
	return saturatingMulU64(units.Uint64(), p.TxEnergyPerThreshold)
}

// saturatingMulU64 returns a*b clamped to ^uint64(0) on overflow.
// Local copy intentional: types package cannot import the keeper
// package, and the helper is identical in behavior to the keeper-side
// saturatingMul used elsewhere in this module.
func saturatingMulU64(a, b uint64) uint64 {
	if a == 0 || b == 0 {
		return 0
	}
	if a > ^uint64(0)/b {
		return ^uint64(0)
	}
	return a * b
}

// Audit Recommendation-3 (round2): DeployRecoverPerSecond was removed.
// The function had no production callers — only docstring mentions —
// and its overflow hardening (Issue-17 part 2 at commit 86f431c
// follow-up) was identical to what x/energy/keeper/settle.go's
// deployAddOverElapsed already does on the live refill path. Having
// both invited drift between the documented per-second rate and the
// actually-used elapsed-window helper. The live path stays via
// deployAddOverElapsed; tests covering DeployRecoverPerSecond are
// removed in the same commit.

// DefaultGenesisState is the energy module's default genesis.
func DefaultGenesisState() *GenesisState {
	return &GenesisState{
		Params:           DefaultParams(),
		Accounts:         []EnergyAccount{},
		Delegations:      []EnergyDelegation{},
		NextDelegationId: 1,
	}
}

// Validate enforces invariants on the energy genesis state.
func (gs GenesisState) Validate() error {
	if err := gs.Params.Validate(); err != nil {
		return fmt.Errorf("invalid energy params: %w", err)
	}
	for i, acct := range gs.Accounts {
		if _, err := sdk.AccAddressFromBech32(acct.Address); err != nil {
			return fmt.Errorf("genesis account %d has bad address: %w", i, err)
		}
	}
	for i, d := range gs.Delegations {
		if d.Id == 0 {
			return fmt.Errorf("genesis delegation %d has zero id", i)
		}
		if _, err := sdk.AccAddressFromBech32(d.Delegator); err != nil {
			return fmt.Errorf("genesis delegation %d bad delegator: %w", i, err)
		}
		if _, err := sdk.AccAddressFromBech32(d.Delegatee); err != nil {
			return fmt.Errorf("genesis delegation %d bad delegatee: %w", i, err)
		}
	}
	return nil
}

// ----- Msg ValidateBasic -----

func (msg MsgDelegateEnergy) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(msg.Delegator); err != nil {
		return err
	}
	if _, err := sdk.AccAddressFromBech32(msg.Delegatee); err != nil {
		return err
	}
	if msg.Delegator == msg.Delegatee {
		return ErrSelfDelegation
	}
	if msg.Amount == 0 {
		return ErrInvalidAmount
	}
	// DurationSeconds == 0 means "use protocol default"
	// (DefaultDelegationDurationSeconds, applied by the msg server).
	// Only NEGATIVE values are rejected here — they have no sensible
	// interpretation. The server normalizes 0 → default before
	// reaching the keeper's Delegate, which still requires > 0.
	if msg.DurationSeconds < 0 {
		return ErrInvalidDuration
	}
	return nil
}

func (msg MsgUndelegateEnergy) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(msg.Delegator); err != nil {
		return err
	}
	if msg.DelegationId == 0 {
		return ErrDelegationNotFound
	}
	return nil
}

func (msg MsgUpdateParams) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(msg.Authority); err != nil {
		return err
	}
	return msg.Params.Validate()
}
