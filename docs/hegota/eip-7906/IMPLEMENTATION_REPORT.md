# EIP-7906 Implementation Report — Transaction Assertions via State Diff Opcode

**Branch**: `feat/eip-7906-tx-assertions` — worktree `/home/tanmay/xdc-fix-work/eip-7906-assertions`
**Base**: main @ `48a7727` + staged EIP-8141-M10 patch (identical layering to the EIP-8250/EIP-8272 sibling worktrees; the staged tree was verified sha256-equal to the reference EIP-8141 worktree's staged patch before any EIP-7906 edit).
**Spec**: https://eips.ethereum.org/EIPS/eip-7906 (Draft) — pinned snapshot `eip-7906-spec-snapshot.md`, sha256 `fcde21b0f6cb92292c97e31e1546a2b7b50e0897748950b03e0ae8f7b3a777fea`. Upstream last touched 2026-10-27 (before pinning).

## 1. What was built

EIP-7906 extends EIP-8141 frame transactions with an `ASSERT` frame mode plus
two assertion opcodes, `TXTRACE` (0xb6) and `EVENTDATACOPY` (0xb7), that expose
the transaction's state diff and event list to read-only assertion frames.

### Files created

| File | Content |
|---|---|
| `core/state/txtrace.go` | `TxTraceDiff` builder + per-tx cache on `StateDB` |
| `core/vm/instructions_assert.go` | `opTxTrace`, `opEventDataCopy`, gas/memory fns, `enable7906` |
| `core/types/tx_frame_assert_test.go` | static validity tests (ASSERT suffix/batch/scope/value rules) |
| `core/frame_tx_assert_test.go` | ASSERT frame mode engine tests |
| `core/frame_tx_assert_opcode_test.go` | TXTRACE/EVENTDATACOPY opcode tests |
| `core/frame_tx_assert_bal_test.go` | EIP-7928 BAL interaction + deletion-visibility tests |
| `docs/hegota/eip-7906/*.md` | plan / report / test evidence |

### Files modified

| File | Change |
|---|---|
| `core/types/tx_frame.go` | `FrameModeAssert = 0x3`; `ValidateStatic` accepts mode 3, enforces the trailing-suffix rule, rejects batch flags on/before ASSERT frames; new sentinel errors |
| `core/types/tx_frame_test.go` | one existing case adjusted: mode `3` is now valid (EIP-7906 amends the EIP-8141 mode constraint), the invalid-mode case uses `4` |
| `core/frame_transition.go` | payer-snapshot tracking after the validation prefix; `unrollAssertFailure` (EIP-7906 ASSERT-failure semantics); ASSERT dispatches as STATICCALL from ENTRY_POINT |
| `core/vm/frame.go` | `fc.Approve` halts exceptionally inside ASSERT frames (no APPROVE exception) |
| `core/vm/opcodes.go` | `TXTRACE = 0xb6`, `EVENTDATACOPY = 0xb7` + both name maps |
| `core/vm/jump_table.go` | `enable7906` in the Bogota instruction set |
| `core/state/statedb.go` | `txTraceDiff` cache field on `StateDB` |
| `core/state/statedb_frame.go` | `SetFrameState` drops the cached diff at frame-engine boundaries |

No mempool changes: the pool's conservative frame-tx admission only validates
the statically-declared validation prefix; ASSERT frames are execution-body
suffix frames and flow through `ValidateStatic` automatically.

## 2. Technical decisions

### 2.1 Diff construction — journal-derived, zero new tracking

The TXTRACE diff is computed **lazily from the state journal** at first use
inside an ASSERT frame and cached for the rest of the transaction:

- **Balances**: first `prev` value per account among *live* `balanceChange`
  journal entries (reverted entries are truncated away; a post-revert rewrite
  re-journals against the reverted — i.e. original — value).
- **Nonces / codes**: the per-account journal mutation stash
  (`journalMutationState`, maintained for the EIP-7928 BAL) holds the pre-tx
  nonce and raw pre-tx code; the code-before hash is `keccak256(prevCode)` or
  `EmptyCodeHash`, i.e. the hash is computed **over the stored bytes**, which
  also covers any delegation-indicator-shaped code verbatim.
- **Storage slots**: first `origvalue` (committed pre-tx value) per
  (address, slot) among live `storageChange` entries.
- **Events**: `s.logs[s.thash]` — the transaction's surviving logs in emission
  order (includes EIP-7708 transfer logs; logs of reverted frames are gone with
  the journal truncation).
- **Net-change collapse**: each table entry is included only when before ≠
  after, so a slot written twice yields one entry and a value written back to
  its original vanishes — the spec's "collapsed, rebase-free" semantics.

This matches the spec rationale ("Clients already track per-transaction code
and nonce changes for the EIP-7928 block-level access list") and requires no
new instrumentation of state-mutation paths.

**Deliberately not done**: wiring the journal's dormant `stashBalance` helper.
It is never called in this fork, and calling it would activate the currently
dead balance branch of the EIP-7928 `recordAccessListChanges`, changing BAL
(consensus) output. Balance diffs are derived from live journal entries
instead. (The fork's missing BAL balance entries are a **pre-existing**
baseline failure — see Test Evidence.)

**Cache-validity invariant**: the cached diff is only ever read inside the
ASSERT suffix, which is read-only (STATICCALL enforcement across the subtree)
and terminal (a failure ends frame execution), so no state or log change can
occur between builds/reads. The cache is dropped at frame-engine boundaries
(`SetFrameState`).

### 2.2 ASSERT failure semantics

On an ASSERT frame failure (revert or halt), the engine:

1. Reverts state to the snapshot taken **immediately after the frame whose
   execution set payer** (the end of the EIP-8141 validation prefix) — the
   whole execution body is discarded, overriding atomic-batch unrolling
   (batches cannot contain or directly precede ASSERT frames statically, and
   a body batch is reverted as part of the body).
2. Keeps the reverted body frames' receipt status and execution gas, but
   discards their logs and removes their recorded state gas (the journaled
   per-frame state-gas entries are restored by the same revert; the receipt
   structs are re-pointed at zero logs explicitly).
3. Marks the remaining ASSERT frames skipped (status 2, zero gas → refunded at
   settlement).
4. Keeps the transaction valid: the payer is charged for all consumed gas.
   If no payer was ever set, there is nothing to restore and the transaction
   is invalid (`ErrFrameTxNoPayer`), per spec.

The transaction-level receipt log union shrinks accordingly because the
journal revert truncates the tx logs.

### 2.3 Opcode gating and gas

- Both opcodes are registered only in the Bogota instruction set. Executing
  them outside an ASSERT frame — legacy/EIP-1559 transaction, or DEFAULT /
  VERIFY / SENDER frame mode, at any call depth — is an exceptional halt.
- TXTRACE: flat `WarmStorageReadCostEIP2929` (100) per call (the EIP-8038
  WARM_ACCESS of the spec's gas table); no dynamic component.
- EVENTDATACOPY: `GasFastestStep` (3) + CALLDATACOPY-style dynamic gas
  (`memoryCopierGas(3)`: memory expansion + 3/word) with the length operand at
  stack position back(3). Source-range checks (`event_index < events_count`,
  `dataOffset + length <= dataLen` evaluated **without 256-bit wraparound**,
  checked even for `length == 0`) halt exceptionally.
- Selectors/indices are validated as full 256-bit values (no truncation);
  reserved-zero inputs must be zero; undefined params (> 0x1A) halt.

### 2.4 APPROVE inside ASSERT

`fc.Approve` checks the current frame's mode and produces an exceptional halt
for ASSERT frames — an ordinary STATICCALL violation of that call frame, as
the spec requires (the EIP-8141 VERIFY exception does not extend to ASSERT).

## 3. Spec deviations / clarifications adopted

- **`GAS_PAYER` (0x1A) before any payer approval**: the spec does not define
  the value; the implementation returns the zero address. (Such a transaction
  is invalid at settlement anyway; ASSERT frames can only follow the
  payer-setting frame in a valid transaction.) Documented in the plan.
- **EIP-8264 vs EIP-6780 wording**: the spec references EIP-6780
  end-of-transaction deletion. This fork implements EIP-8264 (Amsterdam) —
  selfdestruct marks the account and moves the balance immediately; nonce/code
  /storage clearing happens at `Finalise`, i.e. after the last frame. TXTRACE
  therefore sees exactly what the spec intends ("the account appears as the
  execution body left it"). Verified by test.

## 4. Non-applicable spec edges on this fork

- **EIP-7819 SETDELEGATE** does not exist in this fork, and EIP-3541 rejects
  CREATE deposits of 0xEF-prefixed code, so no in-transaction path installs a
  delegation indicator within a frame transaction. The codes table still
  hashes stored bytes verbatim (keccak over raw stored code), which is the
  spec'd behaviour for indicators should a future fork add 7819.
- **Witness write-through storage wipes** (the spec's known-diff-completeness
  caveat for stateless-validation clients): this fork has no witness
  write-through layer in the transaction execution path, so the described
  silent-drop scenario cannot occur; the journal-derived diff is complete.

## 5. Limitations

- The fork's BAL is missing **balance** change entries at baseline
  (pre-existing: 10 `TestBAL*` failures, unrelated to this work; the journal
  balance stash is never armed). EIP-7906 tests therefore assert BAL nonce /
  storage / account-presence semantics, not BAL balance entries. TXTRACE's own
  balance table is unaffected (journal-derived).
- `TXTRACE` inside `eth_call`-style simulations of frame transactions works
  identically (same engine); no RPC surface was added or changed (the spec
  defines none).
- XDC mainnet/testnet chains never schedule Bogota, so none of this code is
  reachable there (fork-gated); no XDC-consensus surface was touched.

## 6. Test summary

Detailed commands and outputs in `TEST_EVIDENCE.md`.

- 9 static-validity subtests (core/types).
- 25 EIP-7906 engine/opcode/BAL tests (core), all green.
- Regression: failure sets in `core`, `core/types`, `core/vm`, `core/state`,
  `core/txpool/...`, `internal/ethapi/...`, `eth/protocols/eth/...`, `miner/...`
  compared by normalized test name against the EIP-8141-M10 base tree —
  **zero new failures**.
- `go build ./...` clean; `go vet ./core/...` clean; `gofmt` delta vs base: none.

## 7. Checkpoints

| Milestone | Patch | SHA256 |
|---|---|---|
| M0 base (48a7727 + 8141-M10) | `M0-base-8141-m10.patch` | `665e77a2ea3299ee9625ce1e68be0768c2aa9e6e4b9dfb00a42087b47da8d4b1` |
| M1 ASSERT frame mode | `M1-assert-frame-mode.patch` | `1f500c96584b36246dd5fde4a38fd6e217871afde986f6147345dfc54ea479de` |
| M2+M3 TXTRACE + EVENTDATACOPY | `M2-M3-txtrace-eventdatacopy.patch` | `d80d66d5a6253eff57a4d31d30b1d037d67d232df1047b6e508197602414a991` |
| M4 BAL + edge semantics | `M4-bal-and-edge-semantics.patch` | `549f9f0a51db5d98ada2d17990fc83938c1a9dcbbd8e594ed9cd192ffc0c0b34` |
| FINAL (docs) | `FINAL-eip-7906.patch` | recorded in `FINAL-eip-7906.sha256` alongside the patch |

All checkpoints live in `/home/tanmay/xdc-fix-work/backups-eip-7906/` with
matching `.sha256` files.

## 8. Git safety state

- Main checkout `/home/tanmay/xdc-fix-work/go-ethereum` untouched (clean at 48a7727).
- EIP-8141 / EIP-8250 / EIP-8272 worktrees untouched.
- All work staged (`git apply`-equivalent state) on branch
  `feat/eip-7906-tx-assertions`; **nothing committed, pushed, or merged**.
