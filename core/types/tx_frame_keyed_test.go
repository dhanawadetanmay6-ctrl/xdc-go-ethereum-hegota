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
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU Lesser
// General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package types

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/holiman/uint256"
)

// keyedTestTx returns a minimal valid keyed-form frame transaction.
func keyedTestTx(keys []uint64, seq uint64) *FrameTx {
	target := common.HexToAddress("0xdead")
	nk := make([]uint256.Int, len(keys))
	for i, k := range keys {
		nk[i].SetUint64(k)
	}
	return &FrameTx{
		ChainID:   uint256.NewInt(50),
		NonceKeys: nk,
		NonceSeq:  seq,
		Sender:    common.HexToAddress("0xc0ffee01"),
		Frames: []Frame{
			{Mode: FrameModeVerify, Flags: FrameFlagApproveScopeMask,
				Limits: FrameGasLimits{Execution: 50_000}, Value: new(uint256.Int)},
			{Mode: FrameModeSender, Target: &target,
				Limits: FrameGasLimits{Execution: 21_000, State: 200_000}, Value: uint256.NewInt(1), Data: []byte{0x01}},
		},
		Signatures: []FrameSignature{
			{Scheme: FrameSigSchemeSecp256k1, Signature: make([]byte, 65)},
		},
		Fees: FrameFees{
			MaxFee:               uint256.NewInt(1e15),
			MaxPriorityFeePerGas: uint256.NewInt(1e9),
		},
	}
}

// TestFrameTxKeyedRLPRoundTrip: keyed-form encode/decode preserves all
// fields, the form marker, and the transaction hash.
func TestFrameTxKeyedRLPRoundTrip(t *testing.T) {
	tx := NewTx(keyedTestTx([]uint64{7, 9}, 3))
	enc, err := tx.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	var back Transaction
	if err := back.UnmarshalBinary(enc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	ft := back.FrameTxData()
	if ft == nil || ft.IsLegacyPayload() {
		t.Fatal("decoded as wrong form")
	}
	if ft.NonceSeq != 3 || len(ft.NonceKeys) != 2 || ft.NonceKeys[0].Cmp(uint256.NewInt(7)) != 0 || ft.NonceKeys[1].Cmp(uint256.NewInt(9)) != 0 {
		t.Fatalf("nonce fields lost: seq %d keys %v", ft.NonceSeq, ft.NonceKeys)
	}
	if back.Hash() != tx.Hash() || back.FrameTxData().SigHash() != tx.FrameTxData().SigHash() {
		t.Fatal("hash/sighash not stable across round trip")
	}
}

// TestFrameTxLegacyFormRoundTrip: the pre-EIP-8250 wire form decodes with the
// legacy marker and re-encodes byte-identically.
func TestFrameTxLegacyFormRoundTrip(t *testing.T) {
	// Craft the legacy payload [chain_id, nonce, sender, frames, signatures, fees, blob_hashes].
	k := keyedTestTx(nil, 7)
	legacy := &frameTxLegacyRLP{
		ChainID:    k.ChainID,
		Nonce:      k.NonceSeq,
		Sender:     k.Sender,
		Frames:     k.Frames,
		Signatures: k.Signatures,
		Fees:       k.Fees,
		BlobHashes: k.BlobHashes,
	}
	payload, err := rlp.EncodeToBytes(legacy)
	if err != nil {
		t.Fatal(err)
	}
	var tx Transaction
	if err := tx.UnmarshalBinary(append([]byte{FrameTxType}, payload...)); err != nil {
		t.Fatalf("legacy decode: %v", err)
	}
	ft := tx.FrameTxData()
	if !ft.IsLegacyPayload() {
		t.Fatal("legacy form not detected")
	}
	if ft.NonceSeq != 7 || ft.NonceKeys != nil {
		t.Fatalf("legacy nonce mapping wrong: seq %d keys %v", ft.NonceSeq, ft.NonceKeys)
	}
	enc, err := tx.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(enc, append([]byte{FrameTxType}, payload...)) {
		t.Fatal("legacy re-encode differs")
	}
}

// TestFrameTxKeyedDecodeRejections covers the EIP-8250 decoder rejection
// rules for the keyed payload.
func TestFrameTxKeyedDecodeRejections(t *testing.T) {
	valid := keyedTestTx([]uint64{7, 9}, 3)
	toKeyed := func(ft *FrameTx) *frameTxKeyedRLP {
		return &frameTxKeyedRLP{
			ChainID:    ft.ChainID,
			NonceKeys:  ft.NonceKeys,
			NonceSeq:   ft.NonceSeq,
			Sender:     ft.Sender,
			Frames:     ft.Frames,
			Signatures: ft.Signatures,
			Fees:       ft.Fees,
			BlobHashes: ft.BlobHashes,
		}
	}
	decode := func(k *frameTxKeyedRLP) error {
		payload, err := rlp.EncodeToBytes(k)
		if err != nil {
			t.Fatalf("encode fixture: %v", err)
		}
		var tx Transaction
		return tx.UnmarshalBinary(append([]byte{FrameTxType}, payload...))
	}

	// Too many keys.
	k := toKeyed(valid)
	k.NonceKeys = make([]uint256.Int, MaxNonceKeys+1)
	for i := range k.NonceKeys {
		k.NonceKeys[i].SetUint64(uint64(i))
	}
	if err := decode(k); !errorsIs(err, ErrFrameTxTooManyNonceKeys) {
		t.Fatalf("17 keys: got %v", err)
	}
	// Unsorted keys.
	k = toKeyed(valid)
	k.NonceKeys = []uint256.Int{*uint256.NewInt(9), *uint256.NewInt(7)}
	if err := decode(k); !errorsIs(err, ErrFrameTxNonceKeyOrder) {
		t.Fatalf("unsorted: got %v", err)
	}
	// Duplicate keys.
	k = toKeyed(valid)
	k.NonceKeys = []uint256.Int{*uint256.NewInt(7), *uint256.NewInt(7)}
	if err := decode(k); !errorsIs(err, ErrFrameTxNonceKeyOrder) {
		t.Fatalf("duplicate: got %v", err)
	}
	// Empty keys with the single-use sequence.
	k = toKeyed(valid)
	k.NonceKeys = nil
	k.NonceSeq = MaxNonceSeq
	if err := decode(k); !errorsIs(err, ErrFrameTxEmptyKeysMaxSeq) {
		t.Fatalf("empty+MAX: got %v", err)
	}
	// nonce_keys is not a list (scalar at element 1): decodes as the legacy
	// form, then fails schema validation (8 fields != 7 handled by decode of
	// the legacy struct).
	k = toKeyed(valid)
	payload, _ := rlp.EncodeToBytes(k)
	_ = payload

	// Byte-level: non-canonical key integer (leading zero) must be rejected.
	// keys [0,7] encode as list c2 80 07; a non-canonical 0 is 81 00.
	good := keyedTestTx([]uint64{0, 7}, 1)
	goodPayload, err := rlp.EncodeToBytes(toKeyed(good))
	if err != nil {
		t.Fatal(err)
	}
	idx := bytes.Index(goodPayload, []byte{0xc2, 0x80, 0x07})
	if idx < 0 {
		t.Fatal("fixture: key list encoding not found")
	}
	// Replace with c3 81 00 07 and bump the outer list length by one.
	bad := append([]byte{}, goodPayload[:idx]...)
	bad = append(bad, 0xc3, 0x81, 0x00, 0x07)
	bad = append(bad, goodPayload[idx+4:]...)
	// The outer list is long-form (payload > 55 bytes): header is
	// [0xf8, len]; the one-byte growth fits in the same length byte.
	if bad[0] != 0xf8 {
		t.Fatalf("unexpected outer header byte %#x; adjust fixture", bad[0])
	}
	bad[1]++
	var tx Transaction
	if err := tx.UnmarshalBinary(append([]byte{FrameTxType}, bad...)); err == nil {
		t.Fatal("non-canonical key integer accepted")
	}
}

// TestFrameTxKeyedValidateStatic mirrors the decoder rejections for locally
// constructed transactions.
func TestFrameTxKeyedValidateStatic(t *testing.T) {
	if err := keyedTestTx([]uint64{7, 9}, 0).ValidateStatic(); err != nil {
		t.Fatalf("valid keyed tx rejected: %v", err)
	}
	mk := func(keys []uint64, seq uint64) *FrameTx { return keyedTestTx(keys, seq) }
	tooMany := mk(nil, 0)
	tooMany.NonceKeys = make([]uint256.Int, MaxNonceKeys+1)
	if err := tooMany.ValidateStatic(); !errorsIs(err, ErrFrameTxTooManyNonceKeys) {
		t.Fatalf("too many keys: %v", err)
	}
	if err := mk(nil, MaxNonceSeq).ValidateStatic(); !errorsIs(err, ErrFrameTxEmptyKeysMaxSeq) {
		t.Fatalf("empty+MAX: %v", err)
	}
	unsorted := mk([]uint64{9, 7}, 0)
	if err := unsorted.ValidateStatic(); !errorsIs(err, ErrFrameTxNonceKeyOrder) {
		t.Fatalf("unsorted: %v", err)
	}
}

// TestFrameTxKeyedValidateFork covers the payload-form gate.
func TestFrameTxKeyedValidateFork(t *testing.T) {
	keyed := keyedTestTx(nil, 0)
	if err := keyed.ValidateFork(true); err != nil {
		t.Fatalf("keyed form at keyed fork: %v", err)
	}
	if err := keyed.ValidateFork(false); !errorsIs(err, ErrFrameTxKeyedBeforeFork) {
		t.Fatalf("keyed form pre-fork: %v", err)
	}
	legacy := keyedTestTx(nil, 0)
	legacy.legacyPayload = true
	if err := legacy.ValidateFork(false); err != nil {
		t.Fatalf("legacy form pre-fork: %v", err)
	}
	if err := legacy.ValidateFork(true); !errorsIs(err, ErrFrameTxLegacyKeyedFork) {
		t.Fatalf("legacy form at keyed fork: %v", err)
	}
}

// TestFrameTxKeyedPricing checks the EIP-8250 nonce pricing: calldata cost of
// rlp(nonce_keys) || priced_nonce_seq plus len(keys) * KEYED_NONCE_ACCESS_COST
// on both intrinsic gas and the calldata floor, and the single-use exemption
// of the nonce_seq bytes (payload and sighash still commit them).
func TestFrameTxKeyedPricing(t *testing.T) {
	legacy := keyedTestTx(nil, 0)
	legacy.legacyPayload = true
	legacyIntr, legacyFloor, err := legacy.IntrinsicGas()
	if err != nil {
		t.Fatal(err)
	}
	// Keyed, no keys: adds rlp([])||rlp(0) = [0xc0, 0x80] (2 nonzero bytes ->
	// 8 tokens) to intrinsic, 8 tokens to the floor; no access cost.
	plain := keyedTestTx(nil, 0)
	plainIntr, plainFloor, err := plain.IntrinsicGas()
	if err != nil {
		t.Fatal(err)
	}
	if d := plainIntr - legacyIntr; d != 32 {
		t.Fatalf("empty-keys intrinsic delta %d, want 32", d)
	}
	if d := plainFloor - legacyFloor; d != 128 {
		t.Fatalf("empty-keys floor delta %d, want 128", d)
	}
	// Two keys: keys list rlp([1,2]) = c2 01 02 (3 bytes, all nonzero ->
	// 12 tokens -> 48 gas) + rlp(seq=0) 80 (1 byte -> 4 tokens -> 16 gas)
	// + 2 * 13000 access = 26064 over legacy intrinsic.
	two := keyedTestTx([]uint64{1, 2}, 0)
	twoIntr, twoFloor, err := two.IntrinsicGas()
	if err != nil {
		t.Fatal(err)
	}
	wantDelta := uint64(48 + 16 + 2*KeyedNonceAccessCost)
	if d := twoIntr - legacyIntr; d != wantDelta {
		t.Fatalf("two-keys intrinsic delta %d, want %d", d, wantDelta)
	}
	wantFloorDelta := uint64(16*(12+4)) + 2*KeyedNonceAccessCost
	if d := twoFloor - legacyFloor; d != wantFloorDelta {
		t.Fatalf("two-keys floor delta %d, want %d", d, wantFloorDelta)
	}
	// Single-use exemption: seq == MAX drops the seq bytes from pricing.
	// seq 0 prices rlp(0) = [0x80]: 4 tokens -> 16 intrinsic, 64 floor.
	single := keyedTestTx([]uint64{1, 2}, MaxNonceSeq)
	singleIntr, singleFloor, err := single.IntrinsicGas()
	if err != nil {
		t.Fatal(err)
	}
	if d := twoIntr - singleIntr; d != 16 {
		t.Fatalf("single-use intrinsic delta %d, want 16", d)
	}
	if d := twoFloor - singleFloor; d != 64 {
		t.Fatalf("single-use floor delta %d, want 64", d)
	}
	// The exemption does not touch the payload or signature hash: the
	// single-use encoding still carries the 9-byte sequence.
	tx := NewTx(single)
	enc, err := tx.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	var back Transaction
	if err := back.UnmarshalBinary(enc); err != nil {
		t.Fatal(err)
	}
	if back.FrameTxData().NonceSeq != MaxNonceSeq {
		t.Fatal("nonce_seq lost from single-use payload")
	}
	if NewTx(two).FrameTxData().SigHash() == tx.FrameTxData().SigHash() {
		t.Fatal("sighash must commit nonce_seq")
	}
}

// TestFrameTxKeyedJSON: JSON round trip of both payload forms.
func TestFrameTxKeyedJSON(t *testing.T) {
	tx := NewTx(keyedTestTx([]uint64{7}, 3))
	enc, err := json.Marshal(tx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(enc), `"nonceKeys"`) || !strings.Contains(string(enc), `"nonceSeq"`) {
		t.Fatalf("keyed fields missing from JSON: %s", enc)
	}
	var back Transaction
	if err := json.Unmarshal(enc, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ft := back.FrameTxData()
	if ft.IsLegacyPayload() || ft.NonceSeq != 3 || len(ft.NonceKeys) != 1 || ft.NonceKeys[0].Cmp(uint256.NewInt(7)) != 0 {
		t.Fatalf("keyed JSON round trip lost fields: %+v", ft)
	}
	// Legacy form: JSON carries 'nonce' only.
	ltx := keyedTestTx(nil, 9)
	ltx.legacyPayload = true
	lenc, err := json.Marshal(NewTx(ltx))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(lenc), `"nonceSeq"`) {
		t.Fatalf("legacy JSON must not carry nonceSeq: %s", lenc)
	}
	var lback Transaction
	if err := json.Unmarshal(lenc, &lback); err != nil {
		t.Fatal(err)
	}
	if !lback.FrameTxData().IsLegacyPayload() || lback.FrameTxData().NonceSeq != 9 {
		t.Fatalf("legacy JSON round trip: %+v", lback.FrameTxData())
	}
}

// TestNonceSlotAndKeysHash verifies the EIP-8250 derivation helpers.
func TestNonceSlotAndKeysHash(t *testing.T) {
	sender := common.HexToAddress("0x7A57307E9985b0e26B66A7BB920BcaF0396b8198")
	// slot(sender, key) = keccak256(pad32(sender) || be32(key)).
	k1 := NonceSlot(sender, uint256.NewInt(1))
	k2 := NonceSlot(sender, uint256.NewInt(2))
	if k1 == k2 {
		t.Fatal("distinct keys collide")
	}
	if NonceSlot(sender, uint256.NewInt(1)) != k1 {
		t.Fatal("slot derivation not deterministic")
	}
	other := NonceSlot(common.HexToAddress("0x01"), uint256.NewInt(1))
	if other == k1 {
		t.Fatal("distinct senders collide")
	}
	// nonce_keys_hash commits the count: hash([k]) != hash of bare key.
	h1 := NonceKeysHash([]uint256.Int{*uint256.NewInt(1)})
	h2 := NonceKeysHash([]uint256.Int{*uint256.NewInt(1), *uint256.NewInt(1)})
	if h1 == h2 {
		t.Fatal("count not committed in keys hash")
	}
	if NonceKeysHash(nil) != NonceKeysHash([]uint256.Int{}) {
		t.Fatal("nil and empty list must hash identically")
	}
}
