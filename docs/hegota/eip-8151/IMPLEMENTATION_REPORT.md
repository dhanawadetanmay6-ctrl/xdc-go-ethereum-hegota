# EIP-8151 Implementation Report — Account Code Restricted ecRecover

**Date**: 2026-10-31
**Status**: Complete
**Branch**: `feat/eip-8151-account-code-ecrecover`
**Base**: `48a77271314293cdf057bc05d2c1624c3645a4d0`
**Specification**: https://eips.ethereum.org/EIPS/eip-8151 (Draft)
**Pinned spec SHA256**: `191a5a2d742c38380f815d251f78bf5e315bba23e2dafc239f7197572ac748ea`

The pinned snapshot at `/home/tanmay/xdc-fix-work/eip-8151-spec-snapshot.md`
was compared byte-for-byte with the current `ethereum/EIPs` master source during
final verification; they are identical.

## 1. Implemented scope

The implementation is confined to the EIP-8151 worktree and adds:

- `core/vm/contracts.go`
  - Extracts the unchanged ECDSA recovery body into `ecrecoverRun`; the legacy
    `ecrecover` precompile retains its original behavior.
  - Adds the optional `StatefulPrecompiledContract` interface and invokes it
    from `RunPrecompiledContract` after the existing base-gas charge and
    Amsterdam precompile touch.
  - Adds `ecrecover8151`, which performs the restricted raw-account-code check.
  - Adds `PrecompiledContractsBogota` and `PrecompiledAddressesBogota`, replacing
    only address `0x01` relative to Osaka, and wires both Bogota dispatch paths.
- `core/vm/eip8151_test.go`
  - Six top-level test functions with 28 named subtests covering behavior,
    exact gas, OOG, BAL, reverts, call results, and activation.
- `core/eip8151_test.go`
  - One block-level test function with five named subtests covering EIP-7928
    evidence through `GenerateChainWithGenesis`.

No dependencies, generated files, chain configuration activation values, or
unrelated precompiles were changed. EIP-8141, EIP-8250, EIP-8272, and EIP-7906
code is not part of this minimal dependency base.

## 2. Consensus behavior

`RunPrecompiledContract` charges `params.EcrecoverGas` (3000) before invoking
`ecrecover8151.RunStateful`. Recovery failure returns nil empty output without
an additional charge or recovered-account access.

After successful recovery, `RunStateful`:

1. Derives the recovered address from the existing 32-byte recovery result.
2. Reads `AddressInAccessList(recovered)` before any account read.
3. Charges either `ColdAccountAccessAmsterdam` or
   `WarmAccountAccessAmsterdam`, plus one additional
   `WarmAccountAccessAmsterdam` code-query fee. Totals are 6100 cold and 3200
   warm, including the base charge.
4. Returns `ErrOutOfGas` without warming, reading, or BAL-recording the
   recovered account if this second charge fails.
5. Adds the recovered address to the access list only after payment.
6. Calls `StateDB.GetCode(recovered)`, which reads raw stored code and triggers
   the existing EIP-7928 account-read recording. It does not use the EVM's
   delegation-resolving `resolveCode` path.
7. Returns the recovered address only for empty code or
   `types.ParseDelegation(code)` success. `ParseDelegation` requires exactly
   23 bytes and the `0xef0100` prefix. Every other code value returns nil empty
   output with no execution error.

The existing StateDB journal supplies the required warm-set rollback on a
reverted scope. EIP-7928 construction-BAL account reads are outside that
journal, so rejected checks and outer reverts retain the read with empty change
lists when no other mutation occurred.

## 3. Requirement verification matrix

| Requirement | Status | Evidence |
|---|---|---|
| R1a — charge 3000 base gas; base OOG halts | Verified | Existing runner charge; `TestEIP8151GasCostsAndOutOfGas` |
| R1b — ECDSA recovery unchanged | Verified | Byte-preserving extraction; legacy `TestPrecompiledEcrecover` and full `core/vm` suite |
| R1c — recovery failure returns empty success, no extra gas/access | Verified | `TestEIP8151RecoveryFailure`; block-level recovery-failure subtest |
| R2a — empty code permitted | Verified | Absent and existing-empty-code subtests |
| R2b — exact EIP-7702 indicator permitted | Verified | Delegation-indicator subtests at VM and block level |
| R2c — all other non-empty code rejected | Verified | Arbitrary code, wrong prefix, 22/24-byte, and prefix-only subtests |
| R2d — raw code only; never follow delegation | Verified | Direct `GetCode`; delegate-with-code test proves target is neither needed nor BAL-recorded |
| R2e — absent account treated as empty code | Verified | Absent-account subtest, including empty BAL changes |
| R3a/R3b — rejection is success with zero-length returndata | Verified | Nil return assertions and actual STATICCALL success/RETURNDATASIZE checks |
| R3c — code-check OOG halts | Verified | 6099 cold, 3199 warm, and fixed-3000 STATICCALL cases |
| R4a/R4b — pre-read cold/warm price, 6100/3200 totals | Verified | Cold, warm, and exact-total gas tests |
| R4c — charge before warming/recording/reading | Verified | OOG tests assert no warm access and no recovered-account BAL entry |
| R5a — BAL read only after payment | Verified | VM and block-level OOG absence tests |
| R5b — BAL records recovered raw-code account, not delegate | Verified | VM and block-level delegation BAL tests |
| R5c/R5f — rejected and unchanged accounts retain empty reads | Verified | VM restriction table and block-level rejected-code test |
| R5d — outer revert retains BAL read | Verified | VM snapshot/revert test and block-level outer-revert test |
| R5e — reverted scope restores entry warm set | Verified | Cold restored to cold; pre-existing warm access survives |
| R6a/R6b — Bogota-gated; pre-Bogota unchanged | Verified | Activation test, legacy vectors, and full precompile suite |
| R6c — no unrelated changes | Verified | Staged diff review; Bogota/Osaka map equivalence for every non-0x01 precompile |

## 4. Verification and baseline failures

All new EIP-8151 tests, the complete `core/vm` package, `go build ./...`,
`go vet ./core/vm ./core`, gofmt, and Git diff checks passed.

Full-core and adjacent-package sweeps were run against both this worktree and
the clean base checkout. Failure-name sets are identical, with zero new
failures. The remaining failures are pre-existing and are itemized in
`TEST_EVIDENCE.md`; this report does not claim those suites are all green.
Three known panic-aborting core tests were excluded from both full-core runs
to obtain complete comparable failure lists.

Exact commands, output logs, failure lists, and checkpoint verification are in
`docs/hegota/eip-8151/TEST_EVIDENCE.md` and
`/home/tanmay/xdc-fix-work/backups-eip-8151/`.

## 5. Activation and workspace boundary

EIP-8151 is wired to Bogota only. No XDC mainnet or Apothem activation was
added. The stateless `Run` method remains for `PrecompiledContract` interface
compatibility; production EVM CALL-family paths supply a StateDB and route the
Bogota precompile through `RunStateful`.

No commit, push, merge, reset, or clean operation was performed. The final
cumulative patch and SHA256 sidecar are:

- `/home/tanmay/xdc-fix-work/backups-eip-8151/FINAL-eip-8151.patch`
- `/home/tanmay/xdc-fix-work/backups-eip-8151/FINAL-eip-8151.sha256`

The FINAL hash is intentionally kept in the sidecar rather than embedded in
this report, so the documented tree and checkpoint remain byte-identical.
