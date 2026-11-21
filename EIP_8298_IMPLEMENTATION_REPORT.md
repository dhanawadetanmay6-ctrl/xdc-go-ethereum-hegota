# EIP-8298 SETCODEFROM — Final Implementation Report

**Date:** 2026-11-03
**Worktree:** `/home/tanmay/xdc-fix-work/eip-8298-setcodefrom`
**Branch:** `feat/eip-8298-setcodefrom` (HEAD = base commit; all work uncommitted — zero commits made)
**Toolchain:** `go1.26.8 linux/amd64` (`/home/tanmay/go-sdk/go/bin`)

---

## 1. Specification snapshot

| Item | Value |
|---|---|
| EIP | 8298 — SETCODEFROM Code Reuse Instruction (Draft, Standards Track / Core) |
| Canonical URL | https://eips.ethereum.org/EIPS/eip-8298 |
| Local snapshot | `/home/tanmay/xdc-fix-work/eip-8298-spec-snapshot.md` |
| Snapshot SHA-256 | `9c05ea1f17f5726b793388e7d61e38a6a2af1adc7ff134f2c80de5b399214d13` |
| Snapshot integrity | Re-verified 2026-11-03: local file hash matches the recorded value exactly |
| Authors | Liyi Guo, Ben Adams, Carlos Perez, Nicolas Consigny |
| Created | 2026-06-11 |
| Requires | 3607, 6780, 7702, 7928, 8037, 8038 (all present in the base fork) |
| Upstream "Test Cases" | Empty (`<!-- TODO -->`) upstream — no official fixture set exists |

## 2. Upstream Git commit SHA — VERIFIED (2026-11-03, this session)

Retrieved by two independent methods, results identical:

1. **Sparse partial clone** (`git clone --filter=blob:none --no-checkout --depth 200 https://github.com/ethereum/EIPs.git`, sparse-checkout of `EIPS/eip-8298.md`).
2. **GitHub REST API** (`/repos/ethereum/EIPs/commits?path=EIPS/eip-8298.md&until=2026-11-03T23:59:59Z`).

Latest commit touching `EIPS/eip-8298.md` as of the 2026-11-03 snapshot:

```
f4a9f9655ee88ed923ceac6cb6271820477e11c5  2026-10-22T18:43:13Z
"Update EIP-8298: Add adoption guidance to Security Considerations"
```

Content proof: `git show f4a9f965…:EIPS/eip-8298.md | sha256sum`
= `9c05ea1f17f5726b793388e7d61e38a6a2af1adc7ff134f2c80de5b399214d13` — **exact match** with the pinned snapshot.

Master HEAD at verification time: `dc5d5fa688b7fe87b95a907fcc128b4fde2f4d2f` (2026-11-01T04:20:25-07:00); no commits touched `eip-8298.md` between `f4a9f965` and the snapshot date. The earlier limitation (API blocked) is resolved; the upstream SHA is now pinned and content-verified.

## 3. Integration base

| Item | Value |
|---|---|
| Base commit | `48a77271314293cdf057bc05d2c1624c3645a4d0` (pristine main checkout) |
| Base patch 1 | `backups-frame-tx-8250/final-20261029.patch` — EIP-8141 M1–M10 (frame transactions) + EIP-8250 (keyed nonces) |
| Base patch 2 | `backups-eip-8151/FINAL-eip-8151.patch` — EIP-8151 account-code-restricted ecRecover |
| Canonical base tree | `c77ff3c11c6a679781fe1dcb1bb37589ac377334` (git tree hash over the full base state) |
| Implementation tree (code+tests, pre-report) | `1102f1d0d4b594379d4cfc35b8d40f58a241edd8` (git tree hash; the final recovery patch adds this report on top — final tree hash is recorded next to the patch checksum in `backups-cross-client/`) |
| Stacked EIPs in base | EIP-8141, EIP-8250, EIP-8151. EIP-8272 and EIP-7906 are NOT in this base (no normative interaction with SETCODEFROM; BAL CodeChange extension is forward-compatible with 7906's use of the same type) |

## 4. Implemented features (22 source files, +2041/−20)

### 4.1 Opcode and registration
- `SETCODEFROM = 0xA7` — `core/vm/opcodes.go:218`, registered in both `opCodeToString` (:341) and `stringToOp` (:532).
- `enable8298()` — `core/vm/instructions_setcodefrom.go:44`; wired into `newBogotaInstructionSet()` at `core/vm/jump_table.go:149` (immediately after `enable8141`).
- Stack: pops 1 (`source`, low 160 bits), pushes `1`/`0`.

### 4.2 Source validation (`opSetCodeFrom`)
All required checks, in spec order: account exists; `codeHash != EMPTYCODEHASH`; code does not start with `0xEF` (EIP-3541/7702 exclusion); source not created in the current transaction (EIP-6780 `IsNewContract` rule); validation reads raw `StateDB.GetCode` and never resolves EIP-7702 delegation (delegation target never accessed/charged/warmed); precompile/empty accounts invalid via the empty-hash rule.

### 4.3 Execution restrictions (halts before any charge)
- Static context → exceptional halt.
- Current account is a system contract (frame caller == `params.SystemAddress`, the fork's `isSystemCall` notion) → exceptional halt (`ErrSetCodeFromSystemContract`, `core/vm/errors.go:39`).
- Both halts occur before `SETCODEFROM_SOURCE_GAS` is charged; `source` is not accessed.

### 4.4 Gas (execution gas only; state gas = 0)
Derived exclusively from fork params, never hardcoded:

| Fork param (params/protocol_params.go:123-127) | Value | Maps to |
|---|---|---|
| `ColdAccountAccessAmsterdam` | 3000 | `COLD_ACCOUNT_ACCESS` |
| `WarmAccountAccessAmsterdam` | 100 | `WARM_ACCESS` |
| `AccountWriteAmsterdam` | 8000 | `ACCOUNT_WRITE` |

Verified costs (asserted in `TestSetCodeFromGas`):

| Result | Cold source | Warm source |
|---|---:|---:|
| Invalid source | 3100 | 200 |
| Valid, same code hash | 3200 | 300 |
| Code hash updated | **11200** | **8300** |

**Deviation from the spec-text table (12200/9300) is by parameterization, not by hardcoding:** the spec table assumes `ACCOUNT_WRITE = 9000`; this fork's EIP-8038 Amsterdam schedule pins `AccountWriteAmsterdam = 8000`. The invalid (3100/200) and same-hash (3200/300) rows match the spec exactly. The original mission prompt's expected figures (12200/9300) came from the spec text's illustrative table; the verified fork values follow the spec's normative formulas applied to this fork's published constants — the same convention used for the fork's KeyedNonceAccessCost derivation.

- `source` is added to `accessed_addresses` once source gas is charged, valid or not; on charge failure it is not warmed.
- OOG behavior verified at all three charge stages (source / current-access / write), including the BAL inclusion/exclusion boundary.

### 4.5 Adoption mechanics
- `StateDB.SetCodeHashReference(addr, hash, code)` (`core/state/statedb.go:535`, interface `core/vm/interface.go:49`) → `stateObject.setCodeHashReference` → journal `codeHashReferenceChange` (`core/state/journal.go:468`): hash-only, fully revertable, no bytecode copy into the account entry (content-addressed blob referenced).
- Repeated adoption and same-hash adoption handled (write gas skipped on same-hash).
- Source changes after adoption do not affect the adopter (tested).

### 4.6 CREATE / CREATE2 / initcode
- SETCODEFROM permitted in initcode (creation tx, CREATE, CREATE2); current account = account under construction; frame keeps executing initcode; `EXTCODE*`/later calls observe adopted code; re-entrant call into the account executes adopted code during its own initcode (tested).
- On successful completion with adopted codeHash: return data treated as empty — not validated, not hashed, not installed; **no code-deposit execution gas and no code-deposit state gas** (delta exactly `32 × 1530 = 48,960` state gas, `CostPerStateByte = 1530`; asserted in `TestSetCodeFromCreateViaOpcode`).
- Account-creation leaf charge preserved: `AccountCreationSize (120) × 1530 = 183,600` state gas still charged on the parent CREATE.
- Failed/reverted creation rolls back the adoption (tested); caller return-data buffer stays empty (EIP-211).

### 4.7 BAL (EIP-7928) integration
- `CodeChange` extended with optional trailing `new_code_hash`: adoption ⇒ `[block_access_index, b"", code_hash]` (3-element); every other change keeps the 2-element form; other encodings invalid (form validation tested, 4 cases).
- RLP optional-tail encoding via the regenerated codec (`core/types/bal/bal_encoding_rlp_generated.go`), RLP round-trip both forms, Merge/Copy/form-replacement, JSON round-trip — all tested (`core/types/bal/bal_eip8298_test.go`, 4 tests).
- Final-value rule: if end-of-tx code hash was set by adoption the entry is hash-form; a later ordinary install in the same tx flips it back to 2-element (tested).
- Source included in the BAL once source gas is charged (empty change lists if no other changes); excluded on charge failure (tested).
- Pre-state gas validation cost of SETCODEFROM = `SETCODEFROM_SOURCE_GAS` (spec rule noted; enforced by construction — the source charge is the first state-touching step).

### 4.8 EIP-8141 interaction (frame transactions)
- SETCODEFROM executes normally inside frame-transaction execution frames post-Bogota (consensus path tested: `TestFrameTxSetCodeFrom`).
- **Validation-prefix restriction is enforced at transaction level**: this fork has no EIP-8141 validation-prefix mempool simulation layer (documented M7 conservative-admission deviation) and no ERC-7562/AA mempool. The fork's enforcement is therefore: SETCODEFROM inside a **verify frame** halts with write-protection, making the whole frame transaction consensus-invalid (`TestFrameTxSetCodeFromVerifyFrameBanned`). The spec's prefix-layer mempool rules (mutable-code dependency tracking, DELEGATECALL/CALLCODE dependency limits) are documented for a future prefix-simulation layer; they are mempool-policy only and do not affect consensus.

### 4.9 EIP-8151 interaction (restricted ecRecover)
- Migration-arc regression test (`TestSetCodeFromAdoptionTriggersEIP8151`): a 7702-delegated EOA adopts regular wallet code via SETCODEFROM → ecRecover at 0x01 transitions permitted → rejected (empty output, success=1) for the adopted account. Adoption does not bypass the 8151 restriction.
- EIP-3607 consequence: after adoption the account has regular deployed code, so ECDSA-originated txs are invalid; 7702 redelegation is blocked (authority code check accepts only empty code or an existing delegation indicator).

### 4.10 Mempool (policy only, non-consensus)
- `core/txpool/validation.go:436`: `ValidateTransactionWithState` rejects ECDSA transactions from accounts with regular deployed code (`core.ErrSenderNoEOA`); frame transactions and 7702-delegated accounts exempt.
- `core/txpool/legacypool/legacypool.go`: `demoteUnexecutables` evicts pending ECDSA transactions of code-adopted accounts on head change, preserving frame transactions.
- Tested: `TestSetCodeFromPoolAdmissionRejectsCodedAccounts`, `TestSetCodeFromPoolEvictsPendingOnHeadChange`.

### 4.11 Fork activation
- Registered in `newBogotaInstructionSet` (Bogota = newest instruction set). Upstream EIPs activate by timestamp; this fork's Hegotá gates (`IsAmsterdam`/`IsBogota`) are timestamp-based, while XDC (XDPoS) chains use block-based gates with Amsterdam/Bogota unscheduled — **no activation is scheduled anywhere**; activation scheduling is a governance decision outside this implementation.

## 5. Test evidence (all this session, 2026-11-03, `-count=1`)

### 5.1 Final post-format test (Task 1, exact command)
```
cd /home/tanmay/xdc-fix-work/eip-8298-setcodefrom && export PATH=/home/tanmay/go-sdk/go/bin:$PATH && go test ./core/vm/ -count=1
→ ok  github.com/ethereum/go-ethereum/core/vm  4.579s   EXIT_CODE=0
```

### 5.2 Affected-package suites (fresh runs)
| Package | Result |
|---|---|
| `core/vm` | ok 4.563s |
| `core/types/bal` | ok 0.008s |
| `core/txpool` | ok 0.012s |
| `core/txpool/legacypool` | ok 7.619s |
| `core` | FAIL — pre-existing only (see §5.4); EIP-8298 tests PASS |
| `core/state` | FAIL — pre-existing only (see §5.4) |
| `core/types` | FAIL — pre-existing only (see §5.4) |

### 5.3 EIP-8298 test inventory — 35 test functions, all PASS
| File | Tests | Evidence |
|---|---:|---|
| `core/vm/eip8298_test.go` | 27 | `-run SetCodeFrom -v`: 27/27 `--- PASS` |
| `core/types/bal/bal_eip8298_test.go` | 4 | 4/4 PASS |
| `core/txpool/legacypool/legacypool_eip8298_test.go` | 2 | 2/2 PASS |
| `core/eip8298_frame_test.go` | 2 | 2/2 PASS |

Coverage: registration/Bogota-gating, pre-fork halt, all source-validity classes (nonexistent/empty/0xEF/precompile/7702-delegated — target not warmed, not in BAL), exact gas table, OOG at all three charge stages with BAL boundary, static + system-contract halts before charge, revert rollback, DELEGATECALL environment semantics, BAL hash-form recording + RLP/JSON round-trips, CREATE/CREATE2 adoption, failed-create rollback, deposit-gas suppression (48,960), opcode-level CREATE pair (leaf 183,600 preserved), same-tx-created source rejection, re-entrant execution of adopted code during initcode, post-adoption source changes, repeated adoption, 8151 migration arc, pool admission + head-change eviction, frame-tx execution + verify-frame rejection.

### 5.4 Baseline failures and proof of zero new failures (freshly reproduced 2026-11-03)
Identical failure sets on the disposable baseline (`tmp-8298-baseline` = base commit + base patches, no 8298 code) and on the implementation worktree:

| Package | Failing tests (BOTH trees, identical) |
|---|---|
| `core` | `TestProcessUBT`, `TestProcessParentBlockHash` (nil-pointer panic in `state.AccessEvents.Merge` via `ProcessParentBlockHash`, `bintrie_witness_test.go`) |
| `core/state` | `FuzzJournal` |
| `core/types` | `TestBlockEncoding`, `TestEIP1559BlockEncoding`, `TestEIP2718BlockEncoding`, `TestEIP4844BlockEncoding` |

All are pre-existing on the integration base (Verkle/bintrie-witness and block-encoding areas untouched by this work). **Zero new failures attributable to EIP-8298.** The `core` panic aborts that package's binary, so the 8298 frame tests were additionally run in isolation (`-run SetCodeFrom`) and pass.

## 6. Build and vet
- `go build ./...` → exit 0 (no output).
- `go vet ./core/... ./core/vm/... ./core/types/bal/... ./core/txpool/... ./core/state/... ./core/tracing/...` → exit 0 (no output).
- gofmt: all files touched by this work are clean (verified earlier this session; the 4 remaining repo-wide gofmt flags exist identically on the baseline, inherited from the 8250-stack patch).

## 7. Architectural deviations and limitations
1. **Gas table parameterization** (see §4.4): 11200/8300 vs spec-text 12200/9300 — fork pin `AccountWriteAmsterdam=8000`; spec formulas untouched.
2. **Validation-prefix ban at transaction level** (see §4.8): no prefix mempool layer exists in this fork; enforcement is consensus-level write-protection in verify frames. ERC-7562 user-op rule not applicable (no AA mempool).
3. **BAL CodeChange vs EIP-7906**: this base excludes 7906; the CodeChange optional-tail extension is forward-compatible (7906 uses the same type for ordinary installs), but a stacked merge must re-verify 7906's assertion paths.
4. **No upstream test fixtures**: spec "Test Cases" section is empty upstream; the 35-test suite is the conformance artifact.
5. **Activation unscheduled** on XDC chains (block-based gates); Bogota registration only.
6. **Pre-existing base-patch defect (not this work)**: `backups-frame-tx-8250/final-20261029.patch` encodes `HEGOTA_XDC_COMPATIBILITY_REPORT.pdf` as a text diff; applying it mangles 22 bytes of the PDF. Canonical bytes (sha256 `5211144e…534f2e`) are preserved in the `frame-tx-8250` and `eip-8298-setcodefrom` worktrees. Recovery procedures must restore the PDF from a canonical copy after applying the 8250 patch (documented in §8 verification notes).
7. **Upstream spec is Draft**: the pinned commit `f4a9f965…` may be superseded; drift check = re-hash the live file against §1.

## 8. Recovery artifacts (disaster recovery)
| Artifact | Value |
|---|---|
| Patch | `/home/tanmay/xdc-fix-work/backups-cross-client/EIP-8298-final.patch` |
| Patch SHA-256 | recorded in `EIP-8298-final.patch.sha256`, verified with `sha256sum -c` |
| Contents | exactly the EIP-8298 delta over the canonical base tree `c77ff3c1…` (23 files including this report; no unrelated files, no binary hunks) |
| Apply test | fresh disposable worktree at `48a7727` + base patch chain (+ canonical PDF restore) → `git write-tree` = `c77ff3c1…` (base reproduced byte-exact) → `git apply EIP-8298-final.patch` → `git write-tree` = feature tree (exact match) → disposable worktree removed |

## 9. Production-readiness assessment
Implementation is **feature-complete against the pinned, hash-verified spec snapshot**: all normative behaviors implemented, 35 self-tests green, zero new failures vs the integration base, build/vet/gofmt clean, recovery patch verified end-to-end. Before mainnet scheduling: (a) governance must schedule Bogota on XDC chains; (b) track upstream Draft drift (§7.7); (c) adopt upstream test fixtures when published; (d) stacked-merge re-verification with EIP-7906 (§7.3). Nothing here is committed or pushed; activation is off by default.
