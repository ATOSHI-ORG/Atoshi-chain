package types

const (
	EventTypeReceiptApplied = "bridge_receipt_applied"
	EventTypeUpdateParams   = "bridge_adapter_update_params"

	AttributeKeyMessageID         = "message_id"
	AttributeKeyBridgeDelta       = "bridge_delta_erc20"
	AttributeKeyProjectDelta      = "project_delta_erc20"
	AttributeKeyAtosToPool        = "atos_to_exchange_pool"
	AttributeKeyAtosToProject     = "atos_authorized_to_project"
	AttributeKeyCumulativeBridge  = "cumulative_bridge_erc20"
	AttributeKeyCumulativeProject = "cumulative_project_erc20"
)
