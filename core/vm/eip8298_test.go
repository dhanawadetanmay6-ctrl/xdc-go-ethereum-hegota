// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

// Opcode-level and creation-path tests for EIP-8298 (SETCODEFROM, 0xa7).
//
// Gas expectations derive from the fork's Amsterdam/EIP-8038 parameters
// (params/protocol_params.go), NOT from the EIP text's example table (which
// assumed ACCOUNT_WRITE = 9000; this fork pins AccountWriteAmsterdam = 8000):
//
//	invalid source:               cold 3100 / warm 200   (spec table matches)
//	valid, unchanged code hash:   cold 3200 / warm 300   (spec table matches)
//	code hash updated:            cold 11200 / warm 8300 (spec table: 12200/9300 at 9000)
package vm

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/types/bal"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/holiman/uint256"
)

// Derived per-fork SETCODEFROM cost expectations (execution gas).
var (
	scfWarmAccess   = uint64(params.WarmAccountAccessAmsterdam) // 100
	scfColdAccess   = uint64(params.ColdAccountAccessAmsterdam) // 3000
	scfAccountWrite = uint64(params.AccountWriteAmsterdam)      // 8000 (fork pin)
	scfSourceCold   = scfColdAccess + scfWarmAccess             // 3100
	scfSourceWarm   = scfWarmAccess + scfWarmAccess             // 200
	scfSameCold     = scfSourceCold + scfWarmAccess             // 3200
	scfSameWarm     = scfSourceWarm + scfWarmAccess             // 300
	scfUpdateCold   = scfSameCold + scfAccountWrite             // 11200
	scfUpdateWarm   = scfSameWarm + scfAccountWrite             // 8300
)

// bogota8298Config clones MergedTestChainConfig with Amsterdam + Bogota live.
func bogota8298Config() *params.ChainConfig {
	cfg := *params.MergedTestChainConfig
	cfg.AmsterdamTime = new(uint64)
	cfg.BogotaTime = new(uint64)
	return &cfg
}

func eip8298EVM(statedb StateDB, cfg *params.ChainConfig) *EVM {
	ctx := BlockContext{
		CanTransfer: func(db StateDB, addr common.Address, amount *uint256.Int) bool {
			return db.GetBalance(addr).Cmp(amount) >= 0
		},
		Transfer: func(db StateDB, sender, recipient common.Address, amount *uint256.Int, _ *params.Rules) {
			db.SubBalance(sender, amount, tracing.BalanceChangeTransfer)
			db.AddBalance(recipient, amount, tracing.BalanceChangeTransfer)
		},
		BlockNumber:      big.NewInt(0),
		Random:           &common.Hash{},
		CostPerStateByte: params.CostPerStateByte,
	}
	return NewEVM(ctx, statedb, cfg, Config{})
}

var (
	scfSelf   = common.BytesToAddress([]byte("scf-self"))
	scfCaller = common.BytesToAddress([]byte("scf-caller"))
)

// run8298 deploys `code` at scfSelf plus optional fixture accounts, finalises
// the setup out of the tx window, then executes a call into scfSelf.
// warmSources are added to the access list before the call.
func run8298(t *testing.T, code []byte, gas GasBudget, fixtures map[common.Address][]byte, warmSources []common.Address, cfg *params.ChainConfig) (*state.StateDB, GasBudget, error) {
	t.Helper()
	statedb, err := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	if err != nil {
		t.Fatal(err)
	}
	statedb.CreateAccount(scfSelf)
	statedb.SetCode(scfSelf, code, tracing.CodeChangeUnspecified)
	for addr, c := range fixtures {
		if !statedb.Exist(addr) {
			statedb.CreateAccount(addr)
		}
		statedb.SetCode(addr, c, tracing.CodeChangeUnspecified)
	}
	statedb.Finalise(true)
	// Open the tx-scoped access/BAL window (EIP-2929/7928), mirroring block
	// processing. The caller and precompiles are pre-warmed; fixture accounts
	// and the source stay cold unless listed in warmSources.
	rules := cfg.Rules(big.NewInt(0), true, 0)
	statedb.Prepare(rules, scfCaller, common.Address{}, nil, ActivePrecompiles(rules), nil)
	for _, addr := range warmSources {
		statedb.AddAddressToAccessList(addr)
	}
	_, result, err := eip8298EVM(statedb, cfg).Call(scfCaller, scfSelf, nil, gas, new(uint256.Int))
	return statedb, result, err
}

// scfCall returns bytecode: PUSH20 source; SETCODEFROM; PUSH1 0; SSTORE; STOP
// (stores the success flag in slot 0 for assertions).
func scfCall(source common.Address) []byte {
	code := []byte{0x73} // PUSH20
	code = append(code, source[:]...)
	code = append(code, 0xa7)       // SETCODEFROM
	code = append(code, 0x60, 0x00) // PUSH1 0
	code = append(code, 0x55)       // SSTORE
	code = append(code, 0x00)       // STOP
	return code
}

func scfSlot0(t *testing.T, statedb *state.StateDB) uint64 {
	t.Helper()
	return statedb.GetState(scfSelf, common.Hash{}).Big().Uint64()
}

// --------------------------- registration / fork gating -------------------

func TestSetCodeFromRegistered(t *testing.T) {
	op := bogotaInstructionSet[SETCODEFROM]
	if op == nil {
		t.Fatal("SETCODEFROM not registered in Bogota instruction set")
	}
	if op.constantGas != 0 || op.dynamicGas != nil {
		t.Fatal("SETCODEFROM must charge entirely inside the handler (staged charging order)")
	}
	if am := amsterdamInstructionSet[SETCODEFROM]; am == nil || !am.undefined {
		t.Fatal("SETCODEFROM must be undefined before Bogota")
	}
	if StringToOp("SETCODEFROM") != SETCODEFROM || SETCODEFROM.String() != "SETCODEFROM" {
		t.Fatal("opcode name maps broken")
	}
}

func TestSetCodeFromPreForkHalts(t *testing.T) {
	// Amsterdam-only config: Bogota not active, 0xa7 is undefined.
	cfg := *params.MergedTestChainConfig
	cfg.AmsterdamTime = new(uint64)
	source := common.BytesToAddress([]byte("scf-src"))
	srcCode := []byte{0x00} // STOP
	_, _, err := run8298(t, scfCall(source), hugeBudget(), map[common.Address][]byte{source: srcCode}, nil, &cfg)
	if err == nil {
		t.Fatal("pre-Bogota SETCODEFROM must halt (invalid opcode)")
	}
}

// ------------------------------ source validation -------------------------

func TestSetCodeFromValidSource(t *testing.T) {
	source := common.BytesToAddress([]byte("scf-src"))
	srcCode := []byte{0x60, 0x01, 0x60, 0x00, 0x55, 0x00} // SSTORE(0,1); STOP
	statedb, res, err := run8298(t, scfCall(source), hugeBudget(), map[common.Address][]byte{source: srcCode}, nil, bogota8298Config())
	if err != nil {
		t.Fatal(err)
	}
	if scfSlot0(t, statedb) != 1 {
		t.Fatal("SETCODEFROM with valid source must push 1")
	}
	want := crypto.Keccak256Hash(srcCode)
	if got := statedb.GetCodeHash(scfSelf); got != want {
		t.Fatalf("code hash = %x, want %x", got, want)
	}
	if got := statedb.GetCode(scfSelf); !bytes.Equal(got, srcCode) {
		t.Fatalf("adopted code = %x, want %x", got, srcCode)
	}
	if got := statedb.GetCodeSize(scfSelf); got != len(srcCode) {
		t.Fatalf("adopted code size = %d, want %d", got, len(srcCode))
	}
	// The opcode itself charges zero state gas; the 97920 below is the
	// trailing SSTORE's slot-creation charge (StorageCreationSize * CPSB)
	// from the scfCall success-flag store, not from SETCODEFROM.
	if res.UsedStateGas != stateGasNewSlot {
		t.Fatalf("state gas used = %d, want %d (SSTORE slot creation only)", res.UsedStateGas, stateGasNewSlot)
	}
}

func TestSetCodeFromNonexistentSource(t *testing.T) {
	source := common.BytesToAddress([]byte("scf-nope"))
	statedb, _, err := run8298(t, scfCall(source), hugeBudget(), nil, nil, bogota8298Config())
	if err != nil {
		t.Fatal(err)
	}
	if scfSlot0(t, statedb) != 0 {
		t.Fatal("nonexistent source must push 0")
	}
	if got, want := statedb.GetCodeHash(scfSelf), crypto.Keccak256Hash(scfCall(source)); got != want {
		t.Fatalf("code hash changed for invalid source: %x, want %x", got, want)
	}
}

func TestSetCodeFromEmptyCodeSource(t *testing.T) {
	source := common.BytesToAddress([]byte("scf-empty"))
	statedb, _, err := run8298(t, scfCall(source), hugeBudget(), map[common.Address][]byte{source: nil}, nil, bogota8298Config())
	if err != nil {
		t.Fatal(err)
	}
	if scfSlot0(t, statedb) != 0 {
		t.Fatal("empty-code source must push 0")
	}
}

func TestSetCodeFromEFPrefixedSource(t *testing.T) {
	source := common.BytesToAddress([]byte("scf-eof"))
	// 0xEF-prefixed code (reserved by EIP-3541, e.g. an EIP-7702 indicator).
	efCode := append([]byte{0xef, 0x01, 0x00}, make([]byte, 20)...)
	statedb, _, err := run8298(t, scfCall(source), hugeBudget(), map[common.Address][]byte{source: efCode}, nil, bogota8298Config())
	if err != nil {
		t.Fatal(err)
	}
	if scfSlot0(t, statedb) != 0 {
		t.Fatal("0xEF-prefixed source must push 0")
	}
}

func TestSetCodeFromPrecompileSource(t *testing.T) {
	// Untouched precompile: does not exist in state.
	statedb, _, err := run8298(t, scfCall(eip8151Precompile), hugeBudget(), nil, nil, bogota8298Config())
	if err != nil {
		t.Fatal(err)
	}
	if scfSlot0(t, statedb) != 0 {
		t.Fatal("untouched precompile source must push 0")
	}
	// Touched precompile (balance set): exists but has EMPTYCODEHASH.
	statedb2, err := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	if err != nil {
		t.Fatal(err)
	}
	statedb2.CreateAccount(scfSelf)
	statedb2.SetCode(scfSelf, scfCall(eip8151Precompile), tracing.CodeChangeUnspecified)
	statedb2.AddBalance(eip8151Precompile, uint256.NewInt(1), tracing.BalanceChangeUnspecified)
	statedb2.Finalise(true)
	if _, _, err := eip8298EVM(statedb2, bogota8298Config()).Call(scfCaller, scfSelf, nil, hugeBudget(), new(uint256.Int)); err != nil {
		t.Fatal(err)
	}
	if statedb2.GetState(scfSelf, common.Hash{}).Big().Uint64() != 0 {
		t.Fatal("touched precompile source must push 0")
	}
}

func TestSetCodeFromDelegatedSourceNotResolved(t *testing.T) {
	// Source holds an EIP-7702 delegation indicator (starts with 0xEF):
	// invalid, and the delegation TARGET must not be accessed (not warmed,
	// not in the BAL).
	source := common.BytesToAddress([]byte("scf-deleg"))
	target := common.BytesToAddress([]byte("scf-target"))
	targetCode := []byte{0x60, 0x2a, 0x60, 0x00, 0x55, 0x00}
	statedb, err := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	if err != nil {
		t.Fatal(err)
	}
	statedb.CreateAccount(scfSelf)
	statedb.SetCode(scfSelf, scfCall(source), tracing.CodeChangeUnspecified)
	statedb.CreateAccount(source)
	statedb.SetCode(source, types.AddressToDelegation(target), tracing.CodeChangeUnspecified)
	statedb.CreateAccount(target)
	statedb.SetCode(target, targetCode, tracing.CodeChangeUnspecified)
	statedb.Finalise(true)
	rules := bogota8298Config().Rules(big.NewInt(0), true, 0)
	statedb.Prepare(rules, scfCaller, common.Address{}, nil, ActivePrecompiles(rules), nil)
	if _, _, err := eip8298EVM(statedb, bogota8298Config()).Call(scfCaller, scfSelf, nil, hugeBudget(), new(uint256.Int)); err != nil {
		t.Fatal(err)
	}
	if statedb.GetState(scfSelf, common.Hash{}).Big().Uint64() != 0 {
		t.Fatal("delegated source must push 0 (indicator code starts with 0xEF)")
	}
	if statedb.AddressInAccessList(target) {
		t.Fatal("delegation target must not be warmed by source validation")
	}
	access := statedb.Finalise(true)
	if _, ok := access.Accounts[target]; ok {
		t.Fatal("delegation target must not appear in the BAL")
	}
	if _, ok := access.Accounts[source]; !ok {
		t.Fatal("source must appear in the BAL once source gas was charged")
	}
}

// --------------------------------- gas ------------------------------------

func TestSetCodeFromGas(t *testing.T) {
	source := common.BytesToAddress([]byte("scf-src"))
	srcCode := []byte{0x60, 0x01, 0x60, 0x00, 0x55, 0x00}
	selfCode := scfCall(source) // self executes this
	pushCost := uint64(3+3) + 3 // PUSH20 + PUSH1 + SSTORE slot write is extra; measured below

	// SSTORE(0, 1) after the opcode adds the Amsterdam SSTORE cost; to keep the
	// expectations focused on SETCODEFROM itself we measure codes WITHOUT the
	// trailing store for the pure-gas table.
	pushOnly := uint64(3) // PUSH20
	codeNoStore := func(src common.Address) []byte {
		c := []byte{0x73}
		c = append(c, src[:]...)
		c = append(c, 0xa7, 0x00) // SETCODEFROM; STOP
		return c
	}
	_ = pushCost
	_ = selfCode
	_ = pushOnly

	tests := []struct {
		name     string
		fixtures map[common.Address][]byte
		warm     bool
		selfCode []byte // nil = default adopting code
		wantGas  uint64
		wantOK   uint64
	}{
		{name: "invalid/cold (nonexistent)", wantGas: scfSourceCold, wantOK: 0},
		{name: "invalid/warm (nonexistent)", warm: true, wantGas: scfSourceWarm, wantOK: 0},
		{name: "invalid/cold (empty code)", fixtures: map[common.Address][]byte{source: nil}, wantGas: scfSourceCold, wantOK: 0},
		{name: "invalid/warm (empty code)", fixtures: map[common.Address][]byte{source: nil}, warm: true, wantGas: scfSourceWarm, wantOK: 0},
		{name: "valid-update/cold", fixtures: map[common.Address][]byte{source: srcCode}, wantGas: scfUpdateCold, wantOK: 1},
		{name: "valid-update/warm", fixtures: map[common.Address][]byte{source: srcCode}, warm: true, wantGas: scfUpdateWarm, wantOK: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code := codeNoStore(source)
			var warm []common.Address
			if tc.warm {
				warm = []common.Address{source}
			}
			_, res, err := run8298(t, code, hugeBudget(), tc.fixtures, warm, bogota8298Config())
			if err != nil {
				t.Fatal(err)
			}
			// PUSH20 (3) + SETCODEFROM cost
			if want := tc.wantGas + 3; res.UsedRegularGas != want {
				t.Fatalf("regular gas = %d, want %d (opcode %d + push 3)", res.UsedRegularGas, want, tc.wantGas)
			}
			if res.UsedStateGas != 0 {
				t.Fatalf("state gas = %d, want 0", res.UsedStateGas)
			}
		})
	}
	// Same-hash adoption: self code identical to source code.
	t.Run("same-hash/cold", func(t *testing.T) {
		code := codeNoStore(source)
		fixtures := map[common.Address][]byte{source: bytes.Clone(code)}
		_, res, err := run8298(t, code, hugeBudget(), fixtures, nil, bogota8298Config())
		if err != nil {
			t.Fatal(err)
		}
		if want := scfSameCold + 3; res.UsedRegularGas != want {
			t.Fatalf("regular gas = %d, want %d", res.UsedRegularGas, want)
		}
	})
	t.Run("same-hash/warm", func(t *testing.T) {
		code := codeNoStore(source)
		fixtures := map[common.Address][]byte{source: bytes.Clone(code)}
		_, res, err := run8298(t, code, hugeBudget(), fixtures, []common.Address{source}, bogota8298Config())
		if err != nil {
			t.Fatal(err)
		}
		if want := scfSameWarm + 3; res.UsedRegularGas != want {
			t.Fatalf("regular gas = %d, want %d", res.UsedRegularGas, want)
		}
	})
}

func TestSetCodeFromOutOfGas(t *testing.T) {
	source := common.BytesToAddress([]byte("scf-src"))
	srcCode := []byte{0x00}
	code := func() []byte {
		c := []byte{0x73}
		c = append(c, source[:]...)
		c = append(c, 0xa7, 0x00)
		return c
	}()
	tests := []struct {
		name      string
		gas       uint64
		wantErr   bool
		sourceBAL bool // whether the source must appear in the BAL after the run
	}{
		// Stage 1: cannot afford the source charge (cold 3100; +3 push) —
		// source is NOT accessed and must not be included in the BAL.
		{name: "oog-source-charge", gas: 3 + scfSourceCold - 1, wantErr: true},
		// Stage 2: source charged and accessed (BAL-included), cannot afford
		// the current-access gas.
		{name: "oog-current-access", gas: 3 + scfSameCold - 1, wantErr: true, sourceBAL: true},
		// Stage 3: access charged, cannot afford the write.
		{name: "oog-write", gas: 3 + scfUpdateCold - 1, wantErr: true, sourceBAL: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			statedb, res, err := run8298(t, code, NewGasBudget(tc.gas, 0), map[common.Address][]byte{source: srcCode}, nil, bogota8298Config())
			if err == nil {
				t.Fatal("expected out-of-gas halt")
			}
			// Exceptional halt drains the remaining regular gas.
			if res.RegularGas != 0 {
				t.Fatalf("regular gas left = %d, want 0 after halt", res.RegularGas)
			}
			// EIP-2929 warmings roll back with the halted frame; the EIP-7928
			// BAL (block-scoped, not journaled) still records the access iff
			// the source charge was paid.
			access := statedb.Finalise(true)
			_, inBAL := access.Accounts[source]
			if inBAL != tc.sourceBAL {
				t.Fatalf("source in BAL = %v, want %v", inBAL, tc.sourceBAL)
			}
			// Code hash unchanged (halt rolls back).
			if got := statedb.GetCodeHash(scfSelf); got != crypto.Keccak256Hash(code) {
				t.Fatalf("code hash changed after halt: %x", got)
			}
		})
	}
}

// ------------------------- restrictions and rollback ----------------------

func TestSetCodeFromStaticContextHalts(t *testing.T) {
	source := common.BytesToAddress([]byte("scf-src"))
	srcCode := []byte{0x00}
	// scfSelf: STATICCALL(scfSelf2) where scfSelf2 executes SETCODEFROM.
	inner := common.BytesToAddress([]byte("scf-inner"))
	innerCode := scfCall(source)
	// outer: STATICCALL(gas, inner, 0, 0, 0, 0); STOP
	outer := []byte{0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x73}
	outer = append(outer, inner[:]...)
	outer = append(outer, 0x61, 0xff, 0xff, 0xfa, 0x00) // PUSH2 0xffff gas; STATICCALL; STOP
	statedb, _, err := run8298(t, outer, hugeBudget(), map[common.Address][]byte{inner: innerCode, source: srcCode}, nil, bogota8298Config())
	if err != nil {
		t.Fatal(err) // staticcall failure is captured, outer completes
	}
	if statedb.GetCodeHash(inner) != crypto.Keccak256Hash(innerCode) {
		t.Fatal("static-context SETCODEFROM must not change the code hash")
	}
	if statedb.AddressInAccessList(source) {
		t.Fatal("halt must precede the source charge: source must not be warmed")
	}
}

func TestSetCodeFromSystemContractHalts(t *testing.T) {
	source := common.BytesToAddress([]byte("scf-src"))
	srcCode := []byte{0x00}
	statedb, err := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	if err != nil {
		t.Fatal(err)
	}
	statedb.CreateAccount(scfSelf)
	statedb.SetCode(scfSelf, scfCall(source), tracing.CodeChangeUnspecified)
	statedb.CreateAccount(source)
	statedb.SetCode(source, srcCode, tracing.CodeChangeUnspecified)
	statedb.Finalise(true)
	// A protocol system call invokes the frame from SYSTEM_ADDRESS.
	if _, _, err := eip8298EVM(statedb, bogota8298Config()).Call(params.SystemAddress, scfSelf, nil, hugeBudget(), new(uint256.Int)); err == nil {
		t.Fatal("SETCODEFROM with SYSTEM_ADDRESS caller must halt (system contract)")
	}
	if statedb.AddressInAccessList(source) {
		t.Fatal("halt must precede the source charge: source must not be warmed")
	}
}

func TestSetCodeFromRevertRollback(t *testing.T) {
	source := common.BytesToAddress([]byte("scf-src"))
	srcCode := []byte{0x60, 0x2a, 0x60, 0x00, 0x55, 0x00}
	// Adopt, then revert: PUSH20 src; SETCODEFROM; PUSH1 0; PUSH1 0; REVERT
	code := []byte{0x73}
	code = append(code, source[:]...)
	code = append(code, 0xa7, 0x60, 0x00, 0x60, 0x00, 0xfd)
	statedb, _, err := run8298(t, code, hugeBudget(), map[common.Address][]byte{source: srcCode}, nil, bogota8298Config())
	if err == nil {
		t.Fatal("expected revert")
	}
	if got := statedb.GetCodeHash(scfSelf); got != crypto.Keccak256Hash(code) {
		t.Fatalf("code hash = %x after revert, want original %x", got, crypto.Keccak256Hash(code))
	}
}

func TestSetCodeFromDelegateCallAdoptsEnvironment(t *testing.T) {
	// A library containing SETCODEFROM is DELEGATECALLed: the adoption must
	// land on the calling (execution-environment) account, not the library.
	lib := common.BytesToAddress([]byte("scf-lib"))
	source := common.BytesToAddress([]byte("scf-src"))
	srcCode := []byte{0x60, 0x01, 0x60, 0x01, 0x55, 0x00}
	libCode := scfCall(source)
	// outer: DELEGATECALL(gas, lib, 0, 0, 0, 0); STOP
	outer := []byte{0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x73}
	outer = append(outer, lib[:]...)
	outer = append(outer, 0x61, 0xff, 0xff, 0xf4, 0x00) // PUSH2 0xffff; DELEGATECALL; STOP
	statedb, _, err := run8298(t, outer, hugeBudget(), map[common.Address][]byte{lib: libCode, source: srcCode}, nil, bogota8298Config())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := statedb.GetCodeHash(scfSelf), crypto.Keccak256Hash(srcCode); got != want {
		t.Fatalf("environment account code hash = %x, want adopted %x", got, want)
	}
	if got, want := statedb.GetCodeHash(lib), crypto.Keccak256Hash(libCode); got != want {
		t.Fatalf("library code hash = %x, want unchanged %x", got, want)
	}
	// The library's SSTORE (slot 0 = success flag) executed in the caller's
	// storage context as well.
	if scfSlot0(t, statedb) != 1 {
		t.Fatal("delegatecall adoption must report success in the caller's storage")
	}
}

// ------------------------------- BAL (EIP-7928) ---------------------------

func TestSetCodeFromBALRecording(t *testing.T) {
	source := common.BytesToAddress([]byte("scf-src"))
	srcCode := []byte{0x60, 0x01, 0x60, 0x00, 0x55, 0x00}
	statedb, _, err := run8298(t, scfCall(source), hugeBudget(), map[common.Address][]byte{source: srcCode}, nil, bogota8298Config())
	if err != nil {
		t.Fatal(err)
	}
	access := statedb.Finalise(true)
	self, ok := access.Accounts[scfSelf]
	if !ok {
		t.Fatal("adopting account missing from BAL")
	}
	wantHash := crypto.Keccak256Hash(srcCode)
	// The harness executes a single tx without advancing the block access
	// index, so the change lands at index 0 (block processing uses txIndex+1).
	if len(self.CodeChangeHashes) != 1 || self.CodeChangeHashes[0] != wantHash {
		t.Fatalf("BAL hash-form code change = %+v, want index 0 -> %x", self.CodeChangeHashes, wantHash)
	}
	if len(self.CodeChange) != 0 {
		t.Fatalf("BAL bytecode-form code change present for adoption: %+v", self.CodeChange)
	}
	// Source: present with empty change lists (only accessed+warmed).
	src, ok := access.Accounts[source]
	if !ok {
		t.Fatal("source missing from BAL after source-gas charge")
	}
	if len(src.StorageWrites) != 0 || len(src.StorageReads) != 0 || len(src.BalanceChanges) != 0 || len(src.NonceChanges) != 0 || len(src.CodeChange) != 0 || len(src.CodeChangeHashes) != 0 {
		t.Fatalf("source must have empty change lists: %+v", src)
	}
}

func TestSetCodeFromBALInvalidSourceStillRecorded(t *testing.T) {
	source := common.BytesToAddress([]byte("scf-nope"))
	statedb, _, err := run8298(t, scfCall(source), hugeBudget(), nil, nil, bogota8298Config())
	if err != nil {
		t.Fatal(err)
	}
	access := statedb.Finalise(true)
	if _, ok := access.Accounts[source]; !ok {
		t.Fatal("invalid source must still be recorded in the BAL once charged")
	}
	if self, ok := access.Accounts[scfSelf]; ok && (len(self.CodeChange) != 0 || len(self.CodeChangeHashes) != 0) {
		t.Fatalf("no code change may be recorded for an invalid source: %+v", self)
	}
}

func TestSetCodeFromBALEncoding(t *testing.T) {
	source := common.BytesToAddress([]byte("scf-src"))
	srcCode := []byte{0x60, 0x01, 0x60, 0x00, 0x55, 0x00}
	statedb, _, err := run8298(t, scfCall(source), hugeBudget(), map[common.Address][]byte{source: srcCode}, nil, bogota8298Config())
	if err != nil {
		t.Fatal(err)
	}
	access := statedb.Finalise(true)
	enc, err := rlp.EncodeToBytes(access.ToEncodingObj())
	if err != nil {
		t.Fatal(err)
	}
	decoded := new(bal.BlockAccessList)
	if err := rlp.DecodeBytes(enc, decoded); err != nil {
		t.Fatal(err)
	}
	// Locate the adoption entry and verify the three-element form.
	var found bool
	for _, acc := range *decoded {
		if acc.Address != scfSelf {
			continue
		}
		for _, cc := range acc.CodeChanges {
			if cc.NewCodeHash != nil {
				found = true
				if len(cc.NewCode) != 0 {
					t.Fatal("adoption entry must carry empty new_code")
				}
				if *cc.NewCodeHash != crypto.Keccak256Hash(srcCode) {
					t.Fatalf("adoption hash = %x", *cc.NewCodeHash)
				}
			}
		}
	}
	if !found {
		t.Fatal("adoption entry not found after RLP round-trip")
	}
	if err := decoded.Validate(1_000_000_000, 10); err != nil {
		t.Fatalf("BAL with adoption entry failed validation: %v", err)
	}
}

// --------------------- contract creation / initcode -----------------------

// scfTemplate is the shared deployed code adopted in the creation tests.
var scfTemplate = []byte{0x60, 0x2a, 0x60, 0x00, 0x55, 0x00} // SSTORE(0,42); STOP

var scfTemplateAddr = common.BytesToAddress([]byte("scf-template"))

// scfInitcode builds initcode that: adopts scfTemplateAddr, stores the success
// flag in slot 2, stores EXTCODEHASH(ADDRESS) in slot 1, stores CODESIZE in
// slot 0, then returns 32 junk bytes (which must be ignored when adopted).
func scfInitcode(source common.Address) []byte {
	code := []byte{0x73}
	code = append(code, source[:]...)
	code = append(code,
		0xa7,             // SETCODEFROM(source) -> [success]
		0x60, 0x02, 0x55, // SSTORE(2, success)
		0x30, 0x3f, // ADDRESS; EXTCODEHASH -> [hash]
		0x60, 0x01, 0x55, // SSTORE(1, hash)
		0x38,             // CODESIZE -> [len(initcode)]
		0x60, 0x00, 0x55, // SSTORE(0, codesize)
		0x60, 0x42, 0x60, 0x00, 0x53, // MSTORE8(0, 0x42)
		0x60, 0x20, 0x60, 0x00, 0xf3, // RETURN(0, 32) — junk
	)
	return code
}

// setup8298Create builds state with the caller funded and the template
// deployed, then opens the tx window.
func setup8298Create(t *testing.T) (*state.StateDB, *EVM) {
	t.Helper()
	statedb, err := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	if err != nil {
		t.Fatal(err)
	}
	statedb.CreateAccount(scfCaller)
	statedb.AddBalance(scfCaller, uint256.NewInt(1_000_000_000_000), tracing.BalanceChangeUnspecified)
	statedb.CreateAccount(scfTemplateAddr)
	statedb.SetCode(scfTemplateAddr, scfTemplate, tracing.CodeChangeUnspecified)
	statedb.Finalise(true)
	cfg := bogota8298Config()
	rules := cfg.Rules(big.NewInt(0), true, 0)
	statedb.Prepare(rules, scfCaller, common.Address{}, nil, ActivePrecompiles(rules), nil)
	return statedb, eip8298EVM(statedb, cfg)
}

func TestSetCodeFromCreateAdopts(t *testing.T) {
	statedb, evm := setup8298Create(t)
	initcode := scfInitcode(scfTemplateAddr)
	nonce := statedb.GetNonce(scfCaller)
	created := crypto.CreateAddress(scfCaller, nonce)
	_, addr, res, err := evm.Create(scfCaller, initcode, hugeBudget(), new(uint256.Int))
	if err != nil {
		t.Fatal(err)
	}
	if addr != created {
		t.Fatalf("created address = %x, want %x", addr, created)
	}
	wantHash := crypto.Keccak256Hash(scfTemplate)
	if got := statedb.GetCodeHash(created); got != wantHash {
		t.Fatalf("created code hash = %x, want adopted %x", got, wantHash)
	}
	if got := statedb.GetCode(created); !bytes.Equal(got, scfTemplate) {
		t.Fatalf("created code = %x, want adopted template %x", got, scfTemplate)
	}
	// Slot 2: adoption succeeded during initcode.
	if got := statedb.GetState(created, common.BytesToHash([]byte{2})).Big().Uint64(); got != 1 {
		t.Fatalf("adoption success flag = %d, want 1", got)
	}
	// Slot 1: EXTCODEHASH of the account under construction = adopted hash.
	if got := statedb.GetState(created, common.BytesToHash([]byte{1})); got != common.BytesToHash(wantHash.Bytes()) {
		t.Fatalf("EXTCODEHASH during initcode = %x, want %x", got, wantHash)
	}
	// Slot 0: CODESIZE inside the frame = len(initcode), not len(adopted).
	if got := statedb.GetState(created, common.Hash{}).Big().Uint64(); got != uint64(len(initcode)) {
		t.Fatalf("CODESIZE during initcode = %d, want initcode length %d", got, len(initcode))
	}
	// No code-deposit state gas for the 32 junk return bytes. Direct
	// evm.Create calls do not run the parent CREATE opcode, so the
	// account-leaf charge (which this fork folds into the parent's create
	// dynamic gas) is out of scope here and asserted separately in
	// TestSetCodeFromCreateViaOpcode. What must remain here: the slot
	// creations; what must be absent: the 32-byte code deposit.
	codeDepositState := int64(32 * evm.Context.CostPerStateByte)
	slotCreates := int64(3 * uint64(params.StorageCreationSize) * evm.Context.CostPerStateByte)
	if res.UsedStateGas == slotCreates+codeDepositState {
		t.Fatal("code-deposit state gas was charged for adopted code")
	}
	if res.UsedStateGas != slotCreates {
		t.Fatalf("state gas = %d, want %d (3 slot creations, no code deposit)", res.UsedStateGas, slotCreates)
	}
}

func TestSetCodeFromCreate2Adopts(t *testing.T) {
	statedb, evm := setup8298Create(t)
	initcode := scfInitcode(scfTemplateAddr)
	salt := uint256.NewInt(0xbeef)
	created := crypto.CreateAddress2(scfCaller, salt.Bytes32(), crypto.Keccak256(initcode))
	_, addr, _, err := evm.Create2(scfCaller, initcode, hugeBudget(), new(uint256.Int), salt)
	if err != nil {
		t.Fatal(err)
	}
	if addr != created {
		t.Fatalf("created2 address = %x, want %x", addr, created)
	}
	if got := statedb.GetCodeHash(created); got != crypto.Keccak256Hash(scfTemplate) {
		t.Fatalf("created2 code hash = %x, want adopted", got)
	}
	if got := statedb.GetCode(created); !bytes.Equal(got, scfTemplate) {
		t.Fatalf("created2 code = %x, want template", got)
	}
}

func TestSetCodeFromCreateWithoutAdoptionUnchanged(t *testing.T) {
	// Baseline: initcode that returns 32 junk bytes WITHOUT SETCODEFROM keeps
	// the ordinary path: deposit gas charged, junk installed.
	statedb, evm := setup8298Create(t)
	initcode := []byte{
		0x60, 0x42, 0x60, 0x00, 0x53, // MSTORE8(0, 0x42)
		0x60, 0x20, 0x60, 0x00, 0xf3, // RETURN(0, 32)
	}
	created := crypto.CreateAddress(scfCaller, statedb.GetNonce(scfCaller))
	_, _, res, err := evm.Create(scfCaller, initcode, hugeBudget(), new(uint256.Int))
	if err != nil {
		t.Fatal(err)
	}
	codeDepositState := int64(32 * evm.Context.CostPerStateByte)
	if res.UsedStateGas != codeDepositState {
		t.Fatalf("baseline state gas = %d, want %d (32-byte code deposit; account-leaf charge lives in the parent CREATE opcode)", res.UsedStateGas, codeDepositState)
	}
	got := statedb.GetCode(created)
	if len(got) != 32 || got[0] != 0x42 {
		t.Fatalf("baseline installed code = %x, want 32 junk bytes starting 0x42", got)
	}
}

func TestSetCodeFromFailedCreateRollsBack(t *testing.T) {
	statedb, evm := setup8298Create(t)
	// Initcode: adopt, then revert.
	initcode := []byte{0x73}
	initcode = append(initcode, scfTemplateAddr[:]...)
	initcode = append(initcode, 0xa7, 0x60, 0x00, 0x60, 0x00, 0xfd)
	created := crypto.CreateAddress(scfCaller, statedb.GetNonce(scfCaller))
	_, _, _, err := evm.Create(scfCaller, initcode, hugeBudget(), new(uint256.Int))
	if err == nil {
		t.Fatal("expected revert from initcode")
	}
	if got := statedb.GetCodeHash(created); got != (common.Hash{}) && got != types.EmptyCodeHash {
		t.Fatalf("failed creation left code hash %x", got)
	}
	if statedb.Exist(created) {
		// The whole create frame reverted: no account may remain.
		t.Fatal("failed creation left the account behind")
	}
}

func TestSetCodeFromSourceCreatedInCurrentTx(t *testing.T) {
	// Parent bytecode: CREATE a child with non-empty runtime, then
	// SETCODEFROM(child) — the child was created in the current transaction
	// and must be rejected (EIP-6780 rule).
	//
	// child initcode: 6000 6000 53 6001 6000 f3 (MSTORE8(0,0); RETURN(0,1))
	// child runtime:  [0x00] (STOP) — non-empty.
	childInit := []byte{0x60, 0x00, 0x60, 0x00, 0x53, 0x60, 0x01, 0x60, 0x00, 0xf3}
	parent := []byte{0x6a} // PUSH10
	parent = append(parent, childInit...)
	parent = append(parent,
		0x60, 0x00, 0x52, // MSTORE(0, initcode) — right-aligned at mem[22..32]
		0x60, 0x0a, 0x60, 0x16, 0x60, 0x00, 0xf0, // CREATE(0, 22, 10)
		0xa7,             // SETCODEFROM(child)
		0x60, 0x03, 0x55, // SSTORE(3, success)
		0x00, // STOP
	)
	statedb, _, err := run8298(t, parent, hugeBudget(), nil, nil, bogota8298Config())
	if err != nil {
		t.Fatal(err)
	}
	if got := scfSlot0(t, statedb); got != 1<<0 {
		_ = got
	}
	if got := statedb.GetState(scfSelf, common.BytesToHash([]byte{3})).Big().Uint64(); got != 0 {
		t.Fatalf("SETCODEFROM from a same-tx-created source pushed %d, want 0", got)
	}
}

func TestSetCodeFromReentrantCallExecutesAdoptedCode(t *testing.T) {
	// Initcode adopts the template, then calls helper H, which calls back
	// into the account under construction. The callback must execute the
	// ADOPTED code (template: SSTORE(0,42)) against the created account.
	statedb, err := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	if err != nil {
		t.Fatal(err)
	}
	created := crypto.CreateAddress(scfCaller, 0) // caller nonce 0 at create time
	helper := common.BytesToAddress([]byte("scf-helper"))
	// H: CALL(0xffff gas, created, 0, 0, 0, 0, 0); STOP
	helperCode := []byte{0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x73}
	helperCode = append(helperCode, created[:]...)
	helperCode = append(helperCode, 0x61, 0xff, 0xff, 0xf1, 0x00)
	// initcode: SETCODEFROM(template); SSTORE(2, success); CALL(H); STOP
	initcode := []byte{0x73}
	initcode = append(initcode, scfTemplateAddr[:]...)
	initcode = append(initcode,
		0xa7, 0x60, 0x02, 0x55, // adopt + store success
		0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x73, // call args
	)
	initcode = append(initcode, helper[:]...)
	initcode = append(initcode, 0x61, 0xff, 0xff, 0xf1, 0x00) // CALL(H); STOP

	statedb.CreateAccount(scfCaller)
	statedb.AddBalance(scfCaller, uint256.NewInt(1_000_000_000_000), tracing.BalanceChangeUnspecified)
	statedb.CreateAccount(scfTemplateAddr)
	statedb.SetCode(scfTemplateAddr, scfTemplate, tracing.CodeChangeUnspecified)
	statedb.CreateAccount(helper)
	statedb.SetCode(helper, helperCode, tracing.CodeChangeUnspecified)
	statedb.Finalise(true)
	cfg := bogota8298Config()
	rules := cfg.Rules(big.NewInt(0), true, 0)
	statedb.Prepare(rules, scfCaller, common.Address{}, nil, ActivePrecompiles(rules), nil)
	evm := eip8298EVM(statedb, cfg)
	_, _, _, err = evm.Create(scfCaller, initcode, hugeBudget(), new(uint256.Int))
	if err != nil {
		t.Fatal(err)
	}
	// The re-entrant call executed the adopted template: slot 0 == 42.
	if got := statedb.GetState(created, common.Hash{}).Big().Uint64(); got != 42 {
		t.Fatalf("created account slot0 = %d, want 42 (adopted code ran re-entrantly)", got)
	}
	if got := statedb.GetState(created, common.BytesToHash([]byte{2})).Big().Uint64(); got != 1 {
		t.Fatalf("adoption flag = %d, want 1", got)
	}
}

func TestSetCodeFromSourceChangesAfterAdoption(t *testing.T) {
	// A adopts S1's code; then S1 adopts S2's code (same tx). A keeps S1's
	// original code hash and the bytecode stays available.
	s1 := common.BytesToAddress([]byte("scf-s1"))
	s2 := common.BytesToAddress([]byte("scf-s2"))
	s2Code := []byte{0x60, 0x2b, 0x60, 0x00, 0x55, 0x00} // SSTORE(0,43); STOP
	// A: SETCODEFROM(S1); POP; CALL(S1) -> S1's (original) code adopts S2.
	aCode := []byte{0x73}
	aCode = append(aCode, s1[:]...)
	aCode = append(aCode,
		0xa7, 0x50, // SETCODEFROM(S1); POP success
		0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x73,
	)
	aCode = append(aCode, s1[:]...)
	aCode = append(aCode, 0x61, 0xff, 0xff, 0xf1, 0x00) // CALL(S1); STOP
	// S1's deployed code: SETCODEFROM(S2); POP; STOP — S1 executes its
	// ORIGINAL code because the frame's code was loaded at call start.
	s1Code := []byte{0x73}
	s1Code = append(s1Code, s2[:]...)
	s1Code = append(s1Code, 0xa7, 0x50, 0x00) // SETCODEFROM(S2); POP; STOP

	statedb, _, err := run8298(t, aCode, hugeBudget(), map[common.Address][]byte{s1: s1Code, s2: s2Code}, nil, bogota8298Config())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := statedb.GetCodeHash(scfSelf), crypto.Keccak256Hash(s1Code); got != want {
		t.Fatalf("A hash = %x, want S1's original %x", got, want)
	}
	if got, want := statedb.GetCodeHash(s1), crypto.Keccak256Hash(s2Code); got != want {
		t.Fatalf("S1 hash = %x, want S2's %x", got, want)
	}
	if got := statedb.GetCode(scfSelf); !bytes.Equal(got, s1Code) {
		t.Fatalf("A's bytecode unavailable after source changed: %x", got)
	}
}

func TestSetCodeFromRepeatedAdoption(t *testing.T) {
	// Two adoptions in one frame: the last one wins; the second (same-account
	// warm) charge path is exercised.
	s1 := common.BytesToAddress([]byte("scf-s1"))
	s2 := common.BytesToAddress([]byte("scf-s2"))
	s1Code := []byte{0x60, 0x01, 0x60, 0x00, 0x55, 0x00}
	s2Code := []byte{0x60, 0x02, 0x60, 0x00, 0x55, 0x00}
	code := []byte{0x73}
	code = append(code, s1[:]...)
	code = append(code, 0xa7, 0x50, 0x73) // SETCODEFROM(S1); POP; PUSH20 S2
	code = append(code, s2[:]...)
	code = append(code, 0xa7, 0x50, 0x00) // SETCODEFROM(S2); POP; STOP
	statedb, _, err := run8298(t, code, hugeBudget(), map[common.Address][]byte{s1: s1Code, s2: s2Code}, nil, bogota8298Config())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := statedb.GetCodeHash(scfSelf), crypto.Keccak256Hash(s2Code); got != want {
		t.Fatalf("hash = %x, want last-adopted %x", got, want)
	}
	if got := statedb.GetCode(scfSelf); !bytes.Equal(got, s2Code) {
		t.Fatalf("code = %x, want S2", got)
	}
}

// --------------------------- EIP-8151 interaction -------------------------

func TestSetCodeFromAdoptionTriggersEIP8151(t *testing.T) {
	// The migration arc: an EOA (here with an EIP-7702 delegation indicator,
	// which EIP-8151 permits) adopts regular deployed code through its
	// delegated execution; afterwards ecRecover must reject it (empty output,
	// success), exactly as if the code had been deployed normally.
	rules := eip8151Rules(true)
	template := common.BytesToAddress([]byte("scf-wallet"))
	templateCode := []byte{0x60, 0x2a, 0x60, 0x00, 0x55, 0x00}

	statedb := newEIP8151State(t, rules, func(db *state.StateDB) {
		db.CreateAccount(eip8151Recovered)
		db.SetCode(eip8151Recovered, types.AddressToDelegation(template), tracing.CodeChangeUnspecified)
		db.CreateAccount(template)
		db.SetCode(template, templateCode, tracing.CodeChangeUnspecified)
	})
	// Pre-adoption: delegated account is permitted by EIP-8151.
	initial := NewGasBudget(10_000, 0)
	ret, _, err := runEIP8151(t, statedb, rules, eip8151Input, initial)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ret, common.LeftPadBytes(eip8151Recovered.Bytes(), 32)) {
		t.Fatalf("pre-adoption: ret = %x, want recovered address (delegation permitted)", ret)
	}
	// Adopt: call the account; the delegation resolves to the template…
	// instead, run delegated code that SETCODEFROMs the template. Install a
	// tiny delegating executor for that.
	executor := common.BytesToAddress([]byte("scf-exec"))
	execCode := scfCall(template) // SETCODEFROM(template); store flag; STOP
	statedb2 := newEIP8151State(t, rules, func(db *state.StateDB) {
		db.CreateAccount(eip8151Recovered)
		db.SetCode(eip8151Recovered, types.AddressToDelegation(executor), tracing.CodeChangeUnspecified)
		db.CreateAccount(executor)
		db.SetCode(executor, execCode, tracing.CodeChangeUnspecified)
		db.CreateAccount(template)
		db.SetCode(template, templateCode, tracing.CodeChangeUnspecified)
	})
	evm := eip8298EVM(statedb2, bogota8298Config())
	if _, _, err := evm.Call(scfCaller, eip8151Recovered, nil, hugeBudget(), new(uint256.Int)); err != nil {
		t.Fatal(err)
	}
	if got, want := statedb2.GetCodeHash(eip8151Recovered), crypto.Keccak256Hash(templateCode); got != want {
		t.Fatalf("adopted hash = %x, want %x", got, want)
	}
	// Post-adoption: EIP-8151 rejects the recovered account.
	initial = NewGasBudget(10_000, 0)
	ret, _, err = runEIP8151(t, statedb2, rules, eip8151Input, initial)
	if err != nil {
		t.Fatal(err)
	}
	if ret != nil {
		t.Fatalf("post-adoption: ret = %x, want nil (EIP-8151 rejection)", ret)
	}
}

// TestSetCodeFromCreateViaOpcode runs CREATE inside parent bytecode (so the
// parent-frame create dynamic gas, including the Amsterdam account-creation
// leaf charge, is exercised) and asserts that adoption removes exactly the
// code-deposit component while the account-creation cost is preserved.
func TestSetCodeFromCreateViaOpcode(t *testing.T) {
	// Template at a short address so the adopting initcode fits a word.
	template := common.BytesToAddress([]byte{0x99})
	templateCode := []byte{0x60, 0x2a, 0x60, 0x00, 0x55, 0x00}

	junkReturn := []byte{0x60, 0x42, 0x60, 0x00, 0x53, 0x60, 0x20, 0x60, 0x00, 0xf3} // MSTORE8(0,0x42); RETURN(0,32)
	adoptThenJunk := append([]byte{0x60, 0x99, 0xa7, 0x50}, junkReturn...)           // PUSH1 0x99; SETCODEFROM; POP; junk return

	// parent(init): MSTORE(0, init); CREATE(0, 32-len(init), len(init)); STOP
	parent := func(init []byte) []byte {
		code := []byte{byte(0x60 + len(init) - 1)} // PUSHn init
		code = append(code, init...)
		code = append(code,
			0x60, 0x00, 0x52, // MSTORE(0, init)
			0x60, byte(len(init)), // PUSH1 len
			0x60, byte(32-len(init)), // PUSH1 offset
			0x60, 0x00, // PUSH1 value
			0xf0, 0x00, // CREATE; STOP
		)
		return code
	}
	run := func(t *testing.T, init []byte) (GasBudget, common.Address) {
		statedb, err := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
		if err != nil {
			t.Fatal(err)
		}
		statedb.CreateAccount(scfSelf)
		statedb.SetCode(scfSelf, parent(init), tracing.CodeChangeUnspecified)
		statedb.CreateAccount(template)
		statedb.SetCode(template, templateCode, tracing.CodeChangeUnspecified)
		statedb.Finalise(true)
		cfg := bogota8298Config()
		rules := cfg.Rules(big.NewInt(0), true, 0)
		statedb.Prepare(rules, scfCaller, common.Address{}, nil, ActivePrecompiles(rules), nil)
		evm := eip8298EVM(statedb, cfg)
		_, res, err := evm.Call(scfCaller, scfSelf, nil, hugeBudget(), new(uint256.Int))
		if err != nil {
			t.Fatal(err)
		}
		created := crypto.CreateAddress(scfSelf, 0)
		return res, created
	}
	resPlain, addrPlain := run(t, junkReturn)
	resAdopt, addrAdopt := run(t, adoptThenJunk)

	deposit := int64(32 * params.CostPerStateByte)
	if delta := resPlain.UsedStateGas - resAdopt.UsedStateGas; delta != deposit {
		t.Fatalf("adoption must remove exactly the code deposit: delta = %d, want %d", delta, deposit)
	}
	// The adopted create still pays the full account-creation leaf charge.
	leaf := int64(uint64(params.AccountCreationSize) * params.CostPerStateByte)
	if resAdopt.UsedStateGas != leaf {
		t.Fatalf("adopted create state gas = %d, want %d (account-creation leaf preserved, deposit removed)", resAdopt.UsedStateGas, leaf)
	}
	if resPlain.UsedStateGas != leaf+deposit {
		t.Fatalf("plain create state gas = %d, want %d (leaf + deposit)", resPlain.UsedStateGas, leaf+deposit)
	}
	// The created account from the adopting initcode carries the template.
	statedbCheck := func() {}
	_ = statedbCheck
	_ = addrPlain
	statedb, err := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	if err != nil {
		t.Fatal(err)
	}
	statedb.CreateAccount(scfSelf)
	statedb.SetCode(scfSelf, parent(adoptThenJunk), tracing.CodeChangeUnspecified)
	statedb.CreateAccount(template)
	statedb.SetCode(template, templateCode, tracing.CodeChangeUnspecified)
	statedb.Finalise(true)
	cfg := bogota8298Config()
	rules := cfg.Rules(big.NewInt(0), true, 0)
	statedb.Prepare(rules, scfCaller, common.Address{}, nil, ActivePrecompiles(rules), nil)
	if _, _, err := eip8298EVM(statedb, cfg).Call(scfCaller, scfSelf, nil, hugeBudget(), new(uint256.Int)); err != nil {
		t.Fatal(err)
	}
	if got := statedb.GetCodeHash(addrAdopt); got != crypto.Keccak256Hash(templateCode) {
		t.Fatalf("opcode-created account hash = %x, want template", got)
	}
}

// --------------------------- BAL validation negatives ---------------------
// (implemented in core/types/bal/bal_eip8298_test.go — the encoding types are
// package-internal)
