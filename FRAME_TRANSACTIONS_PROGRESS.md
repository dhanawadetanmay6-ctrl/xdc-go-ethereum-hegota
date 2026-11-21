# Frame Transactions (EIP-8141) on XDC — Progress

Status date: 2026-10-28
Workspace: /home/tanmay/xdc-fix-work/frame-tx-8141 (git worktree)
Base repo: /home/tanmay/xdc-fix-work/go-ethereum
Branch: feat/eip-8141-frame-transactions
Base commit: 48a7727 (xone-fix-20261025 HEAD: baseline snapshot + XDPoS V2 shadow-sibling heal)
Spec: https://eips.ethereum.org/EIPS/eip-8141 — status DRAFT (created 2026-01-29).
      Local copy of the spec snapshot used: /tmp/eip-8141.md (fetched 2026-10-28 from ethereum/EIPs master).

## Safety posture (mandatory)

- LOCAL DEVELOPMENT ONLY. No mainnet/deploy/push operations.
- No commits without explicit user approval (per task brief §3). Milestone
  checkpoints are instead snapshotted as patch files under
  /home/tanmay/xdc-fix-work/backups-frame-tx/.
- The pre-existing checkout /home/tanmay/xdc-fix-work/go-ethereum and its two
  branches (master, xone-fix-20261025) are untouched; all work happens in the
  isolated worktree above.
- XDC mainnet/apothem chain configs must NOT gain activation values for any
  new fork gate. Activation design is documented, not scheduled.

## Completed milestones

### M0 — Repository safety + reconnaissance (2026-10-28)

- Confirmed previous Frame Transactions work is NOT recoverable: no branches,
  stashes, reflog entries, FRAME_TRANSACTIONS* docs, or frame/EIP-8141 code in
  either local XDC go-ethereum fork (xdc-xdpos-debugging-exercise,
  xdc-fix-work). Started from scratch.
- Chose xdc-fix-work/go-ethereum as base (fixed lineage, modern geth + XDPoS,
  no remote = no accidental push). Exercise repo ruled out: its main carries
  the seeded-bug exercise baseline; active branch holds unrelated uncommitted
  work.
- Created worktree + branch feat/eip-8141-frame-transactions at 48a7727.
  `git status` clean; baseline `go build ./core/... ./params/...` passes with
  Go 1.26.8 (/home/tanmay/go-sdk/go/bin).
- Read the full EIP-8141 spec (1565 lines). Key facts pinned:
  FRAME_TX_TYPE=0x06, FRAME_TX_INTRINSIC_COST=12000, FRAME_TX_PER_FRAME_COST=475,
  ENTRY_POINT=0xaa, EXPIRY_VERIFIER=0x81413f0cF12e9b6a49B1D0439E081c577D57FfFf,
  MAX_FRAMES=64, signature schemes ARBITRARY/SECP256K1/P256, new opcodes
  APPROVE(0xaa), TXPARAM(0xb0), FRAMEDATALOAD(0xb1), FRAMEDATACOPY(0xb2),
  FRAMEPARAM(0xb3), SIGPARAM(0xb4), SIGDATACOPY(0xb5), new per-frame receipt
  format, two-dimensional per-frame gas (limits.execution/limits.state),
  mempool validation-prefix policy.

### Codebase mapping (Phase 2) — key findings

- Dependency layer ALREADY IMPLEMENTED (Hegotá stack), gated on
  params.Rules.IsAmsterdam / IsBogota:
  - EIP-8037 two-dimensional gas: core/vm/gascosts.go (GasBudget{RegularGas,
    StateGas, UsedRegularGas, UsedStateGas, Spilled}), Charge/Forward/Absorb/
    Exit*/RefundState; state_transition.go reservoir split
    (initRuntimeGasBudget), GasPool.CheckGasAmsterdam/ChargeGasAmsterdam.
  - EIP-2780 intrinsic decomposition: intrinsicBaseGasEIP2780
    (state_transition.go:163), params.TxBaseCost2780=12000,
    TxValueCost2780=4244 (NOTE: spec table says 6000; local pinned value is
    4244 — provisional benchmark targets differ between drafts).
  - EIP-7976 calldata floor: FloorDataGas (state_transition.go:199),
    TxCostFloorPerToken7976, TxTokenPerNonZeroByte.
  - EIP-7825 cap: params.MaxTxGas = 1<<24.
  - EIP-7708 transfer log: types.EthTransferLog, emitted in core/evm.go:169.
  - P256VERIFY precompile: core/vm/contracts.go (PrecompiledContractsP256Verify),
    params.P256VerifyGas.
  - EIP-7702 delegation: types.ParseDelegation / AddressToDelegation.
- NOT implemented (the EIP-8141 surface itself):
  - No FrameTx type (core/types has Legacy/AccessList/DynamicFee/Blob/SetCode
    only; dispatch in transaction.go decodeTyped, constants at :47-53).
  - No APPROVE/TXPARAM/FRAME*/SIG* opcodes (0xaa, 0xb0-0xb5 are FREE in
    core/vm/opcodes.go).
  - No frame execution engine (state_transition.execute runs a single
    top-level call/create).
  - No per-frame receipt format.
- Fork gating: Bogota = this fork's Hegotá-equivalent (bogotaInstructionSet,
  core/vm/jump_table.go:71; core/vm/evm.go:165). XDC chains (IsXDC, XDPoS
  engine set) use BLOCK-based forks (Eip1559Block, CancunBlock, PragueBlock);
  Amsterdam/Bogota are timestamp-based and are nil on XDC mainnet/apothem
  configs — i.e. the Hegotá stack is NOT active on XDC chains today.
  Rules assembly: params/config.go:3595 (Rules()), struct at :3566.
- XDC specifics to preserve: TIPTRC21Fee owner fee routing
  (state_transition.go:818), special sign txs (types.IsSpecialTx),
  BigBalance handling in buyGas, EIP-2028 disabled for XDC intrinsic gas
  (state_transition.go:723-726), EIP-2929 deferred to Eip1559Block on XDC.

## Current milestone

M2 — Sender/signature integration: types.Sender() must resolve the explicit
tx.sender field without ECDSA recovery; JSON marshalling; txpool-adjacent
accessor semantics.

### M1 — Frame transaction type (DONE 2026-10-28)

- core/types/tx_frame.go (678 lines): FrameTx/Frame/FrameGasLimits/
  FrameSignature/FrameFees types; RLP per spec; TxData interface impl;
  SigHash with empty-msg elision (length-committed); ValidateStatic (all spec
  constraints incl. atomic-batch structural rules + EIP-7825 cap);
  IntrinsicGas/MaxGas (spec formulas); ValidateSignature(s) for
  SECP256K1 (v||r||s, low-s) / P256 (r||s||qx||qy, low-s,
  signer==keccak(qx||qy)[12:]) / ARBITRARY (structural).
- core/types/transaction.go: decodeTyped dispatch case FrameTxType.
- params/protocol_params.go: TxStandardTokenCost=4 (EIP-7976).
- core/types/tx_frame_test.go (427 lines): round-trip, hash/sighash elision,
  19 static-constraint cases, hand-computed intrinsic/floor/max-gas, real-key
  secp256k1 + P256 vectors (incl. high-s / wrong-key / wrong-digest rejects).
- Tests: all TestFrameTx* PASS. Full core/types: 4 pre-existing baseline
  failures (TestBlockEncoding, TestEIP1559BlockEncoding, TestEIP2718BlockEncoding,
  TestEIP4844BlockEncoding — XDC header RLP vs upstream fixtures; confirmed
  failing on untouched base 48a7727). ZERO new failures.
- Divergence noted: TxValueCost2780=4244 in this fork (spec table lists 6000;
  EIP-2780 values are provisional per spec rationale; we use the fork's pin).
- Checkpoint: /home/tanmay/xdc-fix-work/backups-frame-tx/M1-frame-tx-type.patch
  (cumulative diff snapshots; tree kept staged, no commits without approval).

## Test log

- 2026-10-28 baseline: `go build ./core/... ./params/...` OK (Go 1.26.8).
- 2026-10-28 M1: `go test ./core/types/ -run TestFrameTx` PASS (all 7 funcs);
  `go build ./...` OK; core/types full run: 4 baseline failures, 0 new.

### M3 — Frame execution engine (DONE 2026-10-28)

- core/state/statedb_frame.go (224 lines): journaled FrameState — approval
  context (SenderApproved/PayerSet), per-frame receipt state-gas, SSTORE
  state-gas ownership; three journalEntry kinds; StateDB.Copy propagation.
- core/vm/gascosts.go: GasBudget.NoSpill (strict regular/state separation
  inside frames; EIP-8141 overrides 8037 spillover), propagated to child
  budgets at all 4 opcode-level NewGasBudget sites in instructions.go.
- core/vm/frame.go: FrameTxContext + frameStateDB interface + shared
  Approve() (scope checks, sender-approval, payment escrow incl. sender
  creation state-gas charge).
- core/vm/{opcodes,jump_table,instructions_frame}.go: APPROVE (0xaa) in
  Bogota set; exceptional halt outside frame txs.
- core/frame_transition.go (618 lines): full engine — static → signatures →
  fee budget (required_max_fee/max_priority_fee/max_cost/GASPRICE) → 2D
  block reservation → frame loop (entry access charge, default-code VERIFY,
  expiry constraints, STATICCALL VERIFY, per-frame NoSpill budgets, per-frame
  log capture, atomic-batch snapshot/unroll/skip) → settlement (tx-scoped
  3529 refund, 7976 floor on execution component, 7778 pre-refund block
  accounting, payer refund, tip to coinbase, base burned).
- core/state_transition.go + core/state_processor.go: dispatch, Message
  wiring, ExecutionResult{Payer, FrameReceipts}, MakeReceipt (frame type,
  no ContractAddress derivation).
- core/vm/gas_table.go: SSTORE ownership journaling + cross-frame refill
  (2.2.2.1 refunds the creating frame's receipt, not the clearer's).
- Bugs fixed during bring-up: SignFrameTx returns a new tx (builder must
  use it); batch unroll needs index-loop skip (Go range semantics).
- Tests: core/frame_tx_test.go — 13/13 PASS (self-verify transfer,
  unapproved sender, no-payer, verify failure, contract sender / 3607 skip,
  revert isolation, atomic batch, cross-frame state refund, state-gas
  separation incl. full-pool halt, fee-settlement math, pre-Bogota reject,
  nonce replay, APPROVE-outside-frame halt). Full core suite: only the 2
  pre-existing failures (TestProcessParentBlockHash, TestProcessUBT —
  byte-identical on pristine base). core/state: only pre-existing
  FuzzJournal. core/vm: PASS.
- Checkpoint: backups-frame-tx/M3-frame-engine.patch (22 files, +3522).

### M5 — Introspection opcodes (DONE 2026-10-28)

- TXPARAM/FRAMEDATALOAD/FRAMEDATACOPY/FRAMEPARAM/SIGPARAM/SIGDATACOPY
  (0xb0–0xb5) implemented to the spec's parameter tables (verified against
  fetched spec text — caught ID drift in earlier notes). Halt semantics:
  outside frame txs, OOB indices, completed-frame-only status/gas reads,
  ARBITRARY-only signature introspection. resolved_signer = signer-metadata
  or tx.sender for both schemes (spec-confirmed).
- Note: spec scheme table is ARBITRARY=0x0, SECP256K1=0x1, P256=0x2 (test
  expectation corrected to match).
- Tests: core/frame_tx_opcode_test.go — 12/12 PASS incl. hand-assembled
  probe bytecode, halt cases, sig data copy.
- Checkpoint: backups-frame-tx/M5-introspection-opcodes.patch (23 files,
  +4170 cumulative).

### M6 — Receipts (DONE 2026-10-28)

- Consensus encoding: typed receipt 0x06 || rlp([cumulative, payer,
  [[status, [exec, state], logs]...]]) — byte-level verified against the
  spec shape. EncodeRLP/MarshalBinary/decodeTyped/EncodeIndex (DeriveSha)
  all handle it. Golden trie root pinned (0x669d2ef6…) + decode/re-encode
  invariance.
- Storage encoding: [status, cumulative, consensusPayload] — chosen for
  eth/69 wire compatibility; content-discriminated decode vs legacy
  [status, cumulative, logs] (payload non-empty + starts with string).
- DeriveFields: union logs rebuilt from frames, block-wide log indices,
  bloom, derived top-level status; ContractAddress skipped for frame txs.
- RPC: MarshalReceipt adds payer + frameReceipts[] (frameIndex, status,
  gasUsedExecution, gasUsedState, logs); top-level logs = union.
- Block integration test: GenerateChain → Process → receipt trie matches
  header → rawdb write/read round-trip → state effects (balance, nonce).
  NOTE: InsertChain is bypassed — the chain-maker harness has a
  PRE-EXISTING BAL-hash asymmetry on Amsterdam-configured generated chains
  (reproduced with a plain legacy DynamicFeeTx on the pristine base:
  identical mismatch hashes).
- Tests: core/types/frame_receipt_test.go (round-trip, storage, legacy
  shape, DeriveFields), frame_receipt_trie_test.go (golden root),
  core/frame_tx_integration_test.go — all PASS. internal/ethapi receipt
  tests: 2 failures identical on pristine base (pre-existing).
- Checkpoint: backups-frame-tx/M6-frame-receipts.patch (27 files, +4863).

### M7 — Txpool (DONE 2026-10-28)

- legacypool Accept bitmap + frame branch in txpool.ValidateTransaction:
  Bogota gate, chain-id binding, ValidateStatic, ValidateSignatures,
  EIP-2681, 2D reservation vs block gas limit, min-tip, fee sanity,
  blob-carrying frame txs rejected (blobpool support deferred).
- Transaction.Cost() frame branch: max_fee + Σ frame values (exact
  worst-case; legacy gas*price division would under-count).
- validateAuth: delegation/authority limits skipped for frame txs (sender
  code = validation logic, not 7702 delegation).
- Conservative policy documented (no validation-prefix simulation,
  no ERC-7562 tracing; sender-pays-worst-case affordability) — see
  IMPLEMENTATION.md §4 and decision D2.
- Tests: core/txpool/validation_frame_test.go — 2/2 PASS (7 negative
  cases + positives, stateless + stateful). Full txpool suites:
  txpool/legacypool/blobpool/locals/txorder ALL PASS.
- Checkpoint: backups-frame-tx/M7-txpool-admission.patch.

### M8 — Networking (DONE 2026-10-28)

- Audit: tx propagation is generic typed-RLP (no change needed);
  NewPooledTransactionHashes does no type validation (length checks only);
  pre-Bogota peers drop unknown type 0x06 on decode (standard fork-boundary
  behavior). XDPoS2 legacy receipt path decodes types.Receipts directly —
  works unchanged.
- REAL DEFECT FOUND + FIXED: eth/69 compressed receipt wire shape
  [txType, status, gasUsed, logs] cannot represent frame receipts —
  DeriveSha over wire-fetched receipts would mismatch the block header and
  break sync. Fix: frame receipts carry the full consensus payload in the
  compressed logs slot; encodeForHash re-emits 0x06||payload verbatim.
  Storage form [status, cumulative, payload] makes EncodeForStorage and
  blockReceiptsToNetwork work without further changes.
- Tests: eth/protocols/eth/frame_receipt_test.go — wire encode/decode →
  root equality → EncodeForStorage → storage decode fidelity → frame root
  invariance. PASS. Full eth/protocols/eth suite run in final sweep.
- Checkpoint: backups-frame-tx/M8-networking-wire.patch (32 files, +5269).

### M9 — XDC activation design (DONE 2026-10-28)

- FRAME_TRANSACTIONS_IMPLEMENTATION.md §6: fork-gating options (height-gate
  vs timestamp), TIPTRC21 settlement decision, XDPoS compatibility,
  expiry-verifier deployment, networking capability question. Maintainer
  decisions D1–D4 recorded; nothing activated; no heights invented.

### M10 — Independent audit + spec-drift verification + hardening (DONE 2026-10-29)

Independent audit of the M1–M9 tree against a fresh spec snapshot
(1565 lines; last upstream commit 2026-10-25, predating the implementation
snapshot of 2026-10-28 — no spec drift). Verified: staged tree == M9
checkpoint byte-for-byte (only addition: FOCIL_XDC_GAP_ANALYSIS.md);
baseline-vs-worktree failure sets byte-identical-by-name on a fresh sweep
(13 pre-existing failures, zero new; logs /tmp/audit-sweep-20261029.txt).
EIP-8250 (Keyed Nonces) verified NOT a dependency: absent from EIP-8141's
requires list and body; EIP-8250 itself `requires: ... 8141` (reverse
dependency). Replay protection remains the envelope nonce consumed at
payment approval.

Four confirmed gaps found by the audit and fixed:

- G1 (consensus, spec "Behavior"): a VERIFY frame targeting an ACTIVE
  PRECOMPILE was misrouted to the protocol default code (precompiles carry
  no code, so isDefaultCode matched them); the default code reverts on
  flags == 0, invalidating spec-valid transactions. Fix: precompile
  membership check precedes the default-code route in runFrame
  (core/frame_transition.go). Test: TestFrameTxVerifyPrecompileDispatch.
- G2 (consensus, spec "Blob handling"): the EIP-7594 per-transaction blob
  count cap (params.BlobTxMaxBlobs) was not enforced for frame
  transactions (legacy preCheck is bypassed by the frame dispatch). Fix:
  rules.IsOsaka-gated count check in executeFrameTx → ErrTooManyBlobs.
  Test: TestFrameTxTooManyBlobs (over-limit invalid; at-limit valid).
- G3 (pool policy, spec "Block-level gas accounting"):
  validateFrameTransaction checked intrinsic + Σexecution against the
  block gas limit without the calldata-floor alternative; a floor-dominated
  transaction could be admitted yet never fit a block. Fix: reservation is
  now max(intrinsic + Σexecution, calldata_floor_gas), matching the
  engine. Test: TestValidateFrameTransactionFloorReservation.
- G4 (consensus hardening, spec "Fee budget" exact arithmetic):
  required_max_fee / max_priority_fee multiplication wrapped mod 2^256.
  Fix: MulOverflow/AddOverflow checks invalidate the transaction
  (ErrFrameTxFeeOverflow) instead of wrapping. Test: TestFrameTxFeeOverflow.

Tests: the 4 new tests PASS; full 7-package sweep re-run post-fix
(log /tmp/audit-sweep-postfix-20261029.txt).
Verification follow-up (2026-10-29): `build/ci.go check_generate` surfaced
one stale generated file belonging to this feature: gen_receipt_json.go
lacked the Receipt.Payer/FrameReceipts fields added at M6 — regenerated and
staged. The same check also regenerates gen_header_{json,rlp}.go and
eth/ethconfig/gen_config.go, but those are stale at pristine base 48a7727
(fork fields SlotNumber/BlockAccessListHash/FastSyncPivot* vs committed
generator output; the header RLP one is consensus-shaping) — deliberately
NOT regenerated here; flagged for maintainers as a pre-existing fork issue.
Checkpoint: backups-frame-tx/M10-audit-hardening.patch (cumulative).
