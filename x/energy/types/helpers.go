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
// "always-free" whitelist.
func (p Params) IsSubsidizedMsg(typeUrl string) bool {
	for _, t := range p.SubsidizedMsgTypeUrls {
		if t == typeUrl {
			return true
		}
	}
	return false
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
