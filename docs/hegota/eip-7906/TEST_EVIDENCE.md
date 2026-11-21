# EIP-7906 Test Evidence

All commands run in `/home/tanmay/xdc-fix-work/eip-7906-assertions` with
`PATH=/home/tanmay/go-sdk/go/bin:$PATH` (go1.26.8 linux/amd64), `-count=1`
throughout. Baseline comparison tree: `/home/tanmay/xdc-fix-work/frame-tx-8141`
(EIP-8141-M10 staged), the exact base of this branch.

## 1. EIP-7906 test inventory (all PASS)

### Static validity — `core/types` (9 subtests)

```
go test ./core/types/ -run 'TestFrameTxValidateStaticAssert' -count=1 -v
```

`assert mode accepted` · `assert suffix of two` · `assert not suffix` ·
`assert then verify` · `batch flag on assert` · `batch flag before assert` ·
`approval scope on assert` · `value on assert rejected` · `sender verify assert shape`
— all PASS. The pre-existing `TestFrameTxValidateStatic` (19 cases, EIP-8141)
also PASSes after adjusting its stale "bad mode" case (mode 3 is now valid).

### Engine semantics — `core/frame_tx_assert_test.go` (9 tests)

```
go test ./core/ -run 'TestFrameTxAssert' -count=1 -v
```

| Test | Asserts |
|---|---|
| TestFrameTxAssertSuccess | 3-frame tx, all succeed, body committed |
| TestFrameTxAssertDefaultCode | nil-target ASSERT (empty code) succeeds |
| TestFrameTxAssertFailureRevertsBody | body slot/log reverted; prefix nonce kept; body receipts keep status+exec gas, drop logs+state gas; failing ASSERT status 0 |
| TestFrameTxAssertSkippedSuffix | remaining ASSERT frames status 2, zero gas |
| TestFrameTxAssertApproveHalts | APPROVE in ASSERT = exceptional halt, full frame gas consumed, body reverted |
| TestFrameTxAssertStaticcallEnforced | SSTORE in ASSERT halts; no write; prefix kept |
| TestFrameTxAssertNoPayerInvalid | failing and succeeding payer-less ASSERT txs invalid |
| TestFrameTxAssertBodyBatchFullyReverted | successful atomic batch reverted as part of body |
| TestFrameTxAssertPayerChargedOnFailure | payer spend == gasUsed×(baseFee+tip) only; coinbase tip exact |

### Opcodes — `core/frame_tx_assert_opcode_test.go` (11 tests, 13 subtests)

```
go test ./core/ -run 'TestTxTrace|TestEventDataCopy' -count=1 -v
```

Assertion contracts embed the expected values: each `traceCheck`/`mloadCheck`
is an in-EVM assertion that reverts on mismatch (63 + 17 `traceCheck` emission
sites, 4 `mloadCheck` sites; several inside loops — ≈98 executed in-EVM
assertions per full run).

| Test | Coverage |
|---|---|
| TestTxTraceStateDiffTables | every param 0x00–0x1A: balances (sorted, before/after), slots, codes, nonces, events (incl. EIP-7708 transfer log address/topics/data-len), gas pre-charge, gas payer |
| TestTxTraceNetChangeCollapse | double-write single entry; write-back-to-original excluded; two transfers to one recipient collapse |
| TestTxTraceCodeNonceCreation | CREATE: code entry empty-hash→keccak(runtime); nonce entries sender/factory/child 0→1, address-sorted |
| TestTxTraceNestedSubcall | TXTRACE legal at depth 2 inside ASSERT; halts in SENDER subtree |
| TestTxTraceContextGates | halt in legacy EIP-1559 tx, DEFAULT frame, SENDER frame; VERIFY frame halts and invalidates |
| TestTxTraceReservedAndRangeChecks | 9 subtests: reserved-nonzero, undefined params (0x1b/0xff/huge), empty-table index, topic rules, non-uint64 param and index |
| TestTxTraceGasExact | minimal frame execution gas = 3106 (3000 cold access + 2×PUSH1 + 100 TXTRACE) |
| TestTxTraceGasPayerAndPreCharge | exact max_cost vs `MaxGas()`×(baseFee+tip) with real base fee; payer balance entry = pre-charge |
| TestEventDataCopyRead | event fields + full 64-byte copy + mid-range slice + zero-length-at-exact-end boundary |
| TestEventDataCopyHalts | 4 subtests: event index OOB, data end exceeded, zero-length past end, 256-bit wraparound |
| TestEventDataCopyGas | 3021 = 3000 + 4×PUSH1 + 3 fixed + 3 expansion + 3 word-copy |

### EIP-7928 BAL + deletion visibility — `core/frame_tx_assert_bal_test.go` (5 tests)

```
go test ./core/ -run 'TestFrameTxAssertFailureBAL|TestFrameTxAssertSuccessBALStateCommitted|TestTxTraceCreateThenSelfDestruct|TestTxTraceEventsExcludeRevertedLogs|TestTxTraceConsistentAcrossAssertFrames' -count=1 -v
```

- **TestFrameTxAssertFailureBAL** (block-level, `GenerateChainWithGenesis`):
  ASSERT-failure tx's BAL shows the body's reverted SSTORE as a *storage read*
  with *no storage change*; the committed prefix (sender nonce 0→1) keeps its
  nonce change; the ASSERT target is included as accessed; block receipts match
  the engine-level rules.
- **TestFrameTxAssertSuccessBALStateCommitted**: positive control — succeeding
  ASSERT ⇒ body SSTORE lands in storage_changes.
- **TestTxTraceCreateThenSelfDestruct**: contract created and SELFDESTRUCTed in
  one tx still appears in codes_changed/nonces_changed during frames; EIP-8264
  clearing observed only after `Finalise`.
- **TestTxTraceEventsExcludeRevertedLogs**: reverted body frame's log absent
  from the events table (count=1) and from tx logs.
- **TestTxTraceConsistentAcrossAssertFrames**: two ASSERT frames observe an
  identical diff (frozen-diff invariant).

Total EIP-7906 suite: **25 core tests (13 subtests) + 9 core/types subtests —
all PASS.**

## 2. Regression comparison vs base (normalized test names)

Method: run the same package set in both trees, extract `--- FAIL:` lines,
strip durations, sort, diff.

### core (full coverage; 3 pre-existing panickers skipped per the 8272 recipe)

```
go test -count=1 -skip 'TestProcessParentBlockHash|TestProcessUBT|TestTransientStorageReset' ./core/
```

Both trees: **12 failures, identical names** — 10 `TestBAL*` (pre-existing BAL
balance-entry gap: CoinbasePerTxBalance, CreateDeploysCode,
GasRefundSenderBalance, PrecompileValueTransferRecordsBalance,
SelfDestructBeneficiaryWithValueTransfer, SelfDestructPreExistingContract,
SelfDestructToSelfKeepsBalance, SenderRecordedOnRevert,
SystemAddressIncludedWhenTouched, TxSenderAndRecipient) + `TestGenesisHashes` +
`TestSetupGenesis`. Diff output: only duration fields differed.

### core/types, core/vm, core/state

```
go test -count=1 ./core/types/ ./core/vm/ ./core/state/
```

Identical failure sets: `core/types` 4 pre-existing block-encoding failures
(TestBlockEncoding, TestEIP1559BlockEncoding, TestEIP2718BlockEncoding,
TestEIP4844BlockEncoding); `core/state` pre-existing `FuzzJournal` (seed
balance-count drift — the same dormant-stash issue as the BAL balance gap);
`core/vm` clean in both trees.

### txpool family, ethapi, protocols, miner

```
go test -count=1 ./core/txpool/... ./internal/ethapi/... ./eth/protocols/eth/... ./miner/...
```

Package-level results identical between trees:

```
FAIL    github.com/ethereum/go-ethereum/eth/protocols/eth   (TestEIP7910Config — pre-existing)
FAIL    github.com/ethereum/go-ethereum/internal/ethapi     (TestMessages, TestRPCGetBlockOrHeader, TestRPCGetBlockReceipts, TestRPCGetTransactionReceipt, TestRPCMarshalBlock — pre-existing)
ok      github.com/ethereum/go-ethereum/core/txpool
ok      github.com/ethereum/go-ethereum/core/txpool/blobpool
ok      github.com/ethereum/go-ethereum/core/txpool/legacypool
ok      github.com/ethereum/go-ethereum/core/txpool/locals
ok      github.com/ethereum/go-ethereum/core/txpool/txorder
ok      github.com/ethereum/go-ethereum/internal/ethapi/override
ok      github.com/ethereum/go-ethereum/miner
```

Zero panics in either log. **Zero new failures introduced by EIP-7906.**

### EIP-8141 / EIP-8250 / EIP-8272 regression

- EIP-8141: its full suite lives in this tree (frame-tx tests in core,
  core/types, core/txpool, eth/protocols/eth frame-receipt test) — all green
  per the sweeps above (`go test ./core/ -run 'TestFrameTx'` PASS;
  `go test ./core/types/ -run 'TestFrameTx|TestSignFrameTx'` PASS;
  `go test ./core/txpool/...` PASS).
- EIP-8250 / EIP-8272: sibling branches — their code is deliberately **not**
  in this tree (verified at worktree creation: no cross-markers), so their
  suites are not runnable here. Non-regression is by isolation; their
  worktrees are verified untouched in the final phase.

## 3. Build and static checks

```
go build ./...                # exit 0 (full tree)
go vet ./core/...             # clean (one self-assignment in a test was found and fixed)
gofmt -l core/ core/vm/ core/state/ core/types/   # delta vs base tree: none
```

## 4. Final verification (exact final tree)

See the FINAL checkpoint section below — re-run after documentation:

```
go test ./core/ -run 'TestFrameTxAssert|TestTxTrace|TestEventDataCopy' -count=1   # 25 PASS
go test ./core/types/ -run 'TestFrameTx' -count=1                                  # PASS (incl. 9 ASSERT subtests)
go test -count=1 -skip 'TestProcessParentBlockHash|TestProcessUBT|TestTransientStorageReset' ./core/
go test -count=1 ./core/types/ ./core/vm/ ./core/state/
go test -count=1 ./core/txpool/... ./internal/ethapi/... ./eth/protocols/eth/... ./miner/...
go build ./... && go vet ./core/...
```

Results: recorded in section 2 above and re-confirmed on the final tree
(failure-name sets byte-identical to base; new suites green).

## 5. Checkpoints and SHA256

| Checkpoint | SHA256 (verified via `sha256sum -c`) |
|---|---|
| M0-base-8141-m10.patch | `665e77a2ea3299ee9625ce1e68be0768c2aa9e6e4b9dfb00a42087b47da8d4b1` |
| M1-assert-frame-mode.patch | `1f500c96584b36246dd5fde4a38fd6e217871afde986f6147345dfc54ea479de` |
| M2-M3-txtrace-eventdatacopy.patch | `d80d66d5a6253eff57a4d31d30b1d037d67d232df1047b6e508197602414a991` |
| M4-bal-and-edge-semantics.patch | `549f9f0a51db5d98ada2d17990fc83938c1a9dcbbd8e594ed9cd192ffc0c0b34` |
| FINAL-eip-7906.patch | recorded in `FINAL-eip-7906.sha256` alongside the patch (value kept out of the tree so the checkpoint hash matches the final tree byte-for-byte) |
