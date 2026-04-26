package types

import (
	"fmt"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// ----- Params -----

// DefaultParams returns the genesis tokenomics parameters.
func DefaultParams() Params {
	minerPool := math.NewIntWithDecimal(1, 30)
	projectPool := math.NewIntWithDecimal(89, 29)
	migrationPool := math.NewIntWithDecimal(1, 29)
	blockReward := math.NewIntWithDecimal(19819, 18)

	return Params{
		MinerPoolTotal:     minerPool,
		ProjectPoolTotal:   projectPool,
		MigrationPoolTotal: migrationPool,

		ImmediateRewardBps: 2000,
		LockedRewardBps:    8000,

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
		PriceCheckEpochBlocks:  17_280,

		MigrationMerkleRoot:       "",
		MigrationClaimEndTimeUnix: 0, // 0 = no deadline
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
	if p.ImmediateRewardBps+p.LockedRewardBps != 10000 {
		return fmt.Errorf("immediate + locked reward bps must equal 10000, got %d", p.ImmediateRewardBps+p.LockedRewardBps)
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
	if p.PriceCheckEpochBlocks <= 0 {
		return fmt.Errorf("price check epoch blocks must be positive")
	}
	if p.MigrationClaimEndTimeUnix < 0 {
		return fmt.Errorf("migration claim end time cannot be negative")
	}
	return nil
}

// ----- State constructors -----

// NewMinerLockedBalance returns a zeroed miner locked balance.
func NewMinerLockedBalance(valAddr string) MinerLockedBalance {
	return MinerLockedBalance{
		ValidatorAddress:  valAddr,
		LockedAccrued:     math.ZeroInt(),
		LockedClaimable:   math.ZeroInt(),
		LockedClaimed:     math.ZeroInt(),
		ImmediateReceived: math.ZeroInt(),
	}
}

// DefaultReleaseState returns the genesis release state machine.
func DefaultReleaseState() ReleaseState {
	return ReleaseState{
		CurrentTier:               0,
		ConsecutiveDays:           0,
		LastCheckBlock:            0,
		LastCheckTimeUnix:         0,
		TotalMinerReleased:        math.ZeroInt(),
		TotalProjectReleased:      math.ZeroInt(),
		TotalImmediateDistributed: math.ZeroInt(),
		TotalMinerLocked:          math.ZeroInt(),
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
		Params:              DefaultParams(),
		ReleaseState:        DefaultReleaseState(),
		BlockRewardState:    DefaultBlockRewardState(),
		MinerLockedBalances: []MinerLockedBalance{},
		ProjectClaimable:    math.ZeroInt(),
	}
}

// Validate enforces invariants on the tokenomics genesis state.
func (gs GenesisState) Validate() error {
	if err := gs.Params.Validate(); err != nil {
		return fmt.Errorf("invalid tokenomics params: %w", err)
	}
	return nil
}

// ----- Msg ValidateBasic -----

func (msg MsgClaimMinerLockedReward) ValidateBasic() error {
	_, err := sdk.ValAddressFromBech32(msg.ValidatorAddress)
	return err
}

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
