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

// MaxTransferFeeBps caps Params.TransferFeeBps at 30%. The fee is a behavioural
// lever, not a safety control, so governance has no legitimate reason to push it
// near 100% — and an unbounded value would let one proposal make ATOX
// effectively untransferable, or make every transfer fail for lack of headroom.
const MaxTransferFeeBps = 3000

// BpsDenominator is the basis-point scale.
const BpsDenominator = 10000

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
// applied deltas eventually account for every liao released.
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

// ComputeTransferFee returns the fee charged ON TOP of an ATOX transfer of
// `amount`, rounded up.
//
// Rounding up rather than down keeps the fee from being avoidable by splitting:
// truncating would make any transfer below BpsDenominator/feeBps aatox free, so
// a sender could move an unlimited amount fee-free in dust-sized pieces. Rounding
// up costs at most 1 aatox extra on a legitimate transfer.
func ComputeTransferFee(amount math.Int, feeBps uint32) math.Int {
	if feeBps == 0 || amount.IsNil() || !amount.IsPositive() {
		return math.ZeroInt()
	}
	num := amount.MulRaw(int64(feeBps))
	den := math.NewInt(BpsDenominator)
	fee := num.Quo(den)
	if !num.Mod(den).IsZero() {
		fee = fee.AddRaw(1)
	}
	return fee
}

// MaxSendableWithFee returns the largest amount an account holding `balance` can
// transfer once the on-top fee is accounted for, i.e. the value a wallet's "Max"
// button must use. Sending more than this always fails for want of fee headroom.
func MaxSendableWithFee(balance math.Int, feeBps uint32) math.Int {
	if balance.IsNil() || !balance.IsPositive() {
		return math.ZeroInt()
	}
	if feeBps == 0 {
		return balance
	}
	// Largest a with a + ceil(a*bps/10000) <= balance. Start from the exact
	// division and walk back, which is at most one step.
	a := balance.MulRaw(BpsDenominator).Quo(math.NewInt(int64(BpsDenominator + int(feeBps))))
	for a.IsPositive() && a.Add(ComputeTransferFee(a, feeBps)).GT(balance) {
		a = a.SubRaw(1)
	}
	if a.IsNegative() {
		return math.ZeroInt()
	}
	return a
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
		// 10% on top of every account-to-account transfer, burned to recycle it
		// into the mining pool. Governance-tunable so the rate can be dialled
		// back without a chain upgrade if 10% turns out to be too steep.
		TransferFeeBps: 1000,
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
	if p.TransferFeeBps > MaxTransferFeeBps {
		return fmt.Errorf("transfer fee bps must be <= %d, got %d",
			MaxTransferFeeBps, p.TransferFeeBps)
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
		TotalFeeBurned:      math.ZeroInt(),
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
		{"total_fee_burned", s.TotalFeeBurned},
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
		SweptIndex:  math.LegacyZeroDec(),
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
	if gs.SweptIndex.IsNil() || gs.SweptIndex.IsNegative() {
		return fmt.Errorf("invalid swept_index: must not be negative, got %s", gs.SweptIndex)
	}
	// A swept_index above the live index would make the EndBlocker skip forever,
	// silently disabling automatic conversion for the whole chain.
	if gs.SweptIndex.GT(gs.GlobalState.GlobalIndex) {
		return fmt.Errorf("invalid swept_index: %s exceeds global_index %s",
			gs.SweptIndex, gs.GlobalState.GlobalIndex)
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
