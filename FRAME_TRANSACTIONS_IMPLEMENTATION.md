# EIP-8141 Frame Transactions — Implementation & XDC Activation Design

Status: implementation complete through M8 (type, signing, engine, opcodes,
receipts, txpool admission, networking wire format). Not activated on any
network. All changes live uncommitted on branch
`feat/eip-8141-frame-transactions` (worktree `/home/tanmay/xdc-fix-work/frame-tx-8141`,
base 48a7727); patch checkpoints in `/home/tanmay/xdc-fix-work/backups-frame-tx/`.

This document is the architecture reference (M3–M8) plus the XDC activation
design (M9). `FRAME_TRANSACTIONS_PROGRESS.md` carries the test evidence;
`FRAME_TRANSACTIONS_GAP_ANALYSIS.md` carries the remaining-work ledger.

---

## 1. Architecture map

| Layer | Files | Contents |
|---|---|---|
| Type (M1) | core/types/tx_frame.go | FrameTx RLP `[0x06 || [chain_id, nonce, sender, frames, signatures, blob_versioned_hashes, blob_gas, fees]]`, SigHash (empty-msg elision, length-committed), ValidateStatic, IntrinsicGas/MaxGas, ValidateSignatures (SECP256K1 v‖r‖s; P256 r‖s‖qx‖qy; ARBITRARY structural), expiry-frame rules, FrameReceipt type |
| Signing (M2) | core/types/transaction_signing.go | Bogota signer (wraps Prague), `SignFrameTx`, Sender() → explicit `sender` field, SenderHash |
| Approval state (M3a) | core/state/statedb_frame.go | Journaled `FrameState`: SenderApproved/PayerSet, per-frame receipt state-gas (`ReceiptStateGas`), SSTORE ownership map (`StateGasOwners`); three journal entries; StateDB.Copy propagates |
| Gas 2D (M3b) | core/vm/gascosts.go | `GasBudget.NoSpill`: frames keep strict regular/state pool separation (EIP-8141 overrides the EIP-8037 spillover inside frame execution); propagated to child budgets (CALL/CREATE chains) |
| VM context (M3c) | core/vm/frame.go | `FrameTxContext` (tx data, resolved signers, approvals, per-frame status/gas bookkeeping), `frameStateDB` interface (satisfied by *state.StateDB without widening vm.StateDB), shared `Approve()` logic |
| Opcodes (M3c/M5) | core/vm/{opcodes.go,instructions_frame.go,jump_table.go} | `APPROVE` 0xaa; `TXPARAM` 0xb0, `FRAMEDATALOAD` 0xb1, `FRAMEDATACOPY` 0xb2, `FRAMEPARAM` 0xb3, `SIGPARAM` 0xb4, `SIGDATACOPY` 0xb5; registered via `enable8141` in `newBogotaInstructionSet`; exceptional halt outside frame txs |
| Engine (M3d) | core/frame_transition.go | `executeFrameTx`: static checks → sig validation → fee budget → 2D block reservation → frame loop → settlement |
| Integration (M3e) | core/state_transition.go, core/state_processor.go | Message.FrameTx, dispatch, ExecutionResult{Payer, FrameReceipts}, MakeReceipt |
| SSTORE hooks (M4) | core/vm/gas_table.go | Cross-frame state-gas ownership journaling (2.1.1) and refill routing (2.2.2.1: refund to the frame that paid creation, not the clearer) |
| Receipts (M6) | core/types/receipt.go, internal/ethapi/api.go | Consensus `[cumulative, payer, frames]` typed RLP; storage `[status, cumulative, payload]`; DeriveSha/EncodeIndex; RPC `payer` + `frameReceipts` |
| Txpool (M7) | core/txpool/validation.go, core/txpool/legacypool/legacypool.go, core/types/transaction.go (Cost) | Admission, fork gating, signature/static checks, conservative funds policy |
| Networking (M8) | eth/protocols/eth/receipt.go | eth/69 compressed receipt shape carries frame payloads verbatim |

## 2. Execution engine semantics (core/frame_transition.go)

Transaction lifecycle (Bogota-gated at `rules.IsBogota`):

1. **Static**: `FrameTx.ValidateStatic()` (modes, flags, batch structure,
   expiry-frame constraints, EIP-7825 16M total-gas cap, blob hash versions).
2. **Signatures**: `ValidateSignatures()` — every SECP256K1/P256 entry
   cryptographically verified; ARBITRARY entries structural-only.
3. **Fee budget**: `required_max_fee = max_gas * base_fee +
   blob_gas * blob_base_fee` (overflow → invalid, `ErrFrameTxFeeOverflow`),
   `max_fee >= required_max_fee`, then
   `max_priority_fee = min(max_priority_fee_per_gas * max_gas, max_fee - required_max_fee)`,
   `max_cost = required_max_fee + max_priority_fee`,
   `GASPRICE = base_fee + max_priority_fee / max_gas` (integer div, as spec).
4. **Block reservation**: `gp.CheckGasAmsterdam(intrinsic + Σlimits.execution,
   Σlimits.state)` — EIP-8037 two-dimensional accounting, transaction-level.
5. **Frame loop** in list order, journaled `FrameState` installed per tx:
   - Entry access charge: cold/warm per EIP-7929-Amsterdam table (target;
     resolved target defaults to sender for targetless frames).
   - VERIFY frames execute as STATICCALL with caller ENTRY_POINT; the
     default code path (`isDefaultCode(target)`) uses the protocol default
     verify: secp256k1 sig i = # of VERIFY frames with execution scope before
     this frame, resolved==sender, `APPROVE(flags)` — no code execution.
   - Expiry frames (VERIFY → ExpiryVerifierAddress): normal code execution;
     static rules (flags==0, ≤1 per tx) enforced in ValidateStatic.
   - SENDER frames require prior sender-approval (else tx invalid);
     execution as CALL with caller = tx.sender. **EIP-3607 does not apply**:
     a contract-account sender is the AA case.
   - Payment: `payer` is set by the first successful `APPROVE` with payment
     scope; escrow `SubBalance(payer, max_cost)` happens at approval.
   - Frame gas: `GasBudget{RegularGas: limits.execution, StateGas:
     limits.state, NoSpill: true}`; unused gas returned at frame end;
     per-frame receipt gets `GasUsedExecution/GasUsedState`.
   - Logs: per-frame slice capture (`len(sdb.Logs())` before/after);
     attached only to successful frames (EIP-7708 ETH-transfer logs included).
   - Atomic batch: frames with `ATOMIC_BATCH` flag run under one snapshot;
     on failure of any flagged frame the whole batch rolls back, executed
     in-batch frames retain their status (with logs cleared and state-gas
     zeroed on their receipts), remaining batch frames are marked SKIPPED(2),
     execution jumps past the batch.
6. **Settlement**:
   - Refund: tx-scoped EIP-3529 counter (set-clear refunds from any frame
     feed it), capped `refund <= gasUsedBeforeRefund / RefundQuotientEIP3529`.
   - `gasUsedAfterRefund = gasUsedBeforeRefund - refund`;
     `txStateGas = Σ receipt state gas` (post-refill journaled values);
     `txExecutionGas = max(gasUsedAfterRefund - txStateGas, floor)`;
     `gas_used = txExecutionGas + txStateGas` (calldata floor binds the
     execution component only and is always payable).
   - Block accounting (EIP-7778): the block's execution dimension is charged
     pre-refund (`max(gasUsedBeforeRefund - txStateGas, floor)`), the state
     dimension at `txStateGas`, and the block gas used tracks `gas_used`.
   - Payer: `base_fee_paid = gas_used * base_fee`;
     `priority_paid = min(max_priority_fee_per_gas * gas_used,
     max_cost - base_fee_paid)` to coinbase; base fee burned; payer refunded
     `max_cost - base_fee_paid - priority_paid`. Zero blob fees until
     blobpool support (see gaps).
   - Missing payer with any executed frame → tx invalid (whole-tx revert).

Receipt: `MakeReceipt` builds `[cumulative, payer, frames]`; top-level
`Status` is derived (any frame succeeded); `Logs` = union of frame logs
(used for bloom + RPC `logs`).

## 3. Receipts (M6)

- **Consensus/trie**: typed receipt, byte 0x06, payload
  `rlp([cumulative_gas_used, payer, [[status, [execution_gas_used,
  state_gas_used], logs]...]])`. No top-level status/bloom/logs in the
  encoding. Implemented in `EncodeRLP`/`MarshalBinary`/`decodeTyped`/
  `EncodeIndex` (DeriveSha). Golden root pinned in
  `core/types/frame_receipt_trie_test.go`.
- **Storage**: `[status, cumulative, payload]` (payload = consensus body).
  Chosen deliberately so the eth/69 compressed wire format maps by
  stripping/adding the type byte. `ReceiptForStorage.DecodeRLP`
  discriminates from legacy `[status, cumulative, logs]` by content
  (payload never empty, first element a string; logs list empty or starts
  with a list).
- **Derivation**: `DeriveFields` rebuilds union logs from frame receipts,
  assigns block-wide log indices, recomputes bloom, derives status; skips
  contract-address derivation for frame txs.
- **RPC** (`eth_getTransactionReceipt`): adds `payer` and `frameReceipts[]`
  (`frameIndex, status, gasUsedExecution, gasUsedState, logs`); top-level
  `logs` stays the union (filter compatibility), `status`/`cumulativeGasUsed`/
  `gasUsed` unchanged in meaning.

## 4. Txpool (M7)

- `legacypool` accepts type 0x06 (Accept bitmap); Bogota gate in
  `ValidateTransaction` (`rules.IsBogota`).
- Stateless: `ValidateStatic`, chain-id binding, `ValidateSignatures`,
  EIP-2681 nonce overflow, block-gas-limit fit of the 2D reservation
  (`intrinsic + Σlimits.execution` vs `head.GasLimit`), min-tip vs
  `max_priority_fee_per_gas`, 256-bit fee sanity.
- Stateful: standard nonce/funds via `ValidateTransactionWithState` with
  `Transaction.Cost()` overridden for frame txs to `max_fee + Σ frame values`
  (exact worst-case; the per-gas division in the legacy path would
  under-count).
- Delegation/SetCode authority limits skipped for frame txs (sender code is
  validation logic, not a 7702 delegation).
- **Conservative policy (documented deviation)**: no validation-prefix
  simulation and no ERC-7562-style opcode/storage tracing rules. Admission
  requires the *sender* to cover worst-case cost, which rejects some valid
  paymaster transactions a full mempool policy would accept — safe direction
  for a devnet; the full policy is listed as a maintainer decision (§6 D2).
- Blob-carrying frame txs rejected at admission until blobpool support
  exists.

## 5. Networking (M8)

- Typed-tx RLP decode is generic — frame transactions propagate in
  `Transactions`/`PooledTransactions` messages unchanged.
  `NewPooledTransactionHashes` performs no type validation (length
  consistency only); old peers drop unknown type 0x06 on decode (standard
  typed-tx behavior at a hardfork boundary).
- eth/69 compressed receipts: frame receipts carry the full consensus
  payload in the compressed `logs` slot; `encodeForHash` re-emits
  `0x06 || payload` verbatim (no bloom recompute), so `DeriveSha` over
  wire-fetched receipts matches the block header. Verified by
  `eth/protocols/eth/frame_receipt_test.go` (wire encode/decode → root
  equality → EncodeForStorage → storage decode fidelity).
- XDPoS2 legacy receipt path (`handleReceiptsXDPOS2`) decodes via
  `types.Receipts` consensus RLP — frame receipts work unchanged there.

## 6. XDC activation design (M9)

### 6.1 Fork gating

This fork models Amsterdam/Bogota as **timestamp-based** rules
(`ChainConfig.AmsterdamTime`/`BogotaTime`, `Rules.IsBogota`). XDC mainnet
and testnet activate upgrades by **block height** (`Eip1559Block` etc.) and
have no Amsterdam/Bogota scheduled — therefore frame transactions are
inert on XDC networks today: type-0x06 txs are rejected by signer decoding
(`ErrTxTypeNotSupported` from `LatestSigner` on any XDC config) and by the
engine's `IsBogota` check.

Activation requires a deliberate config addition. Two options:

- **A (recommended): add block-height gates** `XDCFrameTxBlock` (engine)
  mapped into the rules layer (`IsBogota` equivalent) alongside the
  Amsterdam prerequisites (2D gaspool, 2780 costs, 7976 floor, 7708 logs).
  Keeps XDC's height-based UX. Requires touching `params.Config.Rules()` to
  OR-in the height gate — a small, reviewable diff.
- **B: schedule timestamps** `AmsterdamTime`/`BogotaTime` on the XDC
  networks. Zero code change; but XDC operators think in block numbers, and
  Amsterdam brings more than this feature (BAL, UBT hooks) which must be
  audited as a bundle.

**Maintainer decision D1**: choose A or B; no block height/timestamp is
proposed here (must come from governance).

### 6.2 TIPTRC21 fee routing

XDC's TRC21 path routes part of the fee to the token-contract owner via
`st.owner`/TIPTRC21Fee logic in the legacy state transition. Frame
transactions settle **spec-pure**: base fee burned, priority fee to the
block beneficiary, payer refunded. No TRC21 interaction exists for frame
txs (none of the special-transaction carve-outs apply; frame txs are never
`IsSpecialTransaction`).

**Maintainer decision D2**: accept spec-pure settlement for frame txs
(recommended — TRC21 discounts are a legacy-adapter concern) or design a
TRC21-aware payer override; the engine's settlement is a single function,
so a policy hook is a contained change.

### 6.3 XDPoS compatibility

- Special transactions (BlockSigners/RandomizeSMC, gasPrice=0, masternode
  signing txs) are untouched; frame txs never match `IsSpecialTransaction`
  (no top-level `to`), and the txpool's special-tx min-tip exemption is
  unaffected.
- XDPoS block accounting uses the Amsterdam two-dimensional GasPool
  (`CheckGasAmsterdam`) — already the fork's accounting path post-Amsterdam;
  the engine reserves intrinsic+Σexecution and Σstate up front.
- Masternode block rewards/penalty flows do not inspect receipts; the new
  receipt shape is additive (new type byte), so V1/V2 bookkeeping is
  unaffected. Old peers cannot decode 0x06 receipts — see D3.

### 6.4 Networking/versioning

- Receipt serving (eth/69) and legacy XDPoS2 receipt path both handle frame
  receipts (M8). **Maintainer decision D3**: whether to bump/flag a protocol
  capability so Bogota-aware nodes prefer Bogota-aware peers for receipt
  sync after activation; standard practice treats this as optional (mixed
  networks resolve via hash mismatch → peer drop), but XDPoS validator
  sets are small enough that a capability flag may be desirable.

### 6.5 Expiry-verifier deployment

EIP-8141 requires the expiry-verifier contract at `EXPIRY_VERIFIER`
(0x…CA) with spec-defined code. The static constraints are enforced in
`ValidateStatic` and execution is ordinary STATICCALL, but the **contract
deployment transaction at Bogota activation** is not implemented (no
synthetic system-tx mechanism exists in this fork for it).

**Maintainer decision D4**: deploy via (a) synthetic activation transaction
(needs a small state-processor hook), or (b) genesis/allocation at the
activation height in the chain config tooling. Until deployed, expiry
frames revert as "no code" — a safe failure.

### 6.6 Deliberate non-goals for activation

- No blobpool integration for frame txs (rejected at admission; blob fee
  accounting is zero-blob only).
- No public-mempool validation-prefix simulation (D2-adjacent; needed only
  for unrestricted public AA mempool).
- No mainnet activation performed or proposed here.

## 7. Negative-space guarantees (what this code cannot do)

- Pre-Bogota: the type is undecodable by the signer, unexecutable by the
  engine, and unadmittable by the pool. Triple-gated.
- Outside a frame tx, all seven frame opcodes halt exceptionally.
- Frames cannot spill state gas into execution gas or vice versa (NoSpill);
  spillover exists only at the legacy-transaction level per EIP-8037.
- A failed atomic batch leaves zero state effects and zero state-gas
  charges; skipped frames never execute.
- Without a successful payment-scope approval, no frame executes for free:
  the transaction is invalid as a whole.
