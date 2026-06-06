package types

const (
	EventTypeEnergyConsumed   = "energy_consumed"
	EventTypeEnergyDelegated  = "energy_delegated"
	EventTypeEnergyUndelegated = "energy_undelegated"
	EventTypeEnergyExpired    = "energy_delegation_expired"
	EventTypeUpdateParams     = "update_params"

	AttributeKeyAddress      = "address"
	AttributeKeyDelegator    = "delegator"
	AttributeKeyDelegatee    = "delegatee"
	AttributeKeyDelegationID = "delegation_id"
	AttributeKeyAmount       = "amount"
	AttributeKeyLockedATOS   = "locked_atos"
	AttributeKeyEnergyUsed   = "energy_used"
	AttributeKeyShortfallGas = "shortfall_gas"
	AttributeKeyExpiresAt    = "expires_at"
)