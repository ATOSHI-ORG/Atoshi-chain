# Security Audits

| Auditor | Version | Date | Report | Findings | Status |
|---|---|---|---|---|---|
| [BlockSec](https://blocksec.com) | 1.0 | 2026-07-10 | [PDF](./blocksec-atoshi-chain-v1.0-2026-07-10.pdf) | 21 security issues, 4 recommendations, 4 notes | All fixed |

---

## BlockSec v1.0 — 2026-07-10

### What was audited

Commits reviewed:

| Version | Commit |
|---|---|
| Version 1 | `67e450e56e2a638d2d3ac643d82a92003af732ee` |
| Version 2 | `7007bcddefd99f3abbecb80aba7c381fa84156f6` |

Directories in scope:

```
x/oracle/keeper/          x/oracle/types/
x/tokenomics/keeper/      x/tokenomics/types/
x/energy/keeper/          x/energy/types/
x/energy/ante/decorator.go
proto/atoshi/oracle/v1/   proto/atoshi/tokenomics/v1/   proto/atoshi/energy/v1/
```

Excluded from that review: `*.pb.go`, `*.pb.gw.go`, `*_test.go`, and
`x/energy/keeper/grpc_query.go`.

### What was NOT audited

Read this before treating the report as covering the chain as a whole. The
following did not exist, or were outside the agreed scope, when this review was
carried out:

```
x/atox/                    ATOX token: conversion index, transfer fee, burns
x/atox/wrapper/            the bank Msg server wrapper every MsgSend goes through
x/bridgeadapter/           tier-release receipts, the user asset bridge, rate limits
precompiles/atox/          0x…0809
precompiles/bridgeadapter/ 0x…0808
app/app.go                 module wiring
app/ante/                  everything except decorator.go
x/erc20/  x/evm/  types/coin.go
```

Those modules hold or move funds. A follow-up audit covering them is under way;
this page will be updated when it lands.

### Results

| Severity | Count | Status |
|---|---|---|
| High | 13 | Fixed |
| Medium | 6 | Fixed |
| Low | 2 | Fixed |
| Recommendation | 4 | Fixed |
| Note | 4 | Informational, no action required |

Every security issue and recommendation is marked `Fixed` in the report; each
finding carries its own `Status` field naming the version that fixed it.

Fixes were merged through the `fix/audit-round-2` and `fix/audit-round-3`
branches and released in `v20.3.0`.

---

## Reporting a vulnerability

Please report suspected vulnerabilities to **info@atoshi.org** rather than
opening a public issue.

Before sending a report, check this page and the repository history: several
externally reported issues have turned out to be findings already listed above
and fixed months earlier. Including the exact commit hash you reviewed lets us
answer you much faster.
