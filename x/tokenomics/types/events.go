package types

const (
	EventTypeDistributeBlockReward = "distribute_block_reward"
	EventTypeReleaseTriggered      = "release_triggered"
	EventTypeClaimMinerReward      = "claim_miner_reward"
	EventTypeClaimProjectReward    = "claim_project_reward"
	EventTypeClaimMigration        = "claim_migration"
	EventTypeUpdateParams          = "update_tokenomics_params"

	// Daily sampling. One DailySample per reading consumed, one DailyCheck per
	// UTC day when the day settles. Both are emitted because the two questions
	// operators ask are different: "did today's readings clear the bar" needs
	// the individual samples, "how long is the streak" needs the settlement.
	EventTypeDailySample = "tier_daily_sample"
	EventTypeDailyCheck  = "tier_daily_check"

	// migration_pool 从 project_pool 补充（设计文档 1.4 的「半自动补充」）
	EventTypeMigrationPoolRefilled = "migration_pool_refilled"

	AttributeKeyValidator       = "validator"
	AttributeKeyAmount          = "amount"
	AttributeKeyTier            = "tier"
	AttributeKeyConsecutiveDays = "consecutive_days"
	AttributeKeyRecipient       = "recipient"
	AttributeKeyDay             = "day"
	AttributeKeySamples         = "samples"
	AttributeKeyQualified       = "qualified"
	AttributeKeySampleOk        = "sample_ok"
	AttributeKeyPrice           = "price"
	AttributeKeyVolume          = "volume"

	AttributeKeyRemainingAuthorisation = "remaining_authorisation"
)
