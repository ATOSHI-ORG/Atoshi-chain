package types

import (
	"fmt"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// MaxAutoSettlePerBlock bounds Params.AutoSettlePerBlock. The sweep does one
// store read plus a possible bank transfer per account, so an unbounded value
// set by a bad proposal could push block execution past the consensus timeout
// and stall the chain.
const MaxAutoSettlePerBlock = 1000

// IndexPrecision is the fixed-point scale of GlobalIndex, matching
// math.LegacyDec's 18 decimal places. Index arithmetic is done on integers
// scaled by this factor so it stays exact.
var IndexPrecision = math.NewIntWithDecimal(1, 18)

// ----- Index math (pure; the audit-critical core of this module) -----

// ComputeIndexDelta converts an ATOS amount released into the exchange pool
// into an index increment, carrying sub-precision leftovers forward.
//
//	scaled       = amount*10^18 + remainderIn
//	deltaScaled  = scaled / supplyCap        (floor)
//	remainderOut = scaled % supplyCap
//	delta        = deltaScaled * 10^-18
//
// The remainder is what makes accumulation exact. A plain division truncates up
// to one index tick per release, and because the index only ever moves forward,
// the ATOS behind those truncated ticks would sit in the pool with no holder
// able to ever claim it. Carrying the leftover into the next release means the
// applied deltas eventually account for every aatos released.
//
// Callers must persist remainderOut; dropping it silently reintroduces the leak.
func ComputeIndexDelta(amount, remainderIn, supplyCap math.Int) (math.LegacyDec, math.Int, error) {
	if amount.IsNil() || amount.IsNegative() {
		return math.LegacyDec{}, math.Int{}, ErrInvalidAmount
	}
	if supplyCap.IsNil() || !supplyCap.IsPositive() {
		return math.LegacyDec{}, math.Int{}, fmt.Errorf("supply cap must be positive, got %s", supplyCap)
	}
	if remainderIn.IsNil() {
		remainderIn = math.ZeroInt()
	}
	if remainderIn.IsNegative() {
		return math.LegacyDec{}, math.Int{}, fmt.Errorf("index remainder must not be negative, got %s", remainderIn)
	}

	scaled := amount.Mul(IndexPrecision).Add(remainderIn)
	deltaScaled := scaled.Quo(supplyCap)
	remainderOut := scaled.Mod(supplyCap)

	return math.LegacyNewDecFromIntWithPrec(deltaScaled, 18), remainderOut, nil
}

// ComputeOwed returns the ATOS owed to a holder of `atoxBalance` aatox over an
// index span of `delta`, truncated toward zero.
//
// Truncating down is required, not cosmetic: it guarantees
//
//	sum_over_holders(owed) <= liveSupply*delta <= supplyCap*delta <= released
//
// so the pool can always cover what has been booked. Rounding up, or rounding
// to nearest, would let the sum exceed the released amount and eventually
// leave the last claimants unable to withdraw.
func ComputeOwed(atoxBalance math.Int, delta math.LegacyDec) math.Int {
	if atoxBalance.IsNil() || !atoxBalance.IsPositive() {
		return math.ZeroInt()
	}
	if delta.IsNil() || !delta.IsPositive() {
		return math.ZeroInt()
	}
	return delta.MulInt(atoxBalance).TruncateInt()
}

// ----- Params -----

// DefaultParams returns the genesis atox parameters.
func DefaultParams() Params {
	return Params{
		Enabled: true,
		// 1 trillion ATOX in aatox. Matches the tokenomics miner pool total, so
		// the exchange pool and the ATOX cap are the same size and one ATOX
		// ultimately converts to exactly one ATOS.
		SupplyCap:          math.NewIntWithDecimal(1, 30),
		AutoSettlePerBlock: 50,
		// 0.001 ATOS. Below this the sweep still settles (the debt is recorded)
		// but skips the transfer, so dust payouts do not dominate block space.
		MinAutoPayout: math.NewIntWithDecimal(1, 15),
	}
}

// Validate enforces invariants on atox params.
func (p Params) Validate() error {
	if p.SupplyCap.IsNil() || !p.SupplyCap.IsPositive() {
		return fmt.Errorf("supply cap must be positive, got %s", p.SupplyCap)
	}
	if p.AutoSettlePerBlock > MaxAutoSettlePerBlock {
		return fmt.Errorf("auto settle per block must be <= %d, got %d",
			MaxAutoSettlePerBlock, p.AutoSettlePerBlock)
	}
	if p.MinAutoPayout.IsNil() || p.MinAutoPayout.IsNegative() {
		return fmt.Errorf("min auto payout must not be negative, got %s", p.MinAutoPayout)
	}
	return nil
}

// ----- State constructors -----

// DefaultGlobalState returns the genesis accumulator: nothing minted, nothing
// released, index at zero.
func DefaultGlobalState() GlobalState {
	return GlobalState{
		GlobalIndex:         math.LegacyZeroDec(),
		IndexRemainder:      math.ZeroInt(),
		TotalReleasedToPool: math.ZeroInt(),
		TotalPending:        math.ZeroInt(),
		TotalPaidOut:        math.ZeroInt(),
	}
}

// Validate enforces invariants on the accumulator.
func (s GlobalState) Validate() error {
	if s.GlobalIndex.IsNil() || s.GlobalIndex.IsNegative() {
		return fmt.Errorf("global_index must not be negative, got %s", s.GlobalIndex)
	}
	ints := []struct {
		name string
		v    math.Int
	}{
		{"index_remainder", s.IndexRemainder},
		{"total_released_to_pool", s.TotalReleasedToPool},
		{"total_pending", s.TotalPending},
		{"total_paid_out", s.TotalPaidOut},
	}
	for _, c := range ints {
		if c.v.IsNil() {
			return fmt.Errorf("%s: nil", c.name)
		}
		if c.v.IsNegative() {
			return fmt.Errorf("%s: must not be negative, got %s", c.name, c.v)
		}
	}
	// Solvency: everything owed or already handed out must be covered by what
	// tier releases actually paid into the pool. A genesis violating this starts
	// the chain already unable to honour conversions.
	if booked := s.TotalPending.Add(s.TotalPaidOut); booked.GT(s.TotalReleasedToPool) {
		return fmt.Errorf("total_pending + total_paid_out (%s) exceeds total_released_to_pool (%s)",
			booked, s.TotalReleasedToPool)
	}
	return nil
}

// NewAtoxAccount returns a settlement record starting at the given index.
//
// New holders start at the CURRENT global index, never at zero: they held no
// ATOX while the index advanced, so crediting them for that span would pay out
// ATOS that the pool released on behalf of other holders.
func NewAtoxAccount(addr string, index math.LegacyDec) AtoxAccount {
	return AtoxAccount{
		Address:      addr,
		Index:        index,
		Pending:      math.ZeroInt(),
		TotalClaimed: math.ZeroInt(),
	}
}

// Validate enforces invariants on a settlement record.
func (a AtoxAccount) Validate() error {
	if _, err := sdk.AccAddressFromBech32(a.Address); err != nil {
		return fmt.Errorf("address %q: %w", a.Address, err)
	}
	if a.Index.IsNil() || a.Index.IsNegative() {
		return fmt.Errorf("index must not be negative, got %s", a.Index)
	}
	if a.Pending.IsNil() || a.Pending.IsNegative() {
		return fmt.Errorf("pending must not be negative, got %s", a.Pending)
	}
	if a.TotalClaimed.IsNil() || a.TotalClaimed.IsNegative() {
		return fmt.Errorf("total_claimed must not be negative, got %s", a.TotalClaimed)
	}
	return nil
}

// ----- Genesis -----

// DefaultGenesisState returns a fresh atox GenesisState. ATOX has no pre-mine:
// supply starts at zero and grows only through block rewards.
func DefaultGenesisState() *GenesisState {
	return &GenesisState{
		Params:      DefaultParams(),
		GlobalState: DefaultGlobalState(),
		Accounts:    []AtoxAccount{},
		ScanCursor:  nil,
	}
}

// Validate enforces invariants on the atox genesis state.
func (gs GenesisState) Validate() error {
	if err := gs.Params.Validate(); err != nil {
		return fmt.Errorf("invalid atox params: %w", err)
	}
	if err := gs.GlobalState.Validate(); err != nil {
		return fmt.Errorf("invalid global_state: %w", err)
	}

	seen := make(map[string]struct{}, len(gs.Accounts))
	sumPending := math.ZeroInt()
	for i, acct := range gs.Accounts {
		if err := acct.Validate(); err != nil {
			return fmt.Errorf("invalid accounts[%d]: %w", i, err)
		}
		if _, dup := seen[acct.Address]; dup {
			return fmt.Errorf("invalid accounts[%d]: duplicate address %q", i, acct.Address)
		}
		seen[acct.Address] = struct{}{}

		// An account index above the global index means a negative settlement
		// span. Settlement clamps rather than paying out negative amounts, so
		// this would silently freeze the account instead of erroring later.
		if acct.Index.GT(gs.GlobalState.GlobalIndex) {
			return fmt.Errorf("invalid accounts[%d]: index (%s) exceeds global_index (%s)",
				i, acct.Index, gs.GlobalState.GlobalIndex)
		}
		sumPending = sumPending.Add(acct.Pending)
	}

	// TotalPending is maintained incrementally at runtime so the invariant check
	// does not have to walk every account. Genesis is the one place we can
	// afford to verify the cached total against the accounts it summarizes.
	if !sumPending.Equal(gs.GlobalState.TotalPending) {
		return fmt.Errorf("global_state.total_pending (%s) does not match the sum of account pending (%s)",
			gs.GlobalState.TotalPending, sumPending)
	}
	return nil
}

// ----- Msg ValidateBasic -----

func (msg MsgClaimAtos) ValidateBasic() error {
	_, err := sdk.AccAddressFromBech32(msg.Claimer)
	return err
}

func (msg MsgUpdateParams) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(msg.Authority); err != nil {
		return err
	}
	return msg.Params.Validate()
}
