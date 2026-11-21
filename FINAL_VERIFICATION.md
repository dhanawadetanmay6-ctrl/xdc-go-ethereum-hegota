# FINAL VERIFICATION — EIP-8250 + Hegotá research mission

Date: 2026-10-29. Toolchain: Go 1.26.8 (`/home/tanmay/go-sdk/go/bin`).
Worktree: `/home/tanmay/xdc-fix-work/frame-tx-8250`, branch
`feat/eip-8250-keyed-nonces`, HEAD 48a7727, no commits.

## Commands and results (final state, actual runs)

| Command | Exit | Result |
|---|---|---|
| `go build ./...` | 0 | clean |
| `go vet ./params/ ./core/types/ ./core/vm/ ./core/ ./core/txpool/ ./core/txpool/legacypool/ ./internal/ethapi/` | 0 | clean |
| `gofmt -l` on all 16 touched Go files | 0 | empty output (clean) |
| Focused frame suites: `go test -count=1 -run 'TestFrameTx|TestOpcode|TestFrameReceipt|TestSignFrameTx|TestValidateFrameTransaction|TestFrameReceiptTrieRoot|TestFrameReceiptWireHash|TestNonceSlotAndKeysHash' ./core/ ./core/types/ ./core/txpool/ ./eth/protocols/eth/` | 0 | all four packages `ok` |
| Keyed pool tests: `go test -count=1 -run 'TestKeyedFramePool' ./core/txpool/legacypool/` | 0 | 7/7 pass |
| Full txpool: `go test -count=1 ./core/txpool/ ./core/txpool/legacypool/` | 0 | both `ok` (no shared-path regressions) |
| 7-package regression sweep: `go test -count=1 ./core/ ./core/types/ ./core/state/ ./core/vm/ ./core/txpool/... ./internal/ethapi/ ./eth/protocols/eth/` | 1 | **13 failures, byte-identical by name to the pristine base set** (zero new, zero removed) |

Base comparison method: extract `^--- FAIL: (\S+)` names from the sweep log
(`/tmp/p2-sweep.txt`) and from the archived pristine-base log
(`/home/tanmay/xdc-fix-work/backups-frame-tx/base-sweep-log.txt`), set-diff.
Result: added `[]`, removed `[]`.

Pre-existing failures (all documented in Project A; none EIP-8250-related):
FuzzJournal (core/state); TestBlockEncoding/TestEIP1559BlockEncoding/
TestEIP2718BlockEncoding/TestEIP4844BlockEncoding (core/types, XDC header-RLP
vs upstream fixtures); TestProcessParentBlockHash, TestProcessUBT (core);
TestEIP7910Config, TestRPCGetBlockOrHeader, TestRPCGetBlockReceipts,
TestRPCGetTransactionReceipt, TestRPCMarshalBlock (internal/ethapi);
TestMessages (eth/protocols/eth).

## Test totals (EIP-8250 additions)

- Engine (core/frame_tx_keyed_test.go): 16 tests — pass.
- Types (core/types/tx_frame_keyed_test.go): 8 tests — pass.
- Pool validation (core/txpool/validation_frame_test.go): keyed fork-gate +
  stateless admission — pass.
- legacypool side pool (legacypool_keyed_frame_test.go): 7 tests — pass.
- All 49 pre-existing EIP-8141 frame tests pass under the keyed fork gate.

## Checkpoints (all under /home/tanmay/xdc-fix-work/backups-frame-tx-8250/)

| File | Content |
|---|---|
| `M1-keyed-nonces.patch` | pre-Phase-2 staged state (consensus+types+RPC+pool-gate) |
| `pre-phase2-20261029.patch` | timestamped pre-change checkpoint (byte-identical to M1 — verified `cmp`) |
| `pre-phase2-20261029-unstaged.patch` | empty (no unstaged changes existed) |
| `M2-keyed-mempool-20261029.patch` | staged state incl. keyed mempool (superseded by final) |
| `final-20261029.patch` | FINAL: full staged diff incl. reports; sha256-verified against the staged tree at creation |

## Preservation evidence

- Project A (`frame-tx-8141`): 37 staged files, HEAD 48a7727, backup dir
  `backups-frame-tx/` holds its original 11 entries — untouched.
- Main checkout (`go-ethereum`): clean, no stashes.
- XDPoS exercise repo: not touched.
- No commits, no pushes, no fork activation, no deployments anywhere.
