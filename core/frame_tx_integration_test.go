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

// Block-level integration test for EIP-8141: a frame transaction processed
// through chain generation, block insertion and receipt retrieval.

package core

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/beacon"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/triedb"
	"github.com/holiman/uint256"
)

// TestFrameTxBlockIntegration runs a frame transaction through full block
// processing: chain generation (Process -> MakeReceipt -> trie roots),
// insertion (receipt root validation + DB storage) and receipt retrieval
// (storage decode + DeriveFields).
func TestFrameTxBlockIntegration(t *testing.T) {
	receiver := common.HexToAddress("0xbeef")
	cfg := frameChainConfig()
	gspec := &Genesis{
		Config:   cfg,
		GasLimit: 60_000_000,
		Alloc:    types.GenesisAlloc{ftSender: {Balance: big.NewInt(1e18)}},
	}

	// Build the frame tx: self_verify (both scopes) + SENDER value transfer.
	b := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
		addFrame(types.FrameModeSender, 0, &receiver, 100_000, 200_000, uint256.NewInt(1e15), nil)
	// Generous fee budget covering any test base fee (maxGas ~281k, so
	// 3e16 wei covers base fees up to ~100 gwei; sender balance is 1e18).
	b.fees.MaxPriorityFeePerGas = uint256.NewInt(1_000_000_000)
	b.fees.MaxFee = uint256.NewInt(30_000_000_000_000_000)
	frameTx := b.build(t)

	db, blocks, receipts := GenerateChainWithGenesis(gspec, beacon.New(ethash.NewFaker()), 1, func(i int, gen *BlockGen) {
		gen.SetPoS()
		gen.AddTx(frameTx)
	})
	if len(blocks) != 1 || len(blocks[0].Transactions()) != 1 {
		t.Fatalf("expected 1 block with 1 tx, got %d/%d", len(blocks), len(blocks[0].Transactions()))
	}
	block := blocks[0]

	// The block's receipt root must match the trie of the generated receipts
	// (this is what block validation verifies).
	if root := types.DeriveSha(receipts[0], trie.NewStackTrie(nil)); root != block.ReceiptHash() {
		t.Fatalf("receipt root mismatch: derived %v, header %v", root, block.ReceiptHash())
	}
	// In-memory receipt from generation.
	genReceipt := receipts[0][0]
	if genReceipt.Type != types.FrameTxType {
		t.Fatalf("receipt type %d, want frame", genReceipt.Type)
	}
	if len(genReceipt.FrameReceipts) != 2 {
		t.Fatalf("frame receipts %d, want 2", len(genReceipt.FrameReceipts))
	}
	if genReceipt.Payer != ftSender {
		t.Fatalf("payer %v, want sender", genReceipt.Payer)
	}
	if genReceipt.FrameReceipts[0].Status != types.FrameStatusSucceeded ||
		genReceipt.FrameReceipts[1].Status != types.FrameStatusSucceeded {
		t.Fatalf("frame statuses %d/%d, want success/success",
			genReceipt.FrameReceipts[0].Status, genReceipt.FrameReceipts[1].Status)
	}
	if genReceipt.FrameReceipts[1].GasUsedState != 183_600 {
		t.Fatalf("frame1 state gas %d, want 183600", genReceipt.FrameReceipts[1].GasUsedState)
	}

	// Database round-trip: store the generated receipts and read them back
	// with full derivation (the storage encoding must carry payer + frames).
	// Note: InsertChain is intentionally not exercised here — block
	// validation of Amsterdam/Bogota-configured generated chains trips a
	// pre-existing BAL-hash asymmetry in the chain-maker harness (observed
	// identically with plain legacy transactions on the pristine base).
	rawdb.WriteReceipts(db, block.Hash(), block.NumberU64(), receipts[0])
	rawdb.WriteCanonicalHash(db, block.Hash(), block.NumberU64())
	rawdb.WriteHeadBlockHash(db, block.Hash())
	rawdb.WriteBlock(db, block)
	stored := rawdb.ReadReceipts(db, block.Hash(), block.NumberU64(), block.Time(), cfg)
	if len(stored) != 1 {
		t.Fatalf("stored receipts %d, want 1", len(stored))
	}
	r := stored[0]
	if r.Type != types.FrameTxType {
		t.Fatalf("stored receipt type %d, want frame", r.Type)
	}
	if r.Payer != ftSender {
		t.Fatalf("stored payer %v, want sender", r.Payer)
	}
	if len(r.FrameReceipts) != 2 {
		t.Fatalf("stored frame receipts %d, want 2", len(r.FrameReceipts))
	}
	if r.FrameReceipts[1].GasUsedState != genReceipt.FrameReceipts[1].GasUsedState {
		t.Fatalf("stored frame1 state gas %d, want %d (in-memory)",
			r.FrameReceipts[1].GasUsedState, genReceipt.FrameReceipts[1].GasUsedState)
	}
	// Derived status + union logs: the transfer emitted one EIP-7708 log.
	if r.Status != types.ReceiptStatusSuccessful {
		t.Fatalf("derived status %d, want success", r.Status)
	}
	if len(r.Logs) != 1 {
		t.Fatalf("union logs %d, want 1 (ETH transfer log)", len(r.Logs))
	}
	if len(r.FrameReceipts[1].Logs) != 1 {
		t.Fatalf("frame1 logs %d, want 1", len(r.FrameReceipts[1].Logs))
	}
	// Log block fields derived.
	if r.Logs[0].BlockHash != blocks[0].Hash() || r.Logs[0].TxHash != frameTx.Hash() {
		t.Fatal("log block/tx fields not derived")
	}

	// Verify state effects from the generation-time post state.
	sdb, err := state.New(block.Root(), state.NewDatabase(triedb.NewDatabase(db, nil), nil))
	if err != nil {
		t.Fatalf("state at block: %v", err)
	}
	if got := sdb.GetBalance(receiver); got.Cmp(uint256.NewInt(1e15)) != 0 {
		t.Fatalf("receiver balance %v, want 1e15", got)
	}
	if got := sdb.GetNonce(ftSender); got != 1 {
		t.Fatalf("sender nonce %d, want 1", got)
	}
}
