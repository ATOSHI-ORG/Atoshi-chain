package types

const (
	EventTypeMintAtox     = "atox_mint"
	EventTypePoolRelease  = "atox_pool_release"
	EventTypeSettle       = "atox_settle"
	EventTypeTransferFee  = "atox_transfer_fee"
	EventTypeClaim        = "atox_claim"
	EventTypeUpdateParams = "atox_update_params"

	AttributeKeyAddress        = "address"
	AttributeKeyAmount         = "amount"
	AttributeKeyGlobalIndex    = "global_index"
	AttributeKeyIndexDelta     = "index_delta"
	AttributeKeyPending        = "pending"
	AttributeKeyAtoxBalance    = "atox_balance"
	AttributeKeyTrigger        = "trigger"
	AttributeKeyTransferAmount = "transfer_amount"

	// Settlement trigger labels, emitted so an indexer can tell an automatic
	// sweep credit apart from one caused by the holder moving ATOX.
	TriggerTransfer = "transfer"
	TriggerMint     = "mint"
	TriggerClaim    = "claim"
	TriggerSweep    = "sweep"
)
