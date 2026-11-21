# Hegotá → XDC Compatibility Report

Date: 2026-10-29. Author: automated protocol-research pass (Hermes).
Scope: assess the current Hegotá candidate set for the XDC go-ethereum fork
(XDPoS consensus), grounded in the actual repository and prior project work.
Facts are labeled [FACT] (with source), inferences [INFERENCE], and
recommendations [RECOMMENDATION]. Nothing herein activates, deploys, or
schedules anything.

## 1. Sources (all fetched live on 2026-10-29)

| Source | URL | Access date | Key data |
|---|---|---|---|
| ethereum.org Hegotá roadmap | https://ethereum.org/roadmap/hegota/ | 2026-10-29 (page last update 2026-10-02) | status "In planning", expected Q2 2027 (unconfirmed); scheduled: FOCIL + Frame Transactions; CFI list |
| EF Protocol priorities | https://blog.ethereum.org/2026/09/07/protocol-priorities | 2026-10-29 | Hegotá scoping season; 62 EIPs proposed; PQ north star Dec 2029; Glamsterdam ~Dec 2026 |
| EF Hegotá tier list | https://blog.ethereum.org/2026/09/07/protocol-hegota-eips | 2026-10-29 | per-EIP tiers/grades (62 EIPs) |
| EIP specs | https://eips.ethereum.org/ + ethereum/EIPs master | 2026-10-29 | statuses/frontmatter; last-commit dates via GitHub API |
| XDC fork repo | /home/tanmay/xdc-fix-work/go-ethereum (base 48a7727) + worktrees | 2026-10-29 | code evidence |

## 2. Hegotá status summary [FACT]

- Hegotá = Bogotá (EL) + Heze (CL), the upgrade after Glamsterdam. Status
  "In planning"; expected mainnet Q2 2027, date NOT confirmed; next milestone
  "Sepolia fork, 2027" (ethereum.org roadmap, 2026-10-02 update).
- Scheduled for inclusion (2): **EIP-7805 FOCIL** (headliner, S-tier 4.0) and
  **EIP-8141 Frame Transactions** (S-tier 3.89). [tier list, 2026-09-07]
- Considered-for-inclusion per the roadmap page: EIP-3298, 7668, 7906, 7979,
  8015, 8131, 8163, 8198, 8250, 8253, 8272, 8279, 8365 ("being considered
  means client teams are evaluating a proposal, not that it will ship").
- EIP-8081 is the Hegotá Meta EIP (Draft, created 2025-11-11).
- All normative specs cited here remain **Draft** status on eips.ethereum.org.

## 3. Candidate-status table (tier list grades; S=4..DFI=0)

| EIP | Title | Tier (avg, grades) | Status | XDC classification |
|---|---|---|---|---|
| 8141 | Frame Transactions | S (3.89, 9) | Draft | **Implemented** (Project A, M1–M10) |
| 7805 | FOCIL | S (4, 7) | Draft | Research done (Phase-1 audit); CL-heavy adaptation |
| 8250 | Keyed Nonces for Frame Tx | A (3.12, 8) | Draft | **Implemented** (this project, M1–M5+audit) |
| 8272 | Recent Roots for Frame Tx | A (2.86, 7) | Draft | Compatible with adaptation (needs 7843: present) |
| 3298 | Removal of Refunds | A (3.17, 6) | Draft | Compatible with adaptation (XDC gas-schedule coupling) |
| 8131 | Unified Transaction Content Floor | A (2.67, 6) | Draft | Compatible with adaptation (builds on 7976: present) |
| 8279 | Block Access List Byte Floor | A (2.71, 7) | Draft | Compatible (7928 present; pricing wiring only) |
| 7906 | Transaction Assertions (state-diff opcode) | A (2.43, 7) | Draft | Compatible with adaptation (EVM opcode + txpool) |
| 8015 | Remove Deposit & eth1data Fields | A (3.14, 7) | Draft | Likely N/A or trivial (XDC has no eth1 deposit bridge) |
| 8365 | BLS Withdrawal Credential Retirement | A (3, 8) | Draft | N/A to XDPoS (no BLS withdrawal credentials) |
| 8198 | Quick Slots | B (2, 6) | Draft | Blocked/conflicting: XDPoS has 2s rounds + finality model |
| 8253 | Bump Nonce of Zero-Nonce Storage Accounts | B (2.29, 7) | Draft | Compatible; small; needed only if XDC adopts trie migration |
| 7668 | Remove Bloom Filters | C (1.5, 6) | Draft | Optional; RPC/log infra impact; not required |
| 8355 | ML-DSA Verification Precompiles | C (1, 9) | Draft | Optional precompile; PQ roadmap relevance only |
| 8163 | Reserve EXTENSION (0xae) Opcode | DFI (2.33, 6) | Draft | Declined upstream; do not adopt |
| 7979 | Call and Return Opcodes | DFI (1.33, 6) | Draft | Declined upstream; do not adopt |

## 4. Per-EIP analysis (primary set)

### 4.1 EIP-8141 — Frame Transactions [FACT: S-tier headliner, Draft]

1. Purpose: native account abstraction — accounts define their own validity
   (multisig, social recovery, gas sponsorship, PQ signature schemes) without
   bundlers/relayers.
2. Status: Draft (created 2026-01-29; last spec commit 2026-10-25); S-tier
   headliner for Hegotá. Requires 1559, 2718, 2780, 3529, 3607, 4844, 7594,
   7702, 7708, 7778, 7825, 7976, 8037.
3. Dependencies on XDC: the Hegotá stack (Amsterdam/Bogota: 8037 2D gas,
   7976 floor, 7928 BAL, 8038 state-access costs) is already implemented in
   this fork and verified in Projects A/B.
4. Impact (EL: tx type 0x06, engine, receipts, txpool, RPC; CL: none;
   networking: eth/69 receipt compression) — all mapped and implemented in
   Project A.
5. XDC evidence: worktree `/home/tanmay/xdc-fix-work/frame-tx-8141`
   (branch feat/eip-8141-frame-transactions): `core/frame_transition.go`,
   `core/vm/frame.go`, `core/types/tx_frame.go`, receipts
   (consensus/storage/wire/RPC), 49+ tests, regression-identical sweeps.
6. Reusable for other work: frame engine, TXPARAM introspection, fee model.
7. XDC deltas: XDPoS 2s rounds (expiry frames measured in slots must map to
   round events, not CL slots — N/A since 8141 is EL-only); XDC gas-schedule
   pins; special-tx (masternode signing) coexistence preserved.
8. Remaining: devnet soak, activation design D1–D4 (maintainer), public
   mempool prefix policy (deferred, documented).
9. Classification: **implemented** (code review ready).
10. Priority: P0 — it is the Hegotá headliner and the base for 8250/8272.

### 4.2 EIP-8250 — Keyed Nonces [FACT: A-tier "Frames core", Draft]

1. Purpose: per-(sender,key) nonce sequences in NONCE_MANAGER storage —
   parallel/lane nonces, single-use nullifier keys, shared-sender anonymity.
2. Status: Draft (created 2026-04-16; last commit 2026-10-19). Requires
   7928, 7976, 8037, 8038, 8141.
3–8. Implemented in this project (see EIP_8250_IMPLEMENTATION_REPORT.md):
   dual-form payload, consume-on-approve, state gas, BAL, TXPARAM 0x0D–0x10,
   fork gate, keyed mempool side pool (admission/identity/replacement/
   revalidation/boundary eviction), 31 new tests, regression-identical sweep.
   NONCE_MANAGER deployment + activation scheduling remain chain-ops
   decisions (documented plan).
9. Classification: **implemented** (code review ready; Draft-spec caveat).
10. Priority: P0 — completes the frame-transaction nonce model; ships with
    8141 by design ("Frames core").

### 4.3 EIP-8272 — Recent Roots for Frame Transactions [FACT: A-tier "Frames core", Draft, created 2026-05-15]

1. Purpose: a canonical VERIFY frame lets a frame transaction prove
   freshness of application "recent roots" (privacy trees, wallet
   authorization roots) against a recent-root contract — without envelope
   changes or new opcodes. 1–16 (source_id, slot, root) tuples per frame;
   root sources are writers keyed by (address, salt).
2. Status: Draft; requires **EIP-7843 (SLOTNUM, Last Call)** + EIP-8141.
3. Dependencies on XDC: SLOTNUM opcode — **already present** in this fork
   (`core/vm/opcodes.go:108` `SLOTNUM OpCode = 0x4b`, enabled via
   `enable7843` in the Bogota jump table). EIP-8141: implemented (Project A).
   [FACT: repo grep 2026-10-29]
4. Impact: EL-only; a new system contract (recent-root contract), a canonical
   frame shape, storage layout; public-mempool validation runs the frame
   before account validation (mempool policy surface). No CL changes.
5. XDC evidence: no recent-root contract or frame shape exists yet (would be
   new code on top of Project A's frame engine).
6. Reusable from Projects A/B: frame engine, VERIFY semantics, TXPARAM,
   side-pool admission patterns.
7. XDC deltas: "slot" semantics — 8272's current-slot source is SLOTNUM; on
   XDC the nearest equivalent is block number/round. XDC's block-based fork
   gating vs timestamp gating (Amsterdam/Bogota are timestamp forks on
   non-XDC configs) needs the same adaptation Project A documented.
8. Work: small-to-medium; test strategy mirrors Project A (engine-level
   StateDB harness + pool tests). Security risks: stale-root acceptance
   windows after reorgs; mempool ordering assumptions.
9. Classification: **compatible with adaptation**.
10. Priority: P2 — valuable for privacy applications; only after 8141/8250
    are through review.

### 4.4 EIP-7805 — FOCIL [FACT: S-tier headliner, Draft, created 2024-11-01; RESEARCH ONLY per mission scope]

1. Purpose: fork-choice-enforced inclusion lists — a validator committee
   force-includes transactions, defeating builder/proposer censorship.
2. Status: Draft; Hegotá headliner.
3. Dependencies: Ethereum CL machinery (attester committees, fork-choice
   rule, slot timing).
4. Impact: primarily consensus-layer + networking (IL gossip), with EL
   payload-validity interaction.
5. XDC evidence: **XDPoS has no attester/fork-choice split** — the Phase-1
   audit (FOCIL_XDC_GAP_ANALYSIS.md, Project A worktree) found the
   enforcement point maps to vote-suppression in `ProposedBlockHandler`
   (engine_v2/utils.go:550): a censoring proposer's block cannot reach the
   0.667 QC if voters withhold votes on IL-unsatisfying blocks. Block
   validity stays IL-free (followers never see ILs).
6. Reusable: txpool admission predicates (Projects A/B) for IL satisfaction
   checks per tx type (frame txs have explicit sender/approval contexts).
7. XDC deltas: committee = snapshot-derived next-K-leaders (no BLS/RANDAO);
   deadlines become round-event-based (2s rounds, no wall-clock slots); IL
   gossip = new eth wire code + Bft-extension. This is an architectural
   adaptation, not a port.
8. Plan: M1 shadow data plane (types+committee+pool+gossip, observe-only) →
   M2 proposer union-inclusion → M3 vote gate behind an unscheduled fork
   gate (from the Phase-1 audit). Security risks: liveness coupling, QC
   timing, vote withholding incentives.
9. Classification: **compatible with adaptation** (research complete;
   implementation NOT started and out of scope here).
10. Priority: P1 — Hegotá's headliner; XDC's censorship-resistance story
    will be asked about; but it is the largest adaptation in the set.

## 5. Secondary candidates with XDC impact (brief)

- **EIP-3298 Removal of Refunds (A)**: removes gas refunds. XDC fork: refund
  counter exists (`core/state/statedb.go`). Impact: gas accounting + VM.
  Classification: compatible with adaptation (XDC gas-schedule coupling;
  TIPTRC21Fee interactions must be re-checked). [INFERENCE from code]
- **EIP-8131 Unified Transaction Content Floor (A)**: unified calldata floor;
  the fork already implements EIP-7976 floors (state_transition.go).
  Classification: compatible with adaptation. [INFERENCE]
- **EIP-8279 BAL Byte Floor (A)**: prices BAL bytes; the fork implements
  EIP-7928 BAL. Classification: compatible; pricing wiring only. [INFERENCE]
- **EIP-7906 Transaction Assertions (A)**: state-diff assertion opcode;
  touches VM + txpool. Classification: compatible with adaptation.
  [INFERENCE]
- **EIP-8015 (A)** remove deposit/eth1data fields: XDC never had the eth1
  deposit bridge; likely trivial or N/A. [INFERENCE — confirm block header
  fields in core/types/block.go before scheduling]
- **EIP-8365 (A)** BLS withdrawal credential retirement: N/A — XDPoS has no
  BLS withdrawal credentials. [FACT about XDPoS architecture]
- **EIP-8198 Quick Slots (B)**: shortens slot times; XDPoS uses 2-second
  rounds with a different finality model — conflicts/not portable.
  Classification: blocked by architecture. [INFERENCE]
- **EIP-8253 (B)** zero-nonce storage-account nonce bump: only relevant if
  XDC adopts the state trie migration; small change. Optional.
- **EIP-7668 (C)** remove bloom filters: optional; affects log/RPC infra.
- **EIP-8355 (C)** ML-DSA precompiles: optional; PQ-relevant; premature per
  the tier note.
- **EIP-8163 / EIP-7979 (DFI)**: declined upstream; do not adopt.

## 6. Dependency graph (XDC-relevant slice)

```
SLOTNUM (7843, present) ──┐
                          ├─> EIP-8272 Recent Roots (A) ──> privacy apps
EIP-8141 Frame Tx (S) ────┤         ▲
   (Project A: done)      ├─────────┘
   ├──> EIP-8250 Keyed Nonces (A) (Project B: done)
   │        requires: 7928 BAL, 7976 floor, 8037 2D gas, 8038 costs (all present)
   └──> EIP-7906 Assertions (A, frames extensions)
EIP-7928 BAL (present) ──> EIP-8279 BAL byte floor (A)
EIP-7976 floor (present) ──> EIP-8131 unified content floor (A)
EIP-7805 FOCIL (S) ──> XDPoS adaptation track (independent of EL stack)
```

## 7. Risk register

| Risk | Severity | Mitigation |
|---|---|---|
| All cited EIPs are Draft; spec churn invalidates implementation details | Medium | pin spec snapshots; re-diff on each revision (done for 8141/8250; both unchanged at audit time) |
| FOCIL adaptation touches XDPoS liveness (vote withholding vs QC) | High | shadow-mode M1 first; no enforcement until devnet evidence; maintainers decide |
| XDC gas-schedule pins diverge from upstream derivations (e.g. 13000 vs 12100 keyed access) | Low-Med | single derivation points from fork params (done); document each |
| Block-based (XDC) vs timestamp-based (Amsterdam/Bogota) fork gating mismatch | Medium | established pattern: gate field + XDC coupling decision per fork (D1) |
| Keyless system-contract deployments (expiry verifier, NONCE_MANAGER, recent-root contract) need pre-fork inclusion ops | Medium | documented deployment plans; devnet rehearsal |
| Public-mempool policy deltas (no prefix simulation) vs spec text | Low | safe-direction restrictions; documented; revisit if public propagation required |

## 8. Recommended implementation order [RECOMMENDATION]

1. **Review + merge-readiness of Projects A/B** (8141 + 8250) — done code,
   pending human code review; zero-new-failure regression posture.
2. **Devnet** with frame txs + keyed nonces active, incl. NONCE_MANAGER and
   expiry-verifier deployments (chain-ops per the documented plans).
3. **EIP-8272 Recent Roots** (small, builds on 8141; SLOTNUM present).
4. **FOCIL adaptation M1 (shadow, observe-only)** — largest item, CL-heavy;
   start research-to-prototype after the EL stack is reviewed.
5. Secondary repricing EIPs (3298/8131/8279) — schedule with the next gas
   schedule review; each is small but touches fee semantics.
6. Optional/C-tier items only on explicit product demand.
