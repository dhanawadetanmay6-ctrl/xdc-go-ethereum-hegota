# FOCIL (EIP-7805) on XDC — Phase 1 Gap Analysis

Status date: 2026-10-29
Workspace: /home/tanmay/xdc-fix-work/frame-tx-8141 (git worktree)
Base repo: /home/tanmay/xdc-fix-work/go-ethereum (untouched; master + xone-fix-20261025)
Branch: feat/eip-8141-frame-transactions @ 48a7727 — EIP-8141 work M1–M9 staged, unmodified
Spec: https://eips.ethereum.org/EIPS/eip-7805 — status DRAFT (created 2024-11-01).
      Pinned snapshot: /home/tanmay/xdc-fix-work/eip-7805-spec-snapshot.md (fetched 2026-10-29 from ethereum/EIPs master, 204 lines).
      CL sub-specs referenced by the EIP (NOT vendored; read before any implementation):
      ethereum/consensus-specs @ e678deb7 — specs/_features/eip7805/{beacon-chain,fork-choice,p2p-interface,validator}.md

## Safety posture (mandatory)

- AUDIT ONLY. No source modified. No commits, no pushes, no activation values.
- The existing EIP-8141 Frame Transactions worktree state is preserved byte-for-byte
  (verified: `git status` before/after — only this file added).
- XDC mainnet/apothem chain configs must NOT gain activation values. Activation
  design is documented as open maintainer decisions, never scheduled.
- No live-network assumptions. All proposed testing is local/harness-based.

---

## 1. What FOCIL actually requires (rule inventory from the pinned spec)

| # | FOCIL rule (spec section) | Mechanism |
|---|---|---|
| R1 | Per-slot IL committee of 16 validators (`IL_COMMITTEE_SIZE`) selected from the validator set; committee root computable by all (Beacon-chain preset, `DOMAIN_IL_COMMITTEE`) | Committee selection |
| R2 | Each committee member builds one `InclusionList{slot, validator_index, inclusion_list_committee_root, transactions}` from its subjective mempool view, signs it (BLS), gossips it during slot N (t=0–8s) (Roles / containers) | IL construction + gossip |
| R3 | All validators store ILs passing 6 CL P2P validation rules: slot match (current/previous), committee-root match, ≤2 ILs per member, valid signature, signer in committee, ≤ `MAX_BYTES_PER_INCLUSION_LIST` (8 KiB) (CL P2P Validation Rules) | Gossip admission |
| R4 | View freeze deadline t=9s of slot N: no new ILs stored after it; forwarding continues; equivocation evidence recorded until slot N+1 attestation deadline (Validators) | Timing |
| R5 | Proposer/builder of slot N+1 includes transactions from all stored non-equivocator ILs; anywhere-in-block placement; conditional inclusion (missing tx OK if invalid when appended at end, or block full) (Proposer/Builder/Rationale) | Block production |
| R6 | EL check on every new payload: after executing all txs, for each IL tx T not in B — if `T.gas ≤ gas_left` and nonce+balance of `T.origin` valid against post-state S → return `INCLUSION_LIST_UNSATISFIED` (Execution Layer) | Satisfaction predicate |
| R7 | Attesters vote ONLY for blocks passing R6 against their frozen IL view, excluding equivocators' ILs (Attesters / Fork choice) | Fork-choice enforcement |
| R8 | IL equivocation: >1 distinct IL from one member → mark equivocator, ignore all their ILs; network forwards ≤2 per member (bounded ×2 bandwidth) (Security / IL Equivocation) | Equivocation handling |
| R9 | New gossip topic for `SignedInclusionList`; new req/resp topic by committee index; optional builder RPC for missing ILs (P2P changes) | Networking |
| R10 | Engine API: `engine_getInclusionListV1`, `forkchoiceUpdated` payloadAttributes carries IL, `newPayload` returns `INCLUSION_LIST_UNSATISFIED` (Engine API Changes) | CL↔EL interface |
| R11 | No incentive mechanism; 1-of-N honesty on the committee suffices (Rationale) | Incentives |
| R12 | Hard fork; block VALIDITY rules unchanged on EL — an unsatisfied block is valid but unattestable (Backwards Compatibility) | Fork gating |

Core property (spec §Fork-choice enforced): the mechanism works because attesters
REFUSE VOTES for non-compliant blocks, so such blocks cannot become canonical.
Any XDC adaptation that lacks an equivalent vote-side enforcement does NOT
provide censorship resistance, regardless of IL data structures existing.

## 2. XDC XDPoS v2 ground truth (traced in this tree, file:line)

### 2.1 Validator set and leader selection

- Masternode set: fixed per epoch, from the gap-block snapshot. `SnapshotV2`
  (consensus/XDPoS/engines/engine_v2/snapshot.go:35) carries `NextEpochCandidates`,
  `Penalties`; `CandidatePool()` = active ∪ penalized.
- Epoch = 900 blocks, gap = 450 (`x.config.Epoch`, `x.config.Gap`); next-epoch set
  snapshotted at gap block, takes effect at epoch switch (round-based epochs:
  900 ROUNDS, not blocks).
- Leader for a round: `masterNodes[round % Epoch % len(masterNodes)]` —
  produced in `yourturnAligned` (engine_v2/mining.go:67), enforced in
  `verifyHeader` (engine_v2/verifyHeader.go:391, hard-fail `ErrNotItsTurn`
  only in fullVerify; A.87 bulk-sync tolerance otherwise).
- Epoch-switch header carries the canonical ordered validator list
  (`header.Validators`); verifyHeader adopts that order (verifyHeader.go:384).
- Mainnet V2 config (params/config.go:2379+ `MainnetV2Configs`):
  MaxMasternodes=108, CertThreshold=0.667, MinePeriod=2s, TimeoutPeriod
  10–600s by round range. XDC chains: block-based forks, `ChainConfig.IsXDC()`
  (params/config.go:2965).

### 2.2 Voting, QC, finality (the enforcement surface FOCIL needs)

- Every masternode votes on each proposed block — there is no separate
  attester committee; the full masternode set plays both roles.
- Vote = secp256k1 sig over `VoteForSign{BlockInfo{hash,round,number}, gapNumber}`
  (core/types/consensus_v2.go:50,234; `VoteSigHash` = rlpHash).
- Block receipt path: eth/handler_eth.go:265 (NewBlock after import) and
  miner/xdpos_worker.go:842 (self-mined) → `HandleProposedBlock`
  (consensus/XDPoS/xdpos_api.go:199) → `ProposedBlockHandler`
  (engine_v2/utils.go:550): processQC → allowedToSend → **verifyVotingRule →
  sendVote**. THIS is the exact hook point where an IL-satisfaction gate
  belongs.
- Vote decision rules (engine_v2/vote.go:355 verifyVotingRule):
  one-vote-per-round (`highestVotedRound`, fix #1330 latch-only-on-broadcast),
  HotStuff lock rule (`lockQuorumCert` + isExtendingFromAncestor).
- QC: ≥ ceil(N×0.667) valid sigs (vote.go:152 threshold, :303
  onVotePoolThresholdReached → processQC engine_v2/utils.go:321).
  Finality = 3-chain of consecutive QCs.
- **No QC = no canonical progress**: the next round's proposer must embed the
  previous QC in header Extra (`ExtraFields_v2{Round, QuorumCert}`,
  consensus_v2.go:202). Withheld votes ⇒ timeout ⇒ TC ⇒ next round/leader.
  ⇒ In XDPoS, vote-gating IS fork-choice enforcement. R7 maps natively.

### 2.3 Block production and transaction selection

- miner/xdpos_worker.go `commitNewWork` (V2): Prepare (stamps Round+QC into
  header Extra) → makeEnv → `fillTransactions` → Finalize → Seal (ECDSA into
  header.Validator) → commitMinedBlock. Epoch-switch blocks include NO txs
  (design mirrors XDPoSChain).
- Tx selection: miner/worker.go:589 `fillTransactions` →
  `txpool.Pending(PendingFilter{MinTip, BaseFee, ...})` →
  `txorder.TransactionsByPriceAndNonce` (price+nonce heap) →
  `commitTransactions` (worker.go:472) with per-block gas pool, blob cap,
  size cap, nonce-error skip rules. Pure proposer's-choice: nothing obliges
  inclusion of any tx. This is the censorship surface FOCIL targets.
- Mempool: txpool pending set is the IL-builder data source (maps to R2
  "subjective view of the mempool").

### 2.4 Networking

- BFT messages ride the eth wire protocol: VoteMsg=0xe0, TimeoutMsg=0xe1,
  SyncInfoMsg=0xe2 (eth/protocols/eth/protocol.go:110-112).
- Dispatcher: eth/bft/handler.go `Bfter` — narrow `EngineV2` interface
  (Verify/Handler × Vote/Timeout/SyncInfo), distance guards, verify-then-
  forward (goroutine-wrapped blocking send, FIX B), broadcast loop.
- Outbound: eth/handler_xdc.go BroadcastVote/BroadcastTimeout/BroadcastSyncInfo
  → peer set.
- An IL message would add: new code (e.g. 0xe3), `types.InclusionList` +
  `SignedInclusionList` analog in core/types, a `Bfter.InclusionList` handler
  or parallel IL dispatcher, and a pool keyed by (round/committee-slot, member).

### 2.5 Equivocation and penalties (existing analogs for R8)

- Vote/QC equivocation forensics: engine_v2/forensics.go (ForensicsMonitoring,
  ProcessForensics, emits `types.ForensicsEvent`; detective, not preventive).
- Penalties: consensus/XDPoS/penalty.go CalculatePenalties + HookPenalty
  (missed-round penalties remove masternodes from the active set at epoch
  boundaries). IL-layer equivocation could reuse the forensics event path;
  on-chain penalties for IL misbehavior would be a NEW governance decision.

### 2.6 EIP-8141 Frame Transactions interactions (existing work, NOT modified)

- New tx type 0x06 with explicit sender, non-ECDSA signature schemes,
  per-frame 2D gas, approval contexts, own receipt shape; txpool admission
  gated (core/txpool/validation.go); M1–M9 complete and staged.
- Interaction points for FOCIL:
  1. IL byte accounting must measure RLP-encoded frame txs like any other.
  2. R6's "nonce and balance of T.origin" predicate is defined for classic
     EOAs; frame txs carry explicit senders and approval-scoped authorization —
     the satisfaction check needs a per-type validity predicate (reuse the
     txpool admission predicates, NOT a bespoke check).
  3. Deferred 8141 gaps (blob gossip, public-mempool validation-prefix policy)
     mean frame txs may have weaker mempool propagation — ILs are one more
     propagation path and must not become a bypass for admission policy.
  4. No conflict found: 8141 touches tx/signer/vm/receipt/txpool; FOCIL touches
     consensus vote-gating + worker inclusion + BFT gossip. Disjoint surfaces.

## 3. Rule-by-rule mapping: what maps, what needs redesign

| Rule | Verdict | XDC design |
|---|---|---|
| R1 committee | REDESIGN | No BLS, no RANDAO. Deterministic committee from the epoch snapshot: e.g. K masternodes at round-derived offsets into the ordered masternode list for round R (`masterNodes[(leaderIndex+1+i) % N]`, i=0..K-1 — the next K leaders). Verifiable by every node from the gap snapshot; no committee-root container needed — membership proof = snapshot lookup. K is a new consensus parameter (FOCIL uses 16; XDC N=108 mainnet). |
| R2 IL build+sign | MAPS | `types.InclusionList{Round, GapNumber, Signer?, Transactions []}` signed secp256k1 via `signSignature` (same key as votes). Build from `txpool.Pending` with a deterministic-ish policy (e.g. highest tip, oldest-pending — implementer discretion per spec). GapNumber ties the IL to the certifying snapshot, exactly like votes/timeouts. |
| R3 gossip admission | MAPS | Port the 6 rules: (1) round match current/current+1 (mirrors voteHandler round gate), (2) snapshot/gap consistency instead of committee-root, (3) ≤2 per member, (4) ECDSA sig verify via `verifyMsgSignature` against the round's committee, (5) membership check, (6) byte cap (new constant, default 8 KiB). No EL validity checks at gossip (spec intent: bandwidth-bound DoS protection only). |
| R4 view freeze | REDESIGN | XDC has no 12s slot with t=0/4/8/9/11s wall-clock marks; MinePeriod=2s and rounds are event-driven (QC/TC). Freeze must be event-based: ILs for round R are accepted from the moment the round R−1 block is imported until the local node SEES the round R proposal (or votes at R). Anything arriving for R after that is recorded only as equivocation evidence. Same-slot property: ILs for R are built on the R−1 new-head event and gossip during R−1's ≈2s window. |
| R5 proposer inclusion | MAPS | In `commitNewWork` (xdpos_worker.go) after `fillTransactions`: append the union of stored non-equivocator IL txs for this round, skipping ones already present/invalid/gas-exceeding (the spec's §Payload Construction loop; optimize with the EOA nonce/balance tracking the EIP recommends). "Anywhere-in-block" ⇒ append-at-end is spec-compliant and simplest. |
| R6 satisfaction predicate | MAPS (new code) | New core helper (e.g. core/inclusion_list.go): given post-state S of B, gas_left, and the frozen IL set — for each IL tx not in B: if it fits gas AND passes txpool-style validity (nonce/balance/ per-type predicate incl. frame txs) against S → UNSATISFIED. Deterministic given (block, IL set). Must reuse existing admission predicates to avoid a second notion of validity. |
| R7 attester vote-gate | MAPS — THE CORE | In `ProposedBlockHandler` after processQC / before sendVote: run R6 against the voter's frozen IL view for this round. Fail ⇒ do not vote (log + metric). The block then cannot reach the 0.667 QC from honest voters ⇒ timeout ⇒ next leader. This is precisely FOCIL's "cannot be canonical" transplanted: XDC canonical progress REQUIRES the previous QC. Block validity (verifyHeader/Process) MUST remain IL-free — see §4 liveness analysis. |
| R8 equivocation | MAPS | ≤2 ILs per member forwarded; >1 ⇒ ignore all that member's ILs for the round. Reuse the forensics event channel for evidence (new ForensicsType "IL"). No on-chain penalty initially (governance decision D2). |
| R9 networking | REDESIGN (small) | New eth wire code 0xe3 `InclusionListMsg` + optional `GetInclusionListsMsg`/response pair by (round, committee index). Extend `Bfter` with an `InclusionList` handler (verify→forward→engine handler, same pattern as Vote) or a sibling dispatcher. Distance guard analogous to maxBlockDist. |
| R10 engine API | NOT APPLICABLE | No CL/EL split in XDC (merged client). Internal equivalents: worker reads an IL store; an admin/RPC `XDPoS_getInclusionLists(round)` for observability (debug only). |
| R11 no incentives | MAPS | Adopt as-is (1-of-N honesty). XDC's existing penalty system (missed rounds) is orthogonal; do NOT couple IL participation to penalties in v1. |
| R12 fork gating | MAPS (discipline) | All behavior behind a new block-based fork gate (XDC chains use block forks; e.g. `IsHegotaFOCIL` — NAME AND VALUE UNSCHEDULED, maintainer decision D1). Non-upgraded nodes must keep validating blocks identically: IL enforcement is vote-side only, so mixed-network blocks remain valid to all — upgraded validators simply withhold votes from censoring proposers. |

## 4. Consensus-safety and liveness risks (honest analysis — no free CR claims)

### 4.1 Safety (does the design actually enforce inclusion?)

- Enforcement path: censoring proposer at round R omits an IL tx that ≥1
  honest committee member published and that is validly includable → each
  honest voter whose frozen view contains that IL runs R6, fails it, withholds
  its vote. Withheld votes from > N − ceil(N×0.667) masternodes ⇒ no QC ⇒
  the block cannot anchor the next round ⇒ TC moves the chain to round R+1
  with the next leader. The censored tx's next inclusion opportunity is
  immediate (its ILs persist into the next committee's view if re-listed).
- This holds WITHOUT any change to block validity — critical, because:
  non-validator followers and archive/sync nodes never see ILs and MUST NOT
  need them. Making IL satisfaction a block-validity rule would introduce a
  data-availability dependency (block valid only if you also have the IL set)
  and split the network between IL-tracking and non-tracking nodes. FOCIL
  makes the same choice ("block is valid, CL will not attest").
- Residual subjectivity (inherited from FOCIL, must be documented): the vote
  decision depends on each voter's LOCAL frozen IL view. Two honest voters can
  disagree on satisfaction for the same block. Consequences:
  - A proposer who included the full union of all ILs *it* saw can still miss
    an IL some voters saw (network partition / late propagation). Those voters
    withhold; if they are ≤ N − ceil(N×0.667) (i.e. < ~1/3), QC still forms —
    the mechanism degrades gracefully, exactly like FOCIL.
  - Equivocation exclusion (R8) bounds the worst case: an adversarial member
    spraying divergent ILs gets ignored entirely.
- Equivocation of the IL gate itself: none possible — the gate only SUPPRESSES
  votes; it never creates them. One-vote-per-round and the lock rule are
  untouched. Safety of XDPoS (no conflicting QCs) is preserved because the
  gate only shrinks the voting set.
- NEW attack to analyze in Phase 2: "IL stuffing" — committee member fills ILs
  with txs that are valid at gossip time but become invalid by inclusion time
  (self-destructing allowance drains, etc.), forcing honest proposers' blocks
  to fail R6 for voters who stored those ILs. Mitigations: conditional
  inclusion (invalid-at-append ⇒ exempt) already covers this — the R6 check
  treats a tx as satisfied-if-unincludable; the residual risk is only
  bandwidth/CPU, bounded by the byte cap and committee size.

### 4.2 Liveness (can the mechanism stall the chain?)

- Worst case for liveness: a proposer that did NOT receive some ILs builds a
  block honest voters reject → wasted round → 10s-class timeout (per-round
  TimeoutPeriod) → next leader. A streak of unlucky/malconnected proposers
  costs timeouts but CANNOT deadlock: the gate only suppresses votes for
  specific blocks, rounds always advance via TC. XDPoS's existing
  round-skew/timeout machinery (timeout.go, ExpTimeoutConfig) absorbs this.
- Timing risk specific to XDC: FOCIL budgets ≈9s of IL propagation per 12s
  slot; XDC rounds are ≈2s (MinePeriod) with leader changes per round. IL
  gossip must complete within roughly one round for the next leader to see
  the committee's lists. This argues for: (a) small IL byte cap, (b)
  verify-then-forward immediately on receipt (Bfter pattern), (c) optional
  IL req/resp on proposal, (d) committee = upcoming leaders so IL publishers
  and consumers are topologically close (they are the same small set).
- Empty-IL fast path: if a node stored no ILs for round R, the R6 gate is
  vacuous — zero behavior change. Nodes with empty mempools (or all-empty
  ILs) vote exactly as today. Liveness in the absence of censorship pressure
  is unchanged BY CONSTRUCTION.
- QC margin: with N=108 and threshold 0.667, up to ~36 non-votes still allow
  a QC. IL-view divergence among <1/3 of voters cannot stall the chain;
  divergence among >1/3 stalls only the affected round (timeout recovers).

### 4.3 What this design does NOT claim

- No claim that censorship resistance follows from IL data structures alone:
  CR comes exclusively from R7 vote suppression reaching the QC threshold.
- No claim of Byzantine-fee-market neutrality, no incentive compatibility
  (per spec R11 — altruistic committee assumption, 1-of-N honesty).
- No claim about V1 (pre-switch) blocks: mechanism is V2-only.

## 5. Networking and message-format requirements (Phase 2 spec inputs)

New/changed items (all behind the fork gate; wire codes must not collide with
the existing XDC range 0xe0–0xe2 and standard eth/69 codes):

1. `types.InclusionList` (core/types/consensus_v2.go or new file):
   `{Round Round, GapNumber uint64, Transactions []types.Transaction}` —
   signer recovered from signature (mirror `Vote` unexported-signer pattern),
   `Hash()`, `PoolKey()` = round:gap:signer, `DeepCopy()`.
2. `types.InclusionListForSign{Round, GapNumber, TxListHash}` +
   `InclusionListSigHash` (rlpHash). Signing the tx-list HASH (not the txs)
   keeps the signed payload small; the wire message carries full txs.
3. eth wire: `InclusionListMsg = 0xe3` (broadcast), optional
   `GetInclusionListsMsg = 0xe4` / `InclusionListsMsg = 0xe5` (req/resp by
   round+committee-index, FOCIL R9's "RPC topic"). Encode with the same
   Packet conventions as Vote/Timeout (eth/protocols/eth).
4. Bfter extension: `VerifyInclusionListMessage` + `InclusionListHandler` on
   the narrow EngineV2 interface; verify-then-forward with the ≤2-per-member
   rule enforced at the dispatcher (bandwidth bound ×2 per spec).
5. IL pool: keyed round:gap:signer; per-round cap = committee size × 2;
   hygiene on round advance (mirror hygieneVotePool).
6. Size accounting: RLP byte size of the tx list ≤ 8 KiB analog (constant
   name e.g. `MaxBytesPerInclusionList`, value = maintainer decision D3).
7. Metrics/logs: IL received/stored/forwarded counts, gate activations
   (votes withheld), equivocation detections.

## 6. Tests needed (mapped to the claims they prove)

Claim: "a censoring proposer's block cannot get a QC from honest voters."

- T1 (types): IL RLP round-trip, sig-hash stability, signer recovery, DeepCopy.
- T2 (committee): deterministic committee derivation for round R from a fixed
  snapshot; agreement across simulated nodes; epoch-switch boundary rounds.
- T3 (gossip admission): each of the 6 P2P rules rejected-case tests; ≤2
  forwarding bound; equivocator (3rd distinct IL) ⇒ all that member's ILs
  dropped; byte cap enforced pre-decode of txs where possible.
- T4 (satisfaction predicate R6): matrix over {tx present, missing+nonce bad,
  missing+balance bad, missing+gas > gas_left, missing+valid} × {legacy,
  dynamic-fee, frame (0x06)} tx types. Frame txs REQUIRE their own validity
  predicate reuse test (8141 interaction §2.6).
- T5 (vote gate R7): engine-level — ProposedBlockHandler on an IL-violating
  block produces NO vote and highestVotedRound is NOT latched (must reuse the
  #1330 discipline: suppression must not wedge the voter for the round — it
  should still be able to vote for a DIFFERENT compliant block at the same
  round if one appears; that is a deliberate divergence from latch-on-vote
  and needs an explicit test).
- T6 (proposer inclusion R5): worker assembles block containing the IL union
  (append-at-end), skips already-included/invalid/oversized; block-full
  conditional exemption honored.
- T7 (liveness): no ILs stored ⇒ identical behavior to baseline (vote sent);
  IL view held by <1/3 of simulated voters ⇒ QC still forms; >1/3 ⇒ timeout
  and round advance (no wedge), next leader includes the tx.
- T8 (end-to-end, local net): ItWorksinMyLocal-style mixed net or multi-engine
  in-process harness: censoring leader at round R (filter target tx from
  fillTransactions), honest committee lists it → R's block fails QC → round
  R+1 leader includes it → QC forms → tx included within 1 round of a
  censorship attempt. This is THE censorship-resistance demonstration.
- T9 (invalid-block rejection): block containing a tx that fails the IL
  predicate's OWN validity (e.g. bad nonce appended) still processed normally
  (IL gate never makes a block invalid); and a block omitting a VALID
  includable IL tx is vote-rejected (T5 at net scale).
- T10 (regression discipline): full 7-package sweep diffed against the
  pristine-base failure set (strip durations, compare failure NAMES — the
  established method from the 8141 mission). Known baseline failures at
  48a7727 (pre-existing, must not grow): core/types 4 block-encoding,
  core/state FuzzJournal, core TestProcessParentBlockHash + TestProcessUBT,
  internal/ethapi 5, eth/protocols/eth TestMessages.

## 7. Open protocol/maintainer decisions (document, do not schedule)

- D1: Fork gate name/type and activation. XDC uses block-based forks;
  Amsterdam/Bogota are timestamp-based and NOT scheduled on XDC configs.
  The FOCIL gate must be a new block-based (or V2-config round-based) gate.
  NO value to be set by us.
- D2: Whether IL-layer misbehavior (equivocation, refusing to publish) maps
  to on-chain penalties via the existing penalty system, or stays
  off-chain/forensics-only. Recommendation: forensics-only for v1.
- D3: Constants: committee size K (spec 16), max IL bytes (spec 8 KiB),
  freeze offset within a round, per-round pool caps. XDC's 2s rounds may
  justify smaller values than mainnet-Ethereum's.
- D4: Committee derivation function (next-K-leaders vs snapshot-hash sortition).
  Next-K-leaders is simpler and gives publisher/consumer proximity;
  sortition is more manipulation-resistant. Needs maintainer sign-off.
- D5: Interaction policy for frame txs (0x06) in ILs: allowed from day one
  (with 8141 validity predicates) or excluded until the 8141 public-mempool
  validation-prefix policy lands. Recommendation: allowed, predicate-reuse.
- D6: IL req/resp (pull) channel vs gossip-only. FOCIL makes the pull channel
  optional; XDC's short rounds may make it necessary for liveness. Decide
  after local-net measurement (T8).
- D7: RPC surface: read-only `XDPoS_getInclusionLists` observability endpoint
  or logs-only. No consensus impact.
- D8: Does the mechanism apply to epoch-switch blocks (currently tx-free by
  design)? Recommendation: exempt epoch-switch blocks (their IL txs defer to
  the next block) — matches existing no-tx design, needs sign-off.

## 8. Recommended smallest safe first milestone

**M1 — "Shadow FOCIL": types + committee derivation + IL pool + gossip
admission, observe-only, zero consensus behavior change.**

Scope (no vote-gating, no worker inclusion, no activation gate consumption):
1. `core/types/inclusion_list.go`: InclusionList/ForSign/sig-hash/RLP/PoolKey
   (mirrors consensus_v2.go patterns).
2. `consensus/XDPoS/engines/engine_v2/inclusionlist.go`: committee derivation
   from the epoch snapshot for a round (D4 recommendation: next-K-leaders),
   IL verification (sig + membership + round/gap), IL pool with ≤2-per-member
   and equivocator marking.
3. eth wire `InclusionListMsg` 0xe3 + Bfter handler (verify→forward→engine),
   gossip-only (no req/resp yet — D6).
4. IL construction hook: on new-head (round R−1 imported), if local node is in
   round R's committee, build an IL from `txpool.Pending` and broadcast.
   Nodes STORE and LOG; a debug metric records what R6 WOULD have decided for
   each imported block. Nothing is enforced.
5. Tests: T1, T2, T3 + the T10 baseline-diff sweep.

Why this is smallest-and-safe: it builds the entire data plane and lets us
MEASURE on a local net (IL propagation time within 2s rounds, view divergence
rates, would-have-withheld rates) before touching the single consensus-critical
line (vote suppression). M2 would add worker union-inclusion (T6), M3 the
vote gate + T5/T7/T8/T9 behind the (unscheduled) gate.

### Exact verification commands for M1 (this tree)

```sh
export PATH=/home/tanmay/go-sdk/go/bin:$PATH
cd /home/tanmay/xdc-fix-work/frame-tx-8141   # or a new worktree off 48a7727

# build
go build ./core/types/... ./consensus/XDPoS/... ./eth/... ./miner/...

# targeted new tests
go test ./core/types/ -run 'InclusionList' -v
go test ./consensus/XDPoS/engines/engine_v2/ -run 'InclusionList|Committee' -v
go test ./eth/bft/ -run 'InclusionList' -v

# package suites for touched areas
go test ./core/types/ ./consensus/XDPoS/... ./eth/bft/ ./eth/protocols/eth/

# regression discipline (baseline-diff; durations stripped, names compared)
go test ./core/types/ ./core/state/ ./core/ ./internal/ethapi/ ./eth/protocols/eth/ 2>&1 | tee /tmp/focil-m1-sweep.log
# compare failure NAMES against the archived pristine-base sweep from the
# 8141 mission; the set must be byte-identical (core/types 4 block-encoding,
# core/state FuzzJournal, core TestProcessParentBlockHash+TestProcessUBT,
# internal/ethapi 5, eth/protocols/eth TestMessages).
```

## 9. Preservation attestation

- Pre-audit `git status`: branch feat/eip-8141-frame-transactions, 37 staged
  paths (EIP-8141 M1–M9), 0 commits ahead of 48a7727. Untouched.
- The only file this phase adds is this document (FOCIL_XDC_GAP_ANALYSIS.md),
  staged but uncommitted per the standing no-commit rule.
- Spec snapshot stored OUTSIDE the repo at
  /home/tanmay/xdc-fix-work/eip-7805-spec-snapshot.md to keep the tree clean.
