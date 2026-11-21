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

package types

import (
	"encoding/binary"
	"errors"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// EIP-8272 constants (spec "Constants").
const (
	// RecentRootLength is the number of entries in each root source's ring
	// buffer (RECENT_ROOT_LENGTH).
	RecentRootLength = 8192

	// RecentRootUsableWindow is the maximum age of a referenceable root
	// (RECENT_ROOT_USABLE_WINDOW = RECENT_ROOT_LENGTH - 1).
	RecentRootUsableWindow = RecentRootLength - 1

	// MaxRecentRootReferences bounds the tuples of one validation operation
	// (MAX_RECENT_ROOT_REFERENCES).
	MaxRecentRootReferences = 16

	// RecentRootTupleBytes is the packed byte length of one
	// (source_id, slot, root) tuple (RECENT_ROOT_TUPLE_BYTES).
	RecentRootTupleBytes = 72

	// RecentRootWriteDataLength is the calldata length of the write
	// operation: salt (32) || root (32).
	RecentRootWriteDataLength = 64
)

// RecentRootAddress is the canonical address of the recent root contract
// (EIP-8272 RECENT_ROOT_ADDRESS). The contract is an ordinary deployment
// (see RecentRootDeploymentTx); the protocol installs nothing at activation.
var RecentRootAddress = common.HexToAddress("0x8272D9679689Ea2f307140CdF9002D27dC00Ffff")

// RecentRootCode is the canonical runtime code of the recent root contract
// (EIP-8272 RECENT_ROOT_CODE).
var RecentRootCode = common.FromHex("0x346100ba57366040146100c05736604836066100ba5780156100ba5761048081116100ba574b60005b602081013560c01c828110156100ba5780830361200011156100ba577f8f42481679c8e6fefa040974b3c905e0ce3f2e464ba93acdb074a41181617efc60005260488260203760686000207fbdc897da2177d260ff5f4be5d4b2aad43f89c3347a305b584fa5a2546d053daa60005290611fff1660c01b60405260486000205414156100ba5760480182811061002857005b60006000fd5b33600052602060006020376034600c20807f8f42481679c8e6fefa040974b3c905e0ce3f2e464ba93acdb074a41181617efc6040524b606852606052602060206088376068604020817fbdc897da2177d260ff5f4be5d4b2aad43f89c3347a305b584fa5a2546d053daa60a852611fff4b1660d05260c852604860a8205500")

// RecentRootInitCode is the deployment init code of the recent root contract
// (EIP-8272 "Deployment"). It is the standard 10-byte loader stub
// (PUSH2 320, DUP1, PUSH1 10, PUSH0, CODECOPY, PUSH0, RETURN) followed by
// RECENT_ROOT_CODE; executing it returns RecentRootCode. Verified byte-equal
// to the spec's deployment input.
var RecentRootInitCode = append(common.FromHex("0x61014080600a5f395ff3"), RecentRootCode...)

// RecentRootDeploymentTxBytes is the RLP of the presigned legacy deployment
// transaction of the recent root contract (EIP-8272 "Deployment"):
// sender 0x14bf16d4c9842bf1EbF396e553477C66EB0a8A82, nonce 0, keyless-style
// signature (v=0x1b, r=0x539, s=0xfadf66b1e192785c). The reconstruction is
// verified in TestRecentRootDeploymentTx: hash, recovered sender and CREATE
// address all match the spec's claimed values.
var RecentRootDeploymentTxBytes = buildRecentRootDeploymentTx()

// RecentRootDeployer is the sender of the canonical deployment transaction
// (EIP-8272 "Deployment").
var RecentRootDeployer = common.HexToAddress("0x14bf16d4c9842bf1EbF396e553477C66EB0a8A82")

// RecentRootDeploymentTxHash is the hash of the canonical deployment
// transaction (EIP-8272 "Deployment").
var RecentRootDeploymentTxHash = common.HexToHash("0x56c2adbbfa3ba5dfe47adf48828f6c581134e39e14c3b80e06214de5e5875272")

// Domain separators (EIP-8272 "Constants").
var (
	// RecentRootEntryDomain is keccak256("RECENT_ROOT_ENTRY").
	RecentRootEntryDomain = crypto.Keccak256Hash([]byte("RECENT_ROOT_ENTRY"))
	// RecentRootStorageDomain is keccak256("RECENT_ROOT_STORAGE").
	RecentRootStorageDomain = crypto.Keccak256Hash([]byte("RECENT_ROOT_STORAGE"))
)

// buildRecentRootDeploymentTx assembles the canonical deployment transaction
// from the EIP-8272 parameter sheet.
func buildRecentRootDeploymentTx() []byte {
	bigHex := func(s string) *big.Int {
		v, ok := new(big.Int).SetString(s, 0)
		if !ok {
			panic("bad hex constant")
		}
		return v
	}
	tx := NewTx(&LegacyTx{
		Nonce:    0,
		GasPrice: bigHex("0xe8d4a51000"),
		Gas:      0x13d620,
		To:       nil,
		Value:    new(big.Int),
		Data:     RecentRootInitCode,
		V:        bigHex("0x1b"),
		R:        bigHex("0x539"),
		S:        bigHex("0xfadf66b1e192785c"),
	})
	blob, err := tx.MarshalBinary()
	if err != nil {
		panic(err)
	}
	return blob
}

// RecentRootSourceID derives the root source identifier:
//
//	source_id = keccak256(source_address || salt)
//
// (EIP-8272 "Root sources").
func RecentRootSourceID(source common.Address, salt common.Hash) common.Hash {
	var input [20 + 32]byte
	copy(input[:20], source[:])
	copy(input[20:], salt[:])
	return crypto.Keccak256Hash(input[:])
}

// RecentRootEntryHash computes the committed entry for (source_id, slot, root):
//
//	entry_hash = keccak256(RECENT_ROOT_ENTRY_DOMAIN || source_id ||
//	                       uint64_be(slot) || root)
//
// (EIP-8272 "Entry and storage keys").
func RecentRootEntryHash(sourceID common.Hash, slot uint64, root common.Hash) common.Hash {
	var input [32 + 32 + 8 + 32]byte
	copy(input[0:32], RecentRootEntryDomain[:])
	copy(input[32:64], sourceID[:])
	binary.BigEndian.PutUint64(input[64:72], slot)
	copy(input[72:104], root[:])
	return crypto.Keccak256Hash(input[:])
}

// RecentRootStorageKey computes the storage key of ring-buffer index i of a
// root source:
//
//	storage_key = keccak256(RECENT_ROOT_STORAGE_DOMAIN || source_id ||
//	                        uint64_be(i))
//
// (EIP-8272 "Entry and storage keys").
func RecentRootStorageKey(sourceID common.Hash, index uint64) common.Hash {
	var input [32 + 32 + 8]byte
	copy(input[0:32], RecentRootStorageDomain[:])
	copy(input[32:64], sourceID[:])
	binary.BigEndian.PutUint64(input[64:72], index)
	return crypto.Keccak256Hash(input[:])
}

// RecentRootIndex maps a slot to its ring-buffer index.
func RecentRootIndex(slot uint64) uint64 { return slot % RecentRootLength }

// RecentRootTuple is one packed (source_id, slot, root) validation tuple
// (EIP-8272 "Validation operation").
type RecentRootTuple struct {
	SourceID common.Hash
	Slot     uint64
	Root     common.Hash
}

// StorageKey returns the storage key the tuple is (or would be) stored under.
func (t RecentRootTuple) StorageKey() common.Hash {
	return RecentRootStorageKey(t.SourceID, RecentRootIndex(t.Slot))
}

// EntryHash returns the entry hash the tuple must match in storage.
func (t RecentRootTuple) EntryHash() common.Hash {
	return RecentRootEntryHash(t.SourceID, t.Slot, t.Root)
}

// Errors returned by ParseRecentRootTuples.
var (
	ErrRecentRootDataEmpty   = errors.New("recent root validation data empty")
	ErrRecentRootDataLength  = errors.New("recent root validation data length not a multiple of 72")
	ErrRecentRootDataTooLong = errors.New("recent root validation data has more than 16 tuples")
)

// ParseRecentRootTuples decodes the validation-operation calldata: n packed
// tuples of 72 bytes each (source_id || uint64_be(slot) || root), no selector
// or length prefix (EIP-8272 "Validation operation"). The 64-byte write
// encoding is rejected here (it is not a multiple of 72); empty input, lengths
// not divisible by 72, and more than MAX_RECENT_ROOT_REFERENCES tuples are
// rejected.
func ParseRecentRootTuples(data []byte) ([]RecentRootTuple, error) {
	if len(data) == 0 {
		return nil, ErrRecentRootDataEmpty
	}
	if len(data)%RecentRootTupleBytes != 0 {
		return nil, ErrRecentRootDataLength
	}
	n := len(data) / RecentRootTupleBytes
	if n > MaxRecentRootReferences {
		return nil, ErrRecentRootDataTooLong
	}
	tuples := make([]RecentRootTuple, n)
	for i := range tuples {
		t := &tuples[i]
		off := i * RecentRootTupleBytes
		copy(t.SourceID[:], data[off:off+32])
		t.Slot = binary.BigEndian.Uint64(data[off+32 : off+40])
		copy(t.Root[:], data[off+40:off+72])
	}
	return tuples, nil
}

// IsRecentRootVerifyFrame reports whether the frame has the exact shape of an
// EIP-8272 recent root verifier frame:
//
//	mode == VERIFY, target == RECENT_ROOT_ADDRESS, flags == 0, value == 0,
//	limits.state == 0, 72 <= len(data) <= 16*72, len(data) % 72 == 0.
func IsRecentRootVerifyFrame(frame *Frame) bool {
	if frame.Mode != FrameModeVerify {
		return false
	}
	if frame.Target == nil || *frame.Target != RecentRootAddress {
		return false
	}
	if frame.Flags != 0 {
		return false
	}
	if frame.Value != nil && !frame.Value.IsZero() {
		return false
	}
	if frame.Limits.State != 0 {
		return false
	}
	if len(frame.Data) < RecentRootTupleBytes || len(frame.Data) > MaxRecentRootReferences*RecentRootTupleBytes {
		return false
	}
	return len(frame.Data)%RecentRootTupleBytes == 0
}

// IsExpiryVerifyFrame reports whether the frame is an EIP-8141 expiry
// verifier frame (VERIFY, target EXPIRY_VERIFIER, flags 0, no value, no state
// gas, 8 bytes of calldata).
func IsExpiryVerifyFrame(frame *Frame) bool {
	return frame.Mode == FrameModeVerify &&
		frame.Target != nil && *frame.Target == ExpiryVerifierAddress &&
		frame.Flags == 0 &&
		(frame.Value == nil || frame.Value.IsZero()) &&
		frame.Limits.State == 0 &&
		len(frame.Data) == ExpiryDataLength
}

// RecentRootVerifyFrameIndexes returns the indexes of all frames of the
// transaction that match the recent root verifier frame shape.
func RecentRootVerifyFrameIndexes(tx *FrameTx) []int {
	var idx []int
	for i := range tx.Frames {
		if IsRecentRootVerifyFrame(&tx.Frames[i]) {
			idx = append(idx, i)
		}
	}
	return idx
}
