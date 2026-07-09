package v20_2

// UpgradeName is the on-chain plan name for the v20.2 upgrade.
//
// v20.2 bundles the post-audit fixes. Most are pure code changes that
// take effect at the swap automatically. The upgrade handler only runs
// for state migrations that cannot be inferred from the new binary
// alone:
//
//   1. Force-disable x/inflation. The module was inherited from evmos
//      but is logically redundant in Atoshi (x/tokenomics owns ALL
//      issuance). Leaving inflation enabled would mint aatos that
//      bypasses the 10-trillion supply guard. The DefaultParams
//      change ships in the same release, but live chains were
//      initialized with EnableInflation=true at genesis and would
//      still run with inflation on after a binary swap unless the
//      handler explicitly disables it here.
//
// No store layout changes, no module account additions.
const UpgradeName = "v20.2"
