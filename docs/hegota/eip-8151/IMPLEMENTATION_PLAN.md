# EIP-8151 Implementation Plan — Account Code Restricted ecRecover

**Spec**: https://eips.ethereum.org/EIPS/eip-8151 — status **Draft**, type Standards Track (Core),
created 2026-02-09, `requires: 3607, 7702, 8038`. Pinned snapshot:
`/home/tanmay/xdc-fix-work/eip-8151-spec-snapshot.md`,
sha256 `191a5a2d742c38380f815d251f78bf5e315bba23e2dafc239f7197572ac748ea`
(11361 bytes).

## 1. Base decision

**Base = pure main @ 48a7727** (no cumulative patches), because every normative
dependency is already present in main:

| Dependency | Evidence in main @ 48a7727 |
|---|---|
| EIP-3607 | tx-level sender-with-code rejection (long-standing) |
| EIP-7702 | `core/types/tx_setcode.go` — `DelegationPrefix = 0xef0100`, `ParseDelegation` (len==23 && prefix) |
| EIP-8038 | `params/protocol_params.go`: `ColdAccountAccessAmsterdam = 3000` (COLD_ACCOUNT_ACCESS), `WarmAccountAccessAmsterdam = 100` (WARM_ACCESS) — the exact numbers the spec quotes |
| EIP-7928 | `core/types/bal/` package + `core/eip7928_test.go` + automatic account-read recording in `StateDB.getStateObject` → `stateAccessList.AccountRead(addr)` (statedb.go:627), *not journaled* (survives reverts — proven by `TestBALRevertedSStoreRead`) |
| Bogota fork plumbing | `rules.IsBogota` everywhere; `newBogotaInstructionSet`; `activePrecompiledContracts` maps Bogota→Osaka set (contracts.go:240); Amsterdam precompile touch for BAL (contracts.go:306-308) |

Sibling-tree consistency check: `core/vm/contracts.go` sha256 `e4cac370…389cf3`
is **identical** in main, frame-tx-8141, and eip-7906-assertions — the
precompile surface has no frame-tx interference, so an 8151-on-main tree will
layer cleanly under/over the other Hegotá branches later.

EIP-8151 does **not** require EIP-8141 (frame transactions) — the spec's
`requires` list is 3607/7702/8038. Unlike 8250/8272/7906 (which all require
8141), this EIP therefore gets the minimal base. EIP-8141/8250/8272/7906 code
is absent from this tree; their regression suites are not runnable here and
non-regression for them is by isolation (their worktrees verified untouched).

## 2. Requirement-by-requirement matrix

| # | Spec requirement | Implementation point | Test |
|---|---|---|---|
| R1a | Charge 3000 base gas first; OOG halts | `RequiredGas = params.EcrecoverGas` (3000), charged by `RunPrecompiledContract` before `Run*` (unchanged path) | gas-exact: 3000-gas call fails OOG after activation when recovery succeeds |
| R1b | ECDSA recovery unchanged | existing body extracted verbatim to `ecrecoverRun` | JSON vectors (allPrecompiles, stateless path) + new behavioral tests |
| R1c | Recovery failure → empty output, success=1, no account access, no extra gas | `ecrecoverRun` returns nil → return `(nil, gas, nil)` before any state touch | invalid-sig vectors (bad v/r/s), BAL absence assertion |
| R2a | Permit empty code | `len(code) == 0` → return address | empty-code account recovery |
| R2b | Permit exactly 23 bytes starting 0xef0100 | `types.ParseDelegation(code)` (canonical 7702 check) | delegation-indicator account recovery |
| R2c | Reject all other non-empty code | else → return empty | arbitrary contract code; wrong prefix; wrong length (22/24 bytes) |
| R2d | Do not follow the delegation indicator | raw `stateDB.GetCode` (stored bytes only, never resolved); delegate target never loaded | BAL: delegate target absent from access list |
| R2e | Absent account = empty code | `GetCode` on absent account returns nil → permitted | never-seen recovered address |
| R3a | Rejected check → empty output, success=1 | return `(nil, gas, nil)` — no error | caller sees success=1, RETURNDATASIZE=0 |
| R3b | Empty returndata, not 32 zero bytes | nil slice return (not `make([]byte,32)`) | returndata length check |
| R3c | Insufficient gas → OOG halt | `gas.ChargeRegular(cost)` failure → `ErrOutOfGas` | exact-limit and one-less-than-required tests |
| R4a | Cold/warm from accessed_addresses *before* reading | `AddressInAccessList(recovered)` checked before any read | cold 6100 vs warm 3200 measurements |
| R4b | Cold 3000 + 100 code query = 3100 extra; warm 100+100 = 200 extra | `cost = Cold|Warm + WarmAccountAccessAmsterdam` | exact totals 6100/3200 |
| R4c | Charge before warming or recording | charge → then `AddAddressToAccessList` → then `GetCode` | OOG case: address not warm afterwards, no BAL record |
| R5a | BAL read recorded only after cost paid | ordering in RunStateful; `getStateObject` auto-records on `GetCode` | OOG: account absent from BAL |
| R5b | Raw-code access, not delegate-target | `GetCode(recovered)` only | BAL contains recovered account, not delegate |
| R5c | Rejected check keeps the read | read already recorded before the check outcome | rejected-code case: account in BAL with empty changes |
| R5d | Outer-scope revert keeps the read | BAL records are not journaled | caller reverts after precompile call: read still in BAL |
| R5e | Reverted scope restores accessed_addresses to entry set | standard EIP-2929 journaled warm set | post-revert call sees cold price again |
| R5f | Unchanged account → empty change lists | BAL AccountAccess with no changes | `assertEmpty` on the BAL entry |
| R6a | Fork-gated integration | new `PrecompiledContractsBogota` (Osaka set with 0x01 → `&ecrecover8151{}`); `activePrecompiledContracts` + `ActivePrecompiles` Bogota cases | pre-Bogota (Osaka rules) behaves exactly as before; Bogota applies restriction |
| R6b | Pre-activation behaviour preserved | plain `ecrecover{}` untouched in all pre-Bogota sets | existing vector tests unchanged |
| R6c | No unrelated changes | diff review | diff scope check |

## 3. Architecture

### 3.1 Stateful precompile plumbing

`core/vm/contracts.go` — the precompile runner already receives
`(stateDB StateDB, p, address, input, gas GasBudget, logger, rules)`:

```go
// New optional extension interface:
type StatefulPrecompiledContract interface {
    PrecompiledContract
    RunStateful(stateDB StateDB, input []byte, gas GasBudget, logger *tracing.Hooks) (ret []byte, remaining GasBudget, err error)
}
```

In `RunPrecompiledContract`, after the base-cost charge and the Amsterdam
precompile touch:

```go
if sp, ok := p.(StatefulPrecompiledContract); ok && stateDB != nil {
    return sp.RunStateful(stateDB, input, gas, logger)
}
```

The `stateDB != nil` guard keeps the existing test harness
(`testPrecompiled` passes a nil StateDB with empty rules against
`allPrecompiles`, which keeps mapping 0x01 to the plain `ecrecover{}`)
byte-compatible; production paths always pass a real StateDB.

### 3.2 The restricted precompile (Bogota only)

```go
type ecrecover8151 struct{}
RequiredGas = 3000 (unchanged; base charge handled by the runner)
Name() = "ECREC"
Run(input) = ecrecoverRun(input)                    // stateless fallback
RunStateful(stateDB, input, gas, logger):
    out = ecrecoverRun(input); if out == nil → (nil, gas, nil)   // R1c
    recovered = address from out[12:]
    cost = (AddressInAccessList(recovered) ? 100 : 3000) + 100   // R4a/R4b
    if !gas.ChargeRegular(cost) → (nil, gas, ErrOutOfGas)        // R3c/R4c
    emit gas-change hook (same precompile reason)
    AddAddressToAccessList(recovered)                            // warm (journaled)
    code = GetCode(recovered)                                    // raw code + BAL read (R5a/R5b)
    if len(code)==0 or ParseDelegation ok → (out, gas, nil)      // R2a/R2b/R2e
    else → (nil, gas, nil)                                       // R2c/R3a/R3b
```

`ecrecoverRun` is the byte-for-byte extraction of the existing `ecrecover.Run`
body (input padding, v/r/s validation, `crypto.Ecrecover`, left-pad); the
plain `ecrecover{}` keeps it unchanged.

### 3.3 Fork gating

- `PrecompiledContractsBogota`: full literal copy of the Osaka map with 0x01
  replaced (codebase convention; also updates `init()` address-list gen).
- `activePrecompiledContracts`: `case rules.IsBogota: return PrecompiledContractsBogota`.
- `ActivePrecompiles`: `case rules.IsBogota: return PrecompiledAddressesBogota`.
- Pre-Bogota behavior bit-identical (all older sets keep `&ecrecover{}`).
- XDC: XDC chain configs are block-fork-gated (`BogotaTime: nil`) → Bogota
  never activates there → no XDC behavior change.

### 3.4 BAL/revert semantics — why the design satisfies them for free

- Account-read recording: `StateDB.getStateObject` records
  `stateAccessList.AccountRead(addr)` unconditionally and outside the journal
  → rejected checks and outer reverts keep the read (R5c/R5d), and unchanged
  accounts get empty change lists (R5f).
- Warm set: `AddAddressToAccessList`/`AddressInAccessList` are journaled via
  accessListChange entries → a reverted scope restores the entry-time warm set
  (R5e) automatically.
- Absent accounts: `GetCode` returns nil and the read is still recorded
  ("regardless of whether the account exists") (R2e + BAL).

## 4. Milestones

- **M0**: clean dependency base (done: manifest + sha256, baseline build green).
- **M1**: core restriction — extraction + `ecrecover8151` + Bogota set + plumbing; behavioral tests.
- **M2**: gas accounting — cold/warm/exact-boundary tests (6100/3200, one-less failures).
- **M3**: BAL + revert-semantics tests (block-level via GenerateChain).
- **M4**: activation matrix (pre-Bogota vs Bogota), precompile regression, wider suites.
- **FINAL**: docs + fresh full verification + checkpoint.

## 5. Risks / watch-items

- Existing JSON vector tests run the stateless path (allPrecompiles → plain
  ecrecover) — they must stay green unchanged; the restriction is covered by
  new tests with real state.
- Tracer gas hooks: second charge emits `GasChangeCallPrecompiledContract`
  again (sum matches total charge).
- `TestEIP7910Config` (eth/protocols/eth) and the internal/ethapi failures are
  pre-existing baseline failures on main — regression comparisons are by
  normalized test name, never by timing.
