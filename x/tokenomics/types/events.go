package types

const (
	EventTypeDistributeBlockReward = "distribute_block_reward"
	EventTypeReleaseTriggered      = "release_triggered"
	EventTypeClaimMinerReward      = "claim_miner_reward"
	EventTypeClaimProjectReward    = "claim_project_reward"
	EventTypeClaimMigration        = "claim_migration"
	EventTypeUpdateParams          = "update_tokenomics_params"

	AttributeKeyValidator       = "validator"
	AttributeKeyAmount          = "amount"
	AttributeKeyTier            = "tier"
	AttributeKeyConsecutiveDays = "consecutive_days"
	AttributeKeyRecipient       = "recipient"
)
