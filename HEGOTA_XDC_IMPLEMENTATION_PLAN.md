# Hegotá → XDC Implementation Plan

Date: 2026-10-29. Companion to `HEGOTA_XDC_COMPATIBILITY_REPORT.md`.
No dates are assigned to maintainer- or network-dependent milestones.

## Legend

- Gates: every milestone ends with `go build ./...`, `go vet`, gofmt-clean,
  focused tests green, and the 7-package regression sweep
  failure-name-identical to the pristine base.
- Nothing is committed, pushed, deployed, or activated by this plan's
  execution; activation values stay unset until maintainer sign-off.

## Milestone 0 — done (this mission)

- EIP-8141 frame transactions: implemented + audited (Project A, M1–M10).
- EIP-8250 keyed nonces: implemented + audited, keyed mempool included
  (Project B, M1–M5 + final audit; checkpoint
  `backups-frame-tx-8250/M2-keyed-mempool-20261029.patch`).
- Acceptance already met: 22+ new EIP-8250 tests + 49 EIP-8141 tests green;
  13 pre-existing base failures unchanged.

## Milestone 1 — Maintainer code review (external dependency)

- Human review of both worktrees' staged diffs (checkpoints listed above).
- Resolve review findings; re-run gates after any change.
- Blockers: maintainer availability. No date.

## Milestone 2 — Devnet bring-up (frame txs + keyed nonces)

- New devnet chain config with Amsterdam/Bogota/KeyedNonce active; include
  NONCE_MANAGER (nonce 1, code 60006000fd) in genesis, plus the EIP-8141
  expiry-verifier deployment (Project A decision D4).
- Soak: frame tx flows, keyed flows (sequential/single-use/multi-key),
  RPC/JSON checks, pool behavior under load, boundary-eviction rehearsal.
- Acceptance: scripted end-to-end flows pass on the devnet; no consensus
  failures over the soak window.
- Code areas: none (ops + config only).

## Milestone 3 — EIP-8272 Recent Roots (research → implementation)

- Spec pin + gap analysis doc (follow the xdc-protocol-development method).
- Implement: recent-root system contract layout, canonical VERIFY frame
  handling, mempool pre-validation hook; reuse frame engine + side-pool
  admission patterns from Projects A/B.
- Tests: engine-level StateDB harness + pool tests; boundary cases (reorg
  freshness window, tuple limits 1–16).
- Acceptance: gates green; new tests cover the spec's normative behaviors.
- Blockers: EIP-7843 slot semantics mapping to XDC rounds (needs design
  sign-off).

## Milestone 4 — FOCIL adaptation (research-only here)

- Phase-1 audit exists (FOCIL_XDC_GAP_ANALYSIS.md). Next: M1 shadow data
  plane (observe-only: IL types, committee derivation from masternode
  snapshots, gossip receive path; NO enforcement).
- M2 proposer union-inclusion; M3 vote gate behind an unscheduled fork gate.
- Acceptance per phase: shadow metrics stable; devnet evidence before any
  enforcement.
- Blockers: XDPoS liveness analysis; maintainer sign-off.

## Milestone 5 — Secondary repricing set (3298 / 8131 / 8279)

- Each gets its own mini-milestone with the same gates; schedule together
  with the next gas-schedule review to avoid double-touching fee code.
- Blockers: none technical; prioritization only.

## Explicitly out of scope

- EIP-8198 Quick Slots (architecture conflict), EIP-8163/7979 (declined
  upstream), C-tier items (7668, 8355) absent explicit product demand.
- Any mainnet/testnet activation, deployment, or scheduling.
