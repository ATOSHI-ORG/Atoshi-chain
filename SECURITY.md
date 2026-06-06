# Security

This document is the Atoshi chain vulnerability disclosure policy. It outlines how
to report security issues responsibly and how we handle them once received.

## Reporting a vulnerability

**Please do NOT open public GitHub issues for security vulnerabilities.**

Report all security issues privately to **info@atoshi.org** (or to the
project lead via a direct, encrypted channel if you have one). We commit to
acknowledging receipt within 48 hours.

We ask whitehat researchers to:

- Use private email — not GitHub, Discord, Telegram, X/Twitter, or any other
  public channel.
- Avoid privacy violations, service degradation, data destruction, or any
  action that disrupts production systems.
- Keep the issue confidential between you and the Atoshi engineering team until
  it has been resolved and publicly disclosed.
- Refrain from posting personally identifiable information.

If you follow these guidelines we commit to:

- Not pursue or support any legal action related to your research on the issue.
- Work with you to understand, reproduce, fix, and ultimately disclose the
  vulnerability in a timely fashion.
- Credit you in the public disclosure (with your permission).

## Disclosure process

1. **Receipt and acknowledgement (within 48h).** A team member confirms
   receipt of the report and begins triage.
2. **Verification and severity scoring.** Two engineers reproduce the issue and
   score it using [CVSS v4](https://nvd.nist.gov/vuln-metrics/cvss).
3. **Patch development.** A fix is developed in a private fork. The reporter is
   kept in the loop and may be asked to validate the patch.
4. **Coordinated release.**
   1. Patches are prepared for all supported releases of Atoshi.
   2. A patch release is cut on the affected version branches.
   3. Validators and node operators are notified privately through the operator
      channels with the new binary and any required configuration changes.
   4. After validators have had a reasonable window to upgrade (typically one
      week), a public disclosure is published in a GitHub Security Advisory.
5. **Public credit.** The reporter is credited in the advisory unless they
   request anonymity.

## Severity classification

| Severity | Definition | Target response time |
|---|---|---|
| **Critical** | Loss of funds, consensus halt, signing key compromise | 24 hours |
| **High** | State corruption, privilege escalation, DoS at chain level | 72 hours |
| **Medium** | Limited DoS, information leak with low impact | 7 days |
| **Low** | Minor issue with negligible practical impact | best-effort |

## Scope

In scope:

- `Atoshi-chain` (this repository) — the L1 Cosmos SDK chain and Ethermint EVM
  modules (`x/energy`, `x/oracle`, `x/tokenomics`, custom ante handlers).
- `atoshi-zkevm-contracts` — the L1 bridge contracts (Polygon zkEVM stack).
- `atoshi-privacy-contracts` — the L2 shield pool and energy settlement
  contracts.
- `atoshi-privacy-circuits` — the Groth16 circuits used by the privacy
  contracts.
- `atoshi-privacy-sdk` and `atoshi-chain-sdk` — official client SDKs.

Out of scope:

- Third-party wallets, explorers, infrastructure operated by community
  partners — please report those to the respective maintainers.
- Issues in upstream dependencies (Cosmos SDK, Tendermint/CometBFT,
  Ethermint, Polygon CDK) — please report directly to the upstream
  maintainers and CC us at info@atoshi.org.
- Issues that require physical access to a validator's machine.
- Issues that depend on social engineering of an operator.

## Bounty

A bug bounty program is currently being established. For now, Critical and
High severity findings will be considered for an ex-gratia reward at the
Atoshi Foundation's discretion. Bounty amounts and process will be published
here once finalized; please continue to report issues in the meantime — early
reporters will be considered as the bounty program is rolled out.

## Supported versions

We release security patches for the latest minor release of Atoshi. Operators
running older versions are strongly encouraged to upgrade. We may, at our
discretion, backport critical patches to older releases — please ask if you
need a specific version supported.

## Contact

Security reports: **info@atoshi.org**

For all other inquiries (general support, integration questions, bugs that are
not security-sensitive) please use GitHub issues or our community channels.
