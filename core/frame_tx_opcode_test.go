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

// Tests for the EIP-8141 frame opcodes (APPROVE + introspection set).

package core

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/holiman/uint256"
)

var probeAddr = common.HexToAddress("0x9e0b")

// runProbe executes [self_verify(scope 3), SENDER probe, <extra frames...>]
// with the probe contract deployed with the given code, and returns the
// execution result and post-state.
func runProbe(t *testing.T, probeCode []byte, extraData [][]byte) (*ExecutionResult, *state.StateDB) {
	t.Helper()
	alloc := types.GenesisAlloc{probeAddr: {Code: probeCode}}
	sdb := mkFrameState(t, alloc)
	b := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
		addFrame(types.FrameModeSender, 0, &probeAddr, 500_000, 500_000, new(uint256.Int), nil)
	for _, d := range extraData {
		b.addFrame(types.FrameModeSender, 0, &ftSender, 100_000, 100_000, new(uint256.Int), d)
	}
	tx := b.build(t)
	res, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx)
	if err != nil {
		t.Fatalf("frame tx failed: %v", err)
	}
	return res, sdb
}

// probeResult returns the probe frame's receipt status and the value stored
// in slot 0 of the probe contract.
func probeResult(t *testing.T, res *ExecutionResult, sdb *state.StateDB) (uint8, common.Hash) {
	t.Helper()
	fr := res.FrameReceipts[1]
	return fr.Status, sdb.GetState(probeAddr, common.Hash{})
}

// storeSlot0 program suffix: PUSH1 0, SSTORE, STOP after a value is on stack.
var storeSlot0 = []byte{0x60, 0x00, 0x55, 0x00}

func TestOpcodeTxParamType(t *testing.T) {
	// PUSH1 0x00, TXPARAM, store -> 6 (FRAME_TX_TYPE)
	code := append([]byte{0x60, 0x00, 0xb0}, storeSlot0...)
	res, sdb := runProbe(t, code, nil)
	status, slot := probeResult(t, res, sdb)
	if status != vm.FrameStatusSucceeded {
		t.Fatal("probe frame failed")
	}
	if slot.Big().Cmp(big.NewInt(6)) != 0 {
		t.Fatalf("TXPARAM_TYPE = %v, want 6", slot)
	}
}

func TestOpcodeTxParamBasics(t *testing.T) {
	// PUSH1 0x09 (FRAME_COUNT), TXPARAM, store
	code := append([]byte{0x60, 0x09, 0xb0}, storeSlot0...)
	res, sdb := runProbe(t, code, [][]byte{word(0x77), word(0x88)})
	status, slot := probeResult(t, res, sdb)
	if status != vm.FrameStatusSucceeded {
		t.Fatal("probe frame failed")
	}
	if slot.Big().Cmp(big.NewInt(4)) != 0 { // verify + probe + 2 extra
		t.Fatalf("TXPARAM_FRAME_COUNT = %v, want 4", slot)
	}

	// PUSH1 0x0a (CURRENT_FRAME_INDEX) -> 1 (probe is frame 1)
	code = append([]byte{0x60, 0x0a, 0xb0}, storeSlot0...)
	res, sdb = runProbe(t, code, nil)
	_, slot = probeResult(t, res, sdb)
	if slot.Big().Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("TXPARAM_CURRENT_FRAME_INDEX = %v, want 1", slot)
	}

	// PUSH1 0x02 (SENDER) -> sender address
	code = append([]byte{0x60, 0x02, 0xb0}, storeSlot0...)
	res, sdb = runProbe(t, code, nil)
	_, slot = probeResult(t, res, sdb)
	if common.BytesToAddress(slot.Bytes()[12:]) != ftSender {
		t.Fatalf("TXPARAM_SENDER = %v, want %v", slot, ftSender)
	}

	// PUSH1 0x0c (STATE_GAS_LEFT): probe frame starts with the full state
	// pool (500000); the opcode itself charges no state gas.
	code = append([]byte{0x60, 0x0c, 0xb0}, storeSlot0...)
	res, sdb = runProbe(t, code, nil)
	_, slot = probeResult(t, res, sdb)
	if slot.Big().Cmp(big.NewInt(500_000)) != 0 {
		t.Fatalf("TXPARAM_STATE_GAS_LEFT = %v, want 500000", slot)
	}

	// PUSH1 0x0b (SIGNATURE_COUNT) -> 1
	code = append([]byte{0x60, 0x0b, 0xb0}, storeSlot0...)
	res, sdb = runProbe(t, code, nil)
	_, slot = probeResult(t, res, sdb)
	if slot.Big().Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("TXPARAM_SIGNATURE_COUNT = %v, want 1", slot)
	}
}

func TestOpcodeTxParamUndefinedHalts(t *testing.T) {
	// PUSH1 0x05 (undefined), TXPARAM -> halt
	code := append([]byte{0x60, 0x05, 0xb0}, storeSlot0...)
	res, sdb := runProbe(t, code, nil)
	status, slot := probeResult(t, res, sdb)
	if status != vm.FrameStatusFailed {
		t.Fatalf("undefined TXPARAM must halt; status %d", status)
	}
	if slot != (common.Hash{}) {
		t.Fatal("halted frame must not store")
	}
}

func TestOpcodeFrameParam(t *testing.T) {
	// FRAMEPARAM stack: [frameIndex(top), param]. PUSH param first, then idx.
	// param 0x02 (MODE) of frame 1 (the probe frame itself, SENDER=1):
	code := append([]byte{0x60, 0x02, 0x60, 0x01, 0xb3}, storeSlot0...)
	res, sdb := runProbe(t, code, nil)
	status, slot := probeResult(t, res, sdb)
	if status != vm.FrameStatusSucceeded {
		t.Fatal("probe frame failed")
	}
	if slot.Big().Cmp(big.NewInt(int64(types.FrameModeSender))) != 0 {
		t.Fatalf("FRAMEPARAM_MODE = %v, want %d", slot, types.FrameModeSender)
	}

	// param 0x00 (RESOLVED_TARGET) of frame 0 (verify frame, target nil -> sender):
	code = append([]byte{0x60, 0x00, 0x60, 0x00, 0xb3}, storeSlot0...)
	res, sdb = runProbe(t, code, nil)
	_, slot = probeResult(t, res, sdb)
	if common.BytesToAddress(slot.Bytes()[12:]) != ftSender {
		t.Fatalf("FRAMEPARAM_RESOLVED_TARGET = %v, want sender %v", slot, ftSender)
	}

	// param 0x05 (STATUS) of frame 0 (completed verify) -> 1:
	code = append([]byte{0x60, 0x05, 0x60, 0x00, 0xb3}, storeSlot0...)
	res, sdb = runProbe(t, code, nil)
	_, slot = probeResult(t, res, sdb)
	if slot.Big().Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("FRAMEPARAM_STATUS(frame0) = %v, want 1", slot)
	}

	// param 0x06 (ALLOWED_SCOPE) of frame 0 -> 3:
	code = append([]byte{0x60, 0x06, 0x60, 0x00, 0xb3}, storeSlot0...)
	res, sdb = runProbe(t, code, nil)
	_, slot = probeResult(t, res, sdb)
	if slot.Big().Cmp(big.NewInt(3)) != 0 {
		t.Fatalf("FRAMEPARAM_ALLOWED_SCOPE = %v, want 3", slot)
	}
}

func TestOpcodeFrameParamStatusCurrentFrameHalts(t *testing.T) {
	// STATUS of the currently executing frame (idx 1) -> exceptional halt.
	code := append([]byte{0x60, 0x05, 0x60, 0x01, 0xb3}, storeSlot0...)
	res, sdb := runProbe(t, code, nil)
	status, _ := probeResult(t, res, sdb)
	if status != vm.FrameStatusFailed {
		t.Fatalf("STATUS of current frame must halt; status %d", status)
	}
	// Exceptional halt consumes the whole execution pool.
	if res.FrameReceipts[1].GasUsedExecution != 500_000 {
		t.Fatalf("halt gas used %d, want full 500000", res.FrameReceipts[1].GasUsedExecution)
	}
}

func TestOpcodeFrameDataLoad(t *testing.T) {
	// FRAMEDATALOAD stack: [offset(top), frameIndex]. Load word 0 of frame 2.
	code := append([]byte{0x60, 0x02, 0x60, 0x00, 0xb1}, storeSlot0...)
	res, sdb := runProbe(t, code, [][]byte{word(0xab)})
	status, slot := probeResult(t, res, sdb)
	if status != vm.FrameStatusSucceeded {
		t.Fatal("probe frame failed")
	}
	if slot != common.BytesToHash(word(0xab)) {
		t.Fatalf("FRAMEDATALOAD = %v, want word(0xab)", slot)
	}
}

func TestOpcodeFrameDataLoadOOB(t *testing.T) {
	// frameIndex 9 out of bounds -> halt
	code := append([]byte{0x60, 0x09, 0x60, 0x00, 0xb1}, storeSlot0...)
	res, sdb := runProbe(t, code, nil)
	status, _ := probeResult(t, res, sdb)
	if status != vm.FrameStatusFailed {
		t.Fatalf("OOB FRAMEDATALOAD must halt; status %d", status)
	}
}

func TestOpcodeFrameDataCopy(t *testing.T) {
	// FRAMEDATACOPY stack: [memOffset(top), dataOffset, length, frameIndex].
	// Copy 32 bytes of frame 2 data to mem[0], MLOAD, store.
	// pushes: frameIdx, length=32, dataOffset=0, memOffset=0
	code := append([]byte{
		0x60, 0x02, // PUSH1 2 (frameIndex)
		0x60, 0x20, // PUSH1 32 (length)
		0x60, 0x00, // PUSH1 0 (dataOffset)
		0x60, 0x00, // PUSH1 0 (memOffset)
		0xb2,       // FRAMEDATACOPY
		0x60, 0x00, 0x51, // PUSH1 0, MLOAD
	}, storeSlot0...)
	res, sdb := runProbe(t, code, [][]byte{word(0xcd)})
	status, slot := probeResult(t, res, sdb)
	if status != vm.FrameStatusSucceeded {
		t.Fatal("probe frame failed")
	}
	if slot != common.BytesToHash(word(0xcd)) {
		t.Fatalf("FRAMEDATACOPY+MLOAD = %v, want word(0xcd)", slot)
	}
}

func TestOpcodeSigParam(t *testing.T) {
	// SIGPARAM stack: [signatureIndex(top), param].
	// param 0x01 (SCHEME) of sig 0 -> 1 (SECP256K1 per EIP-8141 scheme table):
	code := append([]byte{0x60, 0x01, 0x60, 0x00, 0xb4}, storeSlot0...)
	res, sdb := runProbe(t, code, nil)
	status, slot := probeResult(t, res, sdb)
	if status != vm.FrameStatusSucceeded {
		t.Fatal("probe frame failed")
	}
	if slot.Big().Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("SIGPARAM_SCHEME = %v, want 1 (SECP256K1)", slot)
	}

	// param 0x00 (RESOLVED_SIGNER) of sig 0 -> sender:
	code = append([]byte{0x60, 0x00, 0x60, 0x00, 0xb4}, storeSlot0...)
	res, sdb = runProbe(t, code, nil)
	_, slot = probeResult(t, res, sdb)
	if common.BytesToAddress(slot.Bytes()[12:]) != ftSender {
		t.Fatalf("SIGPARAM_RESOLVED_SIGNER = %v, want sender", slot)
	}

	// param 0x03 (SIGNATURE_LENGTH) on a protocol-validated scheme -> halt:
	code = append([]byte{0x60, 0x03, 0x60, 0x00, 0xb4}, storeSlot0...)
	res, _ = runProbe(t, code, nil)
	if res.FrameReceipts[1].Status != vm.FrameStatusFailed {
		t.Fatal("SIGNATURE_LENGTH on secp256k1 entry must halt")
	}
}

func TestOpcodeSigDataCopyArbitrary(t *testing.T) {
	// Build a tx with an ARBITRARY signature entry at index 1 and copy it.
	arb := []byte{0xde, 0xad, 0xbe, 0xef}
	alloc := types.GenesisAlloc{probeAddr: {Code: nil}}
	sdb := mkFrameState(t, alloc)
	// SIGDATACOPY stack: [memOffset(top), dataOffset, length, sigIndex]
	// copy 4 bytes of sig 1 to mem[0], MLOAD, store.
	code := []byte{
		0x60, 0x01, // PUSH1 1 (sigIndex)
		0x60, 0x04, // PUSH1 4 (length)
		0x60, 0x00, // PUSH1 0 (dataOffset)
		0x60, 0x00, // PUSH1 0 (memOffset)
		0xb5,       // SIGDATACOPY
		0x60, 0x00, 0x51, // MLOAD(0)
	}
	code = append(code, storeSlot0...)
	sdb.SetCode(probeAddr, code, tracing.CodeChangeUnspecified)

	b := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
		addFrame(types.FrameModeSender, 0, &probeAddr, 500_000, 500_000, new(uint256.Int), nil)
	b.sigs = append(b.sigs, types.FrameSignature{Scheme: types.FrameSigSchemeArbitrary, Signature: arb})
	tx := b.build(t)
	if _, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx); err != nil {
		t.Fatalf("frame tx failed: %v", err)
	}
	got := sdb.GetState(probeAddr, common.Hash{})
	want := common.BytesToHash(append(arb, make([]byte, 28)...))
	if got != want {
		t.Fatalf("SIGDATACOPY = %v, want %v", got, want)
	}
}

// TestOpcodeSigDataCopyProtocolSchemeHalts: SIGDATACOPY on a secp256k1 entry.
func TestOpcodeSigDataCopyProtocolSchemeHalts(t *testing.T) {
	code := []byte{
		0x60, 0x00, // sigIndex 0 (the secp256k1 entry)
		0x60, 0x04, 0x60, 0x00, 0x60, 0x00, 0xb5,
		0x60, 0x00, 0x51,
	}
	code = append(code, storeSlot0...)
	res, _ := runProbe(t, code, nil)
	if res.FrameReceipts[1].Status != vm.FrameStatusFailed {
		t.Fatal("SIGDATACOPY on protocol-validated scheme must halt")
	}
}

// TestOpcodeIntrospectionOutsideFrameTx: every introspection opcode halts in
// a legacy transaction context.
func TestOpcodeIntrospectionOutsideFrameTx(t *testing.T) {
	for _, op := range []byte{0xb0, 0xb1, 0xb2, 0xb3, 0xb4, 0xb5} {
		// push enough stack items then execute the opcode
		code := []byte{0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x60, 0x00, op, 0x00}
		sdb := mkFrameState(t, types.GenesisAlloc{probeAddr: {Code: code}})
		tx := types.MustSignNewTx(ftKey, frameSigner, &types.DynamicFeeTx{
			ChainID: frameCfg.ChainID, Nonce: 0, To: &probeAddr,
			Gas: 100_000, GasFeeCap: big.NewInt(0), GasTipCap: big.NewInt(0),
		})
		res, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx)
		if err != nil {
			t.Fatalf("legacy tx failed to process: %v", err)
		}
		if res.Err == nil {
			t.Fatalf("opcode %#x outside frame tx must halt", op)
		}
	}
}
