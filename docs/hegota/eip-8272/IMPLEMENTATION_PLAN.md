# EIP-8272 Implementation Plan — Recent Roots for Frame Transactions

Worktree: `/home/tanmay/xdc-fix-work/eip-8272-recent-roots`
Branch: `feat/eip-8272-recent-roots` (base commit 48a7727 + EIP-8141 M10 cumulative patch, sha256-verified
equal to `/home/tanmay/xdc-fix-work/backups-frame-tx/M10-audit-hardening.patch`).

Spec snapshot: `/home/tanmay/xdc-fix-work/eip-8272-spec-snapshot.md`
(upstream `ethereum/EIPs` master, last commit 2026-10-08, fetched 2026-10-30; status: Draft, requires 7843+8141).
Dependency spec snapshot: `/home/tanmay/xdc-fix-work/eip-8141-spec-snapshot.md` (last commit 2026-10-25).

## 1. Specification summary (normative)

EIP-8272 lets EIP-8141 frame transactions verify recent application roots through a canonical VERIFY
frame calling an ordinary deployed contract at `RECENT_ROOT_ADDRESS`. Root sources write entries keyed
by `(source_id, slot)` into a per-source ring buffer of 8192 slots. The public mempool gains a narrow
SLOTNUM/SLOAD permission for exactly this frame, plus dependency tracking and revalidation rules.

Constants (spec "Constants"):

| Name | Value |
| --- | --- |
| `RECENT_ROOT_ADDRESS` | `0x8272D9679689Ea2f307140CdF9002D27dC00Ffff` |
| `RECENT_ROOT_CODE` | 320-byte runtime code (spec "Bytecode") |
| `RECENT_ROOT_LENGTH` | 8192 |
| `RECENT_ROOT_USABLE_WINDOW` | 8191 |
| `MAX_RECENT_ROOT_REFERENCES` | 16 |
| `RECENT_ROOT_TUPLE_BYTES` | 72 |
| `RECENT_ROOT_ENTRY_DOMAIN` | `keccak256("RECENT_ROOT_ENTRY")` = `0x8f424816...617efc` |
| `RECENT_ROOT_STORAGE_DOMAIN` | `keccak256("RECENT_ROOT_STORAGE")` = `0xbdc897da...053daa` |

Encodings: fixed length; domains/roots/salts/source ids/entry hashes/storage keys 32 bytes; addresses 20
bytes; slots and ring indices uint64 big-endian (8 bytes).

Key derivations:
- `source_id = keccak256(source_address || salt)`
- `entry_hash = keccak256(ENTRY_DOMAIN || source_id || uint64_be(slot) || root)`
- `storage_key = keccak256(STORAGE_DOMAIN || source_id || uint64_be(slot mod 8192))`

Reference vector (spec "Reference vector", `current_slot = 2`) verified against a scratch Go program in
this session BEFORE any implementation: source_id / entry_hash / storage_key all reproduce, and
`crypto.CreateAddress(0x14bf16d4c9842bf1EbF396e553477C66EB0a8A82, 0)` equals `RECENT_ROOT_ADDRESS`.
The runtime bytecode was disassembled and matches the spec clause-for-clause (value guard, 64-byte write
dispatch, length rules `<72 / mod 72 / ==0 / >1152`, per-tuple `slot < current_slot`,
`current_slot - slot < 8192`, entry-hash/SLOAD compare loop, write path with CALLER-derived source_id,
two-keccak derivation, SSTORE, no logs/returns).

## 2. Dependency audit

### EIP-8141 (frame transactions) — IMPLEMENTED in this tree (M1..M10)

- `core/types/tx_frame.go`: `FrameTx`, `Frame{Mode, Flags, Target, Limits{Execution, State}, Value, Data}`,
  `ValidateStatic` (incl. expiry-frame structural rules), `ValidateSignatures`, `IntrinsicGas`, `MaxGas`.
- `core/frame_transition.go`: frame engine; VERIFY executes via `evm.StaticCall` (STATICCALL semantics);
  VERIFY failure invalidates the transaction; precompile-vs-default-code dispatch order fixed (M10).
- `core/vm/instructions_frame.go`: APPROVE/TXPARAM/FRAMEDATALOAD/FRAMEDATACOPY/FRAMEPARAM/SIGPARAM/SIGDATACOPY.
- `core/txpool/validation.go` `validateFrameTransaction`: stateless admission (Bogota gate, chain id,
  static constraints, signatures, 7825 cap, block-gas reservation, fee sanity).
- Existing gap (documented in FRAME_TRANSACTIONS_GAP_ANALYSIS.md): no validation-prefix simulation, no
  MAX_VERIFY_GAS/MAX_VERIFY_STATE_GAS caps, no public-mempool prefix grammar enforcement. EIP-8272's
  mempool MUSTs are implemented here for transactions carrying a recent-root verifier frame; the generic
  EIP-8141 prefix simulation remains a documented deferral (see Limitations).

### EIP-7843 (SLOTNUM) — IMPLEMENTED in this tree

- `core/vm/opcodes.go`: `SLOTNUM = 0x4b`; `core/vm/eips.go` `enable7843` (gas `GasQuickStep` = 2);
  wired into `newAmsterdamInstructionSet` (Bogota inherits it).
- `core/types/block.go`: `Header.SlotNumber *uint64` (+ JSON), `Block.SlotNumber()`.
- `core/evm.go`: `NewEVMBlockContext` maps `header.SlotNumber` → `BlockContext.SlotNum`.
- `miner/worker.go`, `miner/miner.go`, `miner/payload_building.go`: slot propagation into payload headers.
- EIP-8272's "MUST obtain current_slot from the slotNumber field, MUST NOT derive from timestamps" is
  satisfied structurally: all 8272 logic reads `Header.SlotNumber` / `BlockContext.SlotNum` only.

## 3. Architecture decisions

D1. **No consensus change.** The contract is ordinary bytecode; block execution already runs VERIFY frames
    through STATICCALL with full gas accounting. Position rules are public-mempool eligibility only
    (spec: "The fixed position affects public mempool eligibility, not block validity").
D2. **Constants + pure derivations in `core/types/recent_root.go`** (types already carries
    `ExpiryVerifierAddress`; imported by every consumer without cycles).
D3. **Stateful evaluation in `core/recent_root.go`**: direct tuple predicate checks against a
    `vm.StateDB`, plus a mempool admission simulation that executes the real `RECENT_ROOT_CODE` on the
    EVM with a permission tracer (real execution → identical gas/receipt/warm-set as block execution,
    satisfying the "direct evaluation MUST produce the same frame receipt and gas use" clause by
    construction). Simulation runs under `pool.mu` on `pool.currentState` with snapshot/revert (geth
    eth_call precedent; access-list warming is journaled and reverted).
D4. **Public-mempool policy in `core/txpool/validation_frame.go`**: validation-prefix classification
    implementing EIP-8141's four recognized shapes with the 8272 skip semantics (skip optional leading
    `expiry_verify` then `recent_root_verify`), MAX_VERIFY_GAS / MAX_VERIFY_STATE_GAS accounting, and the
    recent-root structural rules (at most one, fixed position, shape checks before sender state).
    Wired into `validateFrameTransaction` (stateless part) — enforced for frame txs that carry a
    recent-root verifier frame; other frame txs keep the existing conservative admission (scope
    containment: no behavior change for non-8272 transactions).
D5. **Dependency tracking in `core/txpool/legacypool/recent_roots.go`**: per-tx records
    (`(storage_key, entry_hash)` set, per-tuple slots, expiry slot = min(slot)+8192) plus a
    `storage_key → txhashes` index; registration at admission, atomic replacement, cleanup at every
    removal site, and a revalidation sweep on head change (slot advance + storage re-read + code /
    activation revalidation). Reorganization correctness comes from re-reading state at the new head
    (removed-branch entries vanish from state; added-branch writes appear).
D6. **Deployment is an ordinary transaction** (spec presigned legacy tx, sender
    `0x14bf16d4c9842bf1EbF396e553477C66EB0a8A82`, nonce 0). No predeploy, no genesis allocation, no
    activation state change. The deployment tx bytes are carried as a constant for tests.
D7. **Miner pre-inclusion revalidation**: `miner.commitTransaction` rechecks recent-root tuples against
    the payload's actual `SlotNumber` and state before executing (cheap predicate; the full engine run
    remains the authoritative check and would reject the tx anyway).

## 4. Requirement → code → test mapping

Status legend: Planned / Implemented / Verified / Blocked / N/A. (All requirements reached final status on 2026-10-30. This section was updated as work
proceeds; the report carries the final matrix.)

| # | Spec requirement | Spec ref | Implementation | Test | Status |
|---|---|---|---|---|---|
| 1 | Constants (address, lengths, domains) | Constants | `core/types/recent_root.go` | `TestRecentRootConstants` | Verified |
| 2 | Canonical runtime bytecode (exact) | Bytecode | `types.RecentRootCode` | `TestRecentRootCodeMatchesSpec` (byte equality + length 320) | Verified |
| 3 | source_id / entry_hash / storage_key derivations | Root sources, Entry and storage keys | `types.RecentRootSourceID/EntryHash/StorageKey` | reference vector + derivation tests | Verified |
| 4 | Write op: 64-byte calldata, salt+root, msg.sender source | Write operation | canonical bytecode executed via EVM | contract tests | Verified |
| 5 | Write: SLOTNUM slot, ring index = slot mod 8192 | Write operation | bytecode | `TestRecentRootWrite*` | Verified |
| 6 | Write: nonzero value reverts; static-context write fails | Recent root contract | bytecode + STATICCALL | `TestRecentRootWriteValueAndStatic` | Verified |
| 7 | Write: no return data, no logs | Write operation | bytecode | receipt/log assertions | Verified |
| 8 | Validation op: n*72 calldata, 1<=n<=16, reject 0/71/73/17 tuples | Validation operation | bytecode + `ParseRecentRootTuples` | boundary tests | Verified |
| 9 | Validation: slot < current_slot; current_slot - slot <= 8191 | Validation operation | bytecode | current/future/8191/8192-boundary tests | Verified |
| 10 | Validation: storage compare, revert on any failure; duplicates OK | Validation operation | bytecode | contract tests | Verified |
| 11 | Validation: no writes, no logs, no return data; SLOTNUM read; no calls; only derived keys | Validation operation | bytecode + tracer assertions | `TestRecentRootValidationSideEffects` | Verified |
| 12 | current_slot from EIP-7843 slotNumber (never timestamp) | Current slot | all code paths read `Header.SlotNumber`/`BlockContext.SlotNum` | grep-level + tests with SlotNumber-only heads | Verified |
| 13 | Mempool current_slot = latest header slotNumber + 1; simulated SLOTNUM equals it | Current slot | legacypool admission/revalidation | pool tests | Verified |
| 14 | Pre-inclusion revalidation at payload slotNumber | Current slot | `miner.commitTransaction` pre-check | miner-level test | Verified |
| 15 | Verifier frame shape: VERIFY, target, flags=0, value=0, state=0, len in [72,1152] multiple of 72 | Recent root verifier frame | `types.IsRecentRootVerifyFrame` | shape matrix tests | Verified |
| 16 | Frame executes via STATICCALL semantics; revert/halt ⇒ tx invalid (block) | Recent root verifier frame | existing engine (`callFrame`) | engine test incl. pre-deployment default-code revert | Verified |
| 17 | No envelope change, no new opcode, no gas/warming exemptions | Recent root verifier frame | none added | grep-level + existing regression | Verified |
| 18 | Application introspection via FRAMEPARAM/FRAMEDATALOAD/FRAMEDATACOPY | Application introspection | existing 8141 opcodes | engine test reading frame data from account-validation code | Verified |
| 19 | Mempool: ≤1 recent-root verifier frame | Public mempool handling | `ValidateFramePublicMempool` | duplicate rejection test | Verified |
| 20 | Mempool: position immediately after optional expiry_verify, before all others | Public mempool handling | prefix classifier | ordering matrix tests | Verified |
| 21 | Mempool: prefix-shape matching skips both protocol verifier frames; deploy-first applies after skip | Public mempool handling | prefix classifier | 4 shapes × {none, rr, expiry, expiry+rr} | Verified |
| 22 | Mempool: shape/encoding checks before reading sender state | Public mempool handling | ordering inside `validateFrameTransaction` | code-order audit + tests | Verified |
| 23 | Mempool: reject if code at RECENT_ROOT_ADDRESS ≠ RECENT_ROOT_CODE | Public mempool handling | legacypool stateful admission | wrong-code/missing-code tests | Verified |
| 24 | Mempool: SLOTNUM permission only inside canonical top-level code | Public mempool handling | admission tracer | tracer unit tests (incl. nested-call denial) | Verified |
| 25 | Mempool: SLOAD permission only for derived keys at RECENT_ROOT_ADDRESS, top level | Public mempool handling | admission tracer | tracer unit tests | Verified |
| 26 | Mempool: frame must execute successfully; insufficient limits.execution ⇒ reject | Public mempool handling | EVM admission simulation with declared budget | boundary gas tests (exact, ±1), 16-tuple cold-access case | Verified |
| 27 | Mempool: limits.execution counts toward MAX_VERIFY_GAS (100k), state toward MAX_VERIFY_STATE_GAS (500k) | Public mempool handling + EIP-8141 | prefix accounting | exact-boundary and +1 tests | Verified |
| 28 | Mempool: reject admission if any tuple slot >= current_slot | Public mempool handling | legacypool stateful admission | admission tests | Verified |
| 29 | Mempool: evict pending tx if any tuple slot >= current_slot; or when current_slot - slot >= 8192 | Public mempool handling | revalidation sweep | slot-advance tests | Verified |
| 30 | Mempool: SHOULD margin on admission | Public mempool handling | node margin = 0 (documented) | doc | Verified |
| 31 | Dependencies: record distinct (storage_key, entry_hash), first expiring current_slot, code dep, activation dep; index by key | Public mempool handling | legacypool dep records | registration tests | Verified |
| 32 | Dependency cleanup on eviction/replacement/inclusion; atomic replacement | Public mempool handling | dep helpers at all removal sites | lifecycle tests | Verified |
| 33 | Revalidation on any dependency change; both reorg branches; removed-entry-without-replacement | Public mempool handling | head-change sweep re-reads state | reorg tests (remove/replace branch) | Verified |
| 34 | Code/activation change of RECENT_ROOT_ADDRESS ⇒ revalidate all recent-root txs | Public mempool handling | sweep code check + Bogota-flip eviction | tests | Verified |
| 35 | Deployment: ordinary deployment, exact init code/tx, exact address, empty initial storage | Deployment | constant + tests recovering sender/address, executing init | `TestRecentRootDeployment*` | Verified |
| 36 | Activation at/after 8141+7843; no state change at activation; pre-deployment default-code revert | Activation | Bogota gate (superset), engine default-code path | pre-deployment tests | Verified |
| 37 | Reference vector end-to-end | Reference vector | contract test at current_slot=2 | `TestRecentRootReferenceVector` | Verified |

## 5. Test plan (suites)

- `core/types/recent_root_test.go` — constants, derivations, reference vector, tuple parsing, frame
  classification, deployment-tx sender/address recovery.
- `core/recent_root_test.go` — contract execution (write/validate, all bytecode-level cases), stateful
  evaluation helpers, tracer permission scoping, deployment execution producing exact runtime code and
  empty storage, pre-deployment default-code revert, introspection.
- `core/txpool/validation_frame_test.go` — extended: prefix grammar × protocol-frame combos,
  MAX_VERIFY_GAS/STATE_GAS boundaries, placement/duplicates.
- `core/txpool/legacypool/recent_roots_test.go` — admission (stateful), dependency registration /
  replacement / eviction / inclusion cleanup, slot advancement, reorganization, activation flip.
- Regression: `core/types`, `core` (frame/eip8037 suites), `core/vm`, `core/txpool/...`,
  `core/state`, `eth/protocols/eth`, `internal/ethapi`, `miner` — failure-set diff vs pristine base
  (13 known pre-existing failures at 48a7727; see FRAME_TRANSACTIONS_IMPLEMENTATION.md discipline).

## 6. Limitations (declared up front)

- Generic EIP-8141 validation-prefix simulation for non-protocol frames (self_verify/pay EVM
  simulation, paymaster accounting, sender-storage slot indexing) remains the documented M7/M10
  deferral; EIP-8272 does not expand it. Recent-root txs get: structural grammar + signature checks +
  recent-root frame simulation; other prefix frames are validated statically.
- blob-carrying frame txs remain pool-rejected (M7 policy).
- Activation remains unscheduled for XDC chains (block-based forks; documented for maintainers).
