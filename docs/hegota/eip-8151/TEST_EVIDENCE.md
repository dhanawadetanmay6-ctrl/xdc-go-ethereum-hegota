# EIP-8151 Test Evidence

**Date**: 2026-10-31
**Go**: `go1.26.8 linux/amd64`
**Feature worktree**: `/home/tanmay/xdc-fix-work/eip-8151-ecrecover`
**Base worktree**: `/home/tanmay/xdc-fix-work/go-ethereum`
**Base/feature HEAD**: `48a77271314293cdf057bc05d2c1624c3645a4d0`

All commands below used `PATH=/home/tanmay/go-sdk/go/bin:$PATH`.

## 1. Specification integrity

Commands:

```sh
curl -fsSL https://eips.ethereum.org/EIPS/eip-8151 -o /tmp/eip-8151-official.html
curl -fsSL https://raw.githubusercontent.com/ethereum/EIPs/master/EIPS/eip-8151.md -o /tmp/eip-8151-upstream.md
sha256sum /tmp/eip-8151-upstream.md /home/tanmay/xdc-fix-work/eip-8151-spec-snapshot.md
cmp -s /tmp/eip-8151-upstream.md /home/tanmay/xdc-fix-work/eip-8151-spec-snapshot.md
```

Result: PASS. Both Markdown files have SHA256
`191a5a2d742c38380f815d251f78bf5e315bba23e2dafc239f7197572ac748ea`;
`cmp` returned 0.

## 2. New targeted tests

### VM behavior, gas, OOG, BAL, revert, call, and activation tests

```sh
go test -v ./core/vm -run EIP8151 -count=1
```

Result: PASS (exit 0). Six top-level tests and 28 named subtests passed:

- `TestEIP8151AccountCodeRestriction`
- `TestEIP8151RecoveryFailure`
- `TestEIP8151GasCostsAndOutOfGas`
- `TestEIP8151RevertSemantics`
- `TestEIP8151EVMCallResults`
- `TestEIP8151ActivationAndPrecompileSet`

After independent read-only review, the cold-revert subtest was hardened to
inspect the first call's BAL read immediately after revert, before a second
exact-cold-gas call can re-add that read.

Log: `/home/tanmay/xdc-fix-work/backups-eip-8151/FINAL-vm-eip8151.log`.

### Block-level EIP-7928 tests

```sh
go test -v ./core -run EIP8151 -count=1
```

Result: PASS (exit 0). `TestEIP8151BlockAccessList` and all five subtests
passed: delegation-target exclusion, rejected-code read retention, recovery
failure absence, code-check-OOG absence, and outer-revert read retention.

Log: `/home/tanmay/xdc-fix-work/backups-eip-8151/FINAL-core-eip8151.log`.

### Complete VM/precompile package

```sh
go test ./core/vm -count=1
```

Result: PASS (exit 0), including the unchanged legacy ecRecover vectors and all
existing precompile/EVM tests in the package.

Log: `/home/tanmay/xdc-fix-work/backups-eip-8151/FINAL-core-vm.log`.

## 3. Formatting, build, and vet

```sh
gofmt -l core/vm/contracts.go core/vm/eip8151_test.go core/eip8151_test.go
git diff --check
git diff --cached --check
go build ./...
go vet ./core/vm ./core
```

Results:

- gofmt output empty: PASS.
- Both diff checks: PASS.
- Build: PASS (exit 0).
- Vet: PASS (exit 0).

Build and vet logs are empty on success:

- `/home/tanmay/xdc-fix-work/backups-eip-8151/M4-build.log`
- `/home/tanmay/xdc-fix-work/backups-eip-8151/M4-vet.log`

## 4. Full-core baseline comparison

Command run independently in base and feature worktrees:

```sh
go test -count=1 -skip 'TestProcessParentBlockHash|TestProcessUBT|TestTransientStorageReset' ./core/
```

The three skipped tests are known panic-aborting baseline tests; excluding them
on both sides prevents an early panic from hiding later failures. Both commands
returned exit 1 because of the same 12 pre-existing failures:

- `TestBALCoinbasePerTxBalance`
- `TestBALCreateDeploysCode`
- `TestBALGasRefundSenderBalance`
- `TestBALPrecompileValueTransferRecordsBalance`
- `TestBALSelfDestructBeneficiaryWithValueTransfer`
- `TestBALSelfDestructPreExistingContract`
- `TestBALSelfDestructToSelfKeepsBalance`
- `TestBALSenderRecordedOnRevert`
- `TestBALSystemAddressIncludedWhenTouched`
- `TestBALTxSenderAndRecipient`
- `TestGenesisHashes`
- `TestSetupGenesis`

Normalized comparison result: **0 new failures, 0 removed failures**.

Logs:

- `/home/tanmay/xdc-fix-work/backups-eip-8151/M4-core-base.log`
- `/home/tanmay/xdc-fix-work/backups-eip-8151/M4-core-feature.log`

`TestBALPrecompileValueTransferRecordsBalance` was also reproduced individually
on the clean base before classification.

## 5. Adjacent-package baseline comparison

Command run independently in base and feature worktrees:

```sh
go test -count=1 ./core/types ./core/state ./internal/ethapi ./eth/protocols/eth
```

Both commands returned exit 1 with the same 15 failure entries (11 top-level
tests plus four FuzzJournal seed subtests):

- `core/types`: `TestBlockEncoding`, `TestEIP1559BlockEncoding`,
  `TestEIP2718BlockEncoding`, `TestEIP4844BlockEncoding`.
- `core/state`: `FuzzJournal` (seed subtests 0, 1, 2, and 4).
- `internal/ethapi`: `TestEIP7910Config`, `TestRPCGetBlockOrHeader`,
  `TestRPCGetBlockReceipts`, `TestRPCGetTransactionReceipt`,
  `TestRPCMarshalBlock`.
- `eth/protocols/eth`: `TestMessages`.

Normalized comparison result: **0 new failures, 0 removed failures**.

Logs:

- `/home/tanmay/xdc-fix-work/backups-eip-8151/M4-adjacent-base.log`
- `/home/tanmay/xdc-fix-work/backups-eip-8151/M4-adjacent-feature.log`

These are baseline failures, not assertions that the affected packages are
fully green. No failure was hidden by comparing only package exit codes;
failure test names were extracted and compared as sets.

## 6. Checkpoints

Checkpoint directory: `/home/tanmay/xdc-fix-work/backups-eip-8151/`.

- `M0-base.manifest` / `M0-base.sha256`: original clean dependency base.
- `M1-resume-partial.patch` / `.sha256`: exact work found at session resume,
  before new edits.
- `M1-M3-core-gas-bal.patch` / `.sha256`: implementation plus behavior, gas,
  BAL, and revert tests.
- `M4-activation-regression.patch` / `.sha256`: activation and regression
  milestone; staged-tree-to-patch comparison passed.
- `FINAL-eip-8151.patch` / `.sha256`: final cumulative staged tree, including
  the plan, implementation, tests, report, and this evidence file.

Final verification commands:

```sh
sha256sum -c /home/tanmay/xdc-fix-work/backups-eip-8151/FINAL-eip-8151.sha256
git diff --cached --binary > /tmp/eip8151-final-check.patch
cmp -s /tmp/eip8151-final-check.patch /home/tanmay/xdc-fix-work/backups-eip-8151/FINAL-eip-8151.patch
```

The FINAL SHA256 value is recorded only in the `.sha256` sidecar and the chat
report, avoiding a documentation/hash circular dependency.
