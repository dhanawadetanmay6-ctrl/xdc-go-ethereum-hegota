# EIP-8298 SETCODEFROM — Specification and Requirements

**Worktree:** `/home/tanmay/xdc-fix-work/eip-8298-setcodefrom` (branch `feat/eip-8298-setcodefrom`)
**Date:** 2026-11-03

## 1. Pinned specification

- **EIP:** 8298 — SETCODEFROM Code Reuse Instruction (Draft, Standards Track / Core)
- **Canonical URL:** https://eips.ethereum.org/EIPS/eip-8298
- **Local snapshot:** `/home/tanmay/xdc-fix-work/eip-8298-spec-snapshot.md`
- **Snapshot SHA-256:** `9c05ea1f17f5726b793388e7d61e38a6a2af1adc7ff134f2c80de5b399214d13`
- **Retrieved:** 2026-11-03 from `ethereum/EIPs` `master` (raw.githubusercontent.com)
- **Created:** 2026-06-11 · **Authors:** Liyi Guo, Ben Adams, Carlos Perez, Nicolas Consigny
- **Requires:** 3607, 6780, 7702, 7928, 8037, 8038
- **Upstream commit SHA:** unavailable — the GitHub API call used for SHA pinning was blocked in this environment; the snapshot hash + retrieval date above pin the content. (A drift check can be re-run later by comparing the snapshot against the live EIP text.)
- **Spec "Test Cases" section:** empty (`<!-- TODO -->`) upstream — no official fixture set exists. No upstream client implementation was found (checked: this fork is the implementation target).

## 2. Integration base

- Base commit: `48a77271314293cdf057bc05d2c1624c3645a4d0` (pristine main checkout; never modified).
- Applied on top, in order:
  1. `backups-frame-tx-8250/final-20261029.patch` — EIP-8141 M1–M10 (frame transactions) + EIP-8250 (keyed nonces). This is the standard cumulative Hegotá tx stack used by the 8250/8272/7906 missions.
  2. `backups-eip-8151/FINAL-eip-8151.patch` — EIP-8151 account-code-restricted ecRecover (stateful precompile at 0x01 on Bogota).
- Both patches applied cleanly (no conflicts); 52 staged files; base builds clean (`go build ./core/... ./params/...` exit 0, Go 1.26.8).
- Relationship to the five completed EIPs:
  - **EIP-8141** (frame txs): IN BASE — required for §Public Mempool interaction analysis (frame-tx execution paths).
  - **EIP-8250** (keyed nonces): IN BASE (rides on 8141; no direct 8298 interaction).
  - **EIP-8272** (recent roots): NOT in base — no normative interaction with SETCODEFROM (opcode does not touch SLOTNUM/recent-roots surfaces).
  - **EIP-7906** (assertions): NOT in base — no normative interaction (TXTRACE/ASSERT operate on value/storage diffs; code-hash adoption is orthogonal; BAL integration in 8298 extends the same CodeChange type 7906 uses for ordinary installs — documented for a future stacked merge).
  - **EIP-8151** (restricted ecRecover): IN BASE — required for the §7 adoption/ecRecover interaction tests.
- Consensus dependencies from `requires:`: 3607, 6780, 7702, 7928, 8037, 8038 — all present in base 48a7727 (Prague/Cancun plus the Amsterdam 8037/8038/7928 work).

## 3. Normative requirements (spec §Specification)

### 3.1 Opcode
- `SETCODEFROM = 0xA7`; pops 1 stack item, low 160 bits = `source`; pushes `1` (valid + adopted) or `0` (invalid, no state change). [R-OP]
- Current account = execution-environment address (ADDRESS/SSTORE account), NOT the code-source account under 7702/delegatecall. [R-ENV]
- Address-based (reads `source.codeHash` from live consensus state), never a raw code hash input. [R-ADDR]

### 3.2 Source validity (all required)
- Source account exists in state. [R-SRC-EXIST]
- `source.codeHash != EMPTYCODEHASH` (`0xc5d2…a470`). [R-SRC-NONEMPTY]
- `source.code` does not start with `0xEF` (EIP-3541/7702 exclusion). [R-SRC-EF]
- Source not created in the current transaction (EIP-6780 rule: exactly when SELFDESTRUCT would delete it). Current account MAY be created this tx (incl. its own initcode). [R-SRC-CREATED]
- Validation never resolves EIP-7702 delegation on the source. [R-SRC-NODELEG]
- Precompile/no-code accounts are invalid (they have empty code ⇒ EMPTYCODEHASH). [R-SRC-PRECOMPILE]

### 3.3 Execution restrictions
- Static context ⇒ exceptional halt. [R-HALT-STATIC]
- Current account is a system contract (protocol calls it from `SYSTEM_ADDRESS = 0xffff…fffe` during block processing) ⇒ exceptional halt. EIP-6110 deposit contract (called from elsewhere/never called) is not one. [R-HALT-SYSCON]
- Both halts happen BEFORE `SETCODEFROM_SOURCE_GAS` is charged; source is not accessed. [R-HALT-ORDER]

### 3.4 Code adoption
- Valid source ⇒ set current account's `codeHash = source.codeHash`; no bytecode copy into the destination account entry. [R-ADOPT]
- Later changes to source must not affect adopter's codeHash or bytecode availability. [R-PERSIST]
- Normal revert semantics (frame/tx revert reverts the update). [R-REVERT]
- Repeated adoption and same-hash adoption handled correctly. [R-REPEAT]

### 3.5 Deployed-code execution visibility
- Current frame keeps executing already-loaded code; CODESIZE/CODECOPY keep referring to it. [R-VIS-FRAME]
- EXTCODESIZE/EXTCODEHASH/EXTCODECOPY and later calls observe the adopted code (subject to revert rules). [R-VIS-EXT]

### 3.6 Contract creation (create tx, CREATE, CREATE2)
- SETCODEFROM allowed during initcode; current account = account under construction; update immediate; frame keeps running initcode; CODESIZE/CODECOPY = initcode; EXTCODE*/calls see adopted code. [R-INIT]
- Successful completion: if created account's codeHash == EMPTYCODEHASH ⇒ normal path (validate returned bytes, charge deposit, install). Otherwise (adopted) ⇒ return data treated as empty: NOT validated/hashed/installed, NO code-deposit execution gas, NO code-deposit state gas; adopted codeHash preserved. [R-INIT-FINAL]
- Account-creation costs unchanged (new account leaf state-gas still charged when applicable; existing destination ⇒ none, as usual). [R-INIT-ACCT]
- Creation revert/halt reverts the adoption. [R-INIT-REVERT]
- Deployed-code reporting (traces/eth_call) reports the adopted code; CREATE/CREATE2 caller return-data buffer stays empty (EIP-211). [R-INIT-REPORT]

### 3.7 Gas (execution gas only; `SETCODEFROM_STATE_GAS = 0`)
- `SOURCE_ACCOUNT_ACCESS_COST` = COLD_ACCOUNT_ACCESS (cold source) else WARM_ACCESS
- `SETCODEFROM_SOURCE_GAS` = SOURCE_ACCOUNT_ACCESS_COST + WARM_ACCESS — charged per execution attempt, covers leaf load + fixed code-inspection charge.
- After the charge, `source` is added to `accessed_addresses` valid or not. Charge failure ⇒ not accessed. [R-GAS-WARM]
- Valid source ⇒ `SETCODEFROM_CURRENT_ACCESS_GAS` = WARM_ACCESS, charged BEFORE the hash comparison. [R-GAS-CUR]
- Hashes differ ⇒ `SETCODEFROM_WRITE_GAS` = ACCOUNT_WRITE, charged before the update. [R-GAS-WRITE]
- Invalid source: neither current-access nor write gas. Same-hash: current-access but not write. [R-GAS-SKIP]
- No code-deposit gas/state gas for adopted code (also in initcode). [R-GAS-DEPO]

**Fork-parameter derivation (this fork, Amsterdam schedule — mirroring the KeyedNonceAccessCost precedent; spec text table assumed ACCOUNT_WRITE=9000):**
- COLD_ACCOUNT_ACCESS = `ColdAccountAccessAmsterdam` = 3000; WARM_ACCESS = 100; ACCOUNT_WRITE = `AccountWriteAmsterdam` = **8000** (fork pin, SPEC_INVENTORY §4).
- Resulting fork costs: invalid 3100/200; same-hash 3200/300; update **11200/8300** (spec table with 9000 would be 12200/9300 — documented deviation by parameterization, not hardcoding).

### 3.8 BAL (EIP-7928)
- `CodeChange = [BlockAccessIndex, Bytecode, Optional[Hash32]]`; SETCODEFROM adoption ⇒ `[index, b"", new_code_hash]`; every other change keeps the 2-element form; other encodings invalid. [R-BAL-FORM]
- "Final value set by SETCODEFROM" rule: if the account's end-of-tx code hash was set by adoption, the entry is hash-form; a later ordinary install in the same tx flips it back to 2-element. [R-BAL-FINAL]
- Source is included in the BAL once source-gas is charged (empty change lists if no other changes); charge failure ⇒ excluded. [R-BAL-SRC]
- Bytecode behind `new_code_hash` must exist in pre-block state or an earlier-index `code_changes` entry; no same-or-later-index references. [R-BAL-REF]
- Pre-state gas validation cost of SETCODEFROM = SETCODEFROM_SOURCE_GAS. [R-BAL-GAS]

### 3.9 Public mempool (policy only, non-consensus)
- ECDSA txs of an account that adopted regular code become invalid (EIP-3607): evict on new block; reject new. [R-MP-EVICT]
- ERC-7562 validation should block SETCODEFROM like SELFDESTRUCT. [R-MP-7562]
- EIP-8141 prefix: ban SETCODEFROM during validation-prefix execution; contracts reached via CALL*/EXTCODE* during prefix containing SETCODEFROM (excl. PUSH data) = mutable-code deps; DELEGATECALL/CALLCODE ⇒ track code hash as dependency + limit dependents; sender's own resolved-target code not mutable merely for containing it. [R-MP-8141]

### 3.10 ECDSA origination / EIP-8151 interaction
- Adopted regular code disables ECDSA origination (3607) and 7702 redelegation. [R-ECDSA]
- With EIP-8151: ecRecover rejects recovered addresses whose account code is regular deployed code (empty output, success=1). Verify this holds after adoption — adoption must not bypass the restriction. [R-8151]

## 4. XDC architecture notes / ambiguities

- **Fork activation:** upstream EIPs activate by timestamp; this fork's Hegotá gates (`IsAmsterdam`/`IsBogota`) are already timestamp-based, and XDC chains (XDPoS) use block-based gates with Amsterdam/Bogota unscheduled (SPEC_INVENTORY §3). SETCODEFROM goes into `newBogotaInstructionSet` (Bogota = newest instruction set); no activation is scheduled anywhere.
- **Gas constants:** fork pins (8038) differ from spec-text table (see §3.7). All charges derive from `params` getters, never hardcoded.
- **System-contract detection:** this fork identifies a system call as `caller == params.SystemAddress` (`core/vm/evm.go isSystemCall`); used for the halt check on the frame's caller.
- **Mempool prefix layer absent:** the fork deliberately runs no EIP-8141 validation-prefix simulation (documented M7 conservative-admission deviation) and has no ERC-7562/AA mempool. R-MP-8141/R-MP-7562 are therefore not directly implementable as written; the mapping is: (a) consensus execution is unaffected (SETCODEFROM executes normally in frame txs post-Bogota); (b) the ECDSA-invalidation eviction (R-MP-EVICT) IS implementable in legacypool head-change revalidation and is implemented; (c) prefix-layer rules documented for when a prefix-simulation layer is added.
- **BAL CodeChange encoding:** extends `encodingCodeChange` with optional trailing `NewCodeHash`; RLP optional-tail semantics via the generated RLP codec (regenerated/extended consistently).
- **"Do not copy bytecode":** state-model requirement — the account entry references the existing content-addressed code blob; the blob is persisted once (by its origin) and never deleted mid-block; adoption sets only the account's codeHash (an in-memory code cache may hold the bytes for read paths — that is not a state copy).

## 5. Requirement → implementation map (summary; details in the implementation report)

| Req | Site |
|---|---|
| R-OP/R-ENV | `core/vm/instructions_setcodefrom.go` (opSetCodeFrom), `core/vm/opcodes.go` (0xA7), `jump_table.go` (Bogota set) |
| R-SRC-* | opSetCodeFrom validation (Exist, hash, 0xEF, IsNewContract, raw GetCode) |
| R-HALT-* | opSetCodeFrom prologue (readOnly, isSystemCall) before any charge |
| R-ADOPT/R-PERSIST/R-REVERT | `stateObject.setCodeHashReference` + journal entry + StateDB.SetCodeHashReference |
| R-VIS-* | frame separation is inherent (contract code fixed at call start); EXTCODE* read state |
| R-INIT-* | `evm.initNewContract` adoption check before validation/deposit/install |
| R-GAS-* | dynamicGas in the handler via fork params; GasCosts{RegularGas} only |
| R-BAL-* | `core/types/bal/*` + `journal`/`recordAccessListChanges` form tracking |
| R-MP-EVICT | legacypool head-change eviction for regular-code senders |
| R-8151 | regression tests against in-base `ecrecover8151` |
