# EIP-8272 Test Evidence

All commands run in `/home/tanmay/xdc-fix-work/eip-8272-recent-roots` with the
Go SDK at `/home/tanmay/go-sdk/go/bin` (go1.26.8 linux/amd64). Date: 2026-10-30.

## 1. Focused EIP-8272 suites (all passing)

```
go test ./core/ -run 'TestRecentRoot|TestCheckRecentRoot|TestSimulateRecentRoot' -count=1
→ ok github.com/ethereum/go-ethereum/core   (19 top-level recent-root tests PASS)

go test ./core/types/ -run 'TestParseRecentRootTuples|TestIsRecentRootVerifyFrame|TestRecentRoot' -count=1
→ ok github.com/ethereum/go-ethereum/core/types   (7 tests PASS:
  TestRecentRootConstants, TestRecentRootCodeMatchesSpec,
  TestRecentRootReferenceVectorDerivations, TestParseRecentRootTuples,
  TestIsRecentRootVerifyFrame, TestRecentRootVerifyFrameIndexes,
  TestRecentRootDeploymentTx)

go test ./core/txpool/ -run 'TestClassifyFrameValidationPrefix|TestValidateFrameRecentRootPolicyRejections|TestValidateFrameRecentRootGasBudgets|TestRecentRootPolicyRunsBeforeSenderState' -count=1
→ ok github.com/ethereum/go-ethereum/core/txpool   (4 tests, 37 subtests PASS)

go test ./core/txpool/legacypool/ -run 'TestPoolRecentRoot' -count=1
→ ok github.com/ethereum/go-ethereum/core/txpool/legacypool   (11 tests PASS:
  TestPoolRecentRootAdmission, TestPoolRecentRootAdmissionRejections (4 sub),
  TestPoolRecentRootMultipleDependencies, TestPoolRecentRootReplacement,
  TestPoolRecentRootEvictionCleanup (4 sub), TestPoolRecentRootSlotAdvancement,
  TestPoolRecentRootReorganization, TestPoolRecentRootCodeDependency,
  TestPoolRecentRootActivationFlip, TestPoolRecentRootAdmissionBeforeDeployment,
  TestPoolRecentRootIndexOnlyTouchesRelatedTxs)
```

## 2. Pre-existing EIP-8141 frame suites (regression, all passing)

```
go test ./core/txpool/ -run 'TestValidateFrameTransaction' -count=1 → ok
go test ./core/ -run 'TestFrameTx|TestOpcode|TestFrameReceipt|TestSignFrameTx' (via sweep below)
```

## 3. Full txpool suite (regression)

```
go test -count=1 ./core/txpool/...
ok  github.com/ethereum/go-ethereum/core/txpool            0.025s
ok  github.com/ethereum/go-ethereum/core/txpool/blobpool   128.698s
ok  github.com/ethereum/go-ethereum/core/txpool/legacypool 9.478s
ok  github.com/ethereum/go-ethereum/core/txpool/locals     0.126s
ok  github.com/ethereum/go-ethereum/core/txpool/txorder    0.414s
```

## 4. 7-package regression sweep

```
go test -count=1 ./core/ ./core/types/ ./core/state/ ./core/vm/ ./core/txpool/... ./internal/ethapi/ ./eth/protocols/eth/
```

Failures — name-for-name identical to the pristine-baseline failures
documented by the EIP-8250 mission (pre-existing XDC-fork divergences):

| Package | Test | Note |
|---|---|---|
| core | TestProcessUBT | baseline (bintrie/UBT: "unknown ancestor") |
| core | TestProcessParentBlockHash | baseline; its UBT subtest nil-pointer panic in `state.AccessEvents.Merge` (core/state_processor.go:540, bintrie_witness_test.go) aborts the core test binary early — REPRODUCED in the frame-tx-8141 worktree (M10 base without EIP-8272): pre-existing |
| core/types | TestBlockEncoding, TestEIP1559BlockEncoding, TestEIP2718BlockEncoding, TestEIP4844BlockEncoding | baseline (XDC header RLP divergence) |
| core/state | FuzzJournal | baseline |
| internal/ethapi | TestRPCGetBlockOrHeader, TestRPCGetBlockReceipts, TestRPCGetTransactionReceipt | baseline |
| eth/protocols/eth | TestEIP7910Config, TestMessages | baseline |

Zero new failures attributable to EIP-8272. Zero baseline failures removed.

## 4a. Full core suite with baseline panickers excluded

The baseline core run aborts at 0.4s in `TestProcessParentBlockHash/UBT`
(nil-pointer panic, pre-existing), so the 8250-era "13 failures" list never
observed the remainder of the core package. For complete coverage:

```
go test -count=1 -skip 'TestProcessParentBlockHash|TestProcessUBT|TestTransientStorageReset' ./core/
```

(`TestTransientStorageReset` is a second pre-existing panicker in the same
`AccessEvents.Merge` family, verified to panic identically in the 8141 base.)

Result: 12 failures, failure-name set byte-identical to the same run on the
M10 base (frame-tx-8141 worktree, no EIP-8272 code):

- TestBALTxSenderAndRecipient, TestBALCoinbasePerTxBalance,
  TestBALSystemAddressIncludedWhenTouched,
  TestBALPrecompileValueTransferRecordsBalance, TestBALSenderRecordedOnRevert,
  TestBALCreateDeploysCode, TestBALSelfDestructBeneficiaryWithValueTransfer,
  TestBALSelfDestructPreExistingContract, TestBALSelfDestructToSelfKeepsBalance,
  TestBALGasRefundSenderBalance (10 — Amsterdam BAL family, base count
  verified: 10 in base, 10 here)
- TestGenesisHashes, TestSetupGenesis (XDC genesis divergence)

Including the 3 skipped panickers, core has 15 pre-existing failures, all
reproduced at the M10 base. EIP-8272 adds zero failures and removes none.

## 5. Build / vet / format

```
go build ./... → clean
go vet ./core/ ./core/txpool/legacypool/ (plus all other touched packages) → clean
gofmt -l <all EIP-8272 touched files> → clean
```

Pre-existing gofmt-dirty files (15) in the fork were verified identical in the
frame-tx-8141 worktree (M10 base) — none introduced by EIP-8272:
events_xdc.go, evm.go, frame_tx_opcode_test.go, accessors_chain.go,
accessors_xdc.go, statedb_frame.go, state_processor_xdc_test.go,
state_transition.go, block.go, header_rlp_compat_test.go,
order_transaction.go, receipt.go, vote_pool.go, instructions_frame.go,
xdc_block_validator_test.go.

## 6. Gas measurements (evidence for gas-exactness claims)

From `TestSimulateRecentRootVerifyFrame` and `TestRecentRootValidationSixteen`:

- 1 tuple, cold target + cold storage key: 6,415 execution gas
  (cold account access charged at frame entry + internal execution).
- 16 tuples, 15 distinct cold keys: 55,855 execution gas.
- Admission simulation gas == engine frame receipt gas (asserted in test).
- Exact `limits.execution` boundary: accepted; boundary − 1: rejected.

## 7. Deployment sheet verification

`TestRecentRootDeploymentTx` (core/types) verifies against the spec JSON
parameter sheet:
- RLP reconstruction: type 0, nonce 0, gasPrice 0xe8d4a51000, gas 0x13d620,
  to null, value 0, input = 330-byte init code, v=0x1b, r=0x539,
  s=0xfadf66b1e192785c.
- tx hash == 0x56c2adbbfa3ba5dfe47adf48828f6c581134e39e14c3b80e06214de5e5875272
- recovered sender == 0x14bf16d4c9842bf1EbF396e553477C66EB0a8A82
- CREATE address == 0x8272D9679689Ea2f307140CdF9002D27dC00Ffff
- `TestRecentRootDeploymentExecution` (core): executing the init code in the
  EVM returns exactly RECENT_ROOT_CODE (320 bytes), deployed storage empty.

## 8. Reference vector

`TestRecentRootReferenceVectorDerivations` (core/types) +
`TestRecentRootReferenceVector` (core): all spec values reproduced exactly;
contract executed at current_slot=2 against the reference storage state
succeeds with no return data, no logs, no state changes.
