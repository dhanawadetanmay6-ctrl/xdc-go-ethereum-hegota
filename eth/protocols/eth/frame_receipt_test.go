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

// Wire-format tests for EIP-8141 frame receipts (eth/69 compressed shape).

package eth

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
)

func mkWireFrameReceipt() *types.Receipt {
	return &types.Receipt{
		Type:              types.FrameTxType,
		Status:            types.ReceiptStatusSuccessful,
		CumulativeGasUsed: 424_242,
		Payer:             common.HexToAddress("0x9a7e"),
		FrameReceipts: []*types.FrameReceipt{
			{Status: types.FrameStatusSucceeded, GasUsedExecution: 100, GasUsedState: 0},
			{Status: types.FrameStatusSucceeded, GasUsedExecution: 3000, GasUsedState: 183_600,
				Logs: []*types.Log{{Address: common.HexToAddress("0xaaaa"), Topics: []common.Hash{common.HexToHash("0x01")}}}},
		},
		Logs: []*types.Log{{Address: common.HexToAddress("0xaaaa"), Topics: []common.Hash{common.HexToHash("0x01")}}},
	}
}

// TestFrameReceiptWireHash verifies that receipts fetched over the eth/69
// compressed wire format derive the same receipts root as the locally
// computed one (DeriveSha over the original receipts).
func TestFrameReceiptWireHash(t *testing.T) {
	receipts := []*types.Receipt{
		{Type: types.DynamicFeeTxType, Status: types.ReceiptStatusSuccessful, CumulativeGasUsed: 21_000},
		mkWireFrameReceipt(),
	}
	want := types.DeriveSha(types.Receipts(receipts), trie.NewStackTrie(nil))

	// Encode to the wire format and back.
	rl := NewReceiptList(receipts)
	enc, err := rlp.EncodeToBytes(rl)
	if err != nil {
		t.Fatalf("wire encode: %v", err)
	}
	var dec ReceiptList
	if err := rlp.DecodeBytes(enc, &dec); err != nil {
		t.Fatalf("wire decode: %v", err)
	}
	got := types.DeriveSha(dec.Derivable(), trie.NewStackTrie(nil))
	if got != want {
		t.Fatalf("wire receipts root %v, want %v", got, want)
	}

	// Storage persistence of wire-fetched receipts must decode identically.
	storageEnc, err := dec.EncodeForStorage()
	if err != nil {
		t.Fatalf("EncodeForStorage: %v", err)
	}
	var stored []*types.ReceiptForStorage
	if err := rlp.DecodeBytes(storageEnc, &stored); err != nil {
		t.Fatalf("storage decode: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("stored receipts %d, want 2", len(stored))
	}
	fr := (*types.Receipt)(stored[1])
	if fr.Type != types.FrameTxType {
		t.Fatalf("stored type %d, want frame", fr.Type)
	}
	if fr.Payer != common.HexToAddress("0x9a7e") {
		t.Fatalf("stored payer %v", fr.Payer)
	}
	if len(fr.FrameReceipts) != 2 || fr.FrameReceipts[1].GasUsedState != 183_600 {
		t.Fatalf("stored frames corrupt: %+v", fr.FrameReceipts)
	}
	// And the re-derived consensus encoding still hashes identically for the
	// frame receipt (frame encoding carries no bloom, so it is invariant
	// under the storage round-trip; legacy receipts regain their bloom only
	// via DeriveFields, which is out of scope here).
	wantFrame := types.DeriveSha(types.Receipts{receipts[1]}, trie.NewStackTrie(nil))
	if root := types.DeriveSha(types.Receipts{fr}, trie.NewStackTrie(nil)); root != wantFrame {
		t.Fatalf("frame storage round-trip root %v, want %v", root, wantFrame)
	}
}
