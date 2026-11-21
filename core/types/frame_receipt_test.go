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

// Tests for the EIP-8141 frame receipt encoding: consensus RLP
// ([cumulative, payer, frames]), storage round-trip, trie derivation, and
// field derivation.

package types

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/holiman/uint256"
)

func mkFrameReceipt() *Receipt {
	logA := &Log{
		Address: common.HexToAddress("0xaaaa"),
		Topics:  []common.Hash{common.HexToHash("0x01")},
		Data:    []byte{0xaa, 0xbb},
	}
	logB := &Log{
		Address: common.HexToAddress("0xbbbb"),
		Topics:  []common.Hash{common.HexToHash("0x02"), common.HexToHash("0x03")},
	}
	return &Receipt{
		Type:              FrameTxType,
		Status:            ReceiptStatusSuccessful,
		CumulativeGasUsed: 424_242,
		Payer:             common.HexToAddress("0x9a7e"),
		FrameReceipts: []*FrameReceipt{
			{Status: FrameStatusSucceeded, GasUsedExecution: 100, GasUsedState: 0, Logs: nil},
			{Status: FrameStatusFailed, GasUsedExecution: 100_000, GasUsedState: 0, Logs: []*Log{logA}},
			{Status: FrameStatusSucceeded, GasUsedExecution: 3000, GasUsedState: 183_600, Logs: []*Log{logB}},
			{Status: FrameStatusSkipped, GasUsedExecution: 0, GasUsedState: 0, Logs: nil},
		},
		// Union logs list as MakeReceipt produces it.
		Logs: []*Log{logA, logB},
	}
}

// TestFrameReceiptConsensusRoundTrip verifies MarshalBinary/UnmarshalBinary
// and EncodeRLP/DecodeRLP for frame receipts.
func TestFrameReceiptConsensusRoundTrip(t *testing.T) {
	orig := mkFrameReceipt()

	// Binary round-trip.
	enc, err := orig.MarshalBinary()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if enc[0] != FrameTxType {
		t.Fatalf("type byte %#x, want %#x", enc[0], FrameTxType)
	}
	var dec Receipt
	if err := dec.UnmarshalBinary(enc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if dec.Type != FrameTxType {
		t.Fatalf("type %d, want %d", dec.Type, FrameTxType)
	}
	if dec.Payer != orig.Payer {
		t.Fatalf("payer %v, want %v", dec.Payer, orig.Payer)
	}
	if dec.CumulativeGasUsed != orig.CumulativeGasUsed {
		t.Fatalf("cumulative %d, want %d", dec.CumulativeGasUsed, orig.CumulativeGasUsed)
	}
	if len(dec.FrameReceipts) != len(orig.FrameReceipts) {
		t.Fatalf("frames %d, want %d", len(dec.FrameReceipts), len(orig.FrameReceipts))
	}
	for i := range orig.FrameReceipts {
		o, d := orig.FrameReceipts[i], dec.FrameReceipts[i]
		if o.Status != d.Status || o.GasUsedExecution != d.GasUsedExecution || o.GasUsedState != d.GasUsedState {
			t.Fatalf("frame %d mismatch: %+v vs %+v", i, o, d)
		}
		if len(o.Logs) != len(d.Logs) {
			t.Fatalf("frame %d logs %d, want %d", i, len(d.Logs), len(o.Logs))
		}
		for j := range o.Logs {
			if o.Logs[j].Address != d.Logs[j].Address || !bytes.Equal(o.Logs[j].Data, d.Logs[j].Data) {
				t.Fatalf("frame %d log %d mismatch", i, j)
			}
		}
	}
	// Re-encoding the decoded receipt must produce the identical bytes.
	enc2, err := dec.MarshalBinary()
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	if !bytes.Equal(enc, enc2) {
		t.Fatal("consensus encoding not stable across round-trip")
	}

	// Stream round-trip (rlp.Encoder path, as used by the trie encoder).
	var buf bytes.Buffer
	if err := orig.EncodeRLP(&buf); err != nil {
		t.Fatalf("EncodeRLP: %v", err)
	}
	var dec2 Receipt
	if err := dec2.DecodeRLP(rlp.NewStream(bytes.NewReader(buf.Bytes()), 0)); err != nil {
		t.Fatalf("DecodeRLP: %v", err)
	}
	if dec2.Payer != orig.Payer || len(dec2.FrameReceipts) != 4 {
		t.Fatal("stream round-trip mismatch")
	}
}

// TestFrameReceiptStorageRoundTrip verifies ReceiptForStorage encoding for
// frame receipts (payer and frames must survive the DB round-trip).
func TestFrameReceiptStorageRoundTrip(t *testing.T) {
	orig := mkFrameReceipt()
	var buf bytes.Buffer
	if err := (*ReceiptForStorage)(orig).EncodeRLP(&buf); err != nil {
		t.Fatalf("encode: %v", err)
	}
	var dec ReceiptForStorage
	if err := dec.DecodeRLP(rlp.NewStream(bytes.NewReader(buf.Bytes()), 0)); err != nil {
		t.Fatalf("decode: %v", err)
	}
	r := (*Receipt)(&dec)
	if r.Type != FrameTxType {
		t.Fatalf("type %d, want frame", r.Type)
	}
	if r.Payer != orig.Payer {
		t.Fatalf("payer %v, want %v", r.Payer, orig.Payer)
	}
	if r.Status != orig.Status {
		t.Fatalf("status %d, want %d", r.Status, orig.Status)
	}
	if len(r.FrameReceipts) != 4 {
		t.Fatalf("frames %d, want 4", len(r.FrameReceipts))
	}
	if r.FrameReceipts[2].GasUsedState != 183_600 {
		t.Fatalf("frame2 state gas %d, want 183600", r.FrameReceipts[2].GasUsedState)
	}
	if len(r.FrameReceipts[1].Logs) != 1 || len(r.FrameReceipts[3].Logs) != 0 {
		t.Fatal("frame logs mismatch")
	}
}

// TestFrameReceiptStorageLegacyShape ensures legacy receipts still use the
// three-element storage encoding and decode unchanged (regression).
func TestFrameReceiptStorageLegacyShape(t *testing.T) {
	legacy := &Receipt{
		Type:              LegacyTxType,
		Status:            ReceiptStatusSuccessful,
		CumulativeGasUsed: 21_000,
		Logs:              []*Log{{Address: common.HexToAddress("0x1")}},
	}
	var buf bytes.Buffer
	if err := (*ReceiptForStorage)(legacy).EncodeRLP(&buf); err != nil {
		t.Fatal(err)
	}
	var dec ReceiptForStorage
	if err := dec.DecodeRLP(rlp.NewStream(bytes.NewReader(buf.Bytes()), 0)); err != nil {
		t.Fatal(err)
	}
	r := (*Receipt)(&dec)
	if r.Type == FrameTxType {
		t.Fatal("legacy receipt decoded as frame receipt")
	}
	if len(r.Logs) != 1 || r.CumulativeGasUsed != 21_000 {
		t.Fatal("legacy storage round-trip mismatch")
	}
}

// TestFrameReceiptDeriveFields verifies log union derivation, log indexing,
// bloom computation and derived status for frame receipts.
func TestFrameReceiptDeriveFields(t *testing.T) {
	r := mkFrameReceipt()
	// Simulate a DB-decoded receipt: no top-level logs/status.
	r.Logs = nil
	r.Status = 0

	tx := NewTx(&FrameTx{
		ChainID: uint256.NewInt(1),
		Sender:  common.HexToAddress("0x5"),
		Frames: []Frame{
			{Mode: FrameModeVerify, Limits: FrameGasLimits{Execution: 1}},
			{Mode: FrameModeSender, Limits: FrameGasLimits{Execution: 1}},
			{Mode: FrameModeSender, Limits: FrameGasLimits{Execution: 1}},
			{Mode: FrameModeSender, Limits: FrameGasLimits{Execution: 1}},
		},
		Fees: FrameFees{MaxFee: uint256.NewInt(1), MaxPriorityFeePerGas: uint256.NewInt(1)},
	})
	signer := LatestSignerForChainID(big.NewInt(1))
	r.DeriveFields(signer, DeriveReceiptContext{
		BlockHash:   common.HexToHash("0x99"),
		BlockNumber: 7,
		BlockTime:   100,
		BaseFee:     big.NewInt(1),
		GasUsed:     103_100,
		LogIndex:    5,
		Tx:          tx,
		TxIndex:     3,
	})
	// Union logs rebuilt from frames.
	if len(r.Logs) != 2 {
		t.Fatalf("union logs %d, want 2", len(r.Logs))
	}
	// Log indices continue from the block-wide offset.
	if r.Logs[0].Index != 5 || r.Logs[1].Index != 6 {
		t.Fatalf("log indices %d,%d, want 5,6", r.Logs[0].Index, r.Logs[1].Index)
	}
	if r.Logs[0].BlockNumber != 7 || r.Logs[1].BlockHash != common.HexToHash("0x99") {
		t.Fatal("log block fields not derived")
	}
	// Frame log pointers share the derivation (same objects).
	if r.FrameReceipts[1].Logs[0].Index != 5 {
		t.Fatalf("frame log index %d, want 5", r.FrameReceipts[1].Logs[0].Index)
	}
	// Derived status: frame 0 and 2 succeeded.
	if r.Status != ReceiptStatusSuccessful {
		t.Fatalf("status %d, want successful", r.Status)
	}
	// Bloom must cover both frame logs.
	if r.Bloom == (Bloom{}) {
		t.Fatal("bloom not computed")
	}
	// No contract address for frame transactions.
	if r.ContractAddress != (common.Address{}) {
		t.Fatalf("contract address %v, want zero", r.ContractAddress)
	}
}
