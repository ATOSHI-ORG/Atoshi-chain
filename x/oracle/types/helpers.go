package types

import (
	"context"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// BankKeeper is the subset of the bank keeper used by x/oracle (currently none,
// but kept here so future enhancements like fee deduction or per-feeder
// stipends have a single point of integration).
type BankKeeper interface {
	GetBalance(ctx context.Context, addr sdk.AccAddress, denom string) sdk.Coin
}

// DefaultParams returns sensible defaults for the oracle module.
//
// Audit Issue 10-A: MaxPriceDeviationBps was previously 1000 (10%),
// which the auditor observed would REJECT legitimate large price
// movements (real market crashes / spikes) alongside malicious ones.
// Raised to 5000 (50%) as a wider guardrail — still catches obvious
// oracle manipulation (a feeder reporting 5x the current price) but
// won't lock the chain into a stale price when a real 20-40% market
// move happens. Governance can tighten this later once multi-feeder
// deployment is in place (see Issue 10-B) and manipulation risk is
// diluted by consensus.
func DefaultParams() Params {
	return Params{
		AllowedFeeders:       []string{},
		MaxPriceAgeSeconds:   3600,  // 1 hour
		MinValidReports:      1,
		Denom:                "aatos",
		TWAPLookbackSeconds:  86400, // 24 hours
		MaxPriceDeviationBps: 5000,  // 50% (audit Issue 10-A: widen from 10%)
	}
}

// Validate enforces invariants on the oracle parameters.
func (p Params) Validate() error {
	if p.MaxPriceAgeSeconds == 0 {
		return fmt.Errorf("max price age cannot be zero")
	}
	if p.MinValidReports == 0 {
		return fmt.Errorf("min valid reports cannot be zero")
	}
	if p.Denom == "" {
		return fmt.Errorf("denom cannot be empty")
	}
	if p.TWAPLookbackSeconds == 0 {
		return fmt.Errorf("TWAP lookback cannot be zero")
	}
	for _, feeder := range p.AllowedFeeders {
		if _, err := sdk.AccAddressFromBech32(feeder); err != nil {
			return fmt.Errorf("invalid feeder address %s: %w", feeder, err)
		}
	}
	return nil
}

// IsAllowedFeeder reports whether addr is in the allowed feeders list.
func (p Params) IsAllowedFeeder(addr string) bool {
	for _, f := range p.AllowedFeeders {
		if f == addr {
			return true
		}
	}
	return false
}

// Validate enforces invariants on a price report.
func (p PriceData) Validate() error {
	if p.Price.IsNegative() {
		return fmt.Errorf("price cannot be negative: %s", p.Price)
	}
	if p.Volume24h.IsNegative() {
		return fmt.Errorf("volume cannot be negative: %s", p.Volume24h)
	}
	if p.Timestamp <= 0 {
		return fmt.Errorf("invalid timestamp: %d", p.Timestamp)
	}
	if _, err := sdk.AccAddressFromBech32(p.Feeder); err != nil {
		return fmt.Errorf("invalid feeder address: %s", p.Feeder)
	}
	return nil
}

// DefaultGenesisState returns a fresh GenesisState with default params.
func DefaultGenesisState() *GenesisState {
	return &GenesisState{
		Params:       DefaultParams(),
		PriceHistory: []PriceData{},
	}
}

// Validate enforces invariants on the oracle genesis state.
func (gs GenesisState) Validate() error {
	if err := gs.Params.Validate(); err != nil {
		return fmt.Errorf("invalid params: %w", err)
	}
	for i, pd := range gs.PriceHistory {
		if err := pd.Validate(); err != nil {
			return fmt.Errorf("invalid price history entry %d: %w", i, err)
		}
	}
	return nil
}

// ValidateBasic on MsgReportPrice ensures the report is well-formed before it
// reaches the keeper. The keeper still re-checks the feeder whitelist.
func (msg MsgReportPrice) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(msg.Feeder); err != nil {
		return err
	}
	if msg.Price.IsNegative() || msg.Price.IsZero() {
		return ErrInvalidPrice
	}
	if msg.Volume24h.IsNegative() {
		return ErrInvalidVolume
	}
	if msg.Source == "" {
		return ErrInvalidSource
	}
	return nil
}

// ValidateBasic on MsgUpdateParams enforces the authority is well-formed and
// the embedded params satisfy module invariants.
func (msg MsgUpdateParams) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(msg.Authority); err != nil {
		return err
	}
	return msg.Params.Validate()
}
