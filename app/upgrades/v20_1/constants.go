package v20_1

// UpgradeName is the on-chain plan name for the v20.1 upgrade.
//
// v20.1 is a focused bugfix release that:
//   1. Wires energy.SendRestriction into bank so every base-denom send
//      refreshes both sides' LastBalanceSnapshot.
//   2. Sweeps existing accounts once to re-snapshot stored EnergyAccount
//      entries against the current bank balance, so wallets that
//      received ATOS before the fix immediately get correct capacity.
//
// No store layout changes; no new modules registered.
const UpgradeName = "v20.1"
