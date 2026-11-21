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

// EIP-7906 M4 tests: interaction with the EIP-7928 block-level access list on
// ASSERT failure, and end-of-transaction deletion visibility (EIP-8264, the
// fork's Amsterdam-era replacement for the EIP-6780 selfdestruct semantics
// referenced by the spec).

package core

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/beacon"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// factoryDeploying returns factory bytecode that CREATEs a contract with the
// given runtime (max 32 bytes) and leaves the created address on the stack.
// The runtime is embedded in a RETURN-style init code (MSTORE the runtime
// right-aligned into a word, then RETURN its tail):
//
//	initcode (41 bytes): 7f <runtime padded to 32B> 60 00 52 60 <len> 60 <32-len> f3
//
//	60 29 60 10 60 00 39   CODECOPY(mem=0, codeOffset=16, len=41)
//	60 29 60 00 60 00 f0   CREATE(value=0, offset=0, len=41)
//	50                     POP
//	00                     STOP
//	<initcode>             (at code offset 16)
func factoryDeploying(runtime []byte) []byte {
	if len(runtime) == 0 || len(runtime) > 32 {
		panic("factoryDeploying: runtime length out of range (1..32)")
	}
	l := byte(len(runtime))
	init := []byte{0x7f}
	var word [32]byte
	copy(word[32-len(runtime):], runtime)
	init = append(init, word[:]...)
	init = append(init, 0x60, 0x00, 0x52, 0x60, l, 0x60, byte(32-len(runtime)), 0xf3)
	if len(init) != 41 {
		panic("factoryDeploying: bad initcode length")
	}
	code := []byte{0x60, byte(len(init)), 0x60, 0x10, 0x60, 0x00, 0x39, 0x60, byte(len(init)), 0x60, 0x00, 0x60, 0x00, 0xf0, 0x50, 0x00}
	return append(code, init...)
}

// logRevertCode emits LOG0 (empty data) and then reverts: its log must not
// survive into the transaction's event list.
var logRevertCode = []byte{0x60, 0x00, 0x60, 0x00, 0xa0, 0x60, 0x00, 0x60, 0x00, 0xfd}

// TestFrameTxAssertFailureBAL verifies the EIP-7906 requirement that the
// EIP-7928 BAL treats the reverted execution body of an ASSERT-failed
// transaction like reverted calls: the reverted SSTORE leaves a storage read
// but no storage change, while the committed validation prefix (the sender's
// nonce bump) keeps its change entries.
func TestFrameTxAssertFailureBAL(t *testing.T) {
	writer := common.HexToAddress("0xc7e1")
	reverter := common.HexToAddress("0xf1a1")
	cfg := frameChainConfig()
	gspec := &Genesis{
		Config:   cfg,
		GasLimit: 60_000_000,
		Alloc: types.GenesisAlloc{
			ftSender: {Balance: big.NewInt(1e18)},
			writer:   {Code: setSlotCode},
			reverter: {Code: revertCode},
		},
	}

	b := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
		addFrame(types.FrameModeSender, 0, &writer, 200_000, 150_000, new(uint256.Int), nil).
		addFrame(types.FrameModeAssert, 0, &reverter, 100_000, 0, new(uint256.Int), nil)
	b.fees.MaxPriorityFeePerGas = uint256.NewInt(1_000_000_000)
	b.fees.MaxFee = uint256.NewInt(30_000_000_000_000_000)
	frameTx := b.build(t)

	_, blocks, receipts := GenerateChainWithGenesis(gspec, beacon.New(ethash.NewFaker()), 1, func(i int, gen *BlockGen) {
		gen.SetPoS()
		gen.AddTx(frameTx)
	})
	block := blocks[0]
	if block.AccessList() == nil {
		t.Fatal("expected non-nil block access list")
	}
	bal := block.AccessList()

	// Receipts first: the ASSERT failure kept the transaction valid, reverted
	// the body's logs/state gas, and skipped nothing (single ASSERT frame).
	rc := receipts[0][0]
	if len(rc.FrameReceipts) != 3 {
		t.Fatalf("frame receipts %d, want 3", len(rc.FrameReceipts))
	}
	if rc.FrameReceipts[1].Status != types.FrameStatusSucceeded || len(rc.FrameReceipts[1].Logs) != 0 {
		t.Fatalf("body frame receipt: status %d logs %d, want success with logs discarded",
			rc.FrameReceipts[1].Status, len(rc.FrameReceipts[1].Logs))
	}
	if rc.FrameReceipts[1].GasUsedState != 0 {
		t.Fatalf("body frame state gas %d, want 0 (removed)", rc.FrameReceipts[1].GasUsedState)
	}
	if rc.FrameReceipts[2].Status != types.FrameStatusFailed {
		t.Fatalf("ASSERT frame status %d, want failed", rc.FrameReceipts[2].Status)
	}

	// BAL: the writer's reverted SSTORE is a read, not a write.
	aa := assertPresent(t, bal, writer)
	if !hasSlotIn(aa.StorageReads, common.Hash{}) {
		t.Fatalf("reverted body SSTORE must leave slot 0 in storage_reads\n%s", bal.PrettyPrint())
	}
	if hasStorageWrite(bal, writer, common.Hash{}) {
		t.Fatalf("reverted body SSTORE must not leave a storage change\n%s", bal.PrettyPrint())
	}
	// The validation prefix is committed: the sender's nonce change (0 -> 1)
	// at this transaction's block access index must be present.
	sender := assertPresent(t, bal, ftSender)
	if len(sender.NonceChanges) == 0 || sender.NonceChanges[0].PostNonce != 1 {
		t.Fatalf("sender nonce change missing from BAL: %+v", sender.NonceChanges)
	}
	// The (reverted) ASSERT target was accessed and is included.
	assertPresent(t, bal, reverter)
}

// TestTxTraceCreateThenSelfDestruct covers the spec's deletion-visibility
// rule: a contract created and destroyed within the same transaction still
// appears in codes_changed and nonces_changed during the ASSERT frames,
// because the deletion (here EIP-8264: nonce reset, code cleared) happens at
// Finalise, after the last frame.
func TestTxTraceCreateThenSelfDestruct(t *testing.T) {
	factory := common.HexToAddress("0xfac7")
	benef := common.HexToAddress("0xde1e6a7e")
	checker := common.HexToAddress("0xc4ec")
	child := crypto.CreateAddress(factory, 0)

	// Child runtime: SELFDESTRUCT(benef).
	childRuntime := append([]byte{0x73}, benef.Bytes()...)
	childRuntime = append(childRuntime, 0xff)
	childCodeHash := crypto.Keccak256Hash(childRuntime)

	addrs := sortedAddrs(ftSender, factory, child)
	b := &asmBuf{}
	b.traceCheck(0x02, 0, word64(1)) // CODES_CHANGED: the destroyed child is listed
	b.traceCheck(0x0b, 0, wordAddr(child))
	b.traceCheck(0x0c, 0, types.EmptyCodeHash) // CODE_BEFORE
	b.traceCheck(0x0d, 0, childCodeHash)       // CODE_AFTER (still present mid-tx)
	b.traceCheck(0x03, 0, word64(3))           // NONCES_CHANGED
	for i, a := range addrs {
		b.traceCheck(0x0e, byte(i), wordAddr(a))
		b.traceCheck(0x0f, byte(i), word64(0))
		b.traceCheck(0x10, byte(i), word64(1))
	}
	b.traceCheck(0x00, 0, word64(0)) // BALANCES_CHANGED: nothing funded, nothing moved
	code := b.finish()

	sdb := mkFrameState(t, types.GenesisAlloc{
		factory: {Code: factoryDeploying(childRuntime)},
		checker: {Code: code},
	})
	tx := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
		addFrame(types.FrameModeSender, 0, &factory, 500_000, 400_000, new(uint256.Int), nil).
		addFrame(types.FrameModeSender, 0, &child, 300_000, 300_000, new(uint256.Int), nil). // SELFDESTRUCT
		addFrame(types.FrameModeAssert, 0, &checker, 1_000_000, 0, new(uint256.Int), nil).
		build(t)

	res, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx)
	if err != nil {
		t.Fatalf("frame tx failed: %v", err)
	}
	if fr := res.FrameReceipts[3]; fr.Status != vm.FrameStatusSucceeded {
		t.Fatalf("ASSERT frame failed: status %d (a TXTRACE expectation mismatched)", fr.Status)
	}
	// Mid-transaction (before Finalise): code and nonce are still in place,
	// exactly as TXTRACE reported them.
	if len(sdb.GetCode(child)) != len(childRuntime) {
		t.Fatalf("child code length %d before Finalise, want %d", len(sdb.GetCode(child)), len(childRuntime))
	}
	if n := sdb.GetNonce(child); n != 1 {
		t.Fatalf("child nonce %d before Finalise, want 1", n)
	}
	// At Finalise (after the last frame) EIP-8264 clears the account.
	sdb.Finalise(true)
	if len(sdb.GetCode(child)) != 0 || sdb.GetNonce(child) != 0 {
		t.Fatalf("child not cleared at Finalise: code %d bytes, nonce %d",
			len(sdb.GetCode(child)), sdb.GetNonce(child))
	}
}

// TestTxTraceEventsExcludeRevertedLogs proves the events table contains only
// the transaction's surviving logs: logs from a reverted body frame are
// discarded with the frame.
func TestTxTraceEventsExcludeRevertedLogs(t *testing.T) {
	logger := common.HexToAddress("0x1069")
	revertLogger := common.HexToAddress("0xf106")
	checker := common.HexToAddress("0xc4ec")

	b := &asmBuf{}
	b.traceCheck(0x11, 0, word64(1))        // only the surviving log
	b.traceCheck(0x12, 0, wordAddr(logger)) // ... from the non-reverted frame
	b.traceCheck(0x13, 0, word64(0))        // LOG0: no topics
	b.traceCheck(0x18, 0, word64(0))        // empty data
	code := b.finish()

	sdb := mkFrameState(t, types.GenesisAlloc{
		logger:       {Code: emitLogCode},
		revertLogger: {Code: logRevertCode},
		checker:      {Code: code},
	})
	tx := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
		addFrame(types.FrameModeSender, 0, &logger, 100_000, 10_000, new(uint256.Int), nil).
		addFrame(types.FrameModeSender, 0, &revertLogger, 100_000, 10_000, new(uint256.Int), nil). // logs, then reverts
		addFrame(types.FrameModeAssert, 0, &checker, 300_000, 0, new(uint256.Int), nil).
		build(t)

	res, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx)
	if err != nil {
		t.Fatalf("frame tx failed: %v", err)
	}
	if fr := res.FrameReceipts[3]; fr.Status != vm.FrameStatusSucceeded {
		t.Fatalf("ASSERT frame failed: status %d", fr.Status)
	}
	if n := len(sdb.Logs()); n != 1 {
		t.Fatalf("tx logs %d, want 1 (reverted frame's log discarded)", n)
	}
}

// TestTxTraceConsistentAcrossAssertFrames verifies the frozen-diff invariant:
// multiple ASSERT frames observe the same diff (ASSERT subtrees are
// read-only, so nothing can change between them).
func TestTxTraceConsistentAcrossAssertFrames(t *testing.T) {
	writer := common.HexToAddress("0xc7e1")
	checker := common.HexToAddress("0xc4ec")

	b := &asmBuf{}
	b.traceCheck(0x01, 0, word64(1)) // SLOTS_CHANGED
	b.traceCheck(0x09, 0, word64(1)) // slot after = 1
	b.traceCheck(0x00, 0, word64(0)) // BALANCES_CHANGED (NoBaseFee harness)
	b.traceCheck(0x03, 0, word64(1)) // NONCES_CHANGED (sender)
	code := b.finish()

	sdb := mkFrameState(t, types.GenesisAlloc{
		writer:  {Code: setSlotCode},
		checker: {Code: code},
	})
	tx := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
		addFrame(types.FrameModeSender, 0, &writer, 200_000, 150_000, new(uint256.Int), nil).
		addFrame(types.FrameModeAssert, 0, &checker, 300_000, 0, new(uint256.Int), nil).
		addFrame(types.FrameModeAssert, 0, &checker, 300_000, 0, new(uint256.Int), nil).
		build(t)

	res, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx)
	if err != nil {
		t.Fatalf("frame tx failed: %v", err)
	}
	for i := 2; i <= 3; i++ {
		if fr := res.FrameReceipts[i]; fr.Status != vm.FrameStatusSucceeded {
			t.Fatalf("ASSERT frame %d failed: status %d (diff not stable across frames)", i, fr.Status)
		}
	}
}

// TestFrameTxAssertSuccessBALStateCommitted is the positive BAL control: with
// a succeeding ASSERT frame the body's storage write lands in storage_changes
// at the transaction's block access index.
func TestFrameTxAssertSuccessBALStateCommitted(t *testing.T) {
	writer := common.HexToAddress("0xc7e1")
	assertion := common.HexToAddress("0xa5517") // EOA: default-code success
	cfg := frameChainConfig()
	gspec := &Genesis{
		Config:   cfg,
		GasLimit: 60_000_000,
		Alloc: types.GenesisAlloc{
			ftSender: {Balance: big.NewInt(1e18)},
			writer:   {Code: setSlotCode},
		},
	}

	b := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
		addFrame(types.FrameModeSender, 0, &writer, 200_000, 150_000, new(uint256.Int), nil).
		addFrame(types.FrameModeAssert, 0, &assertion, 100_000, 0, new(uint256.Int), nil)
	b.fees.MaxPriorityFeePerGas = uint256.NewInt(1_000_000_000)
	b.fees.MaxFee = uint256.NewInt(30_000_000_000_000_000)
	frameTx := b.build(t)

	_, blocks, _ := GenerateChainWithGenesis(gspec, beacon.New(ethash.NewFaker()), 1, func(i int, gen *BlockGen) {
		gen.SetPoS()
		gen.AddTx(frameTx)
	})
	bal := blocks[0].AccessList()
	if bal == nil {
		t.Fatal("expected non-nil block access list")
	}
	if !hasStorageWrite(bal, writer, common.Hash{}) {
		t.Fatalf("successful body SSTORE must leave a storage change\n%s", bal.PrettyPrint())
	}
	sender := assertPresent(t, bal, ftSender)
	if len(sender.NonceChanges) == 0 || sender.NonceChanges[0].PostNonce != 1 {
		t.Fatalf("sender nonce change missing from BAL: %+v", sender.NonceChanges)
	}
	_ = params.SystemAddress // silence unused import pattern invariants
}
