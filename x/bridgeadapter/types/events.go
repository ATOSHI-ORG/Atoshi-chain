package types

const (
	EventTypeReceiptApplied = "bridge_receipt_applied"
	EventTypeUpdateParams   = "bridge_adapter_update_params"
	// EventTypeEmergencyHalt fires when a guardian closes the bridge without a
	// governance vote. Alerting should treat it as page-worthy: by the time it
	// is emitted somebody has already decided something is badly wrong.
	EventTypeEmergencyHalt = "bridge_emergency_halt"
	EventTypeBridgeOut      = "bridge_out"
	EventTypeBridgeIn       = "bridge_in"
	// EventTypeInboundCapped fires when the daily inbound circuit breaker
	// rejects a bridge-in. The message is NOT lost -- Hyperlane retries it.
	EventTypeInboundCapped = "bridge_in_capped"
	// EventTypeTierDispatchUnderfunded fires when this module's account cannot
	// cover the interchain gas for a tier release. Page-worthy: tier releases
	// run from EndBlock, so without this the release stops silently -- no failed
	// transaction, no error in any query, just a number that stops moving.
	EventTypeTierDispatchUnderfunded = "tier_dispatch_underfunded"
	// EventTypeInboundCeilingStranding fires when UpdateParams accepts a
	// parameter set that still leaves max_transfer_in above the per-address
	// inbound allowance. It is only ever emitted on the repair path -- a
	// proposal that moves the gap in the right direction without closing it --
	// so it means "better, still wrong", and the next proposal has to finish
	// the job. See the UpdateParams handler for why this is accepted at all.
	EventTypeInboundCeilingStranding = "inbound_ceiling_stranding"

	AttributeKeyMessageID         = "message_id"
	AttributeKeyBridgeDelta       = "bridge_delta_erc20"
	AttributeKeyProjectDelta      = "project_delta_erc20"
	AttributeKeyAtosToPool        = "atos_to_exchange_pool"
	AttributeKeyAtosToProject     = "atos_authorized_to_project"
	AttributeKeyCumulativeBridge  = "cumulative_bridge_erc20"
	AttributeKeyCumulativeProject = "cumulative_project_erc20"
	AttributeKeySender            = "sender"
	AttributeKeyGuardian          = "guardian"
	AttributeKeyReason            = "reason"
	AttributeKeyRecipient         = "recipient"
	AttributeKeyAmount            = "amount"
	AttributeKeyErc20Amount       = "erc20_amount"
	AttributeKeyUsedInbound       = "used_inbound"
	AttributeKeyInboundCap        = "inbound_cap"
	AttributeKeyAddress           = "address"
	AttributeKeyBalance           = "balance"
	AttributeKeyRequired          = "required"
	AttributeKeyMaxTransferIn     = "max_transfer_in"
	AttributeKeyInboundPerAddress = "inbound_per_address"
	AttributeKeyPreviousGap       = "previous_gap"
	AttributeKeyGap               = "gap"
)
