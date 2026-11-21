# EIP-7906 Implementation Plan — Transaction Assertions via State Diff Opcode

**Spec**: https://eips.ethereum.org/EIPS/eip-7906 — status **Draft**, type Standards Track (Core),
created 2025-02-21, `requires: 2929, 8141`.
Pinned snapshot: `/home/tanmay/xdc-fix-work/eip-7906-spec-snapshot.md`
(sha256 `fcde21b02c0c7c04b9e787fbfb5bc33e9fa3cbef419b53b47d979a7451777fea`;
upstream last commit 2026-10-27, retrieved 2026-10-31 — no drift).

**Worktree**: `/home/tanmay/xdc-fix-work/eip-7906-assertions`
**Branch**: `feat/eip-7906-tx-assertions`
**Base**: commit `48a7727` + EIP-8141 M10 cumulative patch
(`backups-frame-tx/M10-audit-hardening.patch`, sha256
`665e77a2ea3299ee9625ce1e68be0768c2aa9e6e4b9dfb00a42087b47da8d4b1`,
byte-identical to the `frame-tx-8141` worktree staged tree).
Same base recipe as the EIP-8250 and EIP-8272 missions: each Hegotá EIP is
implemented independently on the clean 8141 base in its own worktree.

## 1. What the EIP defines (verified against the pinned spec)

| # | Requirement | Spec anchor |
|---|---|---|
| R1 | New frame `mode` value `ASSERT = 3`, accepted by EIP-8141's static mode constraint | Constants; The ASSERT Frame Mode |
| R2 | ASSERT frames form a contiguous trailing suffix of `tx.frames`; violation = invalid tx | ASSERT Frame Mode |
| R3 | ASSERT frame caller = `ENTRY_POINT` (0xaa) | ASSERT Frame Mode |
| R4 | ASSERT frames execute as `STATICCALL`; no APPROVE exception (APPROVE anywhere in an ASSERT subtree = ordinary staticcall violation = exceptional halt of that call frame) | ASSERT Frame Mode |
| R5 | Approval-scope flags valid on ASSERT (no effect); ATOMIC_BATCH flag invalid on ASSERT and on the frame directly before an ASSERT frame | ASSERT Frame Mode |
| R6 | ASSERT revert/exceptional halt: entire execution body reverted unconditionally; state + approval context restored to immediately after the payer-setting frame; overrides atomic-batch unrolling; no payer set → tx invalid | ASSERT Frame Mode |
| R7 | ASSERT failure ends frame execution; remaining ASSERT frames skipped: receipt status 2, no gas used, gas refunded at settlement | ASSERT Frame Mode |
| R8 | ASSERT failure never invalidates the tx; failing ASSERT receipt status 0; reverted body frames keep status + execution gas, logs discarded, receipt state gas removed; tx included | ASSERT Frame Mode |
| R9 | Validation-prefix state changes (gas payment, account creation) permanently committed; payer fully charged up to failure point | ASSERT Frame Mode |
| R10 | Default code: ASSERT handled same as SENDER/DEFAULT (empty-code target succeeds) | ASSERT Frame Mode |
| R11 | `TXTRACE` (0xb6): stack `[param(top), in2]` → value; one 256-bit result per call | Transaction Trace Opcode |
| R12 | TXTRACE param table 0x00–0x1A (counts, balance/slot/code/nonce entries, events, gas_pre_charge 0x19, gas_payer 0x1A) | param table |
| R13 | TXTRACE gas = active fork's `WARM_STORAGE_READ_COST` per call, every param (EIP-8038: `WARM_ACCESS` = 100) | Gas Cost |
| R14 | TXTRACE/EVENTDATACOPY valid only inside ASSERT frames; any other context (legacy/1559 txs, other frame modes) = exceptional halt | Transaction Trace Opcode |
| R15 | State-diff semantics: before = tx prestate; after = current state at call; collapsed multi-writes; storage-wipe slots not reported; code hash = stored-code hash (7702 indicator hashed itself); non-existent account = empty-code hash / nonce 0; EIP-6780(-family) end-of-tx deletions appear as the body left them; protocol-emitted logs (EIP-7708) count as events | State Difference Semantics |
| R16 | `EVENTDATACOPY` (0xb7): stack `[event_index(top), memOffset, dataOffset, length]`; copies event non-indexed data to memory; no output | Event Data Copy Opcode |
| R17 | EVENTDATACOPY gas = CALLDATACOPY (fixed 3 + memory expansion + per-word copy) | Event Data Copy Opcode |
| R18 | EVENTDATACOPY halts: `event_index >= events_count`; `dataOffset + length > data_len` (even when length == 0); addition without 256-bit wraparound | Behavior |
| R19 | Reserved `in2` must be 0 (params 0x00–0x03, 0x11, 0x19, 0x1A); non-zero = halt; undefined param (> 0x1A) = halt; out-of-range index = halt; all checked as full 256-bit values | Reserved Inputs; Undefined Params |
| R20 | Ordering: balances/slots/codes/nonces ascending by address (uint160); slots within address by key (uint256); events in emission order (= tx log index) | Results Ordering |
| R21 | Topic params 0x14–0x17 halt when the event lacks that topic; 0x18 = data byte length | param table |
| R22 | EIP-7928 BAL: ASSERT failure = reverted frames treated as reverted calls (changes discarded, accesses remain; written-then-restored slots in `storage_reads` unless prefix/other tx changed them); ASSERT frames' own accesses remain; prefix changes recorded at the tx's `block_access_index` | Block-Level Access List |
| R23 | TXTRACE/EVENTDATACOPY read recorded data only: no live-state reads, no EIP-2929 access-list interaction, no BAL additions | Gas Cost |

## 2. Dependency verification

| Dependency | Status in fork | Evidence |
|---|---|---|
| EIP-2929 (warm/cold access costs) | Present (long-standing) | `params.WarmStorageReadCostEIP2929` |
| EIP-8141 frame transactions | Implemented (M1–M10, in base patch) | `core/frame_transition.go`, `core/vm/frame.go`, `core/vm/instructions_frame.go`, `core/state/statedb_frame.go`, `core/types/tx_frame.go` |
| EIP-8037/8038 (2-D gas + repricing; `WARM_ACCESS`=100 naming) | Implemented | `core/vm/gascosts.go`, `params.WarmStorageReadCostEIP2929 = 100` |
| EIP-7928 BAL (R22 interaction) | Implemented | `core/types/bal`, `StateDB.Finalise` → `*bal.ConstructionBlockAccessList`, journal mutation stashes |
| EIP-7708 transfer logs (R15: protocol logs are events) | Implemented | `core/evm.go` Transfer emits `types.EthTransferLog` (Amsterdam) |
| EIP-7702 delegation code (codehash semantics) | Implemented | `core/types/tx_setcode.go`, `types.ParseDelegation` |
| EIP-7819 SETDELEGATE (spec mentions as code-change source) | **Absent** — not in this fork. codes_changed still covers every code-mutation path that exists (CREATE/CREATE2 deposit). No action. | grep: no SETDELEGATE in opcodes/eips |
| EIP-6780-family deletion | This fork implements **EIP-8264** (Amsterdam selfdestruct modification: deletion work happens in `Finalise`, after the last frame) — matches R15's "appears as the execution body left it" | `finaliseAmsterdam` in `core/state/statedb.go` |

## 3. Conflict analysis vs previously completed EIPs

**EIP-8141 (base)**: this EIP *amends* 8141 by design (new mode). Touch points:
mode validation (`ValidateStatic`), flag table (batch rules), frame dispatch
(engine), receipts (status reuse 0/1/2 — no new codes), opcodes (0xb6/0xb7
were free — 8141 allocated 0xaa + 0xb0–0xb5). Batch rule extension mirrors the
existing VERIFY rules. No 8141 behavior changes for non-ASSERT transactions.

**EIP-8250 (keyed nonces; sibling branch, not in this tree)**: no opcode or
mode-space collisions (8250 adds neither). Spec interaction is *favorable*: the
2026-10-27 upstream commit added "storage-kept replay protection appears in
`slots_changed` rather than `nonces_changed`" — the journal-based diff builder
surfaces NONCE_MANAGER storage writes automatically. When the branches are
eventually stacked, `ValidateStatic` is shared so the suffix rule flows into
8250's admission path unchanged. No code conflicts expected (8250 touches
payload decode + legacypool side pool; 7906 touches frame engine + vm opcodes).

**EIP-8272 (recent roots; sibling branch, not in this tree)**: no new opcodes;
its recent-root VERIFY frames live in the validation prefix, which an ASSERT
failure deliberately keeps committed (R9). 7906's suffix rule statically
forbids VERIFY frames after an ASSERT frame, so 8272's prefix grammar is
unaffected. Receipt shapes untouched by 8272. No conflicts expected.

## 4. Implementation design (mapped against this tree)

### M1 — ASSERT frame mode
- `core/types/tx_frame.go`: `FrameModeAssert = 0x3`; `ValidateStatic`:
  accept mode 3 (R1); suffix rule (R2, new error); batch flag on ASSERT →
  invalid (R5); batch flag directly before ASSERT → invalid (R5). Existing
  value-only-on-SENDER rule already covers ASSERT. Approval scope on ASSERT
  remains permitted (R5) — the existing batch-membership scope ban can never
  catch an ASSERT frame once the batch rules above hold.
- `core/frame_transition.go`:
  - Precompute `hasAssert`; only then track `payerSnapshot` = snapshot taken at
    the top of the first loop iteration after a *successful* frame leaves
    `fs.PayerSet` (prefix end, per 8141: a payer-setting frame that reverts
    does not end the prefix — its approval context is discarded).
  - `runFrame`: caller = ENTRY_POINT for ASSERT (R3); dispatch falls through to
    `callFrame` (R10 default-code behavior = SENDER/DEFAULT, i.e. ordinary
    call path; the VERIFY-only default-code branch is not taken).
  - `callFrame`: VERIFY **or ASSERT** → `StaticCall` (R4).
  - `runFrames`: ASSERT failure path (R6–R9): if `!fs.PayerSet` → revert to
    tx snapshot, return invalid (`ErrFrameTxNoPayer`). Else
    `RevertToSnapshot(payerSnapshot)` (restores state *and* journaled approval
    context + receipt state-gas writes), then: receipts of frames in
    `(payerIdx, i)` get `Logs = nil` (their journaled state gas is already 0
    post-revert; succeeded ASSERT frames in that range are unaffected — empty
    logs, zero state gas by construction); failing ASSERT keeps status 0 +
    execution gas (already written); frames `> i` marked skipped (status 2,
    zero gas, `FrameFinalized`), then break. Settlement already refunds
    zero-used frames (R7) and keeps prefix committed (R9).
  - Batch state is impossible at ASSERT frames (R5 static rules), so the
    unroll overrides batch handling by construction (R6).
- `core/vm/instructions_frame.go`: `opApprove` halts exceptionally when the
  current frame mode is ASSERT (R4) — APPROVE currently bypasses `readOnly`
  via the frame context, so the mode check is mandatory.
- Receipt/RPC/wire: no shape changes; status codes reused.

### M2 — TXTRACE (0xb6)
- `core/vm/opcodes.go`: `TXTRACE` const + both name maps (slot 0xb6, free —
  verified in this tree).
- `core/state/txtrace.go` (new): `TxTraceDiff` builder reading **journal
  internals** (this fork's journal already stashes pre-tx balance/nonce/code
  per account in `journal.mutations` for the 7928 BAL, and per-slot
  `origvalue` in `storageChange`):
  - balances: `mutations[addr].balanceSet` → before = stash, after =
    `GetBalance`; keep net changes only (same predicate as
    `recordAccessListChanges`).
  - nonces: `nonceSet` → before = stash, after = `GetNonce`.
  - codes: `codeSet` → before = hash of stashed code (empty →
    `EmptyCodeHash`; 7702 indicator hashed as stored), after = `GetCodeHash`
    normalized (non-existent/zero → `EmptyCodeHash`).
  - slots: walk live `journal.entries` for `storageChange`; first-seen
    `origvalue` per `(addr, key)` = before; after = `GetState`; net changes
    only. Reverted writes vanish with the journal (truncate on revert);
    write-back-to-original collapses via the net-change filter; wiped slots
    never generate per-slot entries → not reported (R15).
  - events: `s.logs[s.thash]` in emission order (includes 7708 transfer logs,
    R15); frozen during the ASSERT suffix (read-only).
  - ordering per R20 (address uint160 asc, slot uint256 asc).
  - built lazily on first TXTRACE/EVENTDATACOPY call, cached on the StateDB;
    valid for the whole ASSERT suffix because ASSERT frames cannot write
    state or emit logs, and an ASSERT failure ends execution (no later reads).
    Cache dropped on `SetFrameState(nil)` (tx boundary).
- `core/vm/instructions_assert.go` (new): `opTxTrace` — gate: no frame context
  or current frame mode != ASSERT → exceptional halt (R14); param dispatch
  table 0x00–0x1A (R12), reserved-zero / undefined / out-of-range checks as
  full 256-bit comparisons (R19), topic halts (R21).
  `gas_payer` (0x1A) returns `fs.Payer` (zero address when unset — spec is
  silent; tx is invalid anyway without a payer; documented decision).
  `gas_pre_charge` (0x19) = `fc.MaxCost` (EIP-8141 max_cost incl. blob fees,
  R12).
- `enable7906(jt)` called from `newBogotaInstructionSet` after `enable8141`;
  `constantGas = params.WarmStorageReadCostEIP2929` (= 100 = 8038 WARM_ACCESS,
  R13); `minStack(2,1) / maxStack(2,1)`; no dynamic gas, no memory.

### M3 — EVENTDATACOPY (0xb7)
- `core/vm/opcodes.go`: const + name maps (slot 0xb7).
- `opEventDataCopy` in `instructions_assert.go`: same ASSERT gate (R14);
  `memoryCopierGas(3)` (length at `back(3)`) + new `memoryEventDataCopy` =
  `calcMemSize64(back(1), back(3))`; halt checks per R18 with overflow-safe
  `dataOffset + length` (`AddOverflow` → exceeds → halt); exact copy (no
  zero-padding — out-of-range never copies).
- Registration: `constantGas = GasFastestStep` (3), `dynamicGas`,
  `memorySize`, `minStack(4,0)` (R16/R17).

### M4 — BAL (7928) interaction + edge semantics (R22, R15)
- The fork's BAL is built in `Finalise` from live journal mutations/access
  state *after* the tx: an ASSERT-failure rollback (`RevertToSnapshot`) removes
  reverted body changes from the journal, so they never reach the BAL as
  changes; accesses recorded during execution (access-list / access-events
  path) survive — matching "reverted calls" treatment. Verify by test against
  the constructed BAL (both the ASSERT-failure and all-success shapes).
- Edge tests: 7708 transfer log as event; created+destroyed contract visible
  (8264 defers deletion to Finalise); 7702-style stored-code hashing;
  multi-write collapse; write-back-to-original exclusion; sorting; gas
  exactness (100/call); non-ASSERT contexts halt (legacy tx, 1559 tx, VERIFY/
  DEFAULT/SENDER frames); nested sub-call inside ASSERT subtree allowed;
  no payer + ASSERT failure → invalid; skipped-ASSERT refund.

### Txpool / RPC / networking
- Pool admission calls `ft.ValidateStatic()` (`core/txpool/validation.go`) →
  new mode + suffix rule flow through automatically. No prefix-simulation in
  this tree's pool (conservative policy); ASSERT frames sit after the payer
  frame, outside prefix grammar. No txpool changes planned.
- Receipt/tx JSON, eth/69 wire, storage encodings: unchanged (no shape
  changes). `FRAMEPARAM_MODE` returns 3 automatically.

## 5. Milestones and checkpoints

| Checkpoint | Content |
|---|---|
| M0 | base 48a7727 + 8141-M10 (sha256-verified) |
| M1 | ASSERT frame mode + engine failure semantics + static rules + tests |
| M2 | TXTRACE + diff builder + tests |
| M3 | EVENTDATACOPY + tests |
| M4 | BAL interaction + edge semantics + tests; full regression sweep |

Per checkpoint: targeted tests green, package suites green,
`git diff --cached > backups-eip-7906/M<n>-*.patch` + sha256 recorded.

## 6. Regression/baseline discipline (carried from the 8272 mission)

- Baseline failure set at 48a7727+8141-M10 (12 pre-existing, name-identical
  across sweeps): `core/types` 4 block-encoding; `core/state` FuzzJournal;
  `core` TestProcessParentBlockHash + TestProcessUBT + TestTransientStorageReset
  (panickers — skipped in full-coverage core runs on BOTH trees) + 10 TestBAL*
  + TestGenesisHashes + TestSetupGenesis; `internal/ethapi` 5;
  `eth/protocols/eth` TestMessages.
- Full-coverage recipe: `go test -count=1 -skip
  'TestProcessParentBlockHash|TestProcessUBT|TestTransientStorageReset' ./core/`
  on base worktree and feature worktree; strip durations; diff failure NAMES.
- Build: `go build ./...`; static checks per repo AGENTS.md as far as
  applicable without committing (gofmt on touched files only; check_generate
  if gencodec-covered structs change — none planned).
