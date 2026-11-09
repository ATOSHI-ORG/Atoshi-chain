package v21_2

import "github.com/atoshi-chain/atoshi/v20/utils"

// UpgradeName is the on-chain plan name for the v21.2 upgrade.
//
// v21.2, not v20.5. The previous release was v21.1; the plan name, the git tag
// (v21.2.0) and this directory all carry the same number deliberately. An
// earlier release was named from a separate "on-chain sequence" that does not
// exist, which is how v20.4 came to sit below a v21.1 tag in semver order.
//
// This handler covers what needs state touched rather than logic changed:
//
//  1. destination_gas written explicitly instead of resolved from a fallback.
//  2. The slash_pool module account instantiated at a known height.
//  3. TotalBridgedIn corrected for the clawback v20.4 already performed.
//  4. The 2026-10-18 slashing compensation, minted and paid. Mainnet only.
const UpgradeName = "v21.2"

// MainnetChainID gates the compensation step.
//
// The compensation is a remediation for one incident on one chain, and its
// recipient list is 1,843 mainnet addresses. Running it anywhere else would
// mint tens of millions of ATOS to addresses nobody on that chain holds a key
// for, which is worse than useless on a test network: it breaks the supply
// reconciliation that the network exists to rehearse.
//
// Gated on the chain id rather than compiled out, so a test network runs the
// same binary mainnet will. A build flag or a second binary would mean the
// thing being tested is not the thing being shipped.
//
// Composed from utils rather than written out, so this cannot drift from the
// chain id the rest of the binary recognises. The "-1" is the EIP-155 epoch
// suffix every Ethermint chain id carries; utils holds the prefix alone.
//
// NOTE: a test network has to be on a different chain id for this to protect
// it -- utils.TestnetChainID ("atoshi_88288") is the one to use. A deployment
// that reuses the mainnet chain id would pay the list.
var MainnetChainID = utils.MainnetChainID + "-1"

// ShouldCompensate reports whether the compensation step runs on this chain.
//
// Split out from the handler so the decision is testable without standing up a
// chain. The unit-test network reports the mainnet chain id, so a test that
// asked it instead would be asserting the harness rather than this rule.
func ShouldCompensate(chainID string) bool {
	return chainID == MainnetChainID
}

// ---------- 1: interchain gas ----------

// DestinationGas is written over whatever the stored params hold.
//
// Params.EffectiveDestinationGas already substitutes this figure for an unset
// field, so the chain works without this step. It is written anyway: a value
// present in state is one an operator can read back and a proposal can reason
// about, where a fallback is invisible until somebody reads the Go source.
const DestinationGas = 200_000

// ---------- 3: TotalBridgedIn ----------

// ClawbackAtos is subtracted from ReceiptState.TotalBridgedIn.
//
// v20.4 recovered 44,000,000 ATOS that a replayed message over-issued and
// returned it to the migration pool, but TotalBridgedIn is append-only and no
// handler decremented it. The counter has therefore overstated net bridge-in by
// 44,000,000 ATOS -- 440,000 ERC20 at the peg -- ever since.
//
// Nothing on chain reads the counter; it is statistics, and the solvency panel
// was moved onto the pool-balance derivation instead. This corrects it so the
// counter can be trusted again, and establishes the rule the omission came
// from: anything returned to the migration pool decrements TotalBridgedIn in
// the same handler that returns it.
const ClawbackAtos = 44_000_000

// ---------- 4: slashing compensation ----------

// OwnSelfDelegations are our own two validator self-delegations in the
// compensation list, marked in its last column.
//
// They are in the list because the slash hit them exactly as it hit everyone
// else. Whether we pay ourselves back is a policy question, not a technical
// one, which is what CompensateOwnSelfDelegations decides. Matched by address
// rather than by the comment column so the decision does not rest on a string
// in a spreadsheet.
var OwnSelfDelegations = map[string]struct{}{
	"atoshi1yc8z9fua5rwrcysf0x3gs43yr9wfypwxealqq4": {}, // val-4, 1,000,000 ATOS
	"atoshi15h9u90s9ay8du5tk4qza953a289r7sy3kasv5c": {}, // val-5, 1,000,000 ATOS
}

// CompensateOwnSelfDelegations decides whether the two rows above are paid.
//
//	true   1,843 recipients, 41,965,283.235217 ATOS minted
//	false  1,841 recipients, 39,965,283.235217 ATOS minted
//
// Set to pay everyone, including us. Flip this one constant to exclude
// ourselves; nothing else changes, and the totals are asserted either way.
const CompensateOwnSelfDelegations = true
