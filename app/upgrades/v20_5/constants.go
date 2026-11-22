package v20_5

// UpgradeName is the on-chain plan name for the v20.5 upgrade.
//
// v20.5 is the response to the 2026-11-22 precompile mint and to the audit
// that followed it. Almost all of it is code that rides along with the binary:
//
//   - precompile calls journal their rollback point before flushing EVM state
//     into the cache context, and the flush is atomic;
//   - the precompile call limit is checked before any work, without wrapping;
//   - the EVM may not change a module account's balance;
//   - a contract holding staking positions may not self-destruct;
//   - stateful precompiles reject value sent to non-payable methods;
//   - precompile methods act only for their direct caller (msg.sender), and
//     allowance methods can only be called directly by the signer;
//   - the validator self-stake floor is enforced in the staking msg server;
//   - IBC callbacks are gas-metered and auto-registration of dynamic
//     precompiles is capped;
//   - energy is only ever taken from the fee payer, subsidized txs are capped,
//     and failed txs no longer get their energy back.
//
// The handler covers the one thing that needs state touched: it revokes every
// vesting authorization that could have been planted through the old vesting
// precompile approve, which took its granter from tx.origin.
const UpgradeName = "v20.5"
