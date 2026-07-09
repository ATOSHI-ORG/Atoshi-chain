package types

// ConsumeResultJSON is the on-disk JSON shape of the keeper's
// ConsumeResult used by the audit Issue-1 pending-reservation marker.
//
// We deliberately do NOT expose this as a proto message: the marker is
// transient (lives at most one block, written by AnteHandler, deleted
// by PostHandler-on-success or EndBlocker-on-failure), so a proto
// schema commitment isn't warranted. JSON also keeps the keeper's
// ConsumeResult type free of `json:` struct tags it doesn't otherwise
// need.
//
// Field set MUST stay in sync with keeper.ConsumeResult /
// keeper.DelegationConsumption; the shuttle helpers in
// keeper/pending_reservation.go bridge the two.
type ConsumeResultJSON struct {
	EnergyDeducted         uint64                      `json:"energy_deducted"`
	OwnDeducted            uint64                      `json:"own_deducted"`
	DelegatedDeducted      uint64                      `json:"delegated_deducted"`
	DeployEnergyUsed       uint64                      `json:"deploy_energy_used"`
	ShortfallGas           uint64                      `json:"shortfall_gas"`
	Free                   bool                        `json:"free"`
	DelegationConsumptions []DelegationConsumptionJSON `json:"delegation_consumptions,omitempty"`
}

type DelegationConsumptionJSON struct {
	DelegationID uint64 `json:"id"`
	Amount       uint64 `json:"amount"`
}
