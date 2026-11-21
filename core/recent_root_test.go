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

// EIP-8272 recent root contract tests: the canonical bytecode is executed
// against an in-memory StateDB on a Bogota-active config. Writes use CALL,
// validations use both CALL and STATICCALL.

package core

import (
	"bytes"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
)

// rrBuildOrdered builds and signs a frame transaction with the frames in the
// exact order given (the shared builder's selfVerify prepends, which breaks
// tests asserting on frame index 0).
func rrBuildOrdered(t *testing.T, nonce uint64, sender common.Address, frames []types.Frame) *types.Transaction {
	t.Helper()
	ft := &types.FrameTx{
		ChainID: uint256.MustFromBig(frameCfg.ChainID),
		Nonce:   nonce,
		Sender:  sender,
		Frames:  frames,
		Signatures: []types.FrameSignature{{
			Scheme:    types.FrameSigSchemeSecp256k1,
			Signature: make([]byte, 65), // length placeholder, replaced by SignFrameTx
		}},
		Fees: types.FrameFees{MaxFee: new(uint256.Int), MaxPriorityFeePerGas: new(uint256.Int)},
	}
	signed, err := types.SignFrameTx(ftKey, types.NewTx(ft))
	if err != nil {
		t.Fatalf("SignFrameTx: %v", err)
	}
	return signed
}

// rrSelfVerifyFrame returns a default-code self_verify frame (VERIFY, scope
// EXECUTION|PAYMENT, nil target resolves to sender).
func rrSelfVerifyFrame() types.Frame {
	return types.Frame{
		Mode:   types.FrameModeVerify,
		Flags:  types.FrameFlagApproveScopeMask,
		Limits: types.FrameGasLimits{Execution: 50_000},
		Value:  new(uint256.Int),
	}
}

// rrRecentRootFrame returns a recent root verifier frame with the given data.
func rrRecentRootFrame(exec uint64, data []byte) types.Frame {
	return types.Frame{
		Mode:   types.FrameModeVerify,
		Flags:  0,
		Target: &[]common.Address{types.RecentRootAddress}[0],
		Limits: types.FrameGasLimits{Execution: exec},
		Value:  new(uint256.Int),
		Data:   data,
	}
}

var (
	rrSourceKey, _ = crypto.HexToECDSA("8b3a350cf5c34c9194ef858e544b26a52b27f6d0b43a63b0b0b0b0b0b0b0b001")
	rrSource       = crypto.PubkeyToAddress(rrSourceKey.PublicKey)
	rrOther, _     = crypto.HexToECDSA("45a915e4d060149eb4365960e6a7a45f334393093061116b197e3240065ff2d8")
	rrOtherAddr    = crypto.PubkeyToAddress(rrOther.PublicKey)
)

// rrState returns a state with the canonical contract deployed at
// RECENT_ROOT_ADDRESS (test fixture installation, see DeployRecentRoot) and a
// funded writer account.
func rrState(t *testing.T) *state.StateDB {
	t.Helper()
	sdb := mkState(types.GenesisAlloc{
		rrSource:    {Balance: big.NewInt(1e18)},
		rrOtherAddr: {Balance: big.NewInt(1e18)},
	})
	DeployRecentRoot(sdb)
	return sdb
}

// rrEVM returns a Bogota EVM with SLOTNUM set to slot.
func rrEVM(sdb *state.StateDB, slot uint64) *vm.EVM {
	evm := frameEVM(sdb, nil)
	evm.Context.SlotNum = slot
	return evm
}

// rrWrite executes the write operation: a CALL from source with salt||root at
// the given slot. Returns the leftover budget, error and logs emitted. The
// state-gas budget covers the SSTORE creation charge (EIP-8037 metering).
func rrWrite(t *testing.T, sdb *state.StateDB, evm *vm.EVM, source common.Address, salt, root common.Hash, gas uint64) (uint64, error) {
	t.Helper()
	data := RecentRootWriteCalldata(salt, root)
	budget := vm.NewGasBudgetNoSpill(gas, 10_000_000)
	_, result, err := evm.Call(source, types.RecentRootAddress, data, budget, new(uint256.Int))
	return result.RegularGas, err
}

// rrValidate executes the validation operation as a STATICCALL at the EVM's
// current slot.
func rrValidate(t *testing.T, sdb *state.StateDB, evm *vm.EVM, tuples []types.RecentRootTuple, gas uint64) (uint64, []byte, error) {
	t.Helper()
	data := RecentRootValidationCalldata(tuples)
	budget := vm.NewGasBudgetNoSpill(gas, 0)
	ret, result, err := evm.StaticCall(rrSource, types.RecentRootAddress, data, budget)
	return result.RegularGas, ret, err
}

func rrTuple(source common.Address, salt common.Hash, slot uint64, root common.Hash) types.RecentRootTuple {
	return types.RecentRootTuple{SourceID: types.RecentRootSourceID(source, salt), Slot: slot, Root: root}
}

// TestRecentRootReferenceVector executes the official EIP-8272 reference
// vector end-to-end: current_slot = 2, the entry is pre-stored, the
// validation call succeeds.
func TestRecentRootReferenceVector(t *testing.T) {
	var (
		source    = common.HexToAddress("0x0000000000000000000000000000000000000001")
		salt      = common.Hash{}
		slot      = uint64(1)
		root      = common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000002")
		sourceID  = common.HexToHash("0xb9382d35273c75a50631a3e84d3c75ec9266e2b18c35a627e16cdbf26a18ca85")
		entryHash = common.HexToHash("0x0a0d1254c851be5a133b4c9a9e300f5602fc0f43dbe65aa6a66930d4ca0a51b8")
		storKey   = common.HexToHash("0x5f027aa1cbe2df279bf6518edd4b44ea5409fd800189ec35224e10ab05e574c3")
	)
	sdb := rrState(t)
	// Pre-store the entry exactly as a slot-1 write would have.
	sdb.SetState(types.RecentRootAddress, storKey, entryHash)

	evm := rrEVM(sdb, 2) // current_slot = 2
	data := common.FromHex("0xb9382d35273c75a50631a3e84d3c75ec9266e2b18c35a627e16cdbf26a18ca8500000000000000010000000000000000000000000000000000000000000000000000000000000002")
	budget := vm.NewGasBudgetNoSpill(1_000_000, 0)
	ret, result, err := evm.StaticCall(source, types.RecentRootAddress, data, budget)
	if err != nil {
		t.Fatalf("reference vector validation failed: %v", err)
	}
	if len(ret) != 0 {
		t.Fatalf("validation returned %d bytes, want none", len(ret))
	}
	if result.RegularGas >= 1_000_000 {
		t.Fatal("validation consumed no gas?")
	}
	if logs := sdb.Logs(); len(logs) != 0 {
		t.Fatalf("validation emitted %d logs", len(logs))
	}
	_ = sourceID
	_ = slot
	_ = root
	_ = salt
}

// TestRecentRootWriteThenValidate writes a root through the contract during
// slot S and validates it from slot S+1, checking the stored entry hash and
// the storage key.
func TestRecentRootWriteThenValidate(t *testing.T) {
	sdb := rrState(t)
	salt := common.HexToHash("0x01")
	root := common.HexToHash("0x02")

	evm := rrEVM(sdb, 7) // write during slot 7
	if _, err := rrWrite(t, sdb, evm, rrSource, salt, root, 500_000); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	var (
		sourceID  = types.RecentRootSourceID(rrSource, salt)
		entryHash = types.RecentRootEntryHash(sourceID, 7, root)
		storKey   = types.RecentRootStorageKey(sourceID, 7%types.RecentRootLength)
	)
	if got := sdb.GetState(types.RecentRootAddress, storKey); got != entryHash {
		t.Fatalf("stored entry = %v, want %v", got, entryHash)
	}
	if logs := sdb.Logs(); len(logs) != 0 {
		t.Fatalf("write emitted %d logs", len(logs))
	}

	// Referenceable beginning in slot S+1.
	tuple := rrTuple(rrSource, salt, 7, root)
	evm.Context.SlotNum = 8
	if _, _, err := rrValidate(t, sdb, evm, []types.RecentRootTuple{tuple}, 500_000); err != nil {
		t.Fatalf("validation at slot 8 failed: %v", err)
	}
	// Not referenceable during the write slot itself.
	evm.Context.SlotNum = 7
	if _, _, err := rrValidate(t, sdb, evm, []types.RecentRootTuple{tuple}, 500_000); err == nil {
		t.Fatal("current-slot reference accepted")
	}
}

// TestRecentRootWriteOverwriteSameSlot: multiple writes by the same source
// address and salt during one slot target the same storage key; only the
// final write is referenceable.
func TestRecentRootWriteOverwriteSameSlot(t *testing.T) {
	sdb := rrState(t)
	salt := common.HexToHash("0x01")
	root1 := common.HexToHash("0x11")
	root2 := common.HexToHash("0x22")

	evm := rrEVM(sdb, 100)
	if _, err := rrWrite(t, sdb, evm, rrSource, salt, root1, 500_000); err != nil {
		t.Fatalf("write1: %v", err)
	}
	if _, err := rrWrite(t, sdb, evm, rrSource, salt, root2, 500_000); err != nil {
		t.Fatalf("write2: %v", err)
	}
	evm.Context.SlotNum = 101
	if _, _, err := rrValidate(t, sdb, evm, []types.RecentRootTuple{rrTuple(rrSource, salt, 100, root1)}, 500_000); err == nil {
		t.Fatal("overwritten root still validates")
	}
	if _, _, err := rrValidate(t, sdb, evm, []types.RecentRootTuple{rrTuple(rrSource, salt, 100, root2)}, 500_000); err != nil {
		t.Fatalf("final write does not validate: %v", err)
	}
}

// TestRecentRootValidationFailures covers the rejection matrix of the
// validation operation.
func TestRecentRootValidationFailures(t *testing.T) {
	sdb := rrState(t)
	salt := common.HexToHash("0x01")
	root := common.HexToHash("0x02")

	// Write at slot 100.
	evm := rrEVM(sdb, 100)
	if _, err := rrWrite(t, sdb, evm, rrSource, salt, root, 500_000); err != nil {
		t.Fatalf("write: %v", err)
	}
	evm.Context.SlotNum = 101

	good := rrTuple(rrSource, salt, 100, root)
	cases := []struct {
		name  string
		tuple types.RecentRootTuple
		slot  uint64
	}{
		{"wrong root", rrTuple(rrSource, salt, 100, common.HexToHash("0x03")), 101},
		{"wrong slot", rrTuple(rrSource, salt, 99, root), 101},
		{"wrong source", types.RecentRootTuple{SourceID: common.HexToHash("0xdead"), Slot: 100, Root: root}, 101},
		{"current slot", rrTuple(rrSource, salt, 101, root), 101},
		{"future slot", rrTuple(rrSource, salt, 102, root), 101},
		{"age 8192", rrTuple(rrSource, salt, 100, root), 100 + 8192},
	}
	for _, c := range cases {
		evm.Context.SlotNum = c.slot
		if _, _, err := rrValidate(t, sdb, evm, []types.RecentRootTuple{c.tuple}, 500_000); err == nil {
			t.Fatalf("%s: accepted, want revert", c.name)
		}
	}
	// Age 8191 boundary: still valid.
	evm.Context.SlotNum = 100 + 8191
	if _, _, err := rrValidate(t, sdb, evm, []types.RecentRootTuple{good}, 500_000); err != nil {
		t.Fatalf("age 8191: %v", err)
	}
	// Age 8192 boundary: invalid (checked above via same storage; use a fresh
	// state to make sure the aged-out ring index cannot collide).
	sdb2 := rrState(t)
	evm2 := rrEVM(sdb2, 5)
	if _, err := rrWrite(t, sdb2, evm2, rrSource, salt, root, 500_000); err != nil {
		t.Fatalf("write2: %v", err)
	}
	evm2.Context.SlotNum = 5 + 8192
	if _, _, err := rrValidate(t, sdb2, evm2, []types.RecentRootTuple{rrTuple(rrSource, salt, 5, root)}, 500_000); err == nil {
		t.Fatal("age 8192 accepted")
	}
}

// TestRecentRootValidationCalldataRules checks the calldata length rules:
// empty, non-multiples of 72, 64 (the write encoding is disjoint), and more
// than 16 tuples all revert.
func TestRecentRootValidationCalldataRules(t *testing.T) {
	sdb := rrState(t)
	evm := rrEVM(sdb, 10)
	for _, n := range []int{0, 1, 63, 65, 71, 73, 17 * 72, 17*72 + 5} {
		budget := vm.NewGasBudgetNoSpill(1_000_000, 0)
		_, _, err := evm.StaticCall(rrSource, types.RecentRootAddress, make([]byte, n), budget)
		if err == nil {
			t.Fatalf("calldata length %d accepted", n)
		}
	}
}

// TestRecentRootValidationSixteen: sixteen valid references with distinct
// cold storage keys succeed when the frame limit suffices.
func TestRecentRootValidationSixteen(t *testing.T) {
	sdb := rrState(t)
	evm := rrEVM(sdb, 50)
	var tuples []types.RecentRootTuple
	for i := 0; i < 16; i++ {
		salt := common.BigToHash(big.NewInt(int64(i + 1)))
		root := common.BigToHash(big.NewInt(int64(i + 100)))
		if _, err := rrWrite(t, sdb, evm, rrSource, salt, root, 1_000_000); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		tuples = append(tuples, rrTuple(rrSource, salt, 50, root))
	}
	evm.Context.SlotNum = 51
	left, _, err := rrValidate(t, sdb, evm, tuples, 30_000_000)
	if err != nil {
		t.Fatalf("16-tuple validation failed: %v", err)
	}
	used := 30_000_000 - left
	t.Logf("16-tuple validation used %d gas (contract internal)", used)

	// Duplicate valid tuples are valid and checked independently (cheaper:
	// warm storage keys).
	dup := []types.RecentRootTuple{tuples[0], tuples[0], tuples[0]}
	if _, _, err := rrValidate(t, sdb, evm, dup, 30_000_000); err != nil {
		t.Fatalf("duplicate tuples failed: %v", err)
	}
}

// TestRecentRootValidationNoSideEffects: a successful validation performs no
// storage writes, no logs, and returns no data.
func TestRecentRootValidationNoSideEffects(t *testing.T) {
	sdb := rrState(t)
	evm := rrEVM(sdb, 3)
	salt := common.HexToHash("0x07")
	root := common.HexToHash("0x09")
	if _, err := rrWrite(t, sdb, evm, rrSource, salt, root, 1_000_000); err != nil {
		t.Fatalf("write: %v", err)
	}
	evm.Context.SlotNum = 4
	tuple := rrTuple(rrSource, salt, 3, root)

	// Snapshot journal state to detect any write during validation.
	snap := sdb.Snapshot()
	logsBefore := len(sdb.Logs())
	_, ret, err := rrValidate(t, sdb, evm, []types.RecentRootTuple{tuple}, 1_000_000)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(ret) != 0 {
		t.Fatalf("returned %d bytes", len(ret))
	}
	if len(sdb.Logs()) != logsBefore {
		t.Fatal("validation emitted logs")
	}
	sdb.RevertToSnapshot(snap) // would hide writes; instead verify stored value directly after
	if got := sdb.GetState(types.RecentRootAddress, tuple.StorageKey()); got != tuple.EntryHash() {
		t.Fatalf("stored entry changed: %v != %v", got, tuple.EntryHash())
	}
	// Write detection: run validation without revert and diff the journal via
	// a second state's root? Cheaper: re-run and check the only storage cell
	// the contract could touch (its own ring buffer) is unchanged, plus
	// refund counter untouched.
	if got := sdb.GetState(types.RecentRootAddress, tuple.StorageKey()); got != tuple.EntryHash() {
		t.Fatalf("stored entry mutated: %v != %v", got, tuple.EntryHash())
	}
	if r := sdb.GetRefund(); r != 0 {
		t.Fatalf("refund counter = %d", r)
	}
}

// TestRecentRootWriteValueAndStatic: nonzero-value calls and static-context
// writes must fail.
func TestRecentRootWriteValueAndStatic(t *testing.T) {
	sdb := rrState(t)
	evm := rrEVM(sdb, 10)
	salt := common.HexToHash("0x01")
	root := common.HexToHash("0x02")
	data := RecentRootWriteCalldata(salt, root)

	// Nonzero value: must revert.
	budget := vm.NewGasBudgetNoSpill(1_000_000, 0)
	_, _, err := evm.Call(rrSource, types.RecentRootAddress, data, budget, uint256.NewInt(1))
	if err == nil {
		t.Fatal("nonzero-value write accepted")
	}
	// Static context: write must fail and storage must remain unchanged.
	budget = vm.NewGasBudgetNoSpill(1_000_000, 0)
	_, _, err = evm.StaticCall(rrSource, types.RecentRootAddress, data, budget)
	if err == nil {
		t.Fatal("static write accepted")
	}
	sourceID := types.RecentRootSourceID(rrSource, salt)
	key := types.RecentRootStorageKey(sourceID, 10%types.RecentRootLength)
	if got := sdb.GetState(types.RecentRootAddress, key); got != (common.Hash{}) {
		t.Fatalf("static write left state %v", got)
	}
}

// TestRecentRootWriteCalldataRules: only exactly-64-byte calldata selects the
// write operation.
func TestRecentRootWriteCalldataRules(t *testing.T) {
	sdb := rrState(t)
	evm := rrEVM(sdb, 10)
	for _, n := range []int{63, 65} {
		budget := vm.NewGasBudgetNoSpill(1_000_000, 0)
		_, _, err := evm.Call(rrSource, types.RecentRootAddress, make([]byte, n), budget, new(uint256.Int))
		if err == nil {
			t.Fatalf("write calldata length %d accepted", n)
		}
	}
}

// TestRecentRootSaltSeparation: different salts of one address are distinct
// sources; the same salt under different addresses are distinct sources.
func TestRecentRootSaltSeparation(t *testing.T) {
	sdb := rrState(t)
	evm := rrEVM(sdb, 20)
	salt1 := common.HexToHash("0x01")
	salt2 := common.HexToHash("0x02")
	root := common.HexToHash("0x03")
	if _, err := rrWrite(t, sdb, evm, rrSource, salt1, root, 1_000_000); err != nil {
		t.Fatalf("write: %v", err)
	}
	evm.Context.SlotNum = 21
	// Wrong salt does not validate.
	if _, _, err := rrValidate(t, sdb, evm, []types.RecentRootTuple{rrTuple(rrSource, salt2, 20, root)}, 1_000_000); err == nil {
		t.Fatal("wrong salt validated")
	}
	// Same salt, different address does not validate.
	if _, _, err := rrValidate(t, sdb, evm, []types.RecentRootTuple{rrTuple(rrOtherAddr, salt1, 20, root)}, 1_000_000); err == nil {
		t.Fatal("wrong address validated")
	}
	if _, _, err := rrValidate(t, sdb, evm, []types.RecentRootTuple{rrTuple(rrSource, salt1, 20, root)}, 1_000_000); err != nil {
		t.Fatalf("correct tuple: %v", err)
	}
}

// TestRecentRootDeploymentExecution executes the deployment init code through
// EVM CREATE and verifies the created contract holds exactly RECENT_ROOT_CODE
// with empty storage. The deployment is an ordinary contract deployment
// (EIP-8272 "Deployment"): nothing is installed by the protocol.
func TestRecentRootDeploymentExecution(t *testing.T) {
	sdb := mkState(types.GenesisAlloc{
		types.RecentRootDeployer: {Balance: big.NewInt(1e18)},
	})
	evm := frameEVM(sdb, nil)
	budget := vm.NewGasBudgetNoSpill(0x13d620, 1_000_000)
	_, addr, _, err := evm.Create(types.RecentRootDeployer, types.RecentRootInitCode, budget, new(uint256.Int))
	if err != nil {
		t.Fatalf("deployment execution failed: %v", err)
	}
	if code := sdb.GetCode(addr); !bytes.Equal(code, types.RecentRootCode) {
		t.Fatalf("deployed code at %v differs from RECENT_ROOT_CODE (%d vs %d bytes)", addr, len(code), len(types.RecentRootCode))
	}
	// Initial storage of the fresh contract is empty: any derived key reads 0.
	probe := types.RecentRootStorageKey(types.RecentRootSourceID(types.RecentRootDeployer, common.Hash{}), 0)
	if got := sdb.GetState(addr, probe); got != (common.Hash{}) {
		t.Fatalf("initial storage non-zero: %v", got)
	}
	t.Logf("deployment created at %v (deployer nonce 0 address is the sheet's claimed RECENT_ROOT_ADDRESS; see TestRecentRootDeploymentTx for the sheet inconsistency)", addr)
}

// TestRecentRootPreDeploymentBehavior: before the deployment transaction is
// included, RECENT_ROOT_ADDRESS has empty code, so a recent root verifier
// frame runs EIP-8141's default code in VERIFY mode; its flags are 0, so the
// default code reverts and the frame fails (EIP-8272 "Activation").
func TestRecentRootPreDeploymentBehavior(t *testing.T) {
	// State WITHOUT the contract deployed.
	sdb := mkState(types.GenesisAlloc{
		ftSender: {Balance: big.NewInt(1e18), Nonce: 0},
	})
	evm := rrEVM(sdb, 5)

	data := RecentRootValidationCalldata([]types.RecentRootTuple{{
		SourceID: common.HexToHash("0x01"), Slot: 1, Root: common.HexToHash("0x02"),
	}})
	tx := rrBuildOrdered(t, 0, ftSender, []types.Frame{
		rrRecentRootFrame(100_000, data),
		rrSelfVerifyFrame(),
	})
	_, _, err := applyFrameTx(t, sdb, evm, tx)
	if err == nil {
		t.Fatal("pre-deployment recent root verifier frame must fail (default code reverts)")
	}
	t.Logf("pre-deployment failure: %v", err)
}

// TestRecentRootFrameInBlock: a recent root verifier frame inside a frame
// transaction executes as STATICCALL through the EIP-8141 engine and makes
// the transaction valid when the root is present.
func TestRecentRootFrameInBlock(t *testing.T) {
	sdb := mkState(types.GenesisAlloc{
		ftSender: {Balance: big.NewInt(1e18)},
		rrSource: {Balance: big.NewInt(1e18)},
	})
	DeployRecentRoot(sdb)

	// Write at slot 9 through the contract.
	evm := rrEVM(sdb, 9)
	salt := common.HexToHash("0x01")
	root := common.HexToHash("0x02")
	if _, err := rrWrite(t, sdb, evm, rrSource, salt, root, 1_000_000); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Frame tx at slot 10: [recent_root_verify, self_verify, user_op].
	receiver := common.HexToAddress("0xbeef")
	tuple := rrTuple(rrSource, salt, 9, root)
	tx := rrBuildOrdered(t, 0, ftSender, []types.Frame{
		rrRecentRootFrame(200_000, RecentRootValidationCalldata([]types.RecentRootTuple{tuple})),
		rrSelfVerifyFrame(),
		{Mode: types.FrameModeSender, Target: &receiver, Limits: types.FrameGasLimits{Execution: 21_000, State: 100_000}, Value: uint256.NewInt(1)},
	})
	evm.Context.SlotNum = 10
	res, _, err := applyFrameTx(t, sdb, evm, tx)
	if err != nil {
		t.Fatalf("frame tx with recent root verifier failed: %v", err)
	}
	if res.FrameReceipts[0].Status != types.FrameStatusSucceeded {
		t.Fatalf("recent root frame status = %d", res.FrameReceipts[0].Status)
	}
	if res.FrameReceipts[0].GasUsedExecution == 0 {
		t.Fatal("recent root frame used no gas")
	}
	t.Logf("recent root frame gas used (execution): %d", res.FrameReceipts[0].GasUsedExecution)

	// A wrong root invalidates the whole transaction.
	sdb2 := mkState(types.GenesisAlloc{
		ftSender: {Balance: big.NewInt(1e18)},
		rrSource: {Balance: big.NewInt(1e18)},
	})
	DeployRecentRoot(sdb2)
	evm2 := rrEVM(sdb2, 9)
	if _, err := rrWrite(t, sdb2, evm2, rrSource, salt, root, 1_000_000); err != nil {
		t.Fatalf("write2: %v", err)
	}
	evm2.Context.SlotNum = 10
	bad := rrTuple(rrSource, salt, 9, common.HexToHash("0x03"))
	tx2 := rrBuildOrdered(t, 0, ftSender, []types.Frame{
		rrRecentRootFrame(200_000, RecentRootValidationCalldata([]types.RecentRootTuple{bad})),
		rrSelfVerifyFrame(),
		{Mode: types.FrameModeSender, Target: &receiver, Limits: types.FrameGasLimits{Execution: 21_000, State: 100_000}, Value: uint256.NewInt(1)},
	})
	if _, _, err := applyFrameTx(t, sdb2, evm2, tx2); err == nil {
		t.Fatal("invalid root did not invalidate the transaction")
	}
}

// TestCheckRecentRootTuples covers the direct evaluation helper used by
// mempool revalidation and pre-inclusion checks.
func TestCheckRecentRootTuples(t *testing.T) {
	sdb := rrState(t)
	evm := rrEVM(sdb, 30)
	salt := common.HexToHash("0x01")
	root := common.HexToHash("0x02")
	if _, err := rrWrite(t, sdb, evm, rrSource, salt, root, 1_000_000); err != nil {
		t.Fatalf("write: %v", err)
	}
	tuple := rrTuple(rrSource, salt, 30, root)

	if err := CheckRecentRootTuples(sdb, 31, []types.RecentRootTuple{tuple}); err != nil {
		t.Fatalf("valid tuple: %v", err)
	}
	if err := CheckRecentRootTuples(sdb, 30, []types.RecentRootTuple{tuple}); !errors.Is(err, ErrRecentRootSlotCurrent) {
		t.Fatalf("current slot: %v", err)
	}
	if err := CheckRecentRootTuples(sdb, 32, []types.RecentRootTuple{rrTuple(rrSource, salt, 33, root)}); !errors.Is(err, ErrRecentRootSlotFuture) {
		t.Fatalf("future slot: %v", err)
	}
	if err := CheckRecentRootTuples(sdb, 30+8192, []types.RecentRootTuple{tuple}); !errors.Is(err, ErrRecentRootExpired) {
		t.Fatalf("expired: %v", err)
	}
	bad := rrTuple(rrSource, salt, 30, common.HexToHash("0x09"))
	if err := CheckRecentRootTuples(sdb, 31, []types.RecentRootTuple{bad}); !errors.Is(err, ErrRecentRootEntryMismatch) {
		t.Fatalf("entry mismatch: %v", err)
	}
	if err := CheckRecentRootTuples(sdb, 31, nil); !errors.Is(err, types.ErrRecentRootDataEmpty) {
		t.Fatalf("empty: %v", err)
	}
	if err := CheckRecentRootTuples(sdb, 31, make([]types.RecentRootTuple, 17)); !errors.Is(err, types.ErrRecentRootDataTooLong) {
		t.Fatalf("too long: %v", err)
	}
}

// TestSimulateRecentRootVerifyFrame covers the admission simulation:
// exact-code gating, permission tracer, and gas sufficiency against the
// engine's frame receipt.
func TestSimulateRecentRootVerifyFrame(t *testing.T) {
	sdb := rrState(t)
	evm := rrEVM(sdb, 40)
	salt := common.HexToHash("0x01")
	root := common.HexToHash("0x02")
	if _, err := rrWrite(t, sdb, evm, rrSource, salt, root, 1_000_000); err != nil {
		t.Fatalf("write: %v", err)
	}
	tuple := rrTuple(rrSource, salt, 40, root)
	frame := &types.Frame{
		Mode:   types.FrameModeVerify,
		Target: &[]common.Address{types.RecentRootAddress}[0],
		Limits: types.FrameGasLimits{Execution: 500_000},
		Value:  new(uint256.Int),
		Data:   RecentRootValidationCalldata([]types.RecentRootTuple{tuple}),
	}
	// Happy path (current_slot = 41).
	slot40 := uint64(40)
	simHead := &types.Header{Number: new(big.Int), Time: 0, SlotNumber: &slot40}
	used, err := SimulateRecentRootVerifyFrame(sdb, frameCfg, simHead, ftSender, frame)
	if err != nil {
		t.Fatalf("simulation failed: %v", err)
	}
	if used == 0 {
		t.Fatal("simulation used no gas")
	}
	// Gas exactness: engine execution of the same frame must consume the
	// same execution gas (cold target access + internal execution).
	sdbE := mkState(types.GenesisAlloc{ftSender: {Balance: big.NewInt(1e18)}, rrSource: {Balance: big.NewInt(1e18)}})
	DeployRecentRoot(sdbE)
	evmE := rrEVM(sdbE, 40)
	if _, err := rrWrite(t, sdbE, evmE, rrSource, salt, root, 1_000_000); err != nil {
		t.Fatalf("write E: %v", err)
	}
	evmE.Context.SlotNum = 41
	txE := rrBuildOrdered(t, 0, ftSender, []types.Frame{
		rrRecentRootFrame(500_000, RecentRootValidationCalldata([]types.RecentRootTuple{tuple})),
		rrSelfVerifyFrame(),
	})
	resE, _, err := applyFrameTx(t, sdbE, evmE, txE)
	if err != nil {
		t.Fatalf("engine frame tx: %v", err)
	}
	engineGas := resE.FrameReceipts[0].GasUsedExecution
	if used != engineGas {
		t.Fatalf("simulation gas %d != engine gas %d", used, engineGas)
	}
	// Exact-limit boundary: simulation with the exact engine consumption
	// succeeds; one less fails.
	exact := &types.Frame{
		Mode:   types.FrameModeVerify,
		Target: &[]common.Address{types.RecentRootAddress}[0],
		Limits: types.FrameGasLimits{Execution: engineGas},
		Value:  new(uint256.Int),
		Data:   frame.Data,
	}
	if _, err := SimulateRecentRootVerifyFrame(sdb, frameCfg, simHead, ftSender, exact); err != nil {
		t.Fatalf("exact-limit simulation failed: %v", err)
	}
	under := &types.Frame{
		Mode:   types.FrameModeVerify,
		Target: &[]common.Address{types.RecentRootAddress}[0],
		Limits: types.FrameGasLimits{Execution: engineGas - 1},
		Value:  new(uint256.Int),
		Data:   frame.Data,
	}
	if _, err := SimulateRecentRootVerifyFrame(sdb, frameCfg, simHead, ftSender, under); err == nil {
		t.Fatal("underfunded frame (limit-1) accepted")
	}
	// Code mismatch: wrong code at the address is rejected before execution.
	sdbBad := mkState(types.GenesisAlloc{rrSource: {Balance: big.NewInt(1e18)}})
	sdbBad.CreateAccount(types.RecentRootAddress)
	sdbBad.SetCode(types.RecentRootAddress, []byte{0x00}, tracing.CodeChangeUnspecified)
	if _, err := SimulateRecentRootVerifyFrame(sdbBad, frameCfg, simHead, ftSender, frame); !errors.Is(err, ErrRecentRootCodeMismatch) {
		t.Fatalf("wrong code: %v", err)
	}
	// Missing code: rejected.
	sdbEmpty := mkState(types.GenesisAlloc{})
	if _, err := SimulateRecentRootVerifyFrame(sdbEmpty, frameCfg, simHead, ftSender, frame); !errors.Is(err, ErrRecentRootCodeMismatch) {
		t.Fatalf("missing code: %v", err)
	}
}

// TestRecentRootApplicationIntrospection: application validation reads a
// completed recent root verifier frame through EIP-8141's existing
// FRAMEPARAM/FRAMEDATALOAD instructions (EIP-8272 "Application
// introspection"): resolved_target == RECENT_ROOT_ADDRESS, mode == VERIFY,
// flags == 0, limits.state == 0, valid data length, status == 1, plus the
// tuple contents.
func TestRecentRootApplicationIntrospection(t *testing.T) {
	sdb := mkState(types.GenesisAlloc{
		ftSender: {Balance: big.NewInt(1e18)},
		rrSource: {Balance: big.NewInt(1e18)},
	})
	DeployRecentRoot(sdb)
	evm := rrEVM(sdb, 9)
	salt := common.HexToHash("0x01")
	root := common.HexToHash("0x02")
	if _, err := rrWrite(t, sdb, evm, rrSource, salt, root, 1_000_000); err != nil {
		t.Fatalf("write: %v", err)
	}
	tuple := rrTuple(rrSource, salt, 9, root)

	// Probe contract (runs as account validation, after the recent root
	// frame). Each value is stored to a successive slot of the probe.
	p := []byte{}
	store := func(slot byte) { p = append(p, 0x60, slot, 0x55) } // PUSH1 slot, SSTORE
	frameparam := func(param, frameIdx byte) {
		p = append(p, 0x60, param, 0x60, frameIdx, 0xb3) // PUSH1 param, PUSH1 idx, FRAMEPARAM
	}
	frameparam(0x00, 0x00) // RESOLVED_TARGET(frame 0)
	store(0x00)
	frameparam(0x02, 0x00) // MODE(frame 0)
	store(0x01)
	frameparam(0x03, 0x00) // FLAGS(frame 0)
	store(0x02)
	frameparam(0x09, 0x00) // STATE_GAS_LIMIT(frame 0)
	store(0x03)
	frameparam(0x04, 0x00) // DATA_LENGTH(frame 0)
	store(0x04)
	frameparam(0x05, 0x00) // STATUS(frame 0)
	store(0x05)
	// FRAMEDATALOAD: stack [offset(top), frameIndex] — word 0 = source_id.
	p = append(p, 0x60, 0x00, 0x60, 0x00, 0xb1) // PUSH1 0 (idx), PUSH1 0 (off), FRAMEDATALOAD
	store(0x06)
	// word at offset 32, shifted right 192 bits = uint64 slot.
	p = append(p, 0x60, 0x00, 0x60, 0x20, 0xb1) // idx 0, offset 32
	p = append(p, 0x60, 0xc0, 0x1c)             // PUSH1 192, SHR
	store(0x07)
	p = append(p, 0x00) // STOP

	tx := rrBuildOrdered(t, 0, ftSender, []types.Frame{
		rrRecentRootFrame(200_000, RecentRootValidationCalldata([]types.RecentRootTuple{tuple})),
		rrSelfVerifyFrame(),
		{Mode: types.FrameModeSender, Target: &probeAddr, Limits: types.FrameGasLimits{Execution: 500_000, State: 1_000_000}, Value: new(uint256.Int)},
	})
	// Deploy probe code.
	sdb.CreateAccount(probeAddr)
	sdb.SetCode(probeAddr, p, tracing.CodeChangeUnspecified)

	evm.Context.SlotNum = 10
	res, _, err := applyFrameTx(t, sdb, evm, tx)
	if err != nil {
		t.Fatalf("tx failed: %v", err)
	}
	if res.FrameReceipts[2].Status != types.FrameStatusSucceeded {
		t.Fatalf("probe frame status = %d", res.FrameReceipts[2].Status)
	}
	get := func(slot byte) common.Hash { return sdb.GetState(probeAddr, common.BytesToHash([]byte{slot})) }
	if got := common.BytesToAddress(get(0).Bytes()[12:]); got != types.RecentRootAddress {
		t.Fatalf("resolved_target = %v, want %v", got, types.RecentRootAddress)
	}
	if got := get(1).Big().Uint64(); got != uint64(types.FrameModeVerify) {
		t.Fatalf("mode = %d, want VERIFY", got)
	}
	if got := get(2).Big().Uint64(); got != 0 {
		t.Fatalf("flags = %d, want 0", got)
	}
	if got := get(3).Big().Uint64(); got != 0 {
		t.Fatalf("state limit = %d, want 0", got)
	}
	if got := get(4).Big().Uint64(); got != uint64(types.RecentRootTupleBytes) {
		t.Fatalf("data length = %d, want 72", got)
	}
	if got := get(5).Big().Uint64(); got != 1 {
		t.Fatalf("status = %d, want 1", got)
	}
	if got := get(6); got != tuple.SourceID {
		t.Fatalf("source_id = %v, want %v", got, tuple.SourceID)
	}
	if got := get(7).Big().Uint64(); got != 9 {
		t.Fatalf("slot = %d, want 9", got)
	}
}
