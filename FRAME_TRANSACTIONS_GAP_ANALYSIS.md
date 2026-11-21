# Frame Transactions (EIP-8141) — Requirements-to-Code Gap Analysis

Workspace: /home/tanmay/xdc-fix-work/frame-tx-8141 @ branch feat/eip-8141-frame-transactions
Base: 48a7727 (xdc-fix-work xone-fix-20261025)
Spec: EIP-8141 (Draft), snapshot fetched 2026-10-28.
Statuses: Implemented / Partially Implemented / Missing / Not Applicable / Needs Investigation.

Legend for "Existing XDC implementation": file references relative to repo root.

## 0. Dependency layer (already present in this fork, gated Amsterdam/Bogota)

| Dependency | Status | Evidence |
|---|---|---|
| EIP-8037 two-dimensional gas (GasBudget, reservoir, refills) | Implemented | core/vm/gascosts.go (full 2D budget), state_transition.go initRuntimeGasBudget/settleGas, core/vm/gas_table.go:648-740 |
| EIP-2780 intrinsic decomposition | Implemented | state_transition.go:163 intrinsicBaseGasEIP2780; params TxBaseCost2780=12000, TxValueCost2780=4244 (NOTE: spec table lists 6000; fork pins 4244 — both drafts mark values provisional) |
| EIP-7976 calldata floor | Implemented | state_transition.go:199 FloorDataGas; params TxCostFloorPerToken7976=16; added TxStandardTokenCost=4 (M1) |
| EIP-7825 TX_MAX_GAS_LIMIT | Implemented | params.MaxTxGas = 1<<24; enforced state_transition.go:591/750 |
| EIP-7708 ETH transfer log | Implemented | core/types/log.go:68 EthTransferLog; emitted core/evm.go:169 |
| EIP-7702 delegation semantics | Implemented | types.ParseDelegation/AddressToDelegation (tx_setcode.go:36-47) |
| RIP-7212 P256VERIFY | Implemented | core/vm/contracts.go p256Verify (PrecompiledContractsP256Verify :198); crypto/secp256r1.Verify |
| EIP-3529 refund quotient 5 | Implemented | params.RefundQuotientEIP3529; state_transition.go calcRefund |
| EIP-4844 blob plumbing | Implemented | core/types/tx_blob.go, kzg4844.IsValidVersionedHash |
| EIP-7594 blob wrapper gossip | Implemented (verify before M8) | eth/protocols wrapper for blob txs; Needs Investigation for reuse with frame txs |
| EIP-7778 block gas accounting (refund does not free block capacity) | Implemented | core/gaspool.go:85 ChargeGasAmsterdam(txRegular pre-refund, txState, receiptGasUsed) |
| EIP-7928 block access lists | Implemented | core/state/reader_eip_7928.go, BAL in state_processor |

## 1. Transaction type and data structure

| EIP-8141 requirement | Existing | Required change | File/function | Status |
|---|---|---|---|---|
| FRAME_TX_TYPE = 0x06 | Types 0x00-0x04 only (transaction.go:47-53) | Add constant + FrameTx type | core/types/tx_frame.go | Implemented (M1) |
| Payload RLP `[chain_id, nonce, sender, frames, signatures, fees, blob_versioned_hashes]` | — | FrameTx struct + nested Frame/Limits/Signature/Fees RLP | tx_frame.go | Implemented (M1) |
| Frame `[mode, flags, target, limits, value, data]`, nullable target | — | Frame.Target *common.Address `rlp:"nil"` | tx_frame.go | Implemented (M1) |
| Modes 0/1/2; flags bits 0-2; reserved rejection | — | constants + ValidateStatic | tx_frame.go | Implemented (M1) |
| Signature objects scheme/signer/msg/signature | — | FrameSignature + schemes 0/1/2 | tx_frame.go | Implemented (M1) |
| Static constraints (all spec asserts) | — | ValidateStatic incl. batch rules, expiry-frame rules, 7825 cap | tx_frame.go | Implemented (M1) except expiry-frame constraints — added in M3 |
| Type dispatch in decodeTyped | transaction.go:201 | case FrameTxType | transaction.go:213 | Implemented (M1) |

## 2. Encoding, decoding, hashing, signing

| Requirement | Existing | Change | File | Status |
|---|---|---|---|---|
| Tx hash keccak(0x06 \|\| rlp(tx)) | prefixedRlpHash generic | none beyond type | transaction.go Hash() | Implemented (M1) |
| compute_sig_hash with empty-msg elision (length-committed) | — | FrameTx.SigHash/sigHashPayload | tx_frame.go | Implemented (M1) |
| Signature validation (K1 v\|\|r\|\|s low-s; P256 r\|\|s\|\|qx\|\|qy low-s + derived signer; ARBITRARY structural) | crypto.Ecrecover, secp256r1.Verify | FrameTx.ValidateSignature(s) | tx_frame.go | Implemented (M1) |
| Sender resolution without outer signature | modernSigner ECDSA recovery | explicit-sender path for FrameTxType | transaction_signing.go Sender | Implemented (M2) |
| Signer gating: type valid only from Bogota | txtypeSet by fork | forks.Bogota sets FrameTxType; NewBogotaSigner; MakeSigner/LatestSigner/LatestSignerForChainID wiring | transaction_signing.go | Implemented (M2) |
| Outer SignTx must fail for frame txs | — | SignatureValues returns error | transaction_signing.go | Implemented (M2) |
| Wallet helper to produce SECP256K1 entry honoring length-commitment | — | SignFrameTx (index-0 install, placeholder-length signing) | transaction_signing.go | Implemented (M2) |
| JSON marshal/unmarshal for RPC | transaction_marshalling.go per-type gen | FrameTx JSON (frames, signatures, fees) | core/types/tx_frame_marshalling.go (new) | Missing — planned M6 |

## 3. Frame execution semantics

| Requirement | Existing | Change | File | Status |
|---|---|---|---|---|
| Frame loop with per-frame gas pools (limits.execution / limits.state) | single top-level call | frame execution engine | core/frame_transition.go (new) | Missing — M3 |
| Strict pool separation (no state→regular spillover) inside frames | GasBudget.charge spills (gascosts.go:112) | NoSpill mode on GasBudget carried through Forward | core/vm/gascosts.go | Missing — M3 |
| Frame-entry warm/cold target access charge from gas_left | ColdAccountAccessAmsterdam params | engine charge before dispatch | frame_transition.go | Missing — M3 |
| Caller rules: SENDER→tx.sender (requires sender_approved), DEFAULT/VERIFY→ENTRY_POINT; ORIGIN=frame caller | TxContext.Origin per tx | per-frame Origin mutation | engine + evm | Missing — M3 |
| VERIFY frames as STATICCALL; failure invalidates tx | evm.StaticCall / readOnly | engine dispatch | frame_transition.go | Missing — M3 |
| Default code (empty code hash): VERIFY approval flow / SENDER,DEFAULT succeed empty | evm.Call empty-code path returns success | engine interception for VERIFY | frame_transition.go | Missing — M3 |
| Expiry verifier frame (target=EXPIRY_VERIFIER, data=8B ts) | — | static constraints + normal call dispatch | tx_frame.go ValidateStatic + engine | Missing — M3 |
| Atomic batches: batch rollback, skip subsequent (status 0x2), receipt retention rules | — | engine batch tracking + snapshot | frame_transition.go | Missing — M3 |
| Approval context (payer, sender_approved) journaled with state | journalEntry interface (core/state/journal.go:128) | frame context hung off StateDB + journal entries | core/state/statedb_frame.go (new) | Missing — M3 |
| Transient storage discarded between frames | state.Prepare resets per tx | engine resets per frame | frame_transition.go | Missing — M3 |
| EIP-3607 not applied to frame txs | state_transition.go:595 EOA check | skip for frame txs (sender may be contract) | frame engine preCheck | Missing — M3 |
| Frame tx nonce == state nonce at start; increment at APPROVE_PAYMENT scope | preCheck nonce path | engine | frame_transition.go | Missing — M3 |

## 4. New opcodes

| Requirement | Existing | Change | File | Status |
|---|---|---|---|---|
| APPROVE (0xaa): scope checks, approval effects, nonce bump, max_cost escrow, RETURN-like exit, memexpansion-only gas | opcodes 0xaa free | opApprove + jump table entry (Bogota set) | core/vm/instructions_frame.go (new), jump_table.go | Missing — M3 |
| TXPARAM (0xb0), FRAMEDATALOAD (0xb1), FRAMEDATACOPY (0xb2), FRAMEPARAM (0xb3), SIGPARAM (0xb4), SIGDATACOPY (0xb5) | 0xb0-b5 free | 6 opcode impls, halt when no frame tx | instructions_frame.go | Missing — M5 |
| GASPRICE returns base+max_priority//max_gas for frame txs | opGasprice reads evm.GasPrice | engine sets TxContext.GasPrice accordingly | frame_transition.go | Missing — M3 |
| Introspection validity: halt in non-frame txs | — | nil-check on evm frame ctx | instructions_frame.go | Missing — M5 |

## 5. Gas accounting & settlement

| Requirement | Existing | Change | File | Status |
|---|---|---|---|---|
| frame_tx_intrinsic_gas / calldata_floor_gas / max_gas formulas | — | FrameTx.IntrinsicGas/MaxGas | tx_frame.go | Implemented (M1) |
| Fee budget: required_max_fee, max_cost, GASPRICE value | — | engine compute + pre-execution check | frame_transition.go | Missing — M4 |
| Per-frame receipt gas_used.execution = limits.execution - gas_left | UsedRegularGas accumulators | engine at frame exit | frame_transition.go | Missing — M4 |
| gas_used.state attribution + journaled receipt mutation | — | receipts in frame context | statedb_frame.go | Missing — M4 |
| SSTORE creation ownership (frame index per (addr,slot)) | — | hook in gasSStore8037And8038 creation case (gas_table.go:715) | core/vm/gas_table.go + frame ctx | Missing — M4 |
| Cross-frame refill routing (owner!=current reduces owner's receipt; owner==current credits own pool) | contract.Gas.RefundState at gas_table.go:735 | consult ownership when frame tx | gas_table.go | Missing — M4 |
| Settlement: tx_unused_gas, EIP-3529 refund across frames, calldata floor on execution component, gas_used = exec + state | settleGas (single-frame) | frame settlement path | frame_transition.go | Missing — M4 |
| Fee settlement: max_cost escrow at approval; payer_refund; base burned; tip to coinbase (XDC: check TIPTRC21Fee owner routing!) | settleGas + owner routing (state_transition.go:818) | engine settlement; XDC TRC21 owner-routing decision documented | frame_transition.go | Missing — M4 |
| Block accounting: execution pre-refund + state per dim; reservations both dims | GasPool.CheckGasAmsterdam/ChargeGasAmsterdam | engine calls with frame reservations | frame_transition.go + state_processor | Missing — M4 |

## 6. Transaction pool

| Requirement | Existing | Change | File | Status |
|---|---|---|---|---|
| Type admission gate | validation.go:65 Accept bitmask + fork gates :75-85 | Bogota gate for FrameTxType | core/txpool/validation.go | Missing — M7 |
| Stateless validation (ValidateStatic, sig validation) | ValidateTransaction | frame branch | validation.go | Missing — M7 |
| Validation-prefix simulation + banned opcodes + trace rules | none | validation-prefix simulator | core/txpool/frame_validation.go (new) | Missing — M7 (policy subset) |
| One pending frame tx per sender; replacement rules; payer reservation accounting | legacypool accounting | frame-aware accounting | legacypool | Missing — M7 |
| MAX_VERIFY_GAS=100k / MAX_VERIFY_STATE_GAS=500k caps | — | constants + checks | validation.go | Missing — M7 |

## 7. Block processing & receipts

| Requirement | Existing | Change | File | Status |
|---|---|---|---|---|
| Per-frame receipt `[cumulative_gas_used, payer, [[status,[exec,state],logs]...]]` | Receipt single status/logs (receipt.go) | FrameReceipts on Receipt + typed RLP | core/types/receipt.go | Missing — M6 |
| logsBloom/log indexing = concatenation of frame logs | DeriveFields | frame branch | receipt.go | Missing — M6 |
| No tx-level status (derived) | Receipt.Status | derive for frame tx | receipt.go/state_processor | Missing — M6 |
| Frame tx execution dispatch in block processing | ApplyTransactionWithEVM :324 | route to frame engine | state_processor.go | Missing — M4 |
| Receipts trie/commit verification | DeriveSha + block_validator | works via EncodeIndex once RLP defined | — | Missing — M6 |

## 8. RPC & tooling

| Requirement | Existing | Change | File | Status |
|---|---|---|---|---|
| eth_sendRawTransaction accepts 0x06 | decodeTyped handles it (M1); pool gate needed | txpool admission (M7) | — | Partially Implemented |
| Receipt JSON with payer + frame receipts | GetTransactionReceipt marshalling | frame branch | internal/ethapi | Missing — M6 |
| RPCTransaction JSON for frame tx | transaction_marshalling | frame fields | internal/ethapi + types | Missing — M6 |
| Gas estimation for frame txs (2D per-frame) | eth_estimateGas single-dim | document limitation / basic path | internal/ethapi | Missing — M8 |
| Signing tooling | accounts/abi etc. | SignFrameTx helper (done) + docs | — | Partially Implemented (M2) |

## 9. Chain configuration & activation

| Requirement | Existing | Change | File | Status |
|---|---|---|---|---|
| Fork gate for frame tx feature | BogotaTime (timestamp) exists; XDC uses block forks | FrameTx activation = Bogota on non-XDC; new optional XDC block gate (design doc) | params/config.go | Missing — M9 |
| XDC chains: Hegotá stack (Amsterdam/8037 gas) not active today | XDC configs nil Amsterdam/Bogota | documented activation coupling (FrameTx requires 8037 stack) | params/config.go + docs | Missing — M9 |
| Legacy tx behavior unchanged pre-activation | — | regression tests | existing suites | Planned M9 |
| XDPoS compatibility (special sign txs, TRC21 fee routing, epoch blocks) | XDC hooks in state_transition | engine must not disturb; fee-routing decision | docs + tests | Missing — M9 |

## 10. Networking

| Requirement | Existing | Change | File | Status |
|---|---|---|---|---|
| Receipts message encoding mirrors consensus | eth/protocols receipt RLP | frame branch once receipt RLP defined | eth/protocols/eth | Missing — M8 |
| Blob-carrying frame tx gossip wrapper (EIP-7594) | blob tx wrapper | reuse for frame txs | eth/protocols/eth | Missing — M8 |
| Non-blob frame tx plain payload | standard | no wrapper | — | Missing — M8 |

## Smallest coherent milestone

M1 (DONE) + M2 (DONE) are foundational. The smallest USER-VISIBLE coherent
milestone is M3+M4: a frame transaction executing the canonical "self_verify +
user_op" shape (default-code VERIFY + SENDER transfer) inside a locally
generated Bogota-gated devnet chain, producing a spec-shaped receipt and
correct fee settlement, with legacy txs unaffected. Acceptance criteria:
end-to-end test in core/ building a chain, executing such a tx, asserting
state transitions, gas settlement math, receipt fields, and nonce increment.

## Security notes

- Never weaken sig checks to make tests pass: ValidateSignatures is called
  before ANY frame executes (spec Behavior step 3).
- Approval context and receipt mutations MUST be journaled — reverts at any
  call depth must unroll them (spec: Cross-frame interactions).
- Frame txs skip EIP-3607 (sender may carry code); other types unchanged.
- XDC: TIPTRC21Fee routes full fees to the coinbase OWNER. Frame-tx
  settlement spec: base fee burned + tip to coinbase. Decision required for
  XDC mode: keep spec behavior (recommended: frame tx activation implies the
  Hegotá fee model) — flagged for maintainers in IMPLEMENTATION.md.
- DoS surface (mempool) is policy, not consensus; the public-mempool ruleset
  is deferred to M7 with a conservative default (direct-evaluation prefixes
  only) documented for maintainers.

## Status update 2026-10-28 (M3–M9 complete)

All rows marked "Missing — M3/M4/M5/M6/M7/M8/M9" above are now **Implemented**
unless restated below. Summary of what landed:

- §3 frame execution: all rows Implemented (engine, NoSpill separation,
  entry charges, caller/ORIGIN rules, VERIFY-as-staticcall, default code,
  expiry frames, atomic batches, journaled approval context, per-frame
  transient reset, 3607 skip, nonce-at-approval).
- §4 opcodes: all Implemented + tested (APPROVE + 6 introspection opcodes,
  Bogota-gated, halt outside frame txs).
- §5 gas/settlement: all Implemented incl. SSTORE cross-frame refill
  routing and 7778 pre-refund block accounting. TRC21 decision = spec-pure
  settlement (see IMPLEMENTATION.md §6.2, decision D2).
- §6 txpool: admission, fork gate, static+signature validation, funds via
  exact worst-case Cost(), min-tip, replacement by standard nonce rules.
  **Remaining (documented, deferred):** validation-prefix simulation with
  ERC-7562-style opcode/storage rules (public-mempool DoS policy) and
  MAX_VERIFY_GAS/MAX_VERIFY_STATE_GAS pool caps; current policy is
  conservative (sender covers worst case; no simulation).
- §7 receipts: Implemented (consensus RLP, storage, trie, derivation,
  block integration).
- §8 RPC: receipt JSON (payer + frameReceipts) Implemented; transaction
  JSON (txJSON + RPCTransaction frame fields) Implemented with round-trip
  test. **Remaining:** eth_estimateGas 2D estimation for frame txs (basic
  path only — documented limitation); TransactionArgs-based
  eth_sendTransaction frame construction not wired (use raw txs).
- §9 activation: design documented (IMPLEMENTATION.md §6), maintainer
  decisions D1–D4 open: fork-gate shape (height vs timestamp), TRC21 fee
  routing, protocol capability flag, expiry-verifier deployment mechanism.
  Nothing activated.
- §10 networking: tx propagation generic (no change); eth/69 compressed
  receipts carry frame payloads (defect found + fixed, test added);
  XDPoS2 legacy receipt path unchanged-compatible. **Remaining:**
  EIP-7594-style blob wrapper gossip for blob-carrying frame txs (tied to
  blobpool support; such txs are pool-rejected meanwhile).

Regression ledger: sweep over core, core/types, core/state, core/vm,
core/txpool, internal/ethapi, eth/protocols/eth — 13 failures, all
byte-identical-by-name with the pristine-base sweep (logs:
backups-frame-tx/{final-test-log,base-sweep-log}.txt). Zero new failures.

## Status update 2026-10-29 (M10 audit)

Independent audit against a fresh spec snapshot (no upstream drift since the
implementation snapshot). Four gaps found and fixed (see PROGRESS M10):
VERIFY-frame precompile dispatch order (§3), EIP-7594 per-tx blob count
enforcement (§10/blob handling), pool block-gas reservation now uses
max(·, calldata floor) (§6), and exact (overflow-checked) fee-budget
arithmetic (§5). All previously documented deferrals remain open:
validation-prefix simulation + ERC-7562 trace rules and
MAX_VERIFY_(STATE_)GAS pool caps (§6), eth_estimateGas 2D estimation (§8),
blobpool/blob gossip for frame txs (§10), expiry-verifier deployment (§9,
maintainer decision D4), activation gate choice (§9, D1).
