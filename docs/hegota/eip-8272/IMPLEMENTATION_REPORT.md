# EIP-8272 Implementation Report — Recent Roots for Frame Transactions

Date: 2026-10-30
Worktree: `/home/tanmay/xdc-fix-work/eip-8272-recent-roots`
Branch: `feat/eip-8272-recent-roots` (base commit 48a7727, all work staged — no commits)
Base layer: EIP-8141 frame transactions (M1–M10, applied staged, sha256-verified) + EIP-7843 SLOTNUM (pre-existing).
Spec snapshot: EIP-8272 pinned from ethereum/EIPs master, last upstream commit 2026-10-08T09:57:47Z (Draft).

## 1. Status summary

| Area | Status |
|---|---|
| Contract constants, bytecode, derivations | Implemented and tested |
| Write operation (all rejection paths) | Implemented and tested |
| Validation operation (all predicates) | Implemented and tested |
| Official reference vector | Implemented and tested (passes) |
| Canonical VERIFY-frame recognition | Implemented and tested |
| STATICCALL execution path | Pre-existing EIP-8141 engine, verified with recent-root frame |
| Gas exactness (admission vs engine receipt) | Implemented and tested |
| Deployment transaction + init code | Implemented and tested (spec sheet fully verified) |
| Pre-deployment / post-deployment behavior | Implemented and tested |
| Public mempool: shape grammar + skip semantics | Implemented and tested |
| Public mempool: stateful admission (code/slot/storage/simulation) | Implemented and tested |
| MAX_VERIFY_GAS / MAX_VERIFY_STATE_GAS accounting | Implemented and tested |
| SLOTNUM/SLOAD permission enforcement (tracer) | Implemented and tested |
| Dependency tracking (index, register, cleanup) | Implemented and tested |
| Slot advancement / expiry eviction | Implemented and tested |
| Reorganization handling (removed/added branch) | Implemented and tested |
| Code dependency + activation flip | Implemented and tested |
| Miner pre-inclusion revalidation | Implemented and tested (helper + wiring) |
| Application introspection via EIP-8141 opcodes | Implemented and tested |
| Regression sweep vs baseline | See §7 |

Nothing is blocked. Nothing was left knowingly unimplemented. The only deliberate scope decisions are documented in §8.

## 2. Architecture

### Constants and derivations — `core/types/recent_root.go`
- `RecentRootAddress` = 0x8272D9679689Ea2f307140CdF9002D27dC00Ffff.
- `RecentRootCode` — the exact 320-byte runtime bytecode from the spec's Bytecode section, transcribed as a single hex literal and pinned by a byte-equality test against a second independent transcription.
- `RecentRootInitCode` = 10-byte loader stub (`61014080600a5f395ff3`) + `RecentRootCode`; byte-equality-tested against the spec's 330-byte deployment input.
- `RecentRootDeploymentTxBytes` reconstructed from the spec JSON parameter sheet (nonce 0, gasPrice 0xe8d4a51000, gas 0x13d620, to null, value 0, input, v=0x1b, r=0x539, s=0xfadf66b1e192785c).
- Constants: RECENT_ROOT_LENGTH=8192, RECENT_ROOT_USABLE_WINDOW=8191, MAX_RECENT_ROOT_REFERENCES=16, RECENT_ROOT_TUPLE_BYTES=72; domains RECENT_ROOT_ENTRY_DOMAIN / RECENT_ROOT_STORAGE_DOMAIN.
- Pure functions: `RecentRootSourceID`, `RecentRootEntryHash`, `RecentRootStorageKey`, `ParseRecentRootTuples`, `IsRecentRootVerifyFrame` (full shape predicate), `RecentRootVerifyFrameIndexes`.

### Evaluation and simulation — `core/recent_root.go`
- `CheckRecentRootTuples(state, currentSlot, tuples)` — direct predicate evaluation (the spec's permitted alternative to executing the code): per-tuple slot/current/future/age ≤ 8191 checks and storage comparison. Used by pool revalidation and the miner pre-inclusion check.
- `RecentRootCodeMatches(state)` — exact code equality.
- `CheckRecentRootPreInclusion(state, slotNumber, tx)` — miner-side payload-slot revalidation.
- `SimulateRecentRootVerifyFrame(sdb, chainConfig, head, sender, frame)` — full admission simulation: code match → snapshot → engine-identical transaction-entry warming (`Prepare`: sender, coinbase, precompiles) → charge frame-entry cold/warm account access of the target → `StaticCall` with the frame's declared execution budget under a permission tracer → revert snapshot. The BlockContext carries the head's number/time (fork dispatch) and `SlotNum = head.SlotNumber + 1` (EIP-8272 "Current slot" — never advanced by empty slots, never derived from timestamps).
- `recentRootTrace` — `OnOpcode` tracer enforcing: banned opcodes everywhere (GASPRICE, BLOCKHASH, COINBASE, NUMBER, PREVRANDAO, GASLIMIT, BASEFEE, BLOBBASEFEE, BALANCE, SELFBALANCE, SELFDESTRUCT, INVALID, CREATE-family, GAS, TIMESTAMP note), SLOTNUM only at depth 1 in the canonical code, SLOAD only at depth 1 on RECENT_ROOT_ADDRESS with a key derived from the frame's tuples.
- `DeployRecentRoot` — test/fixture helper installing the contract at the canonical address (explicitly not a protocol predeploy).

### Stateless mempool policy — `core/txpool/validation_frame.go`
- `MaxVerifyGas = 100_000`, `MaxVerifyStateGas = 500_000` (EIP-8141 constants; not previously defined in the fork).
- `ClassifyFrameValidationPrefix(ft)` — EIP-8141 prefix grammar with EIP-8272 skip semantics: optional leading expiry_verify, then optional recent_root_verify, then one of the four recognized prefixes (self_verify / deploy+self_verify / only_verify+pay / deploy+only_verify+pay). Deploy must be first among the remaining frames; protocol verifier frames count as validation-prefix frames for gas accounting.
- `ValidateFrameRecentRootPolicy(ft)` — for any tx containing a VERIFY frame to RECENT_ROOT_ADDRESS: exact canonical shape required (else "not recent_root_verify" rejection), at most one such frame, fixed position, recognized prefix, no ATOMIC_BATCH_FLAG in the prefix, no VERIFY frame after the prefix, sig-cost + Σ prefix execution limits ≤ MAX_VERIFY_GAS, Σ prefix state limits ≤ MAX_VERIFY_STATE_GAS.
- Wired into `validateFrameTransaction` immediately after `ValidateStatic` — fully stateless, before any sender state is read (spec ordering requirement). Frame txs without a VERIFY-to-RECENT_ROOT_ADDRESS frame are unaffected.

### Stateful admission + dependency tracking — `core/txpool/legacypool/recent_roots.go`
- `validateRecentRootTx` (called from `validateTx` after `ValidateTransactionWithState`): activation gate (Bogota), current-slot availability, exact code match, direct tuple predicates, then the full simulation (gas sufficiency + permissions).
- Dependency record per admitted tx: parsed tuples, distinct storage keys, per-key entry hash, expiry slot (min(slot) + 8192). Indexes: `rrDeps` (tx hash → record), `rrByKey` (storage key → tx hashes). Registration at both admission paths (queue insert and pending replace); replacement is atomic (drop old + register new under the same lock).
- Cleanup at every removal site: `removeTx`, `add` replacement, `promoteSpecialTx` replacement, `promoteTx` discard/replace, `promoteExecutables` dropped, `truncatePending` (both cap loops), `truncateQueue`, `demoteUnexecutables` (olds + drops) — 10 sites.
- `revalidateRecentRootFrames(oldHead, newHead)` — called in `runReorg` after `demoteUnexecutables` on every head change: activation-flip detection (Amsterdam/Bogota rule diff), missing-slot head, code change, tuple expiry/current-slot predicates, per-key storage re-read. Evicts via `removeTx` (collect-then-remove; never inside an index iteration). Only recent-root txs are visited — no full-pool scan.

### Miner pre-inclusion — `miner/worker.go`
- `commitTransaction`: for frame txs, `core.CheckRecentRootPreInclusion(env.state, env.header.SlotNumber, tx)` runs before `applyTransaction`. Cheap predicate check against the payload's actual slot number; the frame engine remains the authoritative executor (a VERIFY failure invalidates the tx at execution regardless).

### What was NOT changed
- No new opcode. No new envelope field. No predeploy/genesis allocation. No EVM exception paths outside the tracer-scoped simulation. The block-execution engine needed zero EIP-8272-specific changes: a canonical VERIFY frame to the deployed contract executes via the pre-existing STATICCALL path with SLOTNUM available post-Amsterdam.

## 3. Reference vector (EIP-8272 "Reference test vector")

Verified end-to-end in `core/types/recent_root_test.go` + `core/recent_root_test.go`:
- source_id = keccak256(0x…01 ‖ 0x00…00) = 0xb9382d35273c75a50631a3e84d3c75ec9266e2b18c35a627e16cdbf26a18ca85 ✓
- entry_hash (slot 1, root 0x…02) = 0x0a0d1254c851be5a133b4c9a9e300f5602fc0f43dbe65aa6a66930d4ca0a51b8 ✓
- storage_key = 0x5f027aa1cbe2df279bf6518edd4b44ea5409fd800189ec35224e10ab05e574c3 ✓
- Contract executed at current_slot = 2 against a state holding exactly the reference entry: succeeds with empty return data, no logs, no state changes ✓

The vector was not modified to fit the implementation.

## 4. Deployment sheet verification

Full sheet verified in `TestRecentRootDeploymentTx` (core/types):
- tx hash = 0x56c2adbbfa3ba5dfe47adf48828f6c581134e39e14c3b80e06214de5e5875272 ✓ (matches spec claim)
- recovered sender (Homestead v=27) = 0x14bf16d4c9842bf1EbF396e553477C66EB0a8A82 ✓ (matches spec claim)
- CREATE address (nonce 0) = 0x8272D9679689Ea2f307140CdF9002D27dC00Ffff ✓ (canonical address)
- input = 330-byte init code ✓; executing it (EVM create, `TestRecentRootDeploymentExecution` in core) returns exactly RECENT_ROOT_CODE at the created address with empty storage ✓

An earlier draft of this worktree recorded a "spec inconsistency" caused by a truncated hand-transcribed init-code literal in my own code (180 of 330 bytes). After deriving the init code programmatically (stub + runtime) and byte-verifying against the spec JSON, the sheet is fully self-consistent. No discrepancy remains open; the deployed runtime bytecode and the reference vector were independently verified throughout and are unaffected by the deployment-sheet path.

## 5. Test inventory (all passing unless noted)

### core/types (`recent_root_test.go`) — 7 tests
Constants, byte-exact runtime code, reference-vector derivations, tuple parse bounds (0/17 tuples, 71/73 bytes, truncation), frame shape predicate (flags/value/state/length/target variants), index enumeration, deployment sheet.

### core (`recent_root_test.go`, `recent_root_miner_test.go`) — 17 tests
Write+validate roundtrip; validation failure matrix (wrong root/source/slot, current slot, future slot); age boundary 8191 vs 8192; calldata length matrix (0/63/64/65/71/73 bytes, 17 tuples, 1153 bytes); 16 valid references with measured gas (internal 6,455 + cold access — engine-measured 16-tuple total 55,855); duplicate tuples independently checked; no-side-effects proof (return data, logs, storage diff); nonzero-value rejection; static-context write rejection; reference vector end-to-end; initial storage emptiness; deployment execution (init → exact runtime, empty storage, canonical address); pre-deployment VERIFY rejection (default-code path); in-block frame execution; payload-slot expiry at engine; slot-from-header-only proof; gas exactness (simulation == engine receipt; exact limit accepted, limit−1 rejected); tracer permission tests (wrong-code SLOAD rejection); pre-inclusion check matrix (valid/expired/invalidated/missing code/changed code/nil slot/pass-throughs); application introspection (FRAMEPARAM resolved_target/mode/flags/state-limit/data-length/status + FRAMEDATALOAD tuple decode).

### core/txpool (`validation_recentroot_test.go`) — 4 tests, 37 subtests
Prefix classification matrix: 4 shapes × {none, recent-root, expiry, expiry+recent-root} (16 combos) with prefix lengths and skip indexes; structural rejections (reversed verifiers, duplicate, position after self_verify/deploy, alone, with user_op only); malformed VERIFY-to-RECENT_ROOT_ADDRESS (flags=1, flags=3, value≠0, state≠0, 71/73-byte, 17 tuples, empty, 64-byte write encoding); VERIFY after prefix; batch-in-prefix; MAX_VERIFY_GAS exact/+1; MAX_VERIFY_STATE_GAS exact/+1; recent-root frame counted toward the budget; stateless-before-stateful ordering proof (malformed tx rejected by `ValidateTransaction` without any state access).

### core/txpool/legacypool (`recent_roots_test.go`) — 11 tests
Admission + dependency registration (record content, key index); rejection matrix (bad source/root, current/future slot, age-8192 boundary, age-8191 acceptance, missing code, wrong code, underfunded execution limit); 16-tuple distinct-key indexing with duplicate; atomic replacement (old record/index gone, new present); eviction cleanup ×4 (removeTx, nonce-consumption demotion, revalidation, queue truncation); slot advancement sweep (kept at age 8191, evicted at 8192, records cleaned); reorganization (removed-branch rollback evicts, re-admission works, preserved branch keeps); code dependency (code removal evicts); activation flip (reorg back across Bogota boundary evicts); pre-deployment admission rejection; unrelated-tx isolation (legacy tx untouched by sweeps).

## 6. Gas measurements (this fork, Amsterdam repricing)

| Case | Execution gas (frame entry + internal) |
|---|---|
| 1 tuple, cold target + cold key | 6,415 |
| 16 tuples, 15 distinct cold keys | 55,855 |
| Admission vs engine receipt | identical (asserted in test) |

The 16-tuple case fits MAX_VERIFY_GAS with the reference prefix [recent_root_verify, self_verify]: 2,800 (sig) + 60,000 + 30,000 = 92,800 ≤ 100,000.

## 7. Regression results

See TEST_EVIDENCE.md for the command log. Summary:
- `go build ./...` — clean.
- `go vet` on all touched packages — clean. gofmt — all EIP-8272 files clean (15 pre-existing gofmt-dirty fork files verified identical in the 8141 base; none introduced here).
- Full `./core/txpool/...` suite — ok (including blobpool and the full legacypool suite; zero failures).
- 7-package regression sweep — the failing set matches the documented pristine baseline: FuzzJournal, 4 block-encoding tests, TestEIP7910Config, TestMessages, TestProcessParentBlockHash, TestProcessUBT, TestRPCGetBlockOrHeader, TestRPCGetBlockReceipts, TestRPCGetTransactionReceipt, TestRPCMarshalBlock.
- The baseline core binary aborts early on a pre-existing nil-pointer panic (`TestProcessParentBlockHash/UBT`, `state.AccessEvents.Merge`); a complete core run with the three pre-existing panickers skipped shows 12 further pre-existing failures (10 BAL + 2 genesis), name-for-name identical to the same run on the M10 base. EIP-8272 adds zero failures anywhere and removes none.

## 8. Deliberate scope decisions and limitations

1. **Prefix grammar enforced only for recent-root-carrying txs.** `ValidateFrameRecentRootPolicy` runs its four-shape grammar check only when the tx contains a VERIFY frame to RECENT_ROOT_ADDRESS. EIP-8141's public-mempool prefix simulation for ordinary frame txs remains as it was before this change (the fork's pre-existing conservative admission). This is per EIP-8272: all its MUSTs concern the recent-root verifier frame. Extending the grammar to all frame txs is an EIP-8141 policy question, out of scope here.
2. **Node-chosen admission margin = 0.** The spec permits ("SHOULD NOT") rejecting txs whose oldest reference is within a node-chosen margin of expiry. We admit up to age 8191 exactly; the head-change sweep evicts at 8192. No margin is applied.
3. **Direct evaluation vs execution.** Admission uses BOTH the direct predicate check (cheap reject) and full EVM simulation (exact gas + permission enforcement); head-change revalidation uses the direct check only (spec-permitted; the simulation result is a pure function of the same inputs given fixed code).
4. **XDC mainnet note.** On XDC chains Bogota is never scheduled, so frame txs (and thus this EIP) are inactive there by construction; all activation gates verified.
5. **Blob-carrying frame txs** remain rejected by the pool (pre-existing 8141 deferral, unchanged).
