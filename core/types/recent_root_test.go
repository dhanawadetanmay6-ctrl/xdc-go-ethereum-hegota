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
	"bytes"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
)

// The official EIP-8272 reference vector (spec "Reference vector",
// current_slot = 2). These values MUST NOT be modified to fit the
// implementation.
var (
	refSourceAddress = common.HexToAddress("0x0000000000000000000000000000000000000001")
	refSalt          = common.Hash{}
	refSourceID      = common.HexToHash("0xb9382d35273c75a50631a3e84d3c75ec9266e2b18c35a627e16cdbf26a18ca85")
	refSlot          = uint64(1)
	refRoot          = common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000002")
	refEntryHash     = common.HexToHash("0x0a0d1254c851be5a133b4c9a9e300f5602fc0f43dbe65aa6a66930d4ca0a51b8")
	refStorageKey    = common.HexToHash("0x5f027aa1cbe2df279bf6518edd4b44ea5409fd800189ec35224e10ab05e574c3")
	refCalldata      = common.FromHex("0xb9382d35273c75a50631a3e84d3c75ec9266e2b18c35a627e16cdbf26a18ca8500000000000000010000000000000000000000000000000000000000000000000000000000000002")
)

func TestRecentRootConstants(t *testing.T) {
	if RecentRootLength != 8192 {
		t.Fatalf("RECENT_ROOT_LENGTH = %d", RecentRootLength)
	}
	if RecentRootUsableWindow != 8191 {
		t.Fatalf("RECENT_ROOT_USABLE_WINDOW = %d", RecentRootUsableWindow)
	}
	if MaxRecentRootReferences != 16 {
		t.Fatalf("MAX_RECENT_ROOT_REFERENCES = %d", MaxRecentRootReferences)
	}
	if RecentRootTupleBytes != 72 {
		t.Fatalf("RECENT_ROOT_TUPLE_BYTES = %d", RecentRootTupleBytes)
	}
	if RecentRootAddress != common.HexToAddress("0x8272D9679689Ea2f307140CdF9002D27dC00Ffff") {
		t.Fatalf("RECENT_ROOT_ADDRESS = %v", RecentRootAddress)
	}
	// Domain constants must equal the keccak digests embedded in the
	// canonical bytecode.
	if RecentRootEntryDomain != common.HexToHash("0x8f42481679c8e6fefa040974b3c905e0ce3f2e464ba93acdb074a41181617efc") {
		t.Fatalf("RECENT_ROOT_ENTRY_DOMAIN = %v", RecentRootEntryDomain)
	}
	if RecentRootStorageDomain != common.HexToHash("0xbdc897da2177d260ff5f4be5d4b2aad43f89c3347a305b584fa5a2546d053daa") {
		t.Fatalf("RECENT_ROOT_STORAGE_DOMAIN = %v", RecentRootStorageDomain)
	}
}

// TestRecentRootCodeMatchesSpec pins the canonical runtime code byte-for-byte
// against the EIP-8272 "Bytecode" section.
func TestRecentRootCodeMatchesSpec(t *testing.T) {
	want := common.FromHex("0x346100ba57366040146100c05736604836066100ba5780156100ba5761048081116100ba574b60005b602081013560c01c828110156100ba5780830361200011156100ba577f8f42481679c8e6fefa040974b3c905e0ce3f2e464ba93acdb074a41181617efc60005260488260203760686000207fbdc897da2177d260ff5f4be5d4b2aad43f89c3347a305b584fa5a2546d053daa60005290611fff1660c01b60405260486000205414156100ba5760480182811061002857005b60006000fd5b33600052602060006020376034600c20807f8f42481679c8e6fefa040974b3c905e0ce3f2e464ba93acdb074a41181617efc6040524b606852606052602060206088376068604020817fbdc897da2177d260ff5f4be5d4b2aad43f89c3347a305b584fa5a2546d053daa60a852611fff4b1660d05260c852604860a8205500")
	if !bytes.Equal(RecentRootCode, want) {
		t.Fatalf("RECENT_ROOT_CODE mismatch:\nhave %x\nwant %x", RecentRootCode, want)
	}
	if len(RecentRootCode) != 320 {
		t.Fatalf("RECENT_ROOT_CODE length = %d, want 320", len(RecentRootCode))
	}
	// The two domain constants must be embedded in the bytecode.
	if !bytes.Contains(RecentRootCode, RecentRootEntryDomain[:]) {
		t.Fatal("RECENT_ROOT_CODE does not embed RECENT_ROOT_ENTRY_DOMAIN")
	}
	if !bytes.Contains(RecentRootCode, RecentRootStorageDomain[:]) {
		t.Fatal("RECENT_ROOT_CODE does not embed RECENT_ROOT_STORAGE_DOMAIN")
	}
}

// TestRecentRootReferenceVectorDerivations checks the derivation functions
// against the official reference vector.
func TestRecentRootReferenceVectorDerivations(t *testing.T) {
	if got := RecentRootSourceID(refSourceAddress, refSalt); got != refSourceID {
		t.Fatalf("source_id:\nhave %v\nwant %v", got, refSourceID)
	}
	if got := RecentRootEntryHash(refSourceID, refSlot, refRoot); got != refEntryHash {
		t.Fatalf("entry_hash:\nhave %v\nwant %v", got, refEntryHash)
	}
	if got := RecentRootStorageKey(refSourceID, refSlot%RecentRootLength); got != refStorageKey {
		t.Fatalf("storage_key:\nhave %v\nwant %v", got, refStorageKey)
	}
	tuple := RecentRootTuple{SourceID: refSourceID, Slot: refSlot, Root: refRoot}
	if tuple.StorageKey() != refStorageKey || tuple.EntryHash() != refEntryHash {
		t.Fatal("tuple helper mismatch")
	}
	// The 72-byte validation calldata of the vector must parse to the tuple.
	tuples, err := ParseRecentRootTuples(refCalldata)
	if err != nil {
		t.Fatalf("ParseRecentRootTuples: %v", err)
	}
	if len(tuples) != 1 || tuples[0] != tuple {
		t.Fatalf("parsed %+v, want %+v", tuples, []RecentRootTuple{tuple})
	}
}

func TestParseRecentRootTuples(t *testing.T) {
	// Empty.
	if _, err := ParseRecentRootTuples(nil); err != ErrRecentRootDataEmpty {
		t.Fatalf("empty: got %v", err)
	}
	// 71 bytes: not a multiple.
	if _, err := ParseRecentRootTuples(make([]byte, 71)); err != ErrRecentRootDataLength {
		t.Fatalf("71 bytes: got %v", err)
	}
	// 73 bytes: not a multiple.
	if _, err := ParseRecentRootTuples(make([]byte, 73)); err != ErrRecentRootDataLength {
		t.Fatalf("73 bytes: got %v", err)
	}
	// 64 bytes (write encoding): not a valid validation payload.
	if _, err := ParseRecentRootTuples(make([]byte, 64)); err != ErrRecentRootDataLength {
		t.Fatalf("64 bytes: got %v", err)
	}
	// 17 tuples: too many.
	if _, err := ParseRecentRootTuples(make([]byte, 17*RecentRootTupleBytes)); err != ErrRecentRootDataTooLong {
		t.Fatalf("17 tuples: got %v", err)
	}
	// 16 tuples: ok, big-endian slot decoding.
	data := make([]byte, 16*RecentRootTupleBytes)
	for i := 0; i < 16; i++ {
		data[i*72+32+7] = byte(i + 1) // slot = i+1 (big-endian low byte)
	}
	tuples, err := ParseRecentRootTuples(data)
	if err != nil {
		t.Fatalf("16 tuples: %v", err)
	}
	for i, tup := range tuples {
		if tup.Slot != uint64(i+1) {
			t.Fatalf("tuple %d slot = %d", i, tup.Slot)
		}
	}
}

func TestIsRecentRootVerifyFrame(t *testing.T) {
	mk := func() *Frame {
		return &Frame{
			Mode:   FrameModeVerify,
			Target: &[]common.Address{RecentRootAddress}[0],
			Flags:  0,
			Limits: FrameGasLimits{Execution: 50_000, State: 0},
			Value:  new(uint256.Int),
			Data:   make([]byte, RecentRootTupleBytes),
		}
	}
	if !IsRecentRootVerifyFrame(mk()) {
		t.Fatal("canonical shape rejected")
	}
	// Wrong mode.
	f := mk()
	f.Mode = FrameModeDefault
	if IsRecentRootVerifyFrame(f) {
		t.Fatal("DEFAULT mode accepted")
	}
	// Wrong target.
	f = mk()
	other := common.HexToAddress("0xdead")
	f.Target = &other
	if IsRecentRootVerifyFrame(f) {
		t.Fatal("wrong target accepted")
	}
	// Nil target.
	f = mk()
	f.Target = nil
	if IsRecentRootVerifyFrame(f) {
		t.Fatal("nil target accepted")
	}
	// Nonzero flags.
	f = mk()
	f.Flags = 1
	if IsRecentRootVerifyFrame(f) {
		t.Fatal("flags=1 accepted")
	}
	// Nonzero value.
	f = mk()
	f.Value = uint256.NewInt(1)
	if IsRecentRootVerifyFrame(f) {
		t.Fatal("nonzero value accepted")
	}
	// Nonzero state gas.
	f = mk()
	f.Limits.State = 1
	if IsRecentRootVerifyFrame(f) {
		t.Fatal("nonzero state limit accepted")
	}
	// Data too short / invalid multiple / too long / upper bound.
	f = mk()
	f.Data = nil
	if IsRecentRootVerifyFrame(f) {
		t.Fatal("empty data accepted")
	}
	f = mk()
	f.Data = make([]byte, 71)
	if IsRecentRootVerifyFrame(f) {
		t.Fatal("71-byte data accepted")
	}
	f = mk()
	f.Data = make([]byte, 73)
	if IsRecentRootVerifyFrame(f) {
		t.Fatal("73-byte data accepted")
	}
	f = mk()
	f.Data = make([]byte, 17*RecentRootTupleBytes)
	if IsRecentRootVerifyFrame(f) {
		t.Fatal("17-tuple data accepted")
	}
	f = mk()
	f.Data = make([]byte, 16*RecentRootTupleBytes)
	if !IsRecentRootVerifyFrame(f) {
		t.Fatal("16-tuple data rejected")
	}
}

func TestRecentRootVerifyFrameIndexes(t *testing.T) {
	mk := func(dataLen int) Frame {
		return Frame{
			Mode:   FrameModeVerify,
			Target: &[]common.Address{RecentRootAddress}[0],
			Limits: FrameGasLimits{Execution: 50_000},
			Value:  new(uint256.Int),
			Data:   make([]byte, dataLen),
		}
	}
	tx := &FrameTx{Frames: []Frame{
		mk(72),
		{Mode: FrameModeSender, Limits: FrameGasLimits{Execution: 21000}, Value: new(uint256.Int)},
		mk(144),
	}}
	idx := RecentRootVerifyFrameIndexes(tx)
	if len(idx) != 2 || idx[0] != 0 || idx[1] != 2 {
		t.Fatalf("indexes %v", idx)
	}
}

// TestRecentRootDeploymentTx checks the canonical deployment transaction of
// EIP-8272 "Deployment": type, nonce, gas, input, hash, recovered sender and
// the derived CREATE address must all equal the spec's claimed values.
func TestRecentRootDeploymentTx(t *testing.T) {
	tx := new(Transaction)
	if err := tx.UnmarshalBinary(RecentRootDeploymentTxBytes); err != nil {
		t.Fatalf("decode deployment tx: %v", err)
	}
	if tx.Type() != LegacyTxType {
		t.Fatalf("deployment tx type = %d, want legacy", tx.Type())
	}
	if tx.To() != nil {
		t.Fatalf("deployment tx not a creation")
	}
	if tx.Nonce() != 0 || tx.Gas() != 0x13d620 {
		t.Fatalf("deployment tx nonce=%d gas=%d", tx.Nonce(), tx.Gas())
	}
	if !bytes.Equal(tx.Data(), RecentRootInitCode) {
		t.Fatal("deployment tx input != canonical init code")
	}
	if tx.Hash() != RecentRootDeploymentTxHash {
		t.Fatalf("deployment tx hash = %v, want %v", tx.Hash(), RecentRootDeploymentTxHash)
	}
	sender, err := Sender(HomesteadSigner{}, tx)
	if err != nil {
		t.Fatalf("sender recovery: %v", err)
	}
	if sender != RecentRootDeployer {
		t.Fatalf("deployer = %v, want %v", sender, RecentRootDeployer)
	}
	if got := crypto.CreateAddress(sender, 0); got != RecentRootAddress {
		t.Fatalf("deployed address = %v, want %v", got, RecentRootAddress)
	}
}
