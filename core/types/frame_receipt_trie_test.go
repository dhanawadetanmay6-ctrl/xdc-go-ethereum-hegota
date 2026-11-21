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

// Trie-root tests for the EIP-8141 frame receipt encoding (external test
// package because the trie package cannot be imported by internal tests).

package types_test

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/trie"
)

func mkFrameReceiptExt() *types.Receipt {
	logA := &types.Log{
		Address: common.HexToAddress("0xaaaa"),
		Topics:  []common.Hash{common.HexToHash("0x01")},
		Data:    []byte{0xaa, 0xbb},
	}
	logB := &types.Log{
		Address: common.HexToAddress("0xbbbb"),
		Topics:  []common.Hash{common.HexToHash("0x02"), common.HexToHash("0x03")},
	}
	return &types.Receipt{
		Type:              types.FrameTxType,
		Status:            types.ReceiptStatusSuccessful,
		CumulativeGasUsed: 424_242,
		Payer:             common.HexToAddress("0x9a7e"),
		FrameReceipts: []*types.FrameReceipt{
			{Status: types.FrameStatusSucceeded, GasUsedExecution: 100, GasUsedState: 0, Logs: nil},
			{Status: types.FrameStatusFailed, GasUsedExecution: 100_000, GasUsedState: 0, Logs: []*types.Log{logA}},
			{Status: types.FrameStatusSucceeded, GasUsedExecution: 3000, GasUsedState: 183_600, Logs: []*types.Log{logB}},
			{Status: types.FrameStatusSkipped, GasUsedExecution: 0, GasUsedState: 0, Logs: nil},
		},
		Logs: []*types.Log{logA, logB},
	}
}

// TestFrameReceiptTrieRoot pins the DeriveSha root of a receipt list
// containing a frame receipt (regression guard), and verifies that the root
// is invariant under decode/re-encode (trie depends only on consensus data).
func TestFrameReceiptTrieRoot(t *testing.T) {
	legacy := &types.Receipt{
		Type:              types.DynamicFeeTxType,
		Status:            types.ReceiptStatusSuccessful,
		CumulativeGasUsed: 21_000,
	}
	receipts := types.Receipts{legacy, mkFrameReceiptExt()}
	root := types.DeriveSha(receipts, trie.NewStackTrie(nil))

	// Golden value: computed from this implementation, pinned as regression
	// protection. Any change to the frame receipt encoding must update it
	// deliberately.
	const want = "0x669d2ef61d91237804f39bf5b3acd330ee9a4ad5cc69405527ca677395361821"
	if root.Hex() != want {
		t.Fatalf("receipt root %s, want %s (encoding change?)", root.Hex(), want)
	}

	// Round-trip invariance: decode both receipts, re-derive, must match.
	var decLegacy, decFrame types.Receipt
	bl, _ := legacy.MarshalBinary()
	if err := decLegacy.UnmarshalBinary(bl); err != nil {
		t.Fatal(err)
	}
	bf, _ := receipts[1].MarshalBinary()
	if err := decFrame.UnmarshalBinary(bf); err != nil {
		t.Fatal(err)
	}
	root2 := types.DeriveSha(types.Receipts{&decLegacy, &decFrame}, trie.NewStackTrie(nil))
	if root != root2 {
		t.Fatalf("root not invariant: %s vs %s", root, root2)
	}
}
