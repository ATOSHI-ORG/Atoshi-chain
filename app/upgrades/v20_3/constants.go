package v20_3

// UpgradeName is the on-chain plan name for the v20.3 upgrade.
//
// v20.3 introduces two governance-tunable delegation-duration params:
//
//   - default_delegation_duration_seconds (field 11): substitute value
//     when MsgDelegateEnergy is submitted with duration_seconds == 0.
//   - max_delegation_duration_seconds   (field 12): hard cap; msgs
//     exceeding this value are rejected.
//
// Handler action: set both params to 86400 (24h) explicitly on live
// chains. The msg_server fallback also picks up the compiled 24h
// constant when the field is zero (pre-upgrade state compat), so the
// upgrade could technically be a no-op — but writing the values into
// on-chain state makes the intent explicit and removes any reliance
// on the code-side fallback going forward.
const UpgradeName = "v20.3"
