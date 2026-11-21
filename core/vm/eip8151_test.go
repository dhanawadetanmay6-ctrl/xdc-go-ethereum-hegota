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

package vm

import (
	"bytes"
	"errors"
	"math/big"
	"reflect"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/types/bal"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

var (
	eip8151Precompile = common.BytesToAddress([]byte{0x01})
	eip8151Recovered  = common.HexToAddress("0xceaccac640adf55b2028469bd36ba501f28b699d")
	eip8151Input      = common.Hex2Bytes("38d18acb67d25c8bb9942764b62f18e17054f66a817bd4295423adf9ed98873e000000000000000000000000000000000000000000000000000000000000001b38d18acb67d25c8bb9942764b62f18e17054f66a817bd4295423adf9ed98873e789d1dd423d25f0772d2748d60f7e4b81bb14d086eba8e8e8efb6dcff8a4ae02")
)

func eip8151Config(bogota bool) *params.ChainConfig {
	cfg := *params.MergedTestChainConfig
	cfg.AmsterdamTime = new(uint64)
	if bogota {
		cfg.BogotaTime = new(uint64)
	}
	return &cfg
}

func eip8151Rules(bogota bool) params.Rules {
	return eip8151Config(bogota).Rules(big.NewInt(0), true, 0)
}

// newEIP8151State installs pre-state before starting the transaction access
// and BAL windows. This prevents fixture setup from appearing as EIP-7928
// transaction accesses or changes.
func newEIP8151State(t *testing.T, rules params.Rules, setup func(*state.StateDB)) *state.StateDB {
	t.Helper()
	statedb, err := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	if err != nil {
		t.Fatal(err)
	}
	if setup != nil {
		setup(statedb)
	}
	statedb.Finalise(true)
	statedb.Prepare(rules, common.Address{}, common.Address{}, nil, ActivePrecompiles(rules), nil)
	return statedb
}

func runEIP8151(t *testing.T, statedb StateDB, rules params.Rules, input []byte, gas GasBudget) ([]byte, GasBudget, error) {
	t.Helper()
	p := activePrecompiledContracts(rules)[eip8151Precompile]
	if p == nil {
		t.Fatal("ecRecover precompile missing")
	}
	return RunPrecompiledContract(statedb, p, eip8151Precompile, input, gas, nil, rules)
}

func assertEIP8151Read(t *testing.T, access *bal.ConstructionBlockAccessList, addr common.Address, want bool) {
	t.Helper()
	if access == nil {
		t.Fatal("expected an EIP-7928 construction access list")
	}
	account, ok := access.Accounts[addr]
	if !ok {
		if want {
			t.Fatalf("account %x missing from BAL", addr)
		}
		return
	}
	if !want {
		t.Fatalf("account %x unexpectedly present in BAL", addr)
	}
	if len(account.StorageWrites) != 0 || len(account.StorageReads) != 0 ||
		len(account.BalanceChanges) != 0 || len(account.NonceChanges) != 0 || len(account.CodeChange) != 0 {
		t.Fatalf("account %x has non-empty change lists: %+v", addr, account)
	}
}

func assertEIP8151Gas(t *testing.T, initial, remaining GasBudget, want uint64) {
	t.Helper()
	if remaining.UsedRegularGas != want {
		t.Fatalf("regular gas used = %d, want %d", remaining.UsedRegularGas, want)
	}
	if initial.RegularGas-remaining.RegularGas != want {
		t.Fatalf("regular gas delta = %d, want %d", initial.RegularGas-remaining.RegularGas, want)
	}
	if remaining.UsedStateGas != 0 || remaining.StateGas != initial.StateGas {
		t.Fatalf("unexpected state-gas use: initial=%v remaining=%v", initial, remaining)
	}
}

func TestEIP8151AccountCodeRestriction(t *testing.T) {
	delegate := common.HexToAddress("0x1000000000000000000000000000000000000001")
	delegation := types.AddressToDelegation(delegate)
	tests := []struct {
		name       string
		code       []byte
		existing   bool
		targetCode bool
		permitted  bool
	}{
		{name: "absent account", permitted: true},
		{name: "existing empty code", existing: true, permitted: true},
		{name: "delegation indicator", code: delegation, targetCode: true, permitted: true},
		{name: "arbitrary code", code: []byte{0x00}},
		{name: "wrong delegation prefix", code: append([]byte{0xef, 0x01, 0x01}, delegate.Bytes()...)},
		{name: "delegation too short", code: delegation[:22]},
		{name: "delegation too long", code: append(bytes.Clone(delegation), 0x00)},
		{name: "prefix without address", code: bytes.Clone(types.DelegationPrefix)},
	}
	wantAddress := common.LeftPadBytes(eip8151Recovered.Bytes(), 32)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rules := eip8151Rules(true)
			statedb := newEIP8151State(t, rules, func(db *state.StateDB) {
				if tc.existing || len(tc.code) != 0 {
					db.CreateAccount(eip8151Recovered)
				}
				if tc.existing && len(tc.code) == 0 {
					db.AddBalance(eip8151Recovered, uint256.NewInt(1), tracing.BalanceChangeUnspecified)
				}
				if len(tc.code) != 0 {
					db.SetCode(eip8151Recovered, tc.code, tracing.CodeChangeUnspecified)
				}
				if tc.targetCode {
					db.CreateAccount(delegate)
					db.SetCode(delegate, []byte{0xfe}, tracing.CodeChangeUnspecified)
				}
			})
			initial := NewGasBudget(10_000, 0)
			ret, remaining, err := runEIP8151(t, statedb, rules, eip8151Input, initial)
			if err != nil {
				t.Fatalf("ecRecover returned an execution error: %v", err)
			}
			if tc.permitted {
				if !bytes.Equal(ret, wantAddress) {
					t.Fatalf("ret = %x, want recovered address %x", ret, wantAddress)
				}
			} else if ret != nil {
				t.Fatalf("rejected code returned %x, want nil empty output", ret)
			}
			assertEIP8151Gas(t, initial, remaining, 6100)
			if !statedb.AddressInAccessList(eip8151Recovered) {
				t.Fatal("recovered account was not warmed after its paid code check")
			}
			access := statedb.Finalise(true)
			assertEIP8151Read(t, access, eip8151Recovered, true)
			assertEIP8151Read(t, access, delegate, false)
		})
	}
}

func TestEIP8151RecoveryFailure(t *testing.T) {
	badV := bytes.Clone(eip8151Input)
	badV[63] = 0
	zeroR := bytes.Clone(eip8151Input)
	clear(zeroR[64:96])
	zeroS := bytes.Clone(eip8151Input)
	clear(zeroS[96:128])
	tests := []struct {
		name  string
		input []byte
	}{
		{name: "empty input", input: nil},
		{name: "bad v", input: badV},
		{name: "zero r", input: zeroR},
		{name: "zero s", input: zeroS},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rules := eip8151Rules(true)
			statedb := newEIP8151State(t, rules, func(db *state.StateDB) {
				db.CreateAccount(eip8151Recovered)
				db.SetCode(eip8151Recovered, []byte{0xfe}, tracing.CodeChangeUnspecified)
			})
			initial := NewGasBudget(10_000, 0)
			ret, remaining, err := runEIP8151(t, statedb, rules, tc.input, initial)
			if err != nil {
				t.Fatalf("recovery failure returned an execution error: %v", err)
			}
			if ret != nil {
				t.Fatalf("recovery failure returned %x, want nil empty output", ret)
			}
			assertEIP8151Gas(t, initial, remaining, params.EcrecoverGas)
			if statedb.AddressInAccessList(eip8151Recovered) {
				t.Fatal("recovery failure warmed the recovered account")
			}
			assertEIP8151Read(t, statedb.Finalise(true), eip8151Recovered, false)
		})
	}
}

func TestEIP8151GasCostsAndOutOfGas(t *testing.T) {
	invalid := bytes.Clone(eip8151Input)
	invalid[63] = 0
	tests := []struct {
		name       string
		input      []byte
		gas        uint64
		warm       bool
		wantGas    uint64
		wantOutput bool
		wantOOG    bool
	}{
		{name: "cold", input: eip8151Input, gas: 10_000, wantGas: 6100, wantOutput: true},
		{name: "cold exact", input: eip8151Input, gas: 6100, wantGas: 6100, wantOutput: true},
		{name: "cold code check one less", input: eip8151Input, gas: 6099, wantGas: 3000, wantOOG: true},
		{name: "cold base only", input: eip8151Input, gas: 3000, wantGas: 3000, wantOOG: true},
		{name: "base one less", input: eip8151Input, gas: 2999, wantGas: 0, wantOOG: true},
		{name: "warm", input: eip8151Input, gas: 10_000, warm: true, wantGas: 3200, wantOutput: true},
		{name: "warm exact", input: eip8151Input, gas: 3200, warm: true, wantGas: 3200, wantOutput: true},
		{name: "warm code check one less", input: eip8151Input, gas: 3199, warm: true, wantGas: 3000, wantOOG: true},
		{name: "recovery failure base only", input: invalid, gas: 3000, wantGas: 3000},
	}
	wantAddress := common.LeftPadBytes(eip8151Recovered.Bytes(), 32)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rules := eip8151Rules(true)
			statedb := newEIP8151State(t, rules, nil)
			if tc.warm {
				statedb.AddAddressToAccessList(eip8151Recovered)
			}
			initial := NewGasBudget(tc.gas, 0)
			ret, remaining, err := runEIP8151(t, statedb, rules, tc.input, initial)
			if tc.wantOOG {
				if !errors.Is(err, ErrOutOfGas) {
					t.Fatalf("err = %v, want out of gas", err)
				}
			} else if err != nil {
				t.Fatalf("unexpected execution error: %v", err)
			}
			if tc.wantOutput && !bytes.Equal(ret, wantAddress) {
				t.Fatalf("ret = %x, want %x", ret, wantAddress)
			}
			if !tc.wantOutput && ret != nil {
				t.Fatalf("ret = %x, want nil empty output", ret)
			}
			assertEIP8151Gas(t, initial, remaining, tc.wantGas)
			wantRead := !tc.wantOOG && tc.wantOutput
			assertEIP8151Read(t, statedb.Finalise(true), eip8151Recovered, wantRead)
			if !tc.warm && statedb.AddressInAccessList(eip8151Recovered) != wantRead {
				t.Fatalf("cold-account warm status = %v, want %v", statedb.AddressInAccessList(eip8151Recovered), wantRead)
			}
		})
	}
}

func TestEIP8151RevertSemantics(t *testing.T) {
	t.Run("cold access restored and BAL retained", func(t *testing.T) {
		rules := eip8151Rules(true)
		statedb := newEIP8151State(t, rules, nil)
		snapshot := statedb.Snapshot()
		initial := NewGasBudget(10_000, 0)
		ret, remaining, err := runEIP8151(t, statedb, rules, eip8151Input, initial)
		if err != nil || len(ret) != 32 {
			t.Fatalf("first call: ret=%x err=%v", ret, err)
		}
		assertEIP8151Gas(t, initial, remaining, 6100)
		if !statedb.AddressInAccessList(eip8151Recovered) {
			t.Fatal("first call did not warm the recovered account")
		}
		statedb.RevertToSnapshot(snapshot)
		if statedb.AddressInAccessList(eip8151Recovered) {
			t.Fatal("revert did not restore the recovered account to cold")
		}
		// Inspect the first call's read before any subsequent call can re-add it.
		assertEIP8151Read(t, statedb.Finalise(true), eip8151Recovered, true)
		// An exact cold-gas second call proves the post-revert access is cold
		// again; a warm charge would leave 2,900 gas unused instead.
		initial = NewGasBudget(6100, 0)
		ret, remaining, err = runEIP8151(t, statedb, rules, eip8151Input, initial)
		if err != nil || len(ret) != 32 {
			t.Fatalf("post-revert call: ret=%x err=%v", ret, err)
		}
		assertEIP8151Gas(t, initial, remaining, 6100)
		assertEIP8151Read(t, statedb.Finalise(true), eip8151Recovered, true)
	})

	t.Run("pre-existing warm access survives revert", func(t *testing.T) {
		rules := eip8151Rules(true)
		statedb := newEIP8151State(t, rules, nil)
		statedb.AddAddressToAccessList(eip8151Recovered)
		snapshot := statedb.Snapshot()
		initial := NewGasBudget(3200, 0)
		ret, remaining, err := runEIP8151(t, statedb, rules, eip8151Input, initial)
		if err != nil || len(ret) != 32 {
			t.Fatalf("call: ret=%x err=%v", ret, err)
		}
		assertEIP8151Gas(t, initial, remaining, 3200)
		statedb.RevertToSnapshot(snapshot)
		if !statedb.AddressInAccessList(eip8151Recovered) {
			t.Fatal("revert removed an access that existed on scope entry")
		}
		assertEIP8151Read(t, statedb.Finalise(true), eip8151Recovered, true)
	})
}

// eip8151CallerCode stores a 128-byte ecRecover input, invokes the precompile
// with a fixed gas allowance, and returns two words: the STATICCALL success
// flag and RETURNDATASIZE. Return data is deliberately not copied so that the
// second word measures the precompile's actual output length.
func eip8151CallerCode(input []byte, gas uint64) []byte {
	input = common.RightPadBytes(input, 128)
	var code []byte
	for i := 0; i < 4; i++ {
		code = append(code, 0x7f) // PUSH32 input word
		code = append(code, input[i*32:(i+1)*32]...)
		code = append(code, 0x60, byte(i*32), 0x52) // PUSH1 offset; MSTORE
	}
	code = append(code,
		0x60, 0x00, // retSize
		0x60, 0x00, // retOffset
		0x60, 0x80, // argsSize = 128
		0x60, 0x00, // argsOffset
		0x73, // PUSH20 ecRecover
	)
	code = append(code, eip8151Precompile.Bytes()...)
	code = append(code, 0x61, byte(gas>>8), byte(gas), 0xfa) // PUSH2 gas; STATICCALL
	code = append(code, 0x60, 0x00, 0x52)                    // PUSH1 0; MSTORE(success)
	code = append(code, 0x3d, 0x60, 0x20, 0x52)              // RETURNDATASIZE; PUSH1 32; MSTORE
	return append(code, 0x60, 0x40, 0x60, 0x00, 0xf3)        // RETURN(0, 64)
}

func runEIP8151Caller(t *testing.T, input []byte, callGas uint64, reject bool) ([]byte, error) {
	t.Helper()
	caller := common.HexToAddress("0xca11e7")
	cfg := eip8151Config(true)
	rules := cfg.Rules(big.NewInt(0), true, 0)
	statedb, err := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	if err != nil {
		t.Fatal(err)
	}
	statedb.CreateAccount(caller)
	statedb.SetCode(caller, eip8151CallerCode(input, callGas), tracing.CodeChangeUnspecified)
	if reject {
		statedb.CreateAccount(eip8151Recovered)
		statedb.SetCode(eip8151Recovered, []byte{0xfe}, tracing.CodeChangeUnspecified)
	}
	statedb.Finalise(true)
	statedb.Prepare(rules, common.Address{}, common.Address{}, &caller, ActivePrecompiles(rules), nil)
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
	evm := NewEVM(ctx, statedb, cfg, Config{})
	ret, _, err := evm.Call(common.Address{}, caller, nil, NewGasBudget(1_000_000, 0), new(uint256.Int))
	return ret, err
}

func TestEIP8151EVMCallResults(t *testing.T) {
	invalid := bytes.Clone(eip8151Input)
	invalid[63] = 0
	tests := []struct {
		name        string
		input       []byte
		callGas     uint64
		reject      bool
		wantSuccess uint64
		wantSize    uint64
	}{
		{name: "permitted empty code", input: eip8151Input, callGas: 6100, wantSuccess: 1, wantSize: 32},
		{name: "rejected code is successful empty return", input: eip8151Input, callGas: 6100, reject: true, wantSuccess: 1, wantSize: 0},
		{name: "recovery failure is successful empty return", input: invalid, callGas: 3000, wantSuccess: 1, wantSize: 0},
		{name: "fixed legacy allowance runs out of gas", input: eip8151Input, callGas: 3000, wantSuccess: 0, wantSize: 0},
		{name: "cold code check one less runs out of gas", input: eip8151Input, callGas: 6099, wantSuccess: 0, wantSize: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ret, err := runEIP8151Caller(t, tc.input, tc.callGas, tc.reject)
			if err != nil {
				t.Fatalf("outer call failed: %v", err)
			}
			if len(ret) != 64 {
				t.Fatalf("outer return length = %d, want 64", len(ret))
			}
			success := new(big.Int).SetBytes(ret[:32]).Uint64()
			size := new(big.Int).SetBytes(ret[32:]).Uint64()
			if success != tc.wantSuccess || size != tc.wantSize {
				t.Fatalf("success/returndatasize = %d/%d, want %d/%d", success, size, tc.wantSuccess, tc.wantSize)
			}
		})
	}
}

func TestEIP8151ActivationAndPrecompileSet(t *testing.T) {
	preRules := eip8151Rules(false)
	if preRules.IsBogota || !preRules.IsAmsterdam {
		t.Fatalf("invalid pre-activation rules: %+v", preRules)
	}
	statedb := newEIP8151State(t, preRules, func(db *state.StateDB) {
		db.CreateAccount(eip8151Recovered)
		db.SetCode(eip8151Recovered, []byte{0xfe}, tracing.CodeChangeUnspecified)
	})
	initial := NewGasBudget(10_000, 0)
	ret, remaining, err := runEIP8151(t, statedb, preRules, eip8151Input, initial)
	if err != nil {
		t.Fatal(err)
	}
	wantAddress := common.LeftPadBytes(eip8151Recovered.Bytes(), 32)
	if !bytes.Equal(ret, wantAddress) {
		t.Fatalf("pre-Bogota ret = %x, want legacy result %x", ret, wantAddress)
	}
	assertEIP8151Gas(t, initial, remaining, params.EcrecoverGas)
	if statedb.AddressInAccessList(eip8151Recovered) {
		t.Fatal("pre-Bogota ecRecover unexpectedly warmed the recovered account")
	}
	assertEIP8151Read(t, statedb.Finalise(true), eip8151Recovered, false)

	if len(PrecompiledContractsBogota) != len(PrecompiledContractsOsaka) {
		t.Fatalf("Bogota/Osaka precompile counts = %d/%d", len(PrecompiledContractsBogota), len(PrecompiledContractsOsaka))
	}
	for addr, osaka := range PrecompiledContractsOsaka {
		bogota, ok := PrecompiledContractsBogota[addr]
		if !ok {
			t.Fatalf("Bogota is missing precompile %x", addr)
		}
		if addr == eip8151Precompile {
			if _, ok := bogota.(*ecrecover8151); !ok {
				t.Fatalf("Bogota ecRecover has type %T", bogota)
			}
			if _, ok := osaka.(*ecrecover); !ok {
				t.Fatalf("Osaka ecRecover changed type to %T", osaka)
			}
			continue
		}
		if !reflect.DeepEqual(bogota, osaka) {
			t.Fatalf("Bogota changed unrelated precompile %x: %T/%T", addr, bogota, osaka)
		}
	}
	bogotaAddresses := make(map[common.Address]struct{})
	for _, addr := range ActivePrecompiles(eip8151Rules(true)) {
		bogotaAddresses[addr] = struct{}{}
	}
	if len(bogotaAddresses) != len(PrecompiledContractsBogota) {
		t.Fatalf("Bogota active address count = %d, want %d", len(bogotaAddresses), len(PrecompiledContractsBogota))
	}
	for addr := range PrecompiledContractsBogota {
		if _, ok := bogotaAddresses[addr]; !ok {
			t.Fatalf("Bogota active addresses missing %x", addr)
		}
	}
}
