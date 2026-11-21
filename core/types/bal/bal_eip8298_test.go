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

// EIP-8298 BAL CodeChange extension tests: the three-element adoption form
// [block_access_index, b"", new_code_hash] versus the ordinary two-element
// bytecode form, across validation, RLP and JSON round-trips, Merge and Copy.

package bal

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
)

func eip8298BAL(code []byte, hash *common.Hash) *BlockAccessList {
	return &BlockAccessList{{
		Address:      common.BytesToAddress([]byte{0xaa}),
		CodeChanges:  []encodingCodeChange{{BlockAccessIndex: 1, NewCode: code, NewCodeHash: hash}},
		StorageReads: nil,
	}}
}

func TestEIP8298CodeChangeValidation(t *testing.T) {
	hash := crypto.Keccak256Hash([]byte{0x01})
	tests := []struct {
		name    string
		code    []byte
		hash    *common.Hash
		wantErr bool
	}{
		{name: "bytecode form ok", code: []byte{0x60, 0x00}},
		{name: "adoption form ok", code: nil, hash: &hash},
		{name: "hash form with non-empty code rejected", code: []byte{0x00}, hash: &hash, wantErr: true},
		{name: "hash form with zero hash rejected", code: nil, hash: &common.Hash{}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := eip8298BAL(tc.code, tc.hash).Validate(1_000_000_000, 10)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestEIP8298CodeChangeRLPRoundTrip(t *testing.T) {
	hash := crypto.Keccak256Hash([]byte{0x01})
	for _, tc := range []struct {
		name string
		code []byte
		hash *common.Hash
	}{
		{name: "two-element bytecode form", code: []byte{0x60, 0x00}},
		{name: "three-element adoption form", code: nil, hash: &hash},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enc, err := rlp.EncodeToBytes(eip8298BAL(tc.code, tc.hash))
			if err != nil {
				t.Fatal(err)
			}
			dec := new(BlockAccessList)
			if err := rlp.DecodeBytes(enc, dec); err != nil {
				t.Fatal(err)
			}
			got := (*dec)[0].CodeChanges[0]
			if got.BlockAccessIndex != 1 {
				t.Fatalf("index = %d", got.BlockAccessIndex)
			}
			if !bytes.Equal(got.NewCode, tc.code) && !(len(got.NewCode) == 0 && len(tc.code) == 0) {
				t.Fatalf("code = %x, want %x", got.NewCode, tc.code)
			}
			if (got.NewCodeHash == nil) != (tc.hash == nil) {
				t.Fatalf("hash presence mismatch: got %v want %v", got.NewCodeHash, tc.hash)
			}
			if got.NewCodeHash != nil && *got.NewCodeHash != *tc.hash {
				t.Fatalf("hash = %x, want %x", *got.NewCodeHash, *tc.hash)
			}
			if err := dec.Validate(1_000_000_000, 10); err != nil {
				t.Fatalf("round-tripped BAL failed validation: %v", err)
			}
		})
	}
}

func TestEIP8298CodeChangeConstructionMergeCopy(t *testing.T) {
	hash := crypto.Keccak256Hash([]byte{0x01})
	addr := common.BytesToAddress([]byte{0xaa})

	a := NewConstructionBlockAccessList()
	a.CodeChangeHash(addr, 3, hash)
	b := NewConstructionBlockAccessList()
	b.CodeChange(addr, 1, []byte{0x60, 0x00})
	a.Merge(b)

	acc := a.Accounts[addr]
	if len(acc.CodeChange) != 1 || len(acc.CodeChangeHashes) != 1 {
		t.Fatalf("merged maps: code=%v hashes=%v", acc.CodeChange, acc.CodeChangeHashes)
	}
	enc := a.ToEncodingObj()
	changes := (*enc)[0].CodeChanges
	if len(changes) != 2 || changes[0].BlockAccessIndex != 1 || changes[1].BlockAccessIndex != 3 {
		t.Fatalf("merged encoding not index-sorted: %+v", changes)
	}
	if changes[0].NewCodeHash != nil || changes[1].NewCodeHash == nil || *changes[1].NewCodeHash != hash {
		t.Fatalf("forms wrong: %+v", changes)
	}
	if err := enc.Validate(1_000_000_000, 10); err != nil {
		t.Fatal(err)
	}

	// Copy deep-copies the hash map.
	cpy := a.Copy()
	h2 := crypto.Keccak256Hash([]byte{0x02})
	cpy.Accounts[addr].CodeChangeHashes[3] = h2
	if a.Accounts[addr].CodeChangeHashes[3] != hash {
		t.Fatal("Copy shares the CodeChangeHashes map")
	}

	// A later bytecode form for the same index replaces the hash form (and
	// vice versa) — the last write in a transaction determines the form.
	a.CodeChange(addr, 3, []byte{0x60, 0x01})
	if _, ok := a.Accounts[addr].CodeChangeHashes[3]; ok {
		t.Fatal("bytecode form must evict the hash form at the same index")
	}
	a.CodeChangeHash(addr, 3, hash)
	if _, ok := a.Accounts[addr].CodeChange[3]; ok {
		t.Fatal("hash form must evict the bytecode form at the same index")
	}
}

// TestEIP8298CodeChangeJSON checks the optional newCodeHash JSON field.
func TestEIP8298CodeChangeJSON(t *testing.T) {
	hash := crypto.Keccak256Hash([]byte{0x01})
	for _, tc := range []struct {
		name string
		code []byte
		hash *common.Hash
	}{
		{name: "bytecode form", code: []byte{0x60, 0x00}},
		{name: "adoption form", code: nil, hash: &hash},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := eip8298BAL(tc.code, tc.hash)
			data, err := json.Marshal(in)
			if err != nil {
				t.Fatal(err)
			}
			out := new(BlockAccessList)
			if err := json.Unmarshal(data, out); err != nil {
				t.Fatal(err)
			}
			got := (*out)[0].CodeChanges[0]
			if (got.NewCodeHash == nil) != (tc.hash == nil) {
				t.Fatalf("json round-trip lost hash presence: %s", data)
			}
		})
	}
}
