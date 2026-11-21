# EIP-8250 Implementation Report — Keyed Nonces for Frame Transactions (XDC fork)

Date: 2026-10-29
Worktree: `/home/tanmay/xdc-fix-work/frame-tx-8250`
Branch: `feat/eip-8250-keyed-nonces` (HEAD 48a7727, no commits; all work staged)
Base: EIP-8141 M10 checkpoint state (applied byte-identically from
`/home/tanmay/xdc-fix-work/backups-frame-tx/M10-audit-hardening.patch`)
Spec: `ethereum/EIPs` master `EIPS/eip-8250.md` snapshot 2026-10-29
(status: Draft; freshest spec commit 2026-10-19 predates the snapshot).
Checkpoint: `/home/tanmay/xdc-fix-work/backups-frame-tx-8250/final-20261029.patch`
(cumulative staged diff, sha256-verified against the staged tree; earlier:
`M1-keyed-nonces.patch`, `M2-keyed-mempool-20261029.patch`, `pre-phase2-20261029.patch`)

## 1. Scope and method

EIP-8250 is a delta against EIP-8141: it replaces the `nonce` field of the
`FRAME_TX_TYPE` (0x06) payload with `nonce_keys` and `nonce_seq`, adds
independent per-(sender, key) nonce sequences stored in the NONCE_MANAGER
system contract, reprices the new payload fields, extends TXPARAM, and
preserves legacy-account-nonce behavior when `nonce_keys == []`.

The work was done in a separate git worktree so the preserved, staged-but-
uncommitted EIP-8141 tree (`frame-tx-8141`, 37 staged files) was never
touched. No commits, no destructive git operations, no pushes.

## 2. Dependency audit (EIP-8250 "requires" list)

| Dependency | Status in this fork | Evidence |
|---|---|---|
| EIP-8141 (frame txs) | Implemented + verified (M1–M10, Project A) | `core/frame_transition.go`, 49 frame tests green |
| EIP-7928 (BAL) | Implemented | `core/state/state_object.go` records storage writes with the per-tx `blockAccessIndex`; `ConstructionBlockAccessList.StorageWrite` deletes the slot from `StorageReads` (bal.go), so nonce slots appear only in `storage_changes` — exactly what 8250 requires |
| EIP-7976 (calldata floor) | Implemented | `TxCostFloorPerToken7976`, frame floor accounting in `FrameTx.IntrinsicGas` |
| EIP-8037 (2D gas) | Implemented | `CostPerStateByte=1530`, `StorageCreationSize=64`, `AccountCreationSize=120`; frame state-gas budgets |
| EIP-8038 (state-access costs) | Implemented via the Amsterdam gas schedule, **with a fork-specific value**: this fork pins `ColdStorageAccessAmsterdam = 3000` (spec text table says 2100 "unchanged") and `StorageWriteAmsterdam = 10000` | `params/protocol_params.go` |

**Consequence of the 8038 deviation:** EIP-8250 defines
`KEYED_NONCE_ACCESS_COST = COLD_STORAGE_ACCESS + STORAGE_WRITE` and computes
12100 from the spec-text values. Following the spec's own derivation rule
("defining it from the primitives lets EIP-8038 repricings flow through"),
this fork's value is **3000 + 10000 = 13000** (`types.KeyedNonceAccessCost`).
This matches what a user pays for the same work (cold read + write) under the
fork's active schedule. XDC's fork-specific gas schedule was NOT altered.

## 3. Spec-drift note (payload fees layout)

The 8250 draft illustrates the payload with an outdated EIP-8141 fees field
(`[max_priority_fee_per_gas, max_fee_per_gas, max_fee_per_blob_gas]`). The
current 8141 draft uses `fees = [max_fee, max_priority_fee_per_gas]`.
Per 8250's normative statements ("This specification is a delta against
EIP-8141"; "No other payload field is changed"), the implementation applies
ONLY the nonce replacement to the current 8141 payload:

    [chain_id, nonce_keys, nonce_seq, sender, frames, signatures, fees, blob_versioned_hashes]
    fees = [max_fee, max_priority_fee_per_gas]   (unchanged from EIP-8141)

## 4. Implemented requirements and evidence

### Payload, encoding, decoding (EIP-8250 §Constants/§Transaction payload)
- `FrameTx` carries `NonceKeys []uint256.Int`, `NonceSeq uint64`, and an
  unexported `legacyPayload` marker (core/types/tx_frame.go).
- Two wire forms are discriminated context-free by the RLP kind of element 1
  (list = keyed, scalar = legacy). Legacy-form transactions re-encode
  byte-identically (hash stability proven by test).
- Decoder rejections (all in `FrameTx.decode`): `nonce_keys` not a list;
  more than `MAX_NONCE_KEYS=16` keys (`ErrFrameTxTooManyNonceKeys`);
  non-canonical minimal integers and keys >= 2^256 (rlp package canonicality
  + `errUint256Large`); `nonce_seq` > MAX (uint64 decode bound);
  `nonce_keys == []` with `nonce_seq == MAX` (`ErrFrameTxEmptyKeysMaxSeq`);
  keys not strictly increasing (`ErrFrameTxNonceKeyOrder`). The semantic
  checks are mirrored in `ValidateStatic` for locally constructed txs.
- Evidence: `TestFrameTxKeyedRLPRoundTrip`, `TestFrameTxLegacyFormRoundTrip`,
  `TestFrameTxKeyedDecodeRejections` (incl. byte-level non-canonical key),
  `TestFrameTxKeyedValidateStatic`.

### Signature hashing
- `sigHashPayload` emits the keyed layout (keys list + seq at the nonce
  position) or the legacy layout for legacy-form txs. The single-use pricing
  exemption does NOT touch payload/sighash bytes.
- Evidence: `TestFrameTxKeyedPricing` (sighash commits nonce_seq),
  `TestFrameTxLegacyFormRoundTrip`, all existing signing tests green under
  the keyed fork.

### Gas pricing (§Gas pricing)
- `IntrinsicGas` adds `STANDARD_TOKEN_COST * tokens_in(nonce_calldata)` +
  `len(keys) * KeyedNonceAccessCost` (13000 here) to intrinsic, and
  `16 * floor_tokens_in(nonce_calldata)` + access cost to the floor, keyed
  form only. `nonce_calldata = rlp(nonce_keys) || priced_nonce_seq` with the
  single-use exemption (`priced_nonce_seq` empty when keys != [] and
  seq == MAX).
- Legacy-form txs keep the exact EIP-8141 formula.
- Evidence: `TestFrameTxKeyedPricing` (hand-computed deltas: +32/+128 for
  empty keys; +26064 intrinsic / +26256 floor for two keys; 16/64 exemption
  deltas), updated `TestFrameTxIntrinsicGas` vector.

### Nonce state (§Nonce state)
- `types.NonceSlot(sender, key) = keccak256(pad32(sender) || be32(key))`;
  slots live in `NonceManagerAddress`
  (0x8250968C12e01A19d6F667b9B2F3b3A4d0e51cB7); absent slots read as 0.
- `NonceManagerCode = 60006000fd` constant defined. The engine never checks
  for the deployment (spec: clients MUST NOT check at the boundary); tests
  deploy it via genesis (nonce 1 + code) per the activation precondition.
  Deployment for a real chain is an activation-time decision (documented
  below), exactly like the 8141 expiry verifier.

### Stateful validity (§Stateful validity)
- In `frame_transition.go`, at the same point as 8141's nonce equality check:
  - keys == []: `nonce_seq == state[sender].nonce` (plus the existing
    EIP-2681-style overflow guard);
  - keys != []: per key, `seq == MAX` requires slot == 0, else
    `seq == slot` (`ErrFrameTxNonceSeq`).
- `tx_legacy_nonce` (pre-state account nonce) is captured unconditionally for
  TXPARAM(0x0D). `SkipNonceChecks` (eth_call) skips checks but still captures.
- Evidence: `TestFrameTxKeyedNonceSequentialAndReplay`,
  `TestFrameTxKeyedNonceMultiKeyAtomic`, `TestFrameTxKeyedNonceSingleUse`.

### Nonce consumption (§Nonce consumption, replaces 8141's approval increment)
- In `vm.FrameTxContext.Approve` (payment approval transition): all 8141
  approval checks first; then the nonce state-gas charge (legacy domain:
  `AccountCreationSize * CPSB` iff sender non-existent under the fork's
  Empty() rule; keyed: `KeyedNonceFirstUseStateGas = 64 * CPSB = 97920` per
  slot reading zero); halt without effects if the frame's state pool can't
  cover it; then `consume_nonce_set` (legacy: increment CURRENT nonce —
  `math.MaxUint64` guard halts; keyed: write seq+1, or MAX for single-use);
  then remaining approval effects (escrow, payer). All journaled: frame
  revert or halt rolls the write back with the approval.
- Bookkeeping does not touch EIP-2929 access lists, is not SSTORE-priced,
  and is journaled + BAL-recorded (direct StateDB.SetState).
- Evidence: `TestFrameTxKeyedNonceBasic` (slot 0->1, legacy nonce untouched,
  first-use 97920 on the approving frame's receipt), `TestFrameTxKeyedNonceStateGasHalt`
  (one-short state limit -> halt -> no consumption), `TestFrameTxKeyedNonceLegacyCreateInterplay`
  (CREATE moves nonce 0->1, approval increments CURRENT 1->2),
  `TestFrameTxKeyedNonceLegacyOverflowHalt` (nonce at MAX at approval ->
  exceptional halt -> whole tx invalid and fully rolled back, incl. the
  earlier CREATE).

### Block access list (§Block access list)
- Protocol writes flow through StateDB.SetState -> recorded in
  `storage_changes` at the tx's block access index with the written value;
  the write removes the slot from `storage_reads` (bal.go semantics), so
  nonce slots never appear as reads.
- Evidence: `TestFrameTxKeyedNonceBlockAccessList` (slot write at index 7,
  absent from reads).

### Transaction introspection (§Transaction introspection)
- TXPARAM(0x01) returns `nonce_seq` (both forms; equals the account nonce
  only for keys == []).
- New params, live only under the keyed fork (`fc.KeyedNonce`, else halt as
  undefined): 0x0D pre-state legacy nonce (fixed at stateful validity);
  0x0E key count; 0x0F `keccak256(be32(len) || concat(be32(key)))`;
  0x10 first key or 0.
- Evidence: `TestFrameTxKeyedNonceTXPARAM` (all five read from EVM and
  asserted against state).

### Fork configuration / activation (§Activation)
- `ChainConfig.KeyedNonceTime *uint64`, `IsKeyedNonce(num,time)` requiring
  Bogota (8250 "MUST activate at or after EIP-8141"), `Rules.IsKeyedNonce`,
  fork-order and config-compat checks, banner/String entries.
- Gate semantics: pre-fork only the legacy payload is valid; post-fork only
  the keyed payload ("pre-fork frame transactions do not survive the
  boundary") — `FrameTx.ValidateFork`, enforced in both engine and pool.
- Unset on every shipped config (mainnet/testnet/devnet): **nothing is
  activated**. XDC activation design is unchanged from M9: maintainer decision
  (timestamp field here; XDC block-height coupling like the frame-tx gate),
  and the NONCE_MANAGER deployment (keyless or genesis alloc) is the same
  class of decision as the expiry verifier (D4).
- Evidence: `TestFrameTxKeyedNonceForkGating` (legacy rejected under keyed
  fork; keyed rejected pre-fork; legacy executes with 8141 semantics
  pre-fork and keeps hash-stable encoding), `TestFrameTxKeyedValidateFork`.

### RPC / JSON
- txJSON and RPCTransaction gain `nonceKeys`/`nonceSeq` for the keyed form;
  `nonce` carries `nonce_seq` for both forms (RPC compat). JSON unmarshal:
  keyed iff nonceKeys/nonceSeq present, else legacy (sender nil-guard kept).
- Evidence: `TestFrameTxKeyedJSON` (both forms round-trip; legacy JSON has
  no nonceSeq).

## 5. Transaction pool — keyed-nonce support (implemented)

Keyed frame transactions (`nonce_keys != []`) are admitted to legacypool via
a dedicated **side pool** (`keyedFrame map[sender]*tx`): their validity is
keyed on NONCE_MANAGER slot sequences, not the sender's account nonce, so
they cannot live in the account-nonce-ordered pending/queue lists. The
design, mapped to the EIP-8250 mempool rules:

- **Identity** `(sender, nonce_keys, nonce_seq)`: one pending keyed frame
  transaction per sender (this is also EIP-8141's public-mempool
  one-frame-tx-per-sender rule, which EIP-8250 leaves unchanged — the side
  pool enforces it structurally).
- **Replacement**: same identity + the configured price bump (strictly better
  on both the tip and the derived fee cap, `keyedFeeBumped` mirrors the
  pending-list rule); a different identity is rejected
  (`ErrKeyedFramePending`).
- **Stateful admission**: `txpool.ValidateKeyedFrameNonceState` checks every
  selected key against the head state (sequential: `slot == nonce_seq`;
  single-use: slot unused) inside `ValidateTransactionWithState`; the
  account-nonce ordering/gap checks do not apply; the conservative
  sender-pays-everything funds check (plus shared pending expenditure) is
  kept.
- **Revalidation/eviction**: on every head change, `demoteUnexecutables`
  re-runs the keyed stateful check; any selected sequence moving (mined keyed
  or legacy-domain transaction, or another keyed consumer) evicts the pending
  transaction. This is the spec's dependency-revalidation rule realized
  against our actual admission dependency set.
- **TXPARAM(0x0D) legacy-nonce dependency**: not applicable at admission —
  the pool deliberately does not simulate validation prefixes (Project A
  policy), so the legacy account nonce is never part of the admission
  dependency set. Documented consequence: an admitted keyed transaction whose
  prefix would read TXPARAM(0x0D) can go stale if the legacy nonce moves;
  worst case is a failed VERIFY at inclusion, bounded by the one-per-sender
  slot.
- **Fork boundary**: `reset()` evicts pooled frame transactions whose payload
  form is invalid under the new head's rules whenever the keyed-nonce gate
  flips (spec: "pre-fork frame transactions ... MUST be evicted from
  mempools").
- **Miner visibility**: `Pending()` surfaces keyed transactions at the end of
  the sender's list with the same tip and gas-cap filters as list
  transactions. Global slot accounting (`pool.all`), price eviction, and the
  address reserver all cover keyed transactions.
- **Rule 6 / MAX_VERIFY_GAS** (EIP-8250 mempool note): the consensus-side
  behavior — the payment-approval keyed-slot creation charge draws from the
  approving frame's `limits.state` — is implemented and tested. The
  `MAX_VERIFY_GAS`/`MAX_VERIFY_STATE_GAS` caps themselves are EIP-8141
  *public-mempool policy* parameters that this fork deliberately does not
  enforce (no prefix simulation; Project A's documented conservative policy),
  so the Rule-6 exemption has no policy surface to attach to here. This is
  an admission-policy choice, not a consensus deviation.
- Unchanged deliberate restriction: **blob-carrying** frame transactions are
  still rejected (no blobpool support), and **prefix simulation** is not run
  (both safe-direction admission restrictions).

- Evidence: `TestKeyedFramePool{Admission,Replacement,Revalidation,
  StatefulAdmission,ForkGate,ForkBoundaryEviction,InsufficientFunds}`
  (7 tests, legacypool), `TestValidateFrameTransactionKeyedForkGate` +
  `TestValidateFrameTransactionKeyedKeysStateless` (validation package),
  plus the full legacypool suite green (no shared-path regressions).

## 6. Files changed (8250 delta vs M10 state)

Implementation (existing files):
- `params/config.go` (+31/-…): KeyedNonceTime gate, Rules.IsKeyedNonce.
- `core/types/tx_frame.go`: dual-form payload, decode discrimination +
  rejections, statics, sighash, pricing, helpers/constants, errors.
- `core/types/transaction_marshalling.go`: txJSON nonceKeys/nonceSeq.
- `core/vm/frame.go`: FrameTxContext fields, frameStateDB GetState/SetState,
  consume_nonce_set payment transition.
- `core/vm/instructions_frame.go`: TXPARAM 0x01 -> nonce_seq; 0x0D–0x10.
- `core/frame_transition.go`: ValidateFork gate, keyed stateful validity,
  tx_legacy_nonce capture, ErrFrameTxNonceSeq.
- `core/txpool/validation.go`: fork gate, keyed stateful admission
  (`ValidateKeyedFrameNonceState` + `ErrKeyedFrameNonce`) in
  `ValidateTransactionWithState`; policy comment updated.
- `core/txpool/legacypool/legacypool.go`: keyed side pool (struct, init,
  `addKeyedFrameTx` + identity/fee-bump helpers, `removeTx` cleanup,
  reserver integration, `demoteUnexecutables` revalidation, `reset()`
  fork-boundary eviction, `Pending()` surfacing).
- `internal/ethapi/api.go`: RPCTransaction nonceKeys/nonceSeq.

Tests:
- `core/frame_tx_keyed_test.go` (new, 16 tests: the 12 consensus tests incl.
  persistence-through-revert/batch-rollback, plus 16-key boundary, eth_call
  SkipNonceChecks path, NONCE_MANAGER-absent characterization, and full
  block-generation integration with block-level BAL assertions).
- `core/types/tx_frame_keyed_test.go` (new, 8 tests).
- `core/txpool/validation_frame_test.go` (+keyed builder, fork-gate and
  stateless-admission tests).
- `core/txpool/legacypool/legacypool_keyed_frame_test.go` (new, 7 tests).

## 7. Verification results (actual runs)

- `go build ./...` — OK. `go vet` on all touched packages — clean.
  gofmt — all touched files clean.
- Focused frame suites
  (`go test -count=1 -run 'TestFrameTx|TestOpcode|TestFrameReceipt|TestSignFrameTx|TestValidateFrameTransaction|TestFrameReceiptTrieRoot|TestFrameReceiptWireHash|TestNonceSlotAndKeysHash' ./core/ ./core/types/ ./core/txpool/ ./eth/protocols/eth/`):
  all four packages `ok`. This includes:
  - 16 keyed engine tests (consumption, replay, multi-key atomicity,
    single-use, disjoint independence, state-gas halt, TXPARAM 0x01/0x0D-0x10,
    CREATE interplay, overflow halt + full rollback, BAL, fork gating,
    persistence through later-frame revert and atomic-batch rollback,
    16-key boundary, SkipNonceChecks simulation path, NONCE_MANAGER-absent
    characterization, block-generation integration with block-level BAL);
  - 8 new types tests (round trips, decoder rejections incl. byte-level
    non-canonical key, statics, fork gate, pricing + exemption, JSON,
    slot/hash helpers);
  - pool gate/stateless tests + all pre-existing 8141 tests (49) unchanged in
    behavior under the keyed fork.
- Full legacypool suite (`go test -count=1 ./core/txpool/ ./core/txpool/legacypool/`):
  both packages `ok`, including the 7 new keyed side-pool tests; no
  regressions in the shared pending/queue/reorg code paths.
- Full 7-package regression sweep
  (`./core/ ./core/types/ ./core/state/ ./core/vm/ ./core/txpool/... ./internal/ethapi/ ./eth/protocols/eth/`):
  13 failures, failure-name set **byte-identical** to the pristine-base sweep
  (`FuzzJournal`, 4 block-encoding tests, `TestEIP7910Config`,
  `TestMessages`, `TestProcessParentBlockHash`, `TestProcessUBT`,
  `TestRPCGetBlockOrHeader`, `TestRPCGetBlockReceipts`,
  `TestRPCGetTransactionReceipt`, `TestRPCMarshalBlock`) — all pre-existing
  XDC-fork divergences documented in Project A. Zero new failures, zero
  removed.

## 8. Security review notes

- Replay protection: consumption is atomic with payment approval inside one
  journal scope; a failed VERIFY or halted approval discards everything
  (tested, incl. whole-tx rollback of earlier frames).
- Key ordering/uniqueness/count enforced at decode AND at static validation;
  strictly-increasing ordering makes overlapping sets unambiguous.
- Multi-key txs: every selected key is checked before any write; the tx is
  invalid if any key mismatches (no partial consumption possible).
- Single-use form: seq bytes remain committed by the sighash (pricing
  exemption cannot be abused to malleate); key permanently exhausted at MAX.
- Legacy domain: increment-current semantics prevent double-spend of the
  sequence when earlier frames mutate the sender nonce (CREATE); overflow at
  MAX halts with no effects.
- NONCE_MANAGER slots: protocol-only writes (no SSTORE pricing, no access-
  list warming, not code-executed); BAL-consistent.
- The keyed payload form can never be confused with the legacy form on the
  wire (RLP kind discrimination is unambiguous), and the fork gate rejects
  the wrong form on either side of the boundary.
- Hash stability: legacy-form bytes/hash/sighash preserved bit-exactly, so
  pre-fork tooling and stored transactions keep their identity.

## 9. Remaining limitations / not claimed

1. **NONCE_MANAGER deployment readiness** (activation precondition, spec
   §Deployment): `NONCE_MANAGER` = `0x8250968C12e01A19d6F667b9B2F3b3A4d0e51cB7`
   with code `60006000fd` (revert(0,0)), deployed by a *keyless* transaction
   (the spec's Deployment section fixes the init code so the address is
   deterministic and no private key exists; ordinary calls to it revert, so
   no pre-fork guard is needed). XDC options, in preference order:
   (a) include the keyless deployment transaction in a block before the
   keyed-nonce fork activates — canonical path, identical in kind to the
   EIP-8141 expiry-verifier deployment (Project A decision D4);
   (b) include the account in genesis state with nonce 1 for any new chain
   config;
   (c) a coordinated irregular state transition only if (a) is no longer
   possible on the target chain — requires explicit maintainer approval and
   is out of scope here. Nothing is deployed or scheduled by this work; the
   engine deliberately never checks for the deployment at the boundary
   (tested: `TestFrameTxKeyedNonceManagerAbsent`).
2. **Activation**: KeyedNonceTime unset everywhere; XDC scheduling remains a
   maintainer decision (M9 D1-style: timestamp field exists; XDC may couple
   to block height as for the frame-tx gate). Nothing is active on any
   shipped config.
3. EIP-8250 is a Draft; the spec's `fees` illustration drifted from the
   current EIP-8141 draft (see §3). If the EIP editors later change the
   delta (e.g. a new tx type byte instead of replacing 0x06's payload),
   this implementation needs to follow.
4. The 8038 cold-storage constant follows THIS fork's schedule (13000); if
   XDC's `ColdStorageAccessAmsterdam` is ever repriced, the keyed access
   cost follows automatically (single derivation point).
5. **Mempool policy residuals** (deliberate, documented in §5): no
   validation-prefix simulation (so the TXPARAM(0x0D) legacy-nonce dependency
   is not observable at admission — bounded staleness risk), blob-carrying
   frame transactions rejected, MAX_VERIFY_(STATE_)GAS policy caps not
   enforced. These are node-local admission policies, not consensus gaps.

## 10. Integration readiness

Ready for integration review: consensus engine, types, encoding, pricing,
introspection, BAL, fork gating, RPC, and keyed-aware mempool admission/
replacement/revalidation/eviction are implemented with focused and
regression evidence; the EIP-8141 test suite passes unchanged under the
keyed fork. NOT claimed: production readiness or activation scheduling;
EIP-8250 remains a Draft spec.

## 11. Normative requirement matrix

| # | Requirement (spec section) | Code | Test | Status |
|---|---|---|---|---|
| 1 | Payload `[chain_id, nonce_keys, nonce_seq, sender, frames, signatures, fees, blob_versioned_hashes]` (§Transaction payload) | `core/types/tx_frame.go` | `TestFrameTxKeyedRLPRoundTrip` | Implemented+tested |
| 2 | Empty `nonce_keys` = legacy domain | same | `TestFrameTxKeyedNonceBasic`, all 49 8141 tests | Implemented+tested |
| 3 | Canonical minimal-length RLP ints; keys < 2^256; seq <= MAX (§decoder rejections) | `FrameTx.decode` (+ rlp pkg) | `TestFrameTxKeyedDecodeRejections` | Implemented+tested |
| 4 | Reject >16 keys; empty+MAX; unsorted/dup keys | `FrameTx.decode`/`ValidateStatic` | `TestFrameTxKeyedDecodeRejections`, `TestFrameTxKeyedValidateStatic` | Implemented+tested |
| 5 | 16 keys accepted (boundary) | same | `TestFrameTxKeyedNonceSixteenKeys` | Implemented+tested |
| 6 | Sig hash commits keys+seq; legacy form byte/hash-stable | `sigHashPayload` | `TestFrameTxLegacyFormRoundTrip`, `TestFrameTxKeyedPricing` | Implemented+tested |
| 7 | Intrinsic += tokens(nonce_calldata) + keys*KEYED_NONCE_ACCESS_COST; floor likewise; single-use seq exemption | `FrameTx.IntrinsicGas` | `TestFrameTxKeyedPricing`, `TestFrameTxIntrinsicGas` | Implemented+tested |
| 8 | `NONCE_SLOT = keccak256(pad32(sender) || be32(key))` | `types.NonceSlot` | `TestNonceSlotAndKeysHash` | Implemented+tested |
| 9 | Stateful validity: per-key seq==slot; single-use slot==0 | `core/frame_transition.go` | `TestFrameTxKeyedNonce{SequentialAndReplay,MultiKeyAtomic,SingleUse}` | Implemented+tested |
| 10 | consume_nonce_set inside payment APPROVE, after checks/costs, before effects | `vm/frame.go Approve` | `TestFrameTxKeyedNonceBasic` | Implemented+tested |
| 11 | Legacy domain increments CURRENT nonce (CREATE interplay) | same | `TestFrameTxKeyedNonceLegacyCreateInterplay` | Implemented+tested |
| 12 | Overflow at MAX halts, no effects; invalid tx fully rolled back | same | `TestFrameTxKeyedNonceLegacyOverflowHalt` | Implemented+tested |
| 13 | First-use state gas 97920/slot from approving frame's pool; insufficient -> halt | same | `TestFrameTxKeyedNonceBasic`, `TestFrameTxKeyedNonceStateGasHalt` | Implemented+tested |
| 14 | No SSTORE pricing, no access-list warming for nonce slots | code path (direct SetState) | covered by 13 (exact gas assertions) | Implemented+tested |
| 15 | Consumption persists through later-frame revert + batch rollback | journaled approval scope | `TestFrameTxKeyedNoncePersistsThroughReverts` | Implemented+tested |
| 16 | BAL: slots in storage_changes at tx index, never storage_reads | 7928 machinery (inherited) | `TestFrameTxKeyedNonceBlockAccessList`, `TestFrameTxKeyedNonceBlockIntegration` | Implemented+tested |
| 17 | TXPARAM(0x01)=nonce_seq; 0x0D-0x10 introspection, fork-gated | `core/vm/instructions_frame.go` | `TestFrameTxKeyedNonceTXPARAM` | Implemented+tested |
| 18 | Fork gate: pre-fork legacy form only; post-fork keyed form only | `ValidateFork` + engine/pool wiring | `TestFrameTxKeyedNonceForkGating`, `TestValidateFrameTransactionKeyedForkGate` | Implemented+tested |
| 19 | Pre-fork frame txs evicted from mempool at boundary | `legacypool.reset` | `TestKeyedFramePoolForkBoundaryEviction` | Implemented+tested |
| 20 | Mempool identity (sender, keys, seq); one pending keyed tx per sender | `legacypool` side pool | `TestKeyedFramePool{Admission,Replacement}` | Implemented+tested |
| 21 | Mempool revalidation on selected-sequence changes | `demoteUnexecutables` | `TestKeyedFramePoolRevalidation` | Implemented+tested |
| 22 | TXPARAM(0x0D) legacy-nonce mempool dependency revalidation | n/a — no prefix simulation (policy) | n/a | Deliberately deferred (policy, §5) |
| 23 | MAX_VERIFY_(STATE_)GAS caps + Rule 6 policy exemption | n/a — policy caps unenforced (Project A) | n/a | Deliberately deferred (policy, §5); consensus-side charge implemented+tested |
| 24 | Activation: at/after 8141; no boundary state change; NONCE_MANAGER precondition | `IsKeyedNonce` requires Bogota; no boundary check | `TestFrameTxKeyedNonceManagerAbsent` | Implemented+tested (gate); deployment = chain-ops decision |
| 25 | eth_call / simulation path (SkipNonceChecks) | engine gate | `TestFrameTxKeyedNonceSkipNonceChecks` | Implemented+tested |
| 26 | RPC/JSON exposure of nonceKeys/nonceSeq | marshalling + ethapi | `TestFrameTxKeyedJSON` | Implemented+tested |
