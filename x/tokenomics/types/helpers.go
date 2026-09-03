package types

import (
	"fmt"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// ----- Params -----

const (
	// DefaultDailySamples is how many oracle readings per UTC day count as tier
	// samples. The rule is ANY-of-N: if one of the day's samples clears both the
	// price and volume thresholds, the day counts towards the streak.
	DefaultDailySamples = 3

	// MaxDailySamples caps what governance may set DailySamples to.
	//
	// The cap exists because the parameter is one-directional: under ANY-of-N,
	// every extra sample is another chance for the threshold to be met, so
	// raising it can only make the release easier to trigger. Left unbounded, a
	// proposal setting it to a few thousand would turn "the price held on three
	// random checks" into "the price touched the threshold at least once today"
	// without visibly changing any threshold. 24 keeps it at most hourly.
	MaxDailySamples = 24
)

// DefaultParams returns the genesis tokenomics parameters.
// Pool layout, as fractions of the 10 trillion ATOS supply. Each pool is backed
// 100:1 by ERC20 ATOS on Ethereum, and the three add up to the whole supply:
//
//	miner      10,000 billion ATOS  (10%)  <-  100 billion ERC20
//	project    87,000 billion ATOS  (87%)  <-  870 billion ERC20
//	migration   3,000 billion ATOS   (3%)  <-   30 billion ERC20
//	                                            ------------------
//	                    10 trillion ATOS         1,000 billion ERC20
//
// The miner pool no longer pays block rewards — those are ATOX now. It holds the
// ATOS that backs ATOX one-for-one, and tier releases move it into the x/atox
// conversion pool as the matching ERC20 lands in the Ethereum bridge vault.
func DefaultParams() Params {
	minerPool := math.NewIntWithDecimal(1, 30)     // 10,000 billion ATOS
	projectPool := math.NewIntWithDecimal(87, 29)  // 87,000 billion ATOS
	migrationPool := math.NewIntWithDecimal(3, 29) // 3,000 billion ATOS
	// ATOX per block. Kept at the value calibrated for 5s blocks: over two
	// halvings of 25,228,800 blocks (4 years each) this emits the full 1 trillion
	// ATOX cap, matching the miner pool exactly.
	blockReward := math.NewIntWithDecimal(19819, 18)

	return Params{
		MinerPoolTotal:     minerPool,
		ProjectPoolTotal:   projectPool,
		MigrationPoolTotal: migrationPool,

		HalvingIntervalBlocks: 25_228_800,
		InitialBlockReward:    blockReward,

		PriceBase:               math.LegacyNewDecWithPrec(15, 2),
		VolumeBase:              math.LegacyNewDec(150_000),
		TierMultiplier:          math.LegacyNewDecWithPrec(11, 1),
		ConsecutiveDaysRequired: 30,
		ReleasePercentageBps:    500,
		MinerReleaseShareBps:    5000,
		ProjectReleaseShareBps:  5000,

		ProjectTreasuryAddress: "",
		// Minimum spacing between two tier samples, not an evaluation period.
		// one_day / DefaultDailySamples = 17280 / 3, so the day's three samples
		// cannot land closer together than 8h.
		PriceCheckEpochBlocks:  17_280 / DefaultDailySamples,
		DailySamples:           DefaultDailySamples,

		MigrationMerkleRoot:       "",
		MigrationClaimEndTimeUnix: 0, // 0 = no deadline

		// 100 million ATOS. Backed 100:1 by 1 million ERC20 ATOS on Ethereum,
		// which is the figure the self-stake requirement was specified in.
		ValidatorMinSelfDelegation: math.NewIntWithDecimal(1, 26),
	}
}

// Validate enforces invariants on tokenomics params.
func (p Params) Validate() error {
	if p.MinerPoolTotal.IsNil() || p.MinerPoolTotal.IsNegative() {
		return fmt.Errorf("miner pool total cannot be negative")
	}
	if p.ProjectPoolTotal.IsNil() || p.ProjectPoolTotal.IsNegative() {
		return fmt.Errorf("project pool total cannot be negative")
	}
	if p.MigrationPoolTotal.IsNil() || p.MigrationPoolTotal.IsNegative() {
		return fmt.Errorf("migration pool total cannot be negative")
	}
	if p.HalvingIntervalBlocks <= 0 {
		return fmt.Errorf("halving interval must be positive")
	}
	if p.InitialBlockReward.IsNil() || p.InitialBlockReward.IsNegative() || p.InitialBlockReward.IsZero() {
		return fmt.Errorf("initial block reward must be positive")
	}
	if p.PriceBase.IsNil() || p.PriceBase.IsNegative() || p.PriceBase.IsZero() {
		return fmt.Errorf("price base must be positive")
	}
	if p.VolumeBase.IsNil() || p.VolumeBase.IsNegative() || p.VolumeBase.IsZero() {
		return fmt.Errorf("volume base must be positive")
	}
	if p.TierMultiplier.IsNil() || p.TierMultiplier.LTE(math.LegacyOneDec()) {
		return fmt.Errorf("tier multiplier must be greater than 1")
	}
	if p.ConsecutiveDaysRequired == 0 {
		return fmt.Errorf("consecutive days required must be positive")
	}
	if p.ReleasePercentageBps == 0 || p.ReleasePercentageBps > 10000 {
		return fmt.Errorf("release percentage bps must be 1-10000")
	}
	if p.MinerReleaseShareBps+p.ProjectReleaseShareBps != 10000 {
		return fmt.Errorf("miner + project release share bps must equal 10000")
	}
	if p.ProjectTreasuryAddress != "" {
		if _, err := sdk.AccAddressFromBech32(p.ProjectTreasuryAddress); err != nil {
			return fmt.Errorf("invalid project treasury address: %w", err)
		}
	}
	if p.DailySamples <= 0 {
		return fmt.Errorf("daily_samples must be positive")
	}
	// More samples is strictly more permissive under ANY-of-N semantics (each
	// one is another chance to clear the bar), so an unbounded value would
	// quietly turn the rule into "the price touched the threshold once today".
	if p.DailySamples > MaxDailySamples {
		return fmt.Errorf("daily_samples must be <= %d, got %d", MaxDailySamples, p.DailySamples)
	}
	if p.PriceCheckEpochBlocks <= 0 {
		return fmt.Errorf("price check epoch blocks must be positive")
	}
	if p.MigrationClaimEndTimeUnix < 0 {
		return fmt.Errorf("migration claim end time cannot be negative")
	}
	if p.ValidatorMinSelfDelegation.IsNil() || p.ValidatorMinSelfDelegation.IsNegative() {
		return fmt.Errorf("validator min self delegation cannot be negative")
	}
	return nil
}

// ----- State constructors -----

// DefaultReleaseState returns the genesis release state machine.
func DefaultReleaseState() ReleaseState {
	return ReleaseState{
		CurrentTier:          0,
		ConsecutiveDays:      0,
		LastCheckBlock:       0,
		LastCheckTimeUnix:    0,
		TotalMinerReleased:   math.ZeroInt(),
		TotalProjectReleased: math.ZeroInt(),
	}
}

// DefaultBlockRewardState returns the genesis block-reward bookkeeping state.
func DefaultBlockRewardState() BlockRewardState {
	return BlockRewardState{
		TotalDistributed: math.ZeroInt(),
		CurrentPeriod:    0,
	}
}

// ----- Genesis -----

// DefaultGenesisState returns a fresh tokenomics GenesisState.
func DefaultGenesisState() *GenesisState {
	return &GenesisState{
		Params:           DefaultParams(),
		ReleaseState:     DefaultReleaseState(),
		BlockRewardState: DefaultBlockRewardState(),
		ProjectClaimable: math.ZeroInt(),
	}
}

// Validate enforces invariants on the tokenomics genesis state.
//
// Audit Recommendation-1 (round2): the pre-fix version validated only Params and
// accepted anything for the rest, so a malformed or hostile genesis could ship
// negative running totals or nil math.Int fields that then panic on arithmetic —
// corrupting tokenomics state before the chain produced its first block.
func (gs GenesisState) Validate() error {
	if err := gs.Params.Validate(); err != nil {
		return fmt.Errorf("invalid tokenomics params: %w", err)
	}
	if err := validateReleaseState(gs.ReleaseState); err != nil {
		return fmt.Errorf("invalid release_state: %w", err)
	}
	if err := validateBlockRewardState(gs.BlockRewardState); err != nil {
		return fmt.Errorf("invalid block_reward_state: %w", err)
	}
	if gs.ProjectClaimable.IsNil() {
		return fmt.Errorf("invalid project_claimable: nil")
	}
	if gs.ProjectClaimable.IsNegative() {
		return fmt.Errorf("invalid project_claimable: must not be negative, got %s", gs.ProjectClaimable)
	}
	return nil
}

// validateReleaseState rejects nil math.Int fields and negative totals.
// CurrentTier / ConsecutiveDays / block / time-unix fields are uint and
// int64; zero is the legitimate genesis default for all of them.
func validateReleaseState(s ReleaseState) error {
	checks := []struct {
		name string
		v    math.Int
	}{
		{"total_miner_released", s.TotalMinerReleased},
		{"total_project_released", s.TotalProjectReleased},
	}
	for _, c := range checks {
		if c.v.IsNil() {
			return fmt.Errorf("%s: nil", c.name)
		}
		if c.v.IsNegative() {
			return fmt.Errorf("%s: must not be negative, got %s", c.name, c.v)
		}
	}
	if s.LastCheckBlock < 0 {
		return fmt.Errorf("last_check_block: must not be negative, got %d", s.LastCheckBlock)
	}
	if s.LastCheckTimeUnix < 0 {
		return fmt.Errorf("last_check_time_unix: must not be negative, got %d", s.LastCheckTimeUnix)
	}
	return nil
}

func validateBlockRewardState(s BlockRewardState) error {
	if s.TotalDistributed.IsNil() {
		return fmt.Errorf("total_distributed: nil")
	}
	if s.TotalDistributed.IsNegative() {
		return fmt.Errorf("total_distributed: must not be negative, got %s", s.TotalDistributed)
	}
	return nil
}

// ----- Msg ValidateBasic -----

func (msg MsgClaimProjectTreasuryReward) ValidateBasic() error {
	_, err := sdk.AccAddressFromBech32(msg.Authority)
	return err
}

func (msg MsgClaimMigrationTokens) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(msg.Claimer); err != nil {
		return err
	}
	if msg.Amount.IsNil() || msg.Amount.IsNegative() || msg.Amount.IsZero() {
		return ErrNothingToClaim
	}
	if len(msg.MerkleProof) == 0 {
		return ErrInvalidMerkleProof
	}
	return nil
}

func (msg MsgUpdateParams) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(msg.Authority); err != nil {
		return err
	}
	return msg.Params.Validate()
}
