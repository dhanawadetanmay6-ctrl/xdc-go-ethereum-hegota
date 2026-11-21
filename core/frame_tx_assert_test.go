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

// End-to-end tests for the EIP-7906 ASSERT frame mode: STATICCALL execution
// of a contiguous trailing suffix whose failure reverts the execution body
// without invalidating the transaction.

package core

import (
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/holiman/uint256"
)

// emitLogCode emits LOG0 with empty non-indexed data: PUSH1 0, PUSH1 0, LOG0, STOP.
var emitLogCode = []byte{0x60, 0x00, 0x60, 0x00, 0xa0, 0x00}

// TestFrameTxAssertSuccess runs the canonical shape: validation prefix, one
// state-changing body frame, and a succeeding ASSERT frame. Body effects are
// committed and every frame reports success.
func TestFrameTxAssertSuccess(t *testing.T) {
	writer := common.HexToAddress("0xc7e1")
	assertion := common.HexToAddress("0xa5517") // EOA: default-code success
	sdb := mkFrameState(t, types.GenesisAlloc{
		writer: {Code: setSlotCode},
	})
	tx := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
		addFrame(types.FrameModeSender, 0, &writer, 200_000, 150_000, new(uint256.Int), nil).
		addFrame(types.FrameModeAssert, 0, &assertion, 50_000, 0, new(uint256.Int), nil).
		build(t)

	res, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx)
	if err != nil {
		t.Fatalf("frame tx failed: %v", err)
	}
	for i, fr := range res.FrameReceipts {
		if fr.Status != vm.FrameStatusSucceeded {
			t.Fatalf("frame %d status %d, want success", i, fr.Status)
		}
	}
	if v := sdb.GetState(writer, common.Hash{}); v != (common.Hash{31: 1}) {
		t.Fatalf("body slot not committed: %v", v)
	}
	if got := sdb.GetNonce(ftSender); got != 1 {
		t.Fatalf("sender nonce %d, want 1", got)
	}
}

// TestFrameTxAssertDefaultCode checks that an ASSERT frame with a nil target
// (resolving to the code-less sender) is handled like SENDER/DEFAULT default
// code: it succeeds as an empty-code call.
func TestFrameTxAssertDefaultCode(t *testing.T) {
	sdb := mkFrameState(t, nil)
	tx := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
		addFrame(types.FrameModeAssert, 0, nil, 50_000, 0, new(uint256.Int), nil).
		build(t)

	res, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx)
	if err != nil {
		t.Fatalf("frame tx failed: %v", err)
	}
	if res.FrameReceipts[1].Status != vm.FrameStatusSucceeded {
		t.Fatalf("default-code ASSERT status %d, want success", res.FrameReceipts[1].Status)
	}
}

// TestFrameTxAssertFailureRevertsBody is the core EIP-7906 semantic: the
// failing ASSERT frame reverts the whole execution body while the transaction
// stays valid and the validation prefix (nonce bump, payment) stays committed.
func TestFrameTxAssertFailureRevertsBody(t *testing.T) {
	writer := common.HexToAddress("0xc7e1")
	logger := common.HexToAddress("0x1069")
	reverter := common.HexToAddress("0xf1a1")
	sdb := mkFrameState(t, types.GenesisAlloc{
		writer:   {Code: setSlotCode},
		logger:   {Code: emitLogCode},
		reverter: {Code: revertCode},
	})
	tx := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
		addFrame(types.FrameModeSender, 0, &writer, 200_000, 150_000, new(uint256.Int), nil).
		addFrame(types.FrameModeSender, 0, &logger, 100_000, 10_000, new(uint256.Int), nil).
		addFrame(types.FrameModeAssert, 0, &reverter, 100_000, 10_000, new(uint256.Int), nil).
		build(t)

	res, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx)
	if err != nil {
		t.Fatalf("ASSERT failure must not invalidate the transaction: %v", err)
	}
	// Failing ASSERT frame: status 0, keeps its execution gas.
	if fr := res.FrameReceipts[3]; fr.Status != vm.FrameStatusFailed || fr.GasUsedExecution == 0 {
		t.Fatalf("ASSERT receipt: status %d exec %d, want failed with gas", fr.Status, fr.GasUsedExecution)
	}
	// Reverted body frames: status and execution gas kept, logs discarded,
	// state gas removed.
	for i := 1; i <= 2; i++ {
		fr := res.FrameReceipts[i]
		if fr.Status != vm.FrameStatusSucceeded {
			t.Fatalf("body frame %d status %d, want success (kept)", i, fr.Status)
		}
		if fr.GasUsedExecution == 0 {
			t.Fatalf("body frame %d must keep its execution gas", i)
		}
		if fr.GasUsedState != 0 {
			t.Fatalf("body frame %d state gas %d, want 0 (removed)", i, fr.GasUsedState)
		}
		if len(fr.Logs) != 0 {
			t.Fatalf("body frame %d logs %d, want discarded", i, len(fr.Logs))
		}
	}
	// Validation prefix committed: sender nonce bumped by APPROVE.
	if got := sdb.GetNonce(ftSender); got != 1 {
		t.Fatalf("sender nonce %d, want 1 (prefix committed)", got)
	}
	// Body state reverted: slot cleared, log gone.
	if v := sdb.GetState(writer, common.Hash{}); v != (common.Hash{}) {
		t.Fatalf("body slot survived the ASSERT rollback: %v", v)
	}
	if n := len(sdb.Logs()); n != 0 {
		t.Fatalf("body logs survived the ASSERT rollback: %d", n)
	}
}

// TestFrameTxAssertSkippedSuffix checks that the ASSERT frames after a
// failing one are not executed: status 2, no gas used.
func TestFrameTxAssertSkippedSuffix(t *testing.T) {
	writer := common.HexToAddress("0xc7e1")
	reverter := common.HexToAddress("0xf1a1")
	assertion := common.HexToAddress("0xa5517")
	sdb := mkFrameState(t, types.GenesisAlloc{
		writer:   {Code: setSlotCode},
		reverter: {Code: revertCode},
	})
	tx := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
		addFrame(types.FrameModeSender, 0, &writer, 200_000, 150_000, new(uint256.Int), nil).
		addFrame(types.FrameModeAssert, 0, &reverter, 100_000, 10_000, new(uint256.Int), nil).  // fails
		addFrame(types.FrameModeAssert, 0, &assertion, 100_000, 10_000, new(uint256.Int), nil). // skipped
		build(t)

	res, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx)
	if err != nil {
		t.Fatalf("tx should be valid despite ASSERT failure: %v", err)
	}
	if fr := res.FrameReceipts[2]; fr.Status != vm.FrameStatusFailed {
		t.Fatalf("first ASSERT status %d, want failed", fr.Status)
	}
	if fr := res.FrameReceipts[3]; fr.Status != vm.FrameStatusSkipped {
		t.Fatalf("second ASSERT status %d, want skipped(2)", fr.Status)
	}
	if fr := res.FrameReceipts[3]; fr.GasUsedExecution != 0 || fr.GasUsedState != 0 {
		t.Fatalf("skipped ASSERT gas (%d, %d), want zero", fr.GasUsedExecution, fr.GasUsedState)
	}
	if v := sdb.GetState(writer, common.Hash{}); v != (common.Hash{}) {
		t.Fatalf("body slot survived the ASSERT rollback: %v", v)
	}
}

// TestFrameTxAssertApproveHalts proves that APPROVE inside an ASSERT frame's
// call subtree is an ordinary STATICCALL violation: exceptional halt of the
// call frame (no approval applied), handled as an ASSERT failure.
func TestFrameTxAssertApproveHalts(t *testing.T) {
	writer := common.HexToAddress("0xc7e1")
	approver := common.HexToAddress("0xapp0")
	sdb := mkFrameState(t, types.GenesisAlloc{
		writer:   {Code: setSlotCode},
		approver: {Code: approveBothCode},
	})
	tx := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
		addFrame(types.FrameModeSender, 0, &writer, 200_000, 150_000, new(uint256.Int), nil).
		addFrame(types.FrameModeAssert, 0, &approver, 100_000, 10_000, new(uint256.Int), nil).
		build(t)

	res, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx)
	if err != nil {
		t.Fatalf("tx should be valid despite APPROVE halt: %v", err)
	}
	fr := res.FrameReceipts[2]
	if fr.Status != vm.FrameStatusFailed {
		t.Fatalf("ASSERT-with-APPROVE status %d, want failed", fr.Status)
	}
	if fr.GasUsedExecution != 100_000 {
		t.Fatalf("exceptional halt must consume the frame's execution pool: got %d", fr.GasUsedExecution)
	}
	if v := sdb.GetState(writer, common.Hash{}); v != (common.Hash{}) {
		t.Fatalf("body slot survived the ASSERT rollback: %v", v)
	}
}

// TestFrameTxAssertStaticcallEnforced proves the read-only rule: an ASSERT
// frame whose target attempts SSTORE halts on write protection.
func TestFrameTxAssertStaticcallEnforced(t *testing.T) {
	writer := common.HexToAddress("0xc7e1")
	sdb := mkFrameState(t, types.GenesisAlloc{
		writer: {Code: setSlotCode},
	})
	tx := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
		addFrame(types.FrameModeAssert, 0, &writer, 200_000, 150_000, new(uint256.Int), nil).
		build(t)

	res, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx)
	if err != nil {
		t.Fatalf("tx should be valid despite STATICCALL violation: %v", err)
	}
	if fr := res.FrameReceipts[1]; fr.Status != vm.FrameStatusFailed {
		t.Fatalf("SSTORE inside ASSERT status %d, want failed", fr.Status)
	}
	if v := sdb.GetState(writer, common.Hash{}); v != (common.Hash{}) {
		t.Fatalf("ASSERT frame wrote state despite STATICCALL: %v", v)
	}
	if got := sdb.GetNonce(ftSender); got != 1 {
		t.Fatalf("sender nonce %d, want 1 (prefix committed)", got)
	}
}

// TestFrameTxAssertNoPayerInvalid covers the EIP-7906 corner: an ASSERT
// failure with no payer set has nothing to restore and the transaction is
// invalid. An all-successful ASSERT-only transaction is likewise invalid
// under EIP-8141 (payment never approved).
func TestFrameTxAssertNoPayerInvalid(t *testing.T) {
	reverter := common.HexToAddress("0xf1a1")
	assertion := common.HexToAddress("0xa5517")
	sdb := mkFrameState(t, types.GenesisAlloc{
		reverter: {Code: revertCode},
	})

	// Failing ASSERT, no payer.
	tx := newFrameTx(0, ftSender).
		addFrame(types.FrameModeAssert, 0, &reverter, 100_000, 0, new(uint256.Int), nil).
		build(t)
	if _, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx); !errors.Is(err, ErrFrameTxNoPayer) {
		t.Fatalf("err %v, want ErrFrameTxNoPayer", err)
	}

	// Succeeding ASSERT, still no payer.
	tx2 := newFrameTx(0, ftSender).
		addFrame(types.FrameModeAssert, 0, &assertion, 100_000, 0, new(uint256.Int), nil).
		build(t)
	if _, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx2); !errors.Is(err, ErrFrameTxNoPayer) {
		t.Fatalf("err %v, want ErrFrameTxNoPayer", err)
	}
	if got := sdb.GetNonce(ftSender); got != 0 {
		t.Fatalf("sender nonce %d, want 0 (invalid txs reverted)", got)
	}
}

// TestFrameTxAssertBodyBatchFullyReverted proves that an ASSERT failure
// reverts the entire execution body — including a *successful* atomic batch —
// rather than unwinding batch-wise.
func TestFrameTxAssertBodyBatchFullyReverted(t *testing.T) {
	r1 := common.HexToAddress("0xb471")
	writer := common.HexToAddress("0xc7e1")
	reverter := common.HexToAddress("0xf1a1")
	sdb := mkFrameState(t, types.GenesisAlloc{
		writer:   {Code: setSlotCode},
		reverter: {Code: revertCode},
	})
	tx := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
		addFrame(types.FrameModeSender, types.FrameFlagAtomicBatch, &r1, 100_000, 200_000, uint256.NewInt(11), nil).
		addFrame(types.FrameModeSender, 0, &writer, 200_000, 150_000, new(uint256.Int), nil). // batch terminator, succeeds
		addFrame(types.FrameModeAssert, 0, &reverter, 100_000, 10_000, new(uint256.Int), nil).
		build(t)

	res, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx)
	if err != nil {
		t.Fatalf("tx should be valid despite ASSERT failure: %v", err)
	}
	// Both body frames kept status/execution gas, but all effects are gone.
	for i := 1; i <= 2; i++ {
		fr := res.FrameReceipts[i]
		if fr.Status != vm.FrameStatusSucceeded || fr.GasUsedExecution == 0 {
			t.Fatalf("body frame %d status %d exec %d, want success with gas", i, fr.Status, fr.GasUsedExecution)
		}
		if fr.GasUsedState != 0 || len(fr.Logs) != 0 {
			t.Fatalf("body frame %d not cleaned: state %d logs %d", i, fr.GasUsedState, len(fr.Logs))
		}
	}
	if bal := sdb.GetBalance(r1); bal.Sign() != 0 {
		t.Fatalf("batch transfer survived ASSERT rollback: %v", bal)
	}
	if v := sdb.GetState(writer, common.Hash{}); v != (common.Hash{}) {
		t.Fatalf("batch slot survived ASSERT rollback: %v", v)
	}
	if got := sdb.GetNonce(ftSender); got != 1 {
		t.Fatalf("sender nonce %d, want 1 (prefix committed)", got)
	}
}

// TestFrameTxAssertPayerChargedOnFailure verifies the fee side of an ASSERT
// failure: the payer is charged for all consumed gas (prefix + body + failing
// ASSERT frame) while the body's value transfer is rolled back.
func TestFrameTxAssertPayerChargedOnFailure(t *testing.T) {
	receiver := common.HexToAddress("0xbeef")
	reverter := common.HexToAddress("0xf1a1")
	sdb := mkFrameState(t, types.GenesisAlloc{
		reverter: {Code: revertCode},
	})
	baseFee := big.NewInt(1_000_000_000) // 1 gwei
	tipPerGas := uint64(2_000_000_000)   // 2 gwei

	b := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
		addFrame(types.FrameModeSender, 0, &receiver, 100_000, 200_000, uint256.NewInt(1e15), nil).
		addFrame(types.FrameModeAssert, 0, &reverter, 100_000, 10_000, new(uint256.Int), nil)
	b.fees.MaxPriorityFeePerGas = uint256.NewInt(tipPerGas)
	tmp := &types.FrameTx{Frames: b.frames, Signatures: []types.FrameSignature{{Scheme: 0, Signature: make([]byte, 65)}}, Fees: b.fees, ChainID: b.chainID}
	maxGas := tmp.MaxGas()
	b.fees.MaxFee = new(uint256.Int).Mul(uint256.NewInt(maxGas), uint256.NewInt(baseFee.Uint64()+tipPerGas+1_000_000_000))

	tx := b.build(t)
	evm := frameEVM(sdb, baseFee)
	senderBefore := sdb.GetBalance(ftSender).ToBig()
	coinbase := evm.Context.Coinbase

	res, _, err := applyFrameTx(t, sdb, evm, tx)
	if err != nil {
		t.Fatalf("ASSERT failure must not invalidate the transaction: %v", err)
	}
	// The body transfer is reverted; the payer still pays for all gas used.
	if bal := sdb.GetBalance(receiver); bal.Sign() != 0 {
		t.Fatalf("receiver balance %v, want 0 (body reverted)", bal)
	}
	wantSpend := new(big.Int).Mul(big.NewInt(int64(res.UsedGas)), big.NewInt(int64(baseFee.Uint64()+tipPerGas)))
	gotSpend := new(big.Int).Sub(senderBefore, sdb.GetBalance(ftSender).ToBig())
	if gotSpend.Cmp(wantSpend) != 0 {
		t.Fatalf("payer spend %v, want %v (gasUsed %d, no value transfer)", gotSpend, wantSpend, res.UsedGas)
	}
	wantTip := new(big.Int).Mul(big.NewInt(int64(res.UsedGas)), big.NewInt(int64(tipPerGas)))
	if got := sdb.GetBalance(coinbase).ToBig(); got.Cmp(wantTip) != 0 {
		t.Fatalf("coinbase tip %v, want %v", got, wantTip)
	}
}
