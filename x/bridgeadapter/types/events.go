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
)
