package v20_4

// UpgradeName is the on-chain plan name for the v20.4 upgrade.
//
// v20.4 is the response to the 2026-10-26 bridge incident. The code changes
// ride along with the binary; this handler covers the three things that need
// state touched rather than logic changed:
//
//  1. Rate-limit parameters retuned from observed traffic, plus the four
//     fields added in this release, which would otherwise come up as zero.
//  2. The 1,000,000 ATOX a user sent to the ATOX contract's own bech32
//     address, which nobody holds the key to.
//  3. Clawback of the 44,000,000 ATOS that the replay over-issued and that is
//     still on this chain.
//
// Items 2 and 3 are deliberately non-fatal. See the handler.
const UpgradeName = "v20.4"

// ---------- 2: stuck ATOX ----------

const (
	// StuckAtoxHolder is the bech32 form of the ATOX ERC20 contract's own
	// address, 0xc2b61c3B0Ae42B2E03A652C39b43aAd679Ff7f23. A user sent
	// 1,000,000 ATOX here instead of to their own account.
	//
	// It is a BaseAccount, so no key exists for it and the contract has no
	// withdraw path. Without this handler the funds stay there permanently.
	StuckAtoxHolder = "atoshi1c2mpcwc2us4juqax2tpeksa26eul7lerxfvmdn"

	// StuckAtoxDenom is ATOX's base denom.
	StuckAtoxDenom = "aatox"
)

// StuckAtoxRecipient receives the recovered ATOX.
//
// Left empty deliberately. Fill it in with the address operations confirms,
// in the same change that sets the upgrade height, so that one review covers
// both. An empty value skips the migration with a loud log rather than
// sending a million ATOX somewhere nobody chose.
const StuckAtoxRecipient = ""

// ---------- 3: clawback ----------

const (
	// ClawbackTarget received 44,000,000 ATOS it was never owed: eleven extra
	// deliveries of one genuine Hyperlane message, replayed through mailboxes
	// the attacker created (message id
	// 0x337c68276a132d52984f65e70e47930ca183d10999bd6feea603bfbc8e72558c,
	// delivered 12 times where it should have arrived once).
	//
	// Four and a half hours later, and 45 minutes before our forensic
	// snapshot at height 520359, the address delegated exactly 44,000,000
	// ATOS across three transactions at heights 523506, 523548 and 523568.
	// It holds other balances that predate the incident and are not touched.
	ClawbackTarget = "atoshi17q4u05f7pf0ztk5t8z2gs7yaegwz3pnkhj03d5"

	// ClawbackAtos is the over-issued amount, in whole ATOS. 44,000,000 ATOS
	// = 440,000 ERC20 at the peg. Reconciled against the on-chain bridge_in
	// events: 11 duplicate deliveries of 4,000,000 ATOS each.
	ClawbackAtos = 44_000_000
)
