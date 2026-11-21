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

// Tests for the EIP-7906 TXTRACE (0xb6) and EVENTDATACOPY (0xb7) opcodes.
// The opcode-exercising ASSERT contracts are hand-assembled; each check block
// compares a TXTRACE result (or an EVENTDATACOPY-copied memory word) against
// an expected 32-byte word and REVERTs on mismatch, so a passing ASSERT frame
// proves every embedded expectation.

package core

import (
	"bytes"
	"errors"
	"math/big"
	"sort"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// --- tiny assertion-contract assembler ---

// asmBuf accumulates assertion-contract bytecode. Every check ends with a
// conditional jump to a shared REVERT block appended by finish(); jump
// destinations are patched at finish() time.
type asmBuf struct {
	code   []byte
	fixups []int
}

func (b *asmBuf) emit(bs ...byte) { b.code = append(b.code, bs...) }

// jumpOnFalse emits ISZERO, PUSH2 <fail>, JUMPI: jumps to fail when the stack
// top is zero.
func (b *asmBuf) jumpOnFalse() {
	b.emit(0x15, 0x61) // ISZERO, PUSH2
	b.fixups = append(b.fixups, len(b.code))
	b.emit(0x00, 0x00, 0x57) // placeholder dest, JUMPI
}

// traceCheck emits: TXTRACE(param, in2) == want, else revert. Stack order:
// in2 pushed first so that param is on top (TXTRACE takes param from the top).
func (b *asmBuf) traceCheck(param, in2 byte, want [32]byte) {
	b.emit(0x60, in2, 0x60, param, 0xb6, 0x7f) // PUSH1 in2, PUSH1 param, TXTRACE, PUSH32 want
	b.emit(want[:]...)
	b.emit(0x14) // EQ
	b.jumpOnFalse()
}

// mloadCheck emits: MLOAD(offset) == want, else revert.
func (b *asmBuf) mloadCheck(offset byte, want [32]byte) {
	b.emit(0x60, offset, 0x51, 0x7f) // PUSH1 offset, MLOAD, PUSH32 want
	b.emit(want[:]...)
	b.emit(0x14) // EQ
	b.jumpOnFalse()
}

// eventDataCopy emits EVENTDATACOPY(event_index=idx, memOffset=moff,
// dataOffset=doff, length=ln) — push order is length, dataOffset, memOffset,
// event_index so the stack top is the event index.
func (b *asmBuf) eventDataCopy(idx, moff, doff, ln byte) {
	b.emit(0x60, ln, 0x60, doff, 0x60, moff, 0x60, idx, 0xb7)
}

// finish appends the success STOP and the shared fail block, then patches all
// jump destinations.
func (b *asmBuf) finish() []byte {
	b.emit(0x00) // STOP: all checks passed
	fail := uint16(len(b.code))
	b.emit(0x5b, 0x60, 0x00, 0x60, 0x00, 0xfd) // JUMPDEST, PUSH1 0, PUSH1 0, REVERT
	for _, f := range b.fixups {
		b.code[f] = byte(fail >> 8)
		b.code[f+1] = byte(fail)
	}
	return b.code
}

func word64(v uint64) [32]byte         { return new(uint256.Int).SetUint64(v).Bytes32() }
func wordU256(v *uint256.Int) [32]byte { return v.Bytes32() }
func wordAddr(a common.Address) [32]byte {
	var w [32]byte
	copy(w[12:], a[:])
	return w
}

// --- fixtures ---

// setTwoSlotsCode sets slot 0 = 1 and slot 1 = 2.
var setTwoSlotsCode = []byte{0x60, 0x01, 0x60, 0x00, 0x55, 0x60, 0x02, 0x60, 0x01, 0x55, 0x00}

// logOne32Code emits one LOG1 event with 32 bytes of data.
func logOne32Code(topic, data common.Hash) []byte {
	var code []byte
	code = append(code, 0x7f)
	code = append(code, data[:]...)
	code = append(code, 0x60, 0x00, 0x52) // MSTORE(0, data)
	code = append(code, 0x7f)
	code = append(code, topic[:]...)
	code = append(code, 0x60, 0x20, 0x60, 0x00, 0xa1) // LOG1(offset=0, len=32, topic0)
	code = append(code, 0x00)                         // STOP
	return code
}

var (
	logTopic1 = common.HexToHash("0xaa01")
	logDataA  = common.HexToHash("0xdead0001")
	logDataB  = common.HexToHash("0xbeef0002")
)

// loggerTwoEventsCode emits:
//   - event 0: LOG1, topic0 = logTopic1, data = logDataA (32 bytes)
//   - event 1: LOG0, data = logDataA || logDataB (64 bytes)
func loggerTwoEventsCode() []byte {
	var code []byte
	code = append(code, logOne32Code(logTopic1, logDataA)...) // ends with STOP
	code = code[:len(code)-1]                                 // drop the STOP
	// mstore(32, logDataB); log0(offset=0, len=64)
	code = append(code, 0x7f)
	code = append(code, logDataB[:]...)
	code = append(code, 0x60, 0x20, 0x52)             // MSTORE(32, logDataB)
	code = append(code, 0x60, 0x40, 0x60, 0x00, 0xa0) // LOG0(offset=0, len=64)
	code = append(code, 0x00)                         // STOP
	return code
}

// factoryCode CREATEs a contract whose runtime is a single STOP byte. The
// 10-byte init code is MSTOREd (right-aligned in a 32-byte word) and returns
// one zero byte as the runtime.
//
//	69 600060005360016000 f3   PUSH10 <initcode> (10 bytes, inlined below)
//	60 00                      PUSH1 0
//	52                         MSTORE          (initcode lands at mem[22:32])
//	60 0a 60 16 60 00 f0       CREATE(value=0, offset=22, len=10)
//	50                         POP
//	00                         STOP
//
// initcode: MSTORE8(0, 0); RETURN(0, 1) — 60 00 60 00 53 60 01 60 00 f3.
var factoryCode = []byte{
	0x69, 0x60, 0x00, 0x60, 0x00, 0x53, 0x60, 0x01, 0x60, 0x00, 0xf3,
	0x60, 0x00,
	0x52,
	0x60, 0x0a, 0x60, 0x16, 0x60, 0x00, 0xf0,
	0x50,
	0x00,
}

// sortedAddrs returns the addresses in ascending uint160 order.
func sortedAddrs(addrs ...common.Address) []common.Address {
	out := append([]common.Address(nil), addrs...)
	sort.Slice(out, func(i, j int) bool { return bytes.Compare(out[i][:], out[j][:]) < 0 })
	return out
}

// --- TXTRACE tests ---

// TestTxTraceStateDiffTables walks every TXTRACE table against a transaction
// with a value transfer (which also emits an EIP-7708 transfer log) and a
// storage write, checking counts, entries, ordering, and the gas-payer params.
func TestTxTraceStateDiffTables(t *testing.T) {
	recv := common.HexToAddress("0xbeef")
	writer := common.HexToAddress("0xc7e1")
	checker := common.HexToAddress("0xc4ec")

	transfer := uint256.NewInt(1e15)
	type balChg struct {
		addr          common.Address
		before, after *uint256.Int
	}
	bals := []balChg{
		{ftSender, uint256.NewInt(1e18), new(uint256.Int).Sub(uint256.NewInt(1e18), transfer)},
		{recv, uint256.NewInt(0), transfer},
	}
	sort.Slice(bals, func(i, j int) bool { return bytes.Compare(bals[i].addr[:], bals[j].addr[:]) < 0 })

	b := &asmBuf{}
	b.traceCheck(0x00, 0, word64(2)) // BALANCES_CHANGED
	for i, c := range bals {
		b.traceCheck(0x04, byte(i), wordAddr(c.addr)) // BALANCE_ADDRESS
		b.traceCheck(0x05, byte(i), wordU256(c.before))
		b.traceCheck(0x06, byte(i), wordU256(c.after))
	}
	b.traceCheck(0x01, 0, word64(1))          // SLOTS_CHANGED
	b.traceCheck(0x07, 0, wordAddr(writer))   // SLOT_ADDRESS
	b.traceCheck(0x08, 0, word64(0))          // SLOT_BEFORE
	b.traceCheck(0x09, 0, word64(1))          // SLOT_AFTER
	b.traceCheck(0x0a, 0, word64(0))          // SLOT_KEY
	b.traceCheck(0x02, 0, word64(0))          // CODES_CHANGED
	b.traceCheck(0x03, 0, word64(1))          // NONCES_CHANGED
	b.traceCheck(0x0e, 0, wordAddr(ftSender)) // NONCE_ADDRESS
	b.traceCheck(0x0f, 0, word64(0))          // NONCE_BEFORE
	b.traceCheck(0x10, 0, word64(1))          // NONCE_AFTER
	b.traceCheck(0x11, 0, word64(1))          // EVENTS_COUNT (EIP-7708 transfer log)
	b.traceCheck(0x12, 0, wordAddr(params.SystemAddress))
	b.traceCheck(0x13, 0, word64(3)) // EVENT_TOPIC_COUNT
	b.traceCheck(0x14, 0, params.EthTransferLogEvent)
	b.traceCheck(0x15, 0, wordAddr(ftSender))
	b.traceCheck(0x16, 0, wordAddr(recv))
	b.traceCheck(0x18, 0, word64(32))         // EVENT_DATA_LEN
	b.traceCheck(0x19, 0, word64(0))          // GAS_PRE_CHARGE: zero in the NoBaseFee harness
	b.traceCheck(0x1a, 0, wordAddr(ftSender)) // GAS_PAYER
	code := b.finish()

	sdb := mkFrameState(t, types.GenesisAlloc{
		writer:  {Code: setSlotCode},
		checker: {Code: code},
	})
	tx := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
		addFrame(types.FrameModeSender, 0, &recv, 100_000, 200_000, transfer, nil).
		addFrame(types.FrameModeSender, 0, &writer, 200_000, 150_000, new(uint256.Int), nil).
		addFrame(types.FrameModeAssert, 0, &checker, 1_000_000, 0, new(uint256.Int), nil).
		build(t)

	res, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx)
	if err != nil {
		t.Fatalf("frame tx failed: %v", err)
	}
	if fr := res.FrameReceipts[3]; fr.Status != vm.FrameStatusSucceeded {
		t.Fatalf("ASSERT frame failed (an embedded TXTRACE expectation mismatched): status %d", fr.Status)
	}
}

// TestTxTraceNetChangeCollapse checks the collapsed-diff semantics: multiple
// writes to one slot produce one entry, a write restoring the original value
// drops the entry, and two transfers to the same recipient collapse into one
// balance entry.
func TestTxTraceNetChangeCollapse(t *testing.T) {
	w1 := common.HexToAddress("0xc7e1") // set then clear: net zero
	w2 := common.HexToAddress("0xc7e2") // two slots set: two entries
	recv := common.HexToAddress("0xbeef")
	checker := common.HexToAddress("0xc4ec")

	v := uint256.NewInt(100)
	v2 := new(uint256.Int).Add(v, v)
	senderAfter := new(uint256.Int).Sub(uint256.NewInt(1e18), v2)

	b := &asmBuf{}
	// Slots: only w2's two slots survive (w1's net change is zero).
	b.traceCheck(0x01, 0, word64(2)) // SLOTS_CHANGED
	b.traceCheck(0x07, 0, wordAddr(w2))
	b.traceCheck(0x0a, 0, word64(0)) // key 0
	b.traceCheck(0x09, 0, word64(1)) // after 1
	b.traceCheck(0x07, 1, wordAddr(w2))
	b.traceCheck(0x0a, 1, word64(1)) // key 1
	b.traceCheck(0x09, 1, word64(2)) // after 2
	// Balances: two collapsed entries.
	b.traceCheck(0x00, 0, word64(2))
	if bytes.Compare(ftSender[:], recv[:]) < 0 {
		b.traceCheck(0x04, 0, wordAddr(ftSender))
		b.traceCheck(0x06, 0, wordU256(senderAfter))
		b.traceCheck(0x04, 1, wordAddr(recv))
		b.traceCheck(0x06, 1, wordU256(v2))
	} else {
		b.traceCheck(0x04, 0, wordAddr(recv))
		b.traceCheck(0x06, 0, wordU256(v2))
		b.traceCheck(0x04, 1, wordAddr(ftSender))
		b.traceCheck(0x06, 1, wordU256(senderAfter))
	}
	code := b.finish()

	sdb := mkFrameState(t, types.GenesisAlloc{
		w1:      {Code: setOrClearCode},
		w2:      {Code: setTwoSlotsCode},
		checker: {Code: code},
	})
	tx := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
		addFrame(types.FrameModeSender, 0, &recv, 100_000, 200_000, v, nil).
		addFrame(types.FrameModeSender, 0, &recv, 100_000, 200_000, v, nil).                  // collapses into one entry
		addFrame(types.FrameModeSender, 0, &w1, 200_000, 150_000, new(uint256.Int), word(1)). // set slot 0
		addFrame(types.FrameModeSender, 0, &w1, 200_000, 50_000, new(uint256.Int), word(0)).  // clear slot 0: net zero
		addFrame(types.FrameModeSender, 0, &w2, 200_000, 300_000, new(uint256.Int), nil).
		addFrame(types.FrameModeAssert, 0, &checker, 1_000_000, 0, new(uint256.Int), nil).
		build(t)

	res, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx)
	if err != nil {
		t.Fatalf("frame tx failed: %v", err)
	}
	if fr := res.FrameReceipts[6]; fr.Status != vm.FrameStatusSucceeded {
		t.Fatalf("ASSERT frame failed: status %d", fr.Status)
	}
}

// TestTxTraceCodeNonceCreation covers the code and nonce tables for a
// contract created inside the transaction: codehash_before is the empty-code
// hash, and both the creator and the created contract appear in the nonce
// table alongside the sender.
func TestTxTraceCodeNonceCreation(t *testing.T) {
	factory := common.HexToAddress("0xfac7")
	checker := common.HexToAddress("0xc4ec")
	child := crypto.CreateAddress(factory, 0)
	childCodeHash := crypto.Keccak256Hash([]byte{0x00})

	addrs := sortedAddrs(ftSender, factory, child)
	nonceOf := func(a common.Address) (uint64, uint64) { // before, after
		return 0, 1
	}

	b := &asmBuf{}
	b.traceCheck(0x00, 0, word64(0)) // BALANCES_CHANGED: no value moved
	b.traceCheck(0x02, 0, word64(1)) // CODES_CHANGED
	b.traceCheck(0x0b, 0, wordAddr(child))
	b.traceCheck(0x0c, 0, types.EmptyCodeHash)
	b.traceCheck(0x0d, 0, childCodeHash)
	b.traceCheck(0x03, 0, word64(3)) // NONCES_CHANGED: sender, factory, child
	for i, a := range addrs {
		before, after := nonceOf(a)
		b.traceCheck(0x0e, byte(i), wordAddr(a))
		b.traceCheck(0x0f, byte(i), word64(before))
		b.traceCheck(0x10, byte(i), word64(after))
	}
	b.traceCheck(0x11, 0, word64(0)) // EVENTS_COUNT: no events
	code := b.finish()

	sdb := mkFrameState(t, types.GenesisAlloc{
		factory: {Code: factoryCode},
		checker: {Code: code},
	})
	tx := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
		addFrame(types.FrameModeSender, 0, &factory, 500_000, 400_000, new(uint256.Int), nil).
		addFrame(types.FrameModeAssert, 0, &checker, 1_000_000, 0, new(uint256.Int), nil).
		build(t)

	res, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx)
	if err != nil {
		t.Fatalf("frame tx failed: %v", err)
	}
	if fr := res.FrameReceipts[2]; fr.Status != vm.FrameStatusSucceeded {
		t.Fatalf("ASSERT frame failed: status %d", fr.Status)
	}
	if got := len(sdb.GetCode(child)); got != 1 {
		t.Fatalf("created contract code length %d, want 1", got)
	}
}

// TestTxTraceNestedSubcall proves TXTRACE is available at any depth of an
// ASSERT frame's call subtree, and halts in a SENDER frame's subtree.
func TestTxTraceNestedSubcall(t *testing.T) {
	callee := common.HexToAddress("0xb0bb")
	caller := common.HexToAddress("0xca11")
	// Callee: TXTRACE(BALANCES_CHANGED, 0), POP, STOP — succeeds iff the
	// opcode is legal in the current context.
	calleeCode := []byte{0x60, 0x00, 0x60, 0x00, 0xb6, 0x50, 0x00}
	// Caller: CALL(gas=0xffff, callee, 0, 0, 0, 0, 0); revert if it failed.
	b1 := &asmBuf{}
	b1.emit(0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x73)
	b1.emit(callee.Bytes()...)
	b1.emit(0x61, 0xff, 0xff, 0xf1) // CALL
	b1.jumpOnFalse()
	callerCode := b1.finish()

	sdb := mkFrameState(t, types.GenesisAlloc{
		callee: {Code: calleeCode},
		caller: {Code: callerCode},
	})

	// Inside an ASSERT frame: the nested TXTRACE is legal.
	tx := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
		addFrame(types.FrameModeAssert, 0, &caller, 300_000, 0, new(uint256.Int), nil).
		build(t)
	res, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx)
	if err != nil {
		t.Fatalf("frame tx failed: %v", err)
	}
	if fr := res.FrameReceipts[1]; fr.Status != vm.FrameStatusSucceeded {
		t.Fatalf("nested TXTRACE inside ASSERT subtree must succeed: status %d", fr.Status)
	}

	// Inside a SENDER frame: the nested TXTRACE halts the callee, the CALL
	// returns 0, the caller reverts — frame failure, transaction valid.
	sdb2 := mkFrameState(t, types.GenesisAlloc{
		callee: {Code: calleeCode},
		caller: {Code: callerCode},
	})
	tx2 := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
		addFrame(types.FrameModeSender, 0, &caller, 300_000, 0, new(uint256.Int), nil).
		build(t)
	res2, _, err := applyFrameTx(t, sdb2, frameEVM(sdb2, nil), tx2)
	if err != nil {
		t.Fatalf("tx should be valid despite frame revert: %v", err)
	}
	if fr := res2.FrameReceipts[1]; fr.Status != vm.FrameStatusFailed {
		t.Fatalf("SENDER frame with nested TXTRACE status %d, want failed", fr.Status)
	}
}

// TestTxTraceContextGates proves the ASSERT-only rule of EIP-7906: executing
// TXTRACE in a legacy/EIP-1559 transaction or in any non-ASSERT frame mode is
// an exceptional halt.
func TestTxTraceContextGates(t *testing.T) {
	probe := common.HexToAddress("0x9f0e")
	probeCode := []byte{0x60, 0x00, 0x60, 0x00, 0xb6, 0x00} // TXTRACE(0, 0), STOP

	// Legacy (EIP-1559) transaction.
	sdb := mkFrameState(t, types.GenesisAlloc{probe: {Code: probeCode}})
	normalTx := types.MustSignNewTx(ftKey, frameSigner, &types.DynamicFeeTx{
		ChainID: frameCfg.ChainID, Nonce: 0, To: &probe,
		Gas: 100_000, GasFeeCap: big.NewInt(0), GasTipCap: big.NewInt(0),
	})
	res, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), normalTx)
	if err != nil {
		t.Fatalf("normal tx processing failed: %v", err)
	}
	if res.Err == nil {
		t.Fatal("TXTRACE in a legacy transaction must halt")
	}

	// DEFAULT frame.
	sdb = mkFrameState(t, types.GenesisAlloc{probe: {Code: probeCode}})
	tx := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
		addFrame(types.FrameModeDefault, 0, &probe, 100_000, 0, new(uint256.Int), nil).
		build(t)
	res, _, err = applyFrameTx(t, sdb, frameEVM(sdb, nil), tx)
	if err != nil {
		t.Fatalf("tx should be valid despite frame halt: %v", err)
	}
	if fr := res.FrameReceipts[1]; fr.Status != vm.FrameStatusFailed {
		t.Fatalf("DEFAULT frame with TXTRACE status %d, want failed", fr.Status)
	}

	// SENDER frame.
	sdb = mkFrameState(t, types.GenesisAlloc{probe: {Code: probeCode}})
	tx = newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
		addFrame(types.FrameModeSender, 0, &probe, 100_000, 0, new(uint256.Int), nil).
		build(t)
	res, _, err = applyFrameTx(t, sdb, frameEVM(sdb, nil), tx)
	if err != nil {
		t.Fatalf("tx should be valid despite frame halt: %v", err)
	}
	if fr := res.FrameReceipts[1]; fr.Status != vm.FrameStatusFailed {
		t.Fatalf("SENDER frame with TXTRACE status %d, want failed", fr.Status)
	}

	// VERIFY frame: failure additionally invalidates the transaction.
	sdb = mkFrameState(t, types.GenesisAlloc{probe: {Code: probeCode}})
	tx = newFrameTx(0, ftSender).
		addFrame(types.FrameModeVerify, 0, &probe, 100_000, 0, new(uint256.Int), nil).
		build(t)
	if _, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx); !errors.Is(err, ErrFrameTxVerifyFailed) {
		t.Fatalf("err %v, want ErrFrameTxVerifyFailed", err)
	}
}

// TestTxTraceReservedAndRangeChecks covers the exceptional-halt rules:
// non-zero reserved index, undefined param (incl. non-uint64), out-of-range
// table index, non-uint64 index, and topic access beyond the topic count.
func TestTxTraceReservedAndRangeChecks(t *testing.T) {
	writer := common.HexToAddress("0xc7e1")
	logger := common.HexToAddress("0x1069")
	checker := common.HexToAddress("0xc4ec")

	bigWord := [32]byte{}
	for i := range bigWord {
		bigWord[i] = 0xff
	}

	cases := []struct {
		name    string
		code    []byte
		withLog bool
	}{
		// PUSH1 in2, PUSH1 param, TXTRACE, STOP shapes.
		{"reserved in2 nonzero", []byte{0x60, 0x01, 0x60, 0x00, 0xb6, 0x00}, false},
		{"reserved in2 nonzero payer", []byte{0x60, 0x01, 0x60, 0x1a, 0xb6, 0x00}, false},
		{"undefined param 0x1b", []byte{0x60, 0x00, 0x60, 0x1b, 0xb6, 0x00}, false},
		{"undefined param 0xff", []byte{0x60, 0x00, 0x60, 0xff, 0xb6, 0x00}, false},
		{"index into empty table", []byte{0x60, 0x00, 0x60, 0x04, 0xb6, 0x00}, false},
		{"topic on empty events", []byte{0x60, 0x00, 0x60, 0x14, 0xb6, 0x00}, false},
		{"topic beyond count", []byte{0x60, 0x00, 0x60, 0x14, 0xb6, 0x00}, true}, // LOG0: 0 topics
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			alloc := types.GenesisAlloc{
				writer:  {Code: setSlotCode},
				checker: {Code: tc.code},
			}
			if tc.withLog {
				alloc[logger] = types.Account{Code: emitLogCode}
			}
			sdb := mkFrameState(t, alloc)
			b := newFrameTx(0, ftSender).
				selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
				addFrame(types.FrameModeSender, 0, &writer, 200_000, 150_000, new(uint256.Int), nil)
			if tc.withLog {
				b.addFrame(types.FrameModeSender, 0, &logger, 100_000, 10_000, new(uint256.Int), nil)
			}
			tx := b.addFrame(types.FrameModeAssert, 0, &checker, 100_000, 0, new(uint256.Int), nil).build(t)

			res, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx)
			if err != nil {
				t.Fatalf("tx should be valid despite ASSERT halt: %v", err)
			}
			last := len(res.FrameReceipts) - 1
			if fr := res.FrameReceipts[last]; fr.Status != vm.FrameStatusFailed {
				t.Fatalf("ASSERT status %d, want failed (halt expected)", fr.Status)
			}
			if v := sdb.GetState(writer, common.Hash{}); v != (common.Hash{}) {
				t.Fatalf("body slot survived the ASSERT rollback: %v", v)
			}
		})
	}

	// Non-uint64 selector and index operands halt (checked as full 256-bit
	// values).
	t.Run("non-uint64 param", func(t *testing.T) {
		// PUSH1 0 (in2), PUSH32 0xff..ff (param), TXTRACE, STOP.
		code := append([]byte{0x60, 0x00, 0x7f}, bigWord[:]...)
		code = append(code, 0xb6, 0x00)
		assertHalt(t, writer, checker, code)
	})
	t.Run("non-uint64 index", func(t *testing.T) {
		// PUSH32 0xff..ff (in2), PUSH1 0x04 (param), TXTRACE, STOP.
		code := append([]byte{0x7f}, bigWord[:]...)
		code = append(code, 0x60, 0x04, 0xb6, 0x00)
		assertHalt(t, writer, checker, code)
	})
}

// assertHalt runs a [self_verify, SENDER->writer, ASSERT->checker(code)]
// transaction and requires the ASSERT frame to halt (status failed) with the
// body rolled back.
func assertHalt(t *testing.T, writer, checker common.Address, code []byte) {
	t.Helper()
	sdb := mkFrameState(t, types.GenesisAlloc{
		writer:  {Code: setSlotCode},
		checker: {Code: code},
	})
	tx := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
		addFrame(types.FrameModeSender, 0, &writer, 200_000, 150_000, new(uint256.Int), nil).
		addFrame(types.FrameModeAssert, 0, &checker, 100_000, 0, new(uint256.Int), nil).
		build(t)
	res, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx)
	if err != nil {
		t.Fatalf("tx should be valid despite ASSERT halt: %v", err)
	}
	last := len(res.FrameReceipts) - 1
	if fr := res.FrameReceipts[last]; fr.Status != vm.FrameStatusFailed {
		t.Fatalf("ASSERT status %d, want failed (halt expected)", fr.Status)
	}
	if v := sdb.GetState(writer, common.Hash{}); v != (common.Hash{}) {
		t.Fatalf("body slot survived the ASSERT rollback: %v", v)
	}
}

// TestTxTraceGasExact pins the TXTRACE gas price: one call costs the active
// fork's WARM_STORAGE_READ_COST (100 with EIP-8038), plus the intrinsic PUSH
// overhead and the frame-entry cold account access charge.
func TestTxTraceGasExact(t *testing.T) {
	checker := common.HexToAddress("0xc4ec")
	// PUSH1 0, PUSH1 0, TXTRACE, STOP.
	code := []byte{0x60, 0x00, 0x60, 0x00, 0xb6, 0x00}
	sdb := mkFrameState(t, types.GenesisAlloc{checker: {Code: code}})
	tx := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
		addFrame(types.FrameModeAssert, 0, &checker, 100_000, 0, new(uint256.Int), nil).
		build(t)

	res, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx)
	if err != nil {
		t.Fatalf("frame tx failed: %v", err)
	}
	fr := res.FrameReceipts[1]
	if fr.Status != vm.FrameStatusSucceeded {
		t.Fatalf("ASSERT status %d, want success", fr.Status)
	}
	// 3000 (cold account access) + 3 + 3 (PUSH1s) + 100 (TXTRACE) + 0 (STOP).
	want := uint64(3000 + 3 + 3 + 100)
	if fr.GasUsedExecution != want {
		t.Fatalf("ASSERT frame execution gas %d, want %d", fr.GasUsedExecution, want)
	}
}

// TestTxTraceGasPayerAndPreCharge verifies GAS_PRE_CHARGE (0x19) and
// GAS_PAYER (0x1a) against the EIP-8141 max_cost with real base fees.
func TestTxTraceGasPayerAndPreCharge(t *testing.T) {
	checker := common.HexToAddress("0xc4ec")
	baseFee := big.NewInt(1_000_000_000)
	tipPerGas := uint64(2_000_000_000)

	// Probe the max_gas of the transaction shape to derive max_cost. The
	// signature list must carry the real scheme: secp256k1 validation gas is
	// part of the intrinsic cost.
	probe := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
		addFrame(types.FrameModeAssert, 0, &checker, 100_000, 0, new(uint256.Int), nil)
	tmp := &types.FrameTx{Frames: probe.frames, Signatures: []types.FrameSignature{{Scheme: types.FrameSigSchemeSecp256k1, Signature: make([]byte, 65)}}, Fees: probe.fees, ChainID: probe.chainID}
	maxGas := tmp.MaxGas()
	// Generous max fee: max_priority_fee = tip * max_gas, so
	// max_cost = max_gas * (base_fee + tip).
	maxCost := new(uint256.Int).Mul(uint256.NewInt(maxGas), uint256.NewInt(baseFee.Uint64()+tipPerGas))

	b := &asmBuf{}
	b.traceCheck(0x19, 0, wordU256(maxCost))  // GAS_PRE_CHARGE
	b.traceCheck(0x1a, 0, wordAddr(ftSender)) // GAS_PAYER
	b.traceCheck(0x00, 0, word64(1))          // BALANCES_CHANGED: payer pre-charge
	b.traceCheck(0x04, 0, wordAddr(ftSender)) // the payer's entry
	b.traceCheck(0x06, 0, wordU256(new(uint256.Int).Sub(uint256.NewInt(1e18), maxCost)))
	code := b.finish()

	bb := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
		addFrame(types.FrameModeAssert, 0, &checker, 100_000, 0, new(uint256.Int), nil)
	bb.fees.MaxPriorityFeePerGas = uint256.NewInt(tipPerGas)
	bb.fees.MaxFee = new(uint256.Int).Mul(uint256.NewInt(maxGas), uint256.NewInt(baseFee.Uint64()+tipPerGas+1_000_000_000))

	sdb := mkFrameState(t, types.GenesisAlloc{checker: {Code: code}})
	tx := bb.build(t)
	res, _, err := applyFrameTx(t, sdb, frameEVM(sdb, baseFee), tx)
	if err != nil {
		t.Fatalf("frame tx failed: %v", err)
	}
	if fr := res.FrameReceipts[1]; fr.Status != vm.FrameStatusSucceeded {
		t.Fatalf("ASSERT frame failed: status %d", fr.Status)
	}
}

// --- EVENTDATACOPY tests ---

// TestEventDataCopyRead reads the event tables and copies event data into
// memory, verifying both words of a 64-byte payload and a mid-range slice.
func TestEventDataCopyRead(t *testing.T) {
	logger := common.HexToAddress("0x1069")
	checker := common.HexToAddress("0xc4ec")

	fullData := append(append([]byte(nil), logDataA[:]...), logDataB[:]...)
	var slice [32]byte
	copy(slice[:], fullData[10:42])

	b := &asmBuf{}
	b.traceCheck(0x11, 0, word64(2))        // EVENTS_COUNT
	b.traceCheck(0x12, 0, wordAddr(logger)) // event 0 address
	b.traceCheck(0x13, 0, word64(1))        // event 0 topic count
	b.traceCheck(0x14, 0, logTopic1)        // event 0 topic0
	b.traceCheck(0x18, 0, word64(32))       // event 0 data length
	b.traceCheck(0x12, 1, wordAddr(logger)) // event 1 address
	b.traceCheck(0x13, 1, word64(0))        // event 1 topic count
	b.traceCheck(0x18, 1, word64(64))       // event 1 data length
	b.eventDataCopy(1, 0, 0, 64)            // copy event 1 data to mem[0:64]
	b.mloadCheck(0, logDataA)               // first word
	b.mloadCheck(32, logDataB)              // second word
	b.eventDataCopy(1, 96, 10, 32)          // mid-range slice to mem[96:128]
	b.mloadCheck(96, slice)                 // MLOAD(96) reads bytes [96:128)
	b.eventDataCopy(1, 64, 64, 0)           // boundary: offset == length, zero length
	code := b.finish()

	sdb := mkFrameState(t, types.GenesisAlloc{
		logger:  {Code: loggerTwoEventsCode()},
		checker: {Code: code},
	})
	tx := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
		addFrame(types.FrameModeSender, 0, &logger, 200_000, 50_000, new(uint256.Int), nil).
		addFrame(types.FrameModeAssert, 0, &checker, 1_000_000, 0, new(uint256.Int), nil).
		build(t)

	res, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx)
	if err != nil {
		t.Fatalf("frame tx failed: %v", err)
	}
	if fr := res.FrameReceipts[2]; fr.Status != vm.FrameStatusSucceeded {
		t.Fatalf("ASSERT frame failed (event read mismatch): status %d", fr.Status)
	}
}

// TestEventDataCopyHalts covers the source-range exceptional halts:
// out-of-range event index, dataOffset+length beyond the data length
// (including the zero-length case), and 256-bit addition wraparound.
func TestEventDataCopyHalts(t *testing.T) {
	logger := common.HexToAddress("0x1069")
	checker := common.HexToAddress("0xc4ec")
	writer := common.HexToAddress("0xc7e1")

	// push order for EVENTDATACOPY: length, dataOffset, memOffset, event_index.
	cases := []struct {
		name string
		code []byte
	}{
		{"event index out of range", []byte{0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x60, 0x01, 0xb7, 0x00}},
		{"data end exceeded", []byte{0x60, 0x02, 0x60, 0x1f, 0x60, 0x00, 0x60, 0x00, 0xb7, 0x00}},    // len 2 at offset 31 > 32
		{"zero length past end", []byte{0x60, 0x00, 0x60, 0x21, 0x60, 0x00, 0x60, 0x00, 0xb7, 0x00}}, // len 0 at offset 33 > 32
		{"addition wraparound", []byte{0x60, 0x02, 0x7f,
			0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
			0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
			0x60, 0x00, 0x60, 0x00, 0xb7, 0x00}}, // len 2 + offset 2^256-1 wraps
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sdb := mkFrameState(t, types.GenesisAlloc{
				logger:  {Code: logOne32Code(logTopic1, logDataA)},
				writer:  {Code: setSlotCode},
				checker: {Code: tc.code},
			})
			tx := newFrameTx(0, ftSender).
				selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
				addFrame(types.FrameModeSender, 0, &writer, 200_000, 150_000, new(uint256.Int), nil).
				addFrame(types.FrameModeSender, 0, &logger, 100_000, 10_000, new(uint256.Int), nil).
				addFrame(types.FrameModeAssert, 0, &checker, 100_000, 0, new(uint256.Int), nil).
				build(t)

			res, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx)
			if err != nil {
				t.Fatalf("tx should be valid despite EVENTDATACOPY halt: %v", err)
			}
			last := len(res.FrameReceipts) - 1
			if fr := res.FrameReceipts[last]; fr.Status != vm.FrameStatusFailed {
				t.Fatalf("ASSERT status %d, want failed (halt expected)", fr.Status)
			}
			if v := sdb.GetState(writer, common.Hash{}); v != (common.Hash{}) {
				t.Fatalf("body slot survived the ASSERT rollback: %v", v)
			}
		})
	}
}

// TestEventDataCopyGas pins the CALLDATACOPY-matching gas schedule: fixed 3
// plus memory expansion and per-word copy cost.
func TestEventDataCopyGas(t *testing.T) {
	logger := common.HexToAddress("0x1069")
	checker := common.HexToAddress("0xc4ec")
	// EVENTDATACOPY(idx=0, mem=0, offset=0, len=32), STOP.
	code := []byte{0x60, 0x20, 0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0xb7, 0x00}
	sdb := mkFrameState(t, types.GenesisAlloc{
		logger:  {Code: logOne32Code(logTopic1, logDataA)},
		checker: {Code: code},
	})
	tx := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
		addFrame(types.FrameModeSender, 0, &logger, 100_000, 10_000, new(uint256.Int), nil).
		addFrame(types.FrameModeAssert, 0, &checker, 100_000, 0, new(uint256.Int), nil).
		build(t)

	res, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx)
	if err != nil {
		t.Fatalf("frame tx failed: %v", err)
	}
	fr := res.FrameReceipts[2]
	if fr.Status != vm.FrameStatusSucceeded {
		t.Fatalf("ASSERT status %d, want success", fr.Status)
	}
	// 3000 (cold account access) + 4*3 (PUSH1s) + 3 (fixed) + 3 (memory
	// expansion to one word) + 3 (one word copied) + 0 (STOP).
	want := uint64(3000 + 12 + 3 + 3 + 3)
	if fr.GasUsedExecution != want {
		t.Fatalf("ASSERT frame execution gas %d, want %d", fr.GasUsedExecution, want)
	}
}
