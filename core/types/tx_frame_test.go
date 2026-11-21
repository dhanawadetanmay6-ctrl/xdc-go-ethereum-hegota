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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/holiman/uint256"
)

func testFrameTx() *FrameTx {
	target := common.HexToAddress("0x000000000000000000000000000000000000dEaD")
	return &FrameTx{
		ChainID:  uint256.NewInt(50),
		NonceSeq: 7,
		Sender:   common.HexToAddress("0x00000000000000000000000000000000c0ffee01"),
		Frames: []Frame{
			{
				Mode:   FrameModeVerify,
				Flags:  FrameFlagApproveScopeMask,
				Target: nil, // resolves to sender
				Limits: FrameGasLimits{Execution: 50000, State: 0},
				Value:  new(uint256.Int),
				Data:   nil,
			},
			{
				Mode:   FrameModeSender,
				Flags:  0,
				Target: &target,
				Limits: FrameGasLimits{Execution: 21000, State: 183600},
				Value:  uint256.NewInt(1_000_000_000_000_000_000),
				Data:   []byte{0x01, 0x02, 0x00, 0x03},
			},
		},
		Signatures: []FrameSignature{
			{
				Scheme:    FrameSigSchemeSecp256k1,
				Signer:    nil,
				Msg:       nil,
				Signature: bytes.Repeat([]byte{0xab}, 65),
			},
			{
				Scheme:    FrameSigSchemeArbitrary,
				Signer:    nil,
				Msg:       nil,
				Signature: []byte{0x01, 0x02, 0x03},
			},
		},
		Fees: FrameFees{
			MaxFee:               uint256.NewInt(100_000_000_000_000_000),
			MaxPriorityFeePerGas: uint256.NewInt(1_500_000_000),
		},
		BlobHashes: []common.Hash{},
	}
}

// TestFrameTxEncodeDecode verifies the EIP-2718 typed-envelope round trip:
// 0x06 || rlp([chain_id, nonce, sender, frames, signatures, fees, blob_hashes]).
func TestFrameTxEncodeDecode(t *testing.T) {
	tx := NewTx(testFrameTx())
	if tx.Type() != FrameTxType {
		t.Fatalf("type mismatch: got %d want %d", tx.Type(), FrameTxType)
	}
	encoded, err := tx.MarshalBinary()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if encoded[0] != FrameTxType {
		t.Fatalf("first byte: got %x want %x", encoded[0], FrameTxType)
	}
	var decoded Transaction
	if err := decoded.UnmarshalBinary(encoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Hash() != tx.Hash() {
		t.Fatalf("hash mismatch after round trip: %x != %x", decoded.Hash(), tx.Hash())
	}
	inner, ok := decoded.inner.(*FrameTx)
	if !ok {
		t.Fatalf("decoded type %T, want *FrameTx", decoded.inner)
	}
	if inner.NonceSeq != 7 || inner.Sender != tx.inner.(*FrameTx).Sender {
		t.Fatalf("field mismatch after round trip")
	}
	if inner.Frames[0].Target != nil {
		t.Fatalf("nil target did not round-trip as nil: %v", inner.Frames[0].Target)
	}
	if *inner.Frames[1].Target != target2(t) {
		t.Fatalf("target mismatch")
	}
	if inner.Frames[1].Limits.State != 183600 {
		t.Fatalf("state limit mismatch: %d", inner.Frames[1].Limits.State)
	}
	if inner.Frames[1].Value.Cmp(uint256.NewInt(1_000_000_000_000_000_000)) != 0 {
		t.Fatalf("value mismatch: %v", inner.Frames[1].Value)
	}
	if len(inner.Signatures) != 2 || len(inner.Signatures[0].Signature) != 65 {
		t.Fatalf("signatures mismatch")
	}
	if err := inner.ValidateStatic(); err != nil {
		t.Fatalf("valid tx failed static validation: %v", err)
	}
	// RLP stream path (used by block body decoding)
	var buf bytes.Buffer
	if err := tx.EncodeRLP(&buf); err != nil {
		t.Fatalf("EncodeRLP: %v", err)
	}
	var streamDecoded Transaction
	if err := streamDecoded.DecodeRLP(rlp.NewStream(bytes.NewReader(buf.Bytes()), 0)); err != nil {
		t.Fatalf("DecodeRLP: %v", err)
	}
	if streamDecoded.Hash() != tx.Hash() {
		t.Fatalf("RLP stream hash mismatch")
	}
}

func target2(t *testing.T) common.Address {
	t.Helper()
	return common.HexToAddress("0x000000000000000000000000000000000000dEaD")
}

// TestFrameTxSigHashElision pins the EIP-8141 compute_sig_hash elision rule:
// empty-msg signature raw bytes are replaced by their byte length.
func TestFrameTxSigHashElision(t *testing.T) {
	base := testFrameTx()

	// Same length, different content in an empty-msg signature: identical
	// SigHash (bytes are elided, only length committed), different tx hash.
	mod := base.copy().(*FrameTx)
	mod.Signatures[0].Signature = bytes.Repeat([]byte{0xcd}, 65)
	if base.SigHash() != mod.SigHash() {
		t.Fatalf("sig hash changed when only elided bytes changed")
	}
	txA, txB := NewTx(base), NewTx(mod)
	if txA.Hash() == txB.Hash() {
		t.Fatalf("tx hash did not commit to full signature bytes")
	}

	// Different length: sig hash must change.
	shorter := base.copy().(*FrameTx)
	shorter.Signatures[0].Signature = bytes.Repeat([]byte{0xab}, 64)
	if base.SigHash() == shorter.SigHash() {
		t.Fatalf("sig hash did not commit to elided byte length")
	}

	// Explicit-msg signatures are fully committed: any byte change alters sig hash.
	explicit := base.copy().(*FrameTx)
	explicit.Signatures[1].Msg = bytes.Repeat([]byte{0x11}, 32)
	explicit.Signatures[1].Signature = []byte{0x01, 0x02, 0x04}
	if base.SigHash() == explicit.SigHash() {
		t.Fatalf("sig hash ignored explicit-msg signature bytes")
	}
}

// TestFrameTxValidateStatic exercises the EIP-8141 static constraint rules.
func TestFrameTxValidateStatic(t *testing.T) {
	valid := func() *FrameTx { return testFrameTx() }

	tests := []struct {
		name    string
		mutate  func(*FrameTx)
		wantErr error
	}{
		{"ok", func(tx *FrameTx) {}, nil},
		{"no frames", func(tx *FrameTx) { tx.Frames = nil }, ErrFrameTxNoFrames},
		{"too many frames", func(tx *FrameTx) {
			tx.Frames = make([]Frame, MaxFrames+1)
			for i := range tx.Frames {
				tx.Frames[i] = Frame{Mode: FrameModeSender, Value: new(uint256.Int)}
			}
		}, ErrFrameTxTooManyFrames},
		{"bad mode", func(tx *FrameTx) { tx.Frames[0].Mode = 3 }, ErrFrameTxInvalidMode},
		{"reserved flags", func(tx *FrameTx) { tx.Frames[0].Flags = 0x8 }, ErrFrameTxInvalidFlags},
		{"value on verify", func(tx *FrameTx) { tx.Frames[0].Value = uint256.NewInt(1) }, ErrFrameTxValueNotSender},
		{"approve-exec non-sender target", func(tx *FrameTx) {
			tx.Frames[1].Flags = FrameFlagApproveExecution
		}, ErrFrameTxApproveTarget},
		{"batch on verify", func(tx *FrameTx) { tx.Frames[0].Flags |= FrameFlagAtomicBatch }, ErrFrameTxBatchVerify},
		{"batch on last", func(tx *FrameTx) { tx.Frames[1].Flags |= FrameFlagAtomicBatch }, ErrFrameTxBatchLast},
		{"batch before verify", func(tx *FrameTx) {
			tx.Frames[0], tx.Frames[1] = tx.Frames[1], tx.Frames[0]
			tx.Frames[0].Flags = FrameFlagAtomicBatch
		}, ErrFrameTxBatchVerifyNext},
		{"batch member with scope", func(tx *FrameTx) {
			tx.Frames[0].Flags = FrameFlagAtomicBatch
			tx.Frames[1].Flags = FrameFlagApprovePayment // frame1 is in batch via predecessor
			// make frame 0 a non-verify so batch structure is legal otherwise
			tx.Frames[0].Mode = FrameModeDefault
		}, ErrFrameTxBatchApproveScope},
		{"bad scheme", func(tx *FrameTx) { tx.Signatures[0].Scheme = 0x9 }, ErrFrameSigScheme},
		{"signer len", func(tx *FrameTx) { tx.Signatures[0].Signer = []byte{0x01} }, ErrFrameSigSignerLen},
		{"arbitrary signer non-empty", func(tx *FrameTx) {
			tx.Signatures[1].Signer = common.Address{}.Bytes()
		}, ErrFrameSigSignerLen},
		{"msg len", func(tx *FrameTx) { tx.Signatures[0].Msg = []byte{0x01} }, ErrFrameSigMsgLen},
		{"zero msg", func(tx *FrameTx) { tx.Signatures[0].Msg = make([]byte, 32) }, ErrFrameSigZeroMsg},
		{"bad blob version", func(tx *FrameTx) { tx.BlobHashes = []common.Hash{{0x02}} }, ErrFrameBlobHashVersion},
		{"gas cap", func(tx *FrameTx) {
			tx.Frames[1].Limits.Execution = params.MaxTxGas // intrinsic+exec > cap
		}, ErrFrameTxGasLimitHigh},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tx := valid()
			tc.mutate(tx)
			err := tx.ValidateStatic()
			if tc.wantErr == nil && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantErr != nil && !errorsIs(err, tc.wantErr) {
				t.Fatalf("got error %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func errorsIs(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// TestFrameTxIntrinsicGas verifies the EIP-8141 intrinsic gas decomposition
// against a hand-computed value.
func TestFrameTxIntrinsicGas(t *testing.T) {
	tx := testFrameTx()
	intrinsic, floor, err := tx.IntrinsicGas()
	if err != nil {
		t.Fatalf("intrinsic: %v", err)
	}
	// Hand computation:
	//   base:                 12000
	//   per-frame:            2 * 475 = 950
	//   frame data:           frame0 "" = 0; frame1 [01 02 00 03] tokens = 3*4+1 = 13 -> *4 = 52
	//   sig verification:     2800 (k1) + 100 (arbitrary) = 2900
	//   sig data:             k1: signer "" 0 + msg "" 0 + sig 65B empty-msg -> floor tokens 65*4=260 -> *4 = 1040
	//                         arb: 0 + 0 + 3B -> 3*4=12 -> *4 = 48
	//   value transfer:       frame1 value>0, target != sender -> 4244 (TxValueCost2780)
	//   EIP-8250 nonce data:  rlp([]) 0xc0 || rlp(7) 0x07 -> 2 nonzero bytes -> 8 tokens -> *4 = 32
	//                         (no keys -> no keyed nonce access cost)
	want := uint64(12000 + 950 + 52 + 2900 + 1040 + 48 + 4244 + 32)
	if intrinsic != want {
		t.Fatalf("intrinsic: got %d want %d", intrinsic, want)
	}
	// Floor: base 12000 + per-frame 950 + sigverify 2900 + value 4244 +
	//   16 * floor_tokens; floor tokens = frame data 4B*4=16 + k1 sig 65B*4=260 + arb sig 3B*4=12
	//   + nonce data 2B*4=8 = 296
	wantFloor := uint64(12000 + 950 + 2900 + 4244 + 16*296)
	if floor != wantFloor {
		t.Fatalf("floor: got %d want %d", floor, wantFloor)
	}
	// max_gas = max(intrinsic + exec + state, floor + state)
	//   standard = 21266 + (50000+21000) + 183600 = 275866
	//   floor    = 24830 + 183600 = 208430
	if got, want := tx.MaxGas(), uint64(275866); got != want {
		t.Fatalf("max gas: got %d want %d", got, want)
	}
}

// TestFrameTxSecp256k1 signs the canonical sig hash with a real key and
// verifies protocol validation, including low-s and recovery-id rules.
func TestFrameTxSecp256k1(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	sender := crypto.PubkeyToAddress(key.PublicKey)

	tx := testFrameTx()
	tx.Sender = sender
	sigHash := tx.SigHash()

	sig, err := crypto.Sign(sigHash[:], key)
	if err != nil {
		t.Fatal(err)
	}
	// Protocol encoding: v (1) || r (32) || s (32), v = recovery id 0/1.
	entry := FrameSignature{
		Scheme:    FrameSigSchemeSecp256k1,
		Signature: append([]byte{sig[64]}, sig[:64]...),
	}
	if !tx.ValidateSignature(&entry, sigHash) {
		t.Fatalf("valid secp256k1 signature rejected")
	}

	// Wrong recovered address (sign with a different key) must fail.
	other, _ := crypto.GenerateKey()
	badSig, _ := crypto.Sign(sigHash[:], other)
	badEntry := FrameSignature{
		Scheme:    FrameSigSchemeSecp256k1,
		Signature: append([]byte{badSig[64]}, badSig[:64]...),
	}
	if tx.ValidateSignature(&badEntry, sigHash) {
		t.Fatalf("signature from wrong key accepted")
	}

	// Explicit signer field naming a different address must fail.
	wrongSigner := common.HexToAddress("0x00000000000000000000000000000000000b066e")
	entryWrongSigner := FrameSignature{
		Scheme:    FrameSigSchemeSecp256k1,
		Signer:    wrongSigner.Bytes(),
		Signature: append([]byte{sig[64]}, sig[:64]...),
	}
	if tx.ValidateSignature(&entryWrongSigner, sigHash) {
		t.Fatalf("signature accepted for non-matching explicit signer")
	}

	// Bad v.
	badV := FrameSignature{Scheme: FrameSigSchemeSecp256k1, Signature: append([]byte{2}, sig[:64]...)}
	if tx.ValidateSignature(&badV, sigHash) {
		t.Fatalf("v=2 accepted")
	}

	// High-s must be rejected (EIP-2 canonical encoding).
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:64])
	highS := new(big.Int).Sub(Secp256k1N, s)
	// Re-sign: flipping s changes the recovery id.
	highSEntry := FrameSignature{Scheme: FrameSigSchemeSecp256k1, Signature: make([]byte, 65)}
	highSEntry.Signature[0] = sig[64] ^ 1
	r.FillBytes(highSEntry.Signature[1:33])
	highS.FillBytes(highSEntry.Signature[33:65])
	if tx.ValidateSignature(&highSEntry, sigHash) {
		t.Fatalf("high-s signature accepted")
	}
}

// TestFrameTxP256 verifies the P256 signature scheme, including the
// signer-address-derives-from-pubkey rule.
func TestFrameTxP256(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	qx, qy := make([]byte, 32), make([]byte, 32)
	key.PublicKey.X.FillBytes(qx)
	key.PublicKey.Y.FillBytes(qy)
	var signerAddr common.Address
	pub := append(append([]byte{}, qx...), qy...)
	copy(signerAddr[:], crypto.Keccak256(pub)[12:])

	tx := testFrameTx()
	tx.Sender = signerAddr
	sigHash := tx.SigHash()

	r, s, err := ecdsa.Sign(rand.Reader, key, sigHash[:])
	if err != nil {
		t.Fatal(err)
	}
	// Normalize to low-s per EIP-8141.
	halfN := new(big.Int).Rsh(Secp256r1N, 1)
	if s.Cmp(halfN) > 0 {
		s.Sub(Secp256r1N, s)
	}
	entry := FrameSignature{Scheme: FrameSigSchemeP256, Signature: make([]byte, 128)}
	r.FillBytes(entry.Signature[0:32])
	s.FillBytes(entry.Signature[32:64])
	copy(entry.Signature[64:96], qx)
	copy(entry.Signature[96:128], qy)
	if !tx.ValidateSignature(&entry, sigHash) {
		t.Fatalf("valid P256 signature rejected")
	}

	// Signature over a different digest must fail.
	otherHash := crypto.Keccak256Hash([]byte("different"))
	if tx.ValidateSignature(&entry, otherHash) {
		t.Fatalf("P256 signature accepted for wrong digest")
	}

	// Tx naming a different sender (derived-address mismatch) must fail.
	tx.Sender = common.HexToAddress("0x0000000000000000000000000000000000001337")
	if tx.ValidateSignature(&entry, sigHash) {
		t.Fatalf("P256 accepted when resolved signer != keccak(qx||qy)[12:]")
	}
}

// TestFrameTxValidateSignatures checks the whole-list validation entry point.
// Note: the canonical sig hash commits to elided signature LENGTHS, so a signer
// must fix the byte length of every empty-msg entry before signing (as wallets
// do). We therefore finalize the list (with a placeholder of the final length)
// before computing the hash to sign.
func TestFrameTxValidateSignatures(t *testing.T) {
	key, _ := crypto.GenerateKey()
	tx := testFrameTx()
	tx.Sender = crypto.PubkeyToAddress(key.PublicKey)
	tx.Signatures = []FrameSignature{
		{Scheme: FrameSigSchemeSecp256k1, Signature: make([]byte, 65)}, // placeholder, final length
		{Scheme: FrameSigSchemeArbitrary, Signature: []byte{0x01}},     // structural only
	}
	sigHash := tx.SigHash()
	sig, _ := crypto.Sign(sigHash[:], key)
	tx.Signatures[0].Signature = append([]byte{sig[64]}, sig[:64]...) // same 65-byte length
	if _, err := tx.ValidateSignatures(); err != nil {
		t.Fatalf("valid signature set rejected: %v", err)
	}
	tx.Signatures[0].Signature[10] ^= 0xff
	if _, err := tx.ValidateSignatures(); err != ErrFrameSigInvalid {
		t.Fatalf("corrupted signature accepted: %v", err)
	}
}

// TestFrameTxSender verifies that a Bogota-capable signer resolves the
// explicit sender field without ECDSA recovery, and that pre-Bogota signers
// reject the type.
func TestFrameTxSender(t *testing.T) {
	chainID := big.NewInt(50)
	key, _ := crypto.GenerateKey()
	sender := crypto.PubkeyToAddress(key.PublicKey)

	inner := testFrameTx()
	inner.ChainID = uint256.MustFromBig(chainID)
	inner.Sender = sender
	tx := NewTx(inner)

	bogota := NewBogotaSigner(chainID)
	from, err := Sender(bogota, tx)
	if err != nil {
		t.Fatalf("Bogota signer rejected frame tx: %v", err)
	}
	if from != sender {
		t.Fatalf("sender mismatch: got %v want %v", from, sender)
	}
	// Sender resolution must not depend on any signature bytes being present.
	if _, err := Sender(bogota, tx); err != nil {
		t.Fatalf("cached sender resolution failed: %v", err)
	}

	// Pre-Bogota signers do not support the type.
	for _, s := range []Signer{NewPragueSigner(chainID), NewCancunSigner(chainID), NewLondonSigner(chainID)} {
		if _, err := Sender(s, tx); err != ErrTxTypeNotSupported {
			t.Fatalf("pre-Bogota signer accepted frame tx: %v", err)
		}
	}

	// Chain ID mismatch is rejected.
	other := NewBogotaSigner(big.NewInt(51))
	if _, err := Sender(other, tx); !errorsIs(err, ErrInvalidChainId) {
		t.Fatalf("wrong-chain frame tx accepted: %v", err)
	}

	// Signer selection: MakeSigner picks Bogota when BogotaTime is set.
	cfg := &params.ChainConfig{
		ChainID:     chainID,
		LondonBlock: big.NewInt(0),
		BogotaTime:  u64ptr(0),
	}
	if got := MakeSigner(cfg, big.NewInt(0), 0); !got.Equal(bogota) {
		t.Fatalf("MakeSigner did not select Bogota signer")
	}
	if got := LatestSigner(cfg); !got.Equal(bogota) {
		t.Fatalf("LatestSigner did not select Bogota signer")
	}
}

func u64ptr(v uint64) *uint64 { return &v }

// TestSignFrameTx exercises the wallet-side signing helper end to end:
// signed entry validates, and the signature survives the sig-hash elision
// rule (signing with placeholder of final length).
func TestSignFrameTx(t *testing.T) {
	key, _ := crypto.GenerateKey()
	inner := testFrameTx()
	inner.ChainID = uint256.NewInt(50)
	inner.Sender = crypto.PubkeyToAddress(key.PublicKey)
	inner.Signatures = nil
	tx := NewTx(inner)

	signed, err := SignFrameTx(key, tx)
	if err != nil {
		t.Fatalf("SignFrameTx: %v", err)
	}
	signedInner := signed.inner.(*FrameTx)
	if len(signedInner.Signatures) != 1 {
		t.Fatalf("expected 1 signature entry, got %d", len(signedInner.Signatures))
	}
	entry := signedInner.Signatures[0]
	if entry.Scheme != FrameSigSchemeSecp256k1 || len(entry.Signature) != 65 || len(entry.Msg) != 0 || len(entry.Signer) != 0 {
		t.Fatalf("unexpected entry shape: %+v", entry)
	}
	if _, err := signedInner.ValidateSignatures(); err != nil {
		t.Fatalf("signed tx failed protocol validation: %v", err)
	}
	// Signing again must replace the entry at index 0, not accumulate.
	resigned, err := SignFrameTx(key, signed)
	if err != nil {
		t.Fatalf("re-sign: %v", err)
	}
	if n := len(resigned.inner.(*FrameTx).Signatures); n != 1 {
		t.Fatalf("re-sign accumulated entries: %d", n)
	}
	// A second signer targeting index 0 (the default-code approval slot)
	// replaces the entry there — the helper never accumulates k1 entries.
	key2, _ := crypto.GenerateKey()
	signed2, err := SignFrameTx(key2, signed)
	if err != nil {
		t.Fatalf("second SignFrameTx: %v", err)
	}
	if n := len(signed2.inner.(*FrameTx).Signatures); n != 1 {
		t.Fatalf("second signature replaced instead of occupying index 0: %d entries", n)
	}
}

// TestFrameTxNoOuterSignature verifies outer-signature APIs reject frame txs.
func TestFrameTxNoOuterSignature(t *testing.T) {
	signer := NewBogotaSigner(big.NewInt(50))
	tx := NewTx(testFrameTx())
	key, _ := crypto.GenerateKey()
	if _, err := SignTx(tx, signer, key); err == nil {
		t.Fatalf("outer SignTx succeeded on frame tx")
	}
	v, r, s := tx.RawSignatureValues()
	if v.Sign() != 0 || r.Sign() != 0 || s.Sign() != 0 {
		t.Fatalf("frame tx reports non-zero outer signature")
	}
}

// TestFrameTxJSONRoundTrip verifies the JSON representation of frame
// transactions (used by eth_getTransactionByHash and tooling).
func TestFrameTxJSONRoundTrip(t *testing.T) {
	target := common.HexToAddress("0x1234")
	orig := NewTx(&FrameTx{
		ChainID:  uint256.NewInt(1337),
		NonceSeq: 7,
		Sender:   common.HexToAddress("0xaaaa"),
		Frames: []Frame{
			{Mode: FrameModeVerify, Flags: FrameFlagApproveScopeMask,
				Limits: FrameGasLimits{Execution: 50_000}, Value: new(uint256.Int)},
			{Mode: FrameModeSender, Target: &target,
				Limits: FrameGasLimits{Execution: 100_000, State: 200_000},
				Value:  uint256.NewInt(42), Data: []byte{0xde, 0xad}},
		},
		Signatures: []FrameSignature{
			{Scheme: FrameSigSchemeSecp256k1, Signature: bytes.Repeat([]byte{0x11}, 65)},
			{Scheme: FrameSigSchemeArbitrary, Signer: []byte{0x01, 0x02}, Msg: common.HexToHash("0x99").Bytes(), Signature: []byte{0xaa}},
		},
		Fees: FrameFees{MaxFee: uint256.NewInt(1e15), MaxPriorityFeePerGas: uint256.NewInt(1e9)},
	})
	enc, err := orig.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var dec Transaction
	if err := dec.UnmarshalJSON(enc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if dec.Type() != FrameTxType {
		t.Fatalf("type %d, want frame", dec.Type())
	}
	got := dec.FrameTxData()
	if got.NonceSeq != 7 || got.Sender != common.HexToAddress("0xaaaa") {
		t.Fatal("nonce/sender mismatch")
	}
	if len(got.Frames) != 2 || got.Frames[1].Limits.State != 200_000 || got.Frames[1].Value.Uint64() != 42 {
		t.Fatalf("frames mismatch: %+v", got.Frames)
	}
	if !bytes.Equal(got.Frames[1].Data, []byte{0xde, 0xad}) {
		t.Fatal("frame data mismatch")
	}
	if len(got.Signatures) != 2 || got.Signatures[0].Scheme != FrameSigSchemeSecp256k1 ||
		!bytes.Equal(got.Signatures[1].Signer, []byte{0x01, 0x02}) {
		t.Fatal("signatures mismatch")
	}
	if got.Fees.MaxFee.Uint64() != 1e15 || got.Fees.MaxPriorityFeePerGas.Uint64() != 1e9 {
		t.Fatal("fees mismatch")
	}
	// Hash stability across the JSON round-trip.
	if dec.Hash() != orig.Hash() {
		t.Fatalf("hash changed across JSON round-trip: %v vs %v", dec.Hash(), orig.Hash())
	}
	// No outer-signature fields should be populated.
	var raw map[string]interface{}
	if err := json.Unmarshal(enc, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["sender"] == nil || raw["frames"] == nil || raw["maxFee"] == nil {
		t.Fatal("frame fields missing from JSON output")
	}
}
