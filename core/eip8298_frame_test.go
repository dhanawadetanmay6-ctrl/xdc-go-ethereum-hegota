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

// EIP-8298 SETCODEFROM execution through the EIP-8141 frame-transaction
// engine (consensus path). The EIP's validation-prefix restrictions are
// public-mempool policy rules; this fork intentionally has no prefix
// simulation layer, so the consensus path must execute SETCODEFROM normally.

package core

import (
	"bytes"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
)

// TestFrameTxSetCodeFrom executes a frame transaction whose default-mode
// frame calls a contract that adopts code via SETCODEFROM.
func TestFrameTxSetCodeFrom(t *testing.T) {
	target := common.HexToAddress("0x8298")
	source := common.HexToAddress("0x8297")
	sourceCode := []byte{0x60, 0x01, 0x60, 0x00, 0x55, 0x00} // SSTORE(0,1); STOP

	// target code: SETCODEFROM(source); SSTORE(slot 1, success); STOP
	targetCode := []byte{0x73}
	targetCode = append(targetCode, source[:]...)
	targetCode = append(targetCode, 0xa7, 0x60, 0x01, 0x55, 0x00)

	sdb := mkFrameState(t, types.GenesisAlloc{
		target: {Code: targetCode},
		source: {Code: sourceCode},
	})

	tx := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
		addFrame(types.FrameModeSender, 0, &target, 200_000, 500_000, new(uint256.Int), nil).
		build(t)

	res, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx)
	if err != nil {
		t.Fatalf("frame tx failed: %v", err)
	}
	if len(res.FrameReceipts) != 2 {
		t.Fatalf("receipts: got %d, want 2", len(res.FrameReceipts))
	}
	if res.FrameReceipts[1].Status != vm.FrameStatusSucceeded {
		t.Fatalf("frame1 status %d, want success", res.FrameReceipts[1].Status)
	}
	if got, want := sdb.GetCodeHash(target), crypto.Keccak256Hash(sourceCode); got != want {
		t.Fatalf("target code hash = %x, want adopted %x", got, want)
	}
	if got := sdb.GetCode(target); !bytes.Equal(got, sourceCode) {
		t.Fatalf("target code = %x, want adopted source code", got)
	}
	// Success flag stored by the target's original code in its own storage.
	if got := sdb.GetState(target, common.BytesToHash([]byte{1})).Big(); got.Uint64() != 1 {
		t.Fatalf("success flag = %v, want 1", got)
	}
}

// TestFrameTxSetCodeFromVerifyFrameBanned covers the EIP-8298 × EIP-8141
// mempool rule at the level this fork enforces it: SETCODEFROM is a state
// writer, so it cannot run inside a VERIFY frame at all (the frame engine
// executes verify frames read-only). The spec's "banned during validation-
// prefix execution" mempool rule follows from this consensus restriction.
func TestFrameTxSetCodeFromVerifyFrameBanned(t *testing.T) {
	source := common.HexToAddress("0x8297")
	sourceCode := []byte{0x00}
	// Sender's account code: SETCODEFROM(source); STOP — used as the verify
	// code (frame targets the sender itself).
	senderCode := []byte{0x73}
	senderCode = append(senderCode, source[:]...)
	senderCode = append(senderCode, 0xa7, 0x00)

	sdb := mkFrameState(t, types.GenesisAlloc{
		source: {Code: sourceCode},
	})
	// Give the sender validation code.
	sdb.SetCode(ftSender, senderCode, tracing.CodeChangeUnspecified)

	receiver := common.HexToAddress("0xbeef")
	tx := newFrameTx(0, ftSender).
		addFrame(types.FrameModeVerify, types.FrameFlagApproveScopeMask, nil, 100_000, 100_000, new(uint256.Int), nil).
		addFrame(types.FrameModeSender, 0, &receiver, 100_000, 200_000, uint256.NewInt(1e15), nil).
		build(t)

	res, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx)
	// The verify frame runs read-only: SETCODEFROM halts with write
	// protection and the whole transaction is consensus-invalid.
	if err == nil {
		t.Fatal("frame tx whose verify frame executes SETCODEFROM must be rejected")
	}
	if res != nil {
		t.Fatal("no execution result for a rejected transaction")
	}
	if got := sdb.GetCodeHash(ftSender); got != crypto.Keccak256Hash(senderCode) {
		t.Fatalf("sender code hash changed from verify frame: %x", got)
	}
}
