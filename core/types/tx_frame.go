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
	"errors"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/crypto/kzg4844"
	"github.com/ethereum/go-ethereum/crypto/secp256r1"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/holiman/uint256"
)

// EIP-8141 frame transaction constants.
const (
	// FrameTxType is the EIP-2718 type byte of frame transactions.
	FrameTxType = 0x06

	// FrameTxIntrinsicCost is the base intrinsic cost of a frame transaction
	// (EIP-8141 FRAME_TX_INTRINSIC_COST).
	FrameTxIntrinsicCost = 12000

	// FrameTxPerFrameCost is charged per frame (EIP-8141 FRAME_TX_PER_FRAME_COST).
	FrameTxPerFrameCost = 475

	// MaxFrames bounds the number of frames per transaction (EIP-8141 MAX_FRAMES).
	MaxFrames = 64

	// Signature schemes (EIP-8141).
	FrameSigSchemeArbitrary = 0x0
	FrameSigSchemeSecp256k1 = 0x1
	FrameSigSchemeP256      = 0x2

	// Signature validation gas costs (EIP-8141).
	FrameSigGasArbitrary = 100
	FrameSigGasSecp256k1 = 2800
	FrameSigGasP256      = 6700

	// Frame modes (EIP-8141).
	FrameModeDefault = 0x0 // execute frame as ENTRY_POINT
	FrameModeVerify  = 0x1 // frame identifies as transaction validation
	FrameModeSender  = 0x2 // execute frame as tx.sender
	// FrameModeAssert is the EIP-7906 ASSERT frame mode: a read-only
	// (STATICCALL) trailing-suffix frame whose failure reverts the
	// transaction's execution body without invalidating the transaction.
	FrameModeAssert = 0x3

	// Frame flags (EIP-8141). Bits 0-1 are the approval scope bitmask, bit 2
	// is the atomic-batch flag, bits 3+ are reserved.
	FrameFlagApprovePayment   = 0x1
	FrameFlagApproveExecution = 0x2
	FrameFlagApproveScopeMask = 0x3
	FrameFlagAtomicBatch      = 0x4
	FrameFlagValidMask        = 0x7

	// ExpiryDataLength is the calldata length of an expiry verifier frame.
	ExpiryDataLength = 8
)

// EntryPoint is the caller of DEFAULT and VERIFY frames (EIP-8141 ENTRY_POINT).
var EntryPoint = common.BytesToAddress([]byte{0xaa})

// ExpiryVerifierAddress is the deployment address of the canonical expiry
// verifier contract (EIP-8141 EXPIRY_VERIFIER).
var ExpiryVerifierAddress = common.HexToAddress("0x81413f0cF12e9b6a49B1D0439E081c577D57FfFf")

// Secp256k1N and Secp256r1N are the curve orders used by signature validation.
var (
	Secp256k1N, _ = new(big.Int).SetString("fffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364141", 16)
	Secp256r1N, _ = new(big.Int).SetString("ffffffff00000000ffffffffffffffffbce6faada7179e84f3b9cac2fc632551", 16)
)

// FrameGasLimits carries the two-dimensional gas budget of a frame:
// [execution, state] (EIP-8141 `limits`).
type FrameGasLimits struct {
	Execution uint64
	State     uint64
}

// Frame is a single frame of a frame transaction:
// [mode, flags, target, limits, value, data].
//
// A nil Target resolves to the transaction sender at execution time.
type Frame struct {
	Mode   uint8
	Flags  uint8
	Target *common.Address `rlp:"nil"` // nil encodes as the empty string
	Limits FrameGasLimits
	Value  *uint256.Int
	Data   []byte
}

// FrameTxData returns the frame transaction payload of a Transaction, or nil
// when the transaction is not a frame transaction.
func (tx *Transaction) FrameTxData() *FrameTx {
	if ft, ok := tx.inner.(*FrameTx); ok {
		return ft
	}
	return nil
}

// Frame receipt status codes (EIP-8141). These are protocol constants; the
// vm package re-exports them for the execution layer.
const (
	FrameStatusFailed    = 0x0 // top-level call of the frame failed
	FrameStatusSucceeded = 0x1
	FrameStatusSkipped   = 0x2 // skipped due to a failed atomic batch
)

// FrameSignature is one entry of the transaction's signatures list:
// [scheme, signer, msg, signature].
//
//   - Signer is empty (protocol resolves to tx.sender) or a 20-byte address
//     for SECP256K1 and P256 schemes; it must be empty for ARBITRARY.
//   - Msg is empty (signature over the canonical transaction signature hash)
//     or an explicit 32-byte digest.
//   - Signature carries the raw scheme-dependent signature bytes.
type FrameSignature struct {
	Scheme    uint8
	Signer    []byte
	Msg       []byte
	Signature []byte
}

// FrameFees carries the fee parameters of a frame transaction:
// [max_fee, max_priority_fee_per_gas].
//
// MaxFee is the maximum total fee, in wei, the transaction is willing to pay
// for all of its execution, state, and blob gas. MaxPriorityFeePerGas is the
// EIP-1559 priority fee per gas.
type FrameFees struct {
	MaxFee               *uint256.Int
	MaxPriorityFeePerGas *uint256.Int
}

// FrameReceipt is the per-frame receipt entry of a frame transaction
// (EIP-8141): [status, [execution_gas_used, state_gas_used], logs].
//
// Status is the return code of the frame's top-level call: 0 failure,
// 1 success, 2 skipped due to a failed atomic batch. GasUsedState is the
// final state gas attributed to the frame after all state-gas refills and
// rollbacks in the transaction have been applied.
type FrameReceipt struct {
	Status           uint8
	GasUsedExecution uint64
	GasUsedState     uint64
	Logs             []*Log
}

// FrameTx implements the EIP-8141 frame transaction type:
//
//	[chain_id, nonce, sender, frames, signatures, fees, blob_versioned_hashes]
type FrameTx struct {
	ChainID    *uint256.Int
	Nonce      uint64
	Sender     common.Address
	Frames     []Frame
	Signatures []FrameSignature
	Fees       FrameFees
	BlobHashes []common.Hash
}

// copy creates a deep copy of the transaction data and initializes all fields.
func (tx *FrameTx) copy() TxData {
	cpy := &FrameTx{
		Nonce:      tx.Nonce,
		Sender:     tx.Sender,
		Frames:     make([]Frame, len(tx.Frames)),
		Signatures: make([]FrameSignature, len(tx.Signatures)),
		BlobHashes: make([]common.Hash, len(tx.BlobHashes)),
		ChainID:    new(uint256.Int),
		Fees: FrameFees{
			MaxFee:               new(uint256.Int),
			MaxPriorityFeePerGas: new(uint256.Int),
		},
	}
	copy(cpy.BlobHashes, tx.BlobHashes)
	for i, f := range tx.Frames {
		cpy.Frames[i] = Frame{
			Mode:   f.Mode,
			Flags:  f.Flags,
			Limits: f.Limits,
			Value:  new(uint256.Int),
			Data:   common.CopyBytes(f.Data),
		}
		if f.Target != nil {
			target := *f.Target
			cpy.Frames[i].Target = &target
		}
		if f.Value != nil {
			cpy.Frames[i].Value.Set(f.Value)
		}
	}
	for i, s := range tx.Signatures {
		cpy.Signatures[i] = FrameSignature{
			Scheme:    s.Scheme,
			Signer:    common.CopyBytes(s.Signer),
			Msg:       common.CopyBytes(s.Msg),
			Signature: common.CopyBytes(s.Signature),
		}
	}
	if tx.ChainID != nil {
		cpy.ChainID.Set(tx.ChainID)
	}
	if tx.Fees.MaxFee != nil {
		cpy.Fees.MaxFee.Set(tx.Fees.MaxFee)
	}
	if tx.Fees.MaxPriorityFeePerGas != nil {
		cpy.Fees.MaxPriorityFeePerGas.Set(tx.Fees.MaxPriorityFeePerGas)
	}
	return cpy
}

// accessors for TxData.
func (tx *FrameTx) txType() byte { return FrameTxType }

func (tx *FrameTx) chainID() *big.Int { return tx.ChainID.ToBig() }

// accessList returns nil: frame transactions carry no access list (EIP-8141).
func (tx *FrameTx) accessList() AccessList { return nil }

// data returns nil: a frame transaction has no single top-level calldata.
// Frame calldata is available via the Frames field.
func (tx *FrameTx) data() []byte { return nil }

// gas returns the transaction's total gas limit, max_gas as defined by
// EIP-8141 gas accounting: max(standard_gas_limit, calldata_floor_gas +
// sum(limits.state)). The value is fully determined by the transaction
// fields; arithmetic saturates on overflow (invalid transactions are
// rejected by ValidateFrameTxStatic before admission).
func (tx *FrameTx) gas() uint64 { return tx.MaxGas() }

// gasPrice reports the effective price per gas given the inclusion block's
// base fee. Without a base fee (pre-London paths) it reports the maximum
// effective price implied by the budget: max_fee / max_gas.
//
// NOTE: frame transactions have no per-gas fee cap; TxData-level accessors
// are derived values for generic interfaces (pool sorting, RPC), not fields.
func (tx *FrameTx) gasPrice() *big.Int { return tx.effectiveGasPrice(new(big.Int), nil) }

// gasTipCap returns the max priority fee per gas.
func (tx *FrameTx) gasTipCap() *big.Int { return tx.Fees.MaxPriorityFeePerGas.ToBig() }

// gasFeeCap returns the implied per-gas fee cap max_fee / max_gas (floor).
// This is a derived value: the protocol caps total spend at max_fee, not per
// gas.
func (tx *FrameTx) gasFeeCap() *big.Int {
	maxGas := tx.MaxGas()
	if maxGas == 0 {
		return new(big.Int)
	}
	return new(big.Int).Div(tx.Fees.MaxFee.ToBig(), new(big.Int).SetUint64(maxGas))
}

// value returns the total value transferred by all frames. Note value
// transfers are charged to tx.sender at execution time, frame by frame.
func (tx *FrameTx) value() *big.Int {
	total := new(big.Int)
	for _, f := range tx.Frames {
		if f.Value != nil {
			total.Add(total, f.Value.ToBig())
		}
	}
	return total
}

func (tx *FrameTx) nonce() uint64 { return tx.Nonce }

// to returns nil: a frame transaction has no single recipient.
func (tx *FrameTx) to() *common.Address { return nil }

// effectiveGasPrice computes the price per gas paid by the transaction given
// the inclusion block baseFee: base_fee + max_priority_fee_per_gas, capped so
// that the total charge cannot exceed max_fee (i.e. bounded by
// max_fee / max_gas when the budget does not cover the full tip).
func (tx *FrameTx) effectiveGasPrice(dst *big.Int, baseFee *big.Int) *big.Int {
	if baseFee == nil {
		return tx.gasFeeCap()
	}
	price := dst.Add(baseFee, tx.Fees.MaxPriorityFeePerGas.ToBig())
	if cap := tx.gasFeeCap(); price.Cmp(cap) > 0 {
		price.Set(cap)
	}
	return price
}

// rawSignatureValues returns zeros: a frame transaction carries no outer
// ECDSA signature. Its validated signatures live in the Signatures list.
func (tx *FrameTx) rawSignatureValues() (v, r, s *big.Int) {
	return new(big.Int), new(big.Int), new(big.Int)
}

// setSignatureValues is unsupported for frame transactions: there is no outer
// signature to set. It is a no-op kept for TxData interface conformance.
func (tx *FrameTx) setSignatureValues(chainID, v, r, s *big.Int) {}

func (tx *FrameTx) encode(b *bytes.Buffer) error {
	return rlp.Encode(b, tx)
}

func (tx *FrameTx) decode(input []byte) error {
	return rlp.DecodeBytes(input, tx)
}

// sigHash returns the canonical transaction signature hash
// (EIP-8141 compute_sig_hash): keccak(FRAME_TX_TYPE || rlp(tx')) where tx'
// is the transaction with the raw signature bytes of every empty-msg
// signature entry elided and replaced by their byte length. The passed
// chainID is ignored: the chain ID is part of the frame transaction payload.
func (tx *FrameTx) sigHash(chainID *big.Int) common.Hash {
	return tx.SigHash()
}

// SigHash returns the canonical transaction signature hash
// (EIP-8141 compute_sig_hash).
func (tx *FrameTx) SigHash() common.Hash {
	return prefixedRlpHash(FrameTxType, tx.sigHashPayload())
}

// sigHashPayload returns the RLP-encodable payload with elided signatures.
func (tx *FrameTx) sigHashPayload() []any {
	sigs := make([]any, len(tx.Signatures))
	for i, sig := range tx.Signatures {
		var sigField any = sig.Signature
		if len(sig.Msg) == 0 {
			// Elide the raw bytes, but commit to their length. The
			// replacement is RLP-encoded as an unsigned integer.
			sigField = uint64(len(sig.Signature))
		}
		sigs[i] = []any{sig.Scheme, sig.Signer, sig.Msg, sigField}
	}
	frames := make([]any, len(tx.Frames))
	for i, f := range tx.Frames {
		var target any = []byte{}
		if f.Target != nil {
			target = f.Target.Bytes()
		}
		frames[i] = []any{f.Mode, f.Flags, target, []any{f.Limits.Execution, f.Limits.State}, f.Value, f.Data}
	}
	return []any{
		tx.ChainID,
		tx.Nonce,
		tx.Sender,
		frames,
		sigs,
		[]any{tx.Fees.MaxFee, tx.Fees.MaxPriorityFeePerGas},
		tx.BlobHashes,
	}
}

// --- Static validity constraints (EIP-8141 "Constraints") ---

var (
	ErrFrameTxNoFrames          = errors.New("frame transaction has no frames")
	ErrFrameTxTooManyFrames     = errors.New("frame transaction exceeds MAX_FRAMES")
	ErrFrameTxInvalidMode       = errors.New("frame has invalid mode")
	ErrFrameTxInvalidFlags      = errors.New("frame has reserved flags set")
	ErrFrameTxValueNotSender    = errors.New("non-SENDER frame carries value")
	ErrFrameTxGasOverflow       = errors.New("frame gas exceeds uint64")
	ErrFrameTxApproveTarget     = errors.New("execution approval on frame with non-sender target")
	ErrFrameTxBatchVerify       = errors.New("atomic batch flag on VERIFY frame")
	ErrFrameTxBatchLast         = errors.New("atomic batch flag on last frame")
	ErrFrameTxBatchVerifyNext   = errors.New("atomic batch followed by VERIFY frame")
	ErrFrameTxBatchApproveScope = errors.New("atomic batch frame carries approval scope")
	// EIP-7906 (ASSERT frames).
	ErrFrameTxAssertNotSuffix = errors.New("non-ASSERT frame after an ASSERT frame")
	ErrFrameTxBatchAssert     = errors.New("atomic batch flag on ASSERT frame")
	ErrFrameTxBatchAssertNext = errors.New("atomic batch followed by ASSERT frame")
	ErrFrameSigScheme         = errors.New("invalid signature scheme")
	ErrFrameSigSignerLen      = errors.New("invalid signature signer length")
	ErrFrameSigMsgLen         = errors.New("invalid signature msg length")
	ErrFrameSigZeroMsg        = errors.New("explicit zero signature digest")
	ErrFrameBlobHashVersion   = errors.New("blob versioned hash has invalid version")
	ErrFrameTxGasLimitHigh    = errors.New("frame transaction exceeds EIP-7825 gas cap")
	ErrExpiryFrameFlags       = errors.New("expiry verifier frame has non-zero flags")
	ErrExpiryFrameValue       = errors.New("expiry verifier frame carries value")
	ErrExpiryFrameStateGas    = errors.New("expiry verifier frame has non-zero state gas limit")
	ErrExpiryFrameData        = errors.New("expiry verifier frame calldata is not 8 bytes")
	ErrExpiryFrameMultiple    = errors.New("multiple expiry verifier frames")
)

// ValidateStatic enforces the stateless validity constraints of EIP-8141
// ("Constraints" section). It does not verify signatures (see
// ValidateSignatures) and does not touch state.
func (tx *FrameTx) ValidateStatic() error {
	if len(tx.Frames) == 0 {
		return ErrFrameTxNoFrames
	}
	if len(tx.Frames) > MaxFrames {
		return ErrFrameTxTooManyFrames
	}
	for _, h := range tx.BlobHashes {
		if !kzg4844.IsValidVersionedHash(h[:]) {
			return ErrFrameBlobHashVersion
		}
	}
	for _, sig := range tx.Signatures {
		switch sig.Scheme {
		case FrameSigSchemeSecp256k1, FrameSigSchemeP256:
			if len(sig.Signer) != 0 && len(sig.Signer) != common.AddressLength {
				return ErrFrameSigSignerLen
			}
		case FrameSigSchemeArbitrary:
			if len(sig.Signer) != 0 {
				return ErrFrameSigSignerLen
			}
		default:
			return ErrFrameSigScheme
		}
		if len(sig.Msg) != 0 {
			if len(sig.Msg) != common.HashLength {
				return ErrFrameSigMsgLen
			}
			if bytes.Equal(sig.Msg, make([]byte, common.HashLength)) {
				return ErrFrameSigZeroMsg
			}
		}
	}
	var totalFrameGas, totalFrameExecutionGas uint64
	seenAssert := false
	for i, frame := range tx.Frames {
		if frame.Mode != FrameModeDefault && frame.Mode != FrameModeVerify && frame.Mode != FrameModeSender && frame.Mode != FrameModeAssert {
			return ErrFrameTxInvalidMode
		}
		// EIP-7906: ASSERT frames form a contiguous trailing suffix of
		// tx.frames. Once any frame has mode ASSERT, every subsequent frame
		// must also have mode ASSERT.
		if frame.Mode == FrameModeAssert {
			seenAssert = true
		} else if seenAssert {
			return ErrFrameTxAssertNotSuffix
		}
		if frame.Flags > FrameFlagValidMask {
			return ErrFrameTxInvalidFlags
		}
		if frame.Mode != FrameModeSender && frame.Value != nil && !frame.Value.IsZero() {
			return ErrFrameTxValueNotSender
		}
		var overflow bool
		totalFrameExecutionGas, overflow = add64(totalFrameExecutionGas, frame.Limits.Execution)
		if overflow {
			return ErrFrameTxGasOverflow
		}
		totalFrameGas, overflow = add64(totalFrameGas, frame.Limits.Execution)
		if overflow {
			return ErrFrameTxGasOverflow
		}
		totalFrameGas, overflow = add64(totalFrameGas, frame.Limits.State)
		if overflow {
			return ErrFrameTxGasOverflow
		}
		// Approval of execution is allowed only when target is nil or tx.sender.
		if frame.Flags&FrameFlagApproveExecution != 0 {
			if frame.Target != nil && *frame.Target != tx.Sender {
				return ErrFrameTxApproveTarget
			}
		}
		// Atomic batch flag is only valid on non-VERIFY frames and requires a
		// subsequent non-VERIFY frame to batch with. EIP-7906 additionally
		// forbids the flag on ASSERT frames and on a frame directly before an
		// ASSERT frame, so no atomic batch ever contains an ASSERT frame.
		if frame.Flags&FrameFlagAtomicBatch != 0 {
			if frame.Mode == FrameModeVerify {
				return ErrFrameTxBatchVerify
			}
			if frame.Mode == FrameModeAssert {
				return ErrFrameTxBatchAssert
			}
			if i+1 >= len(tx.Frames) {
				return ErrFrameTxBatchLast
			}
			if tx.Frames[i+1].Mode == FrameModeVerify {
				return ErrFrameTxBatchVerifyNext
			}
			if tx.Frames[i+1].Mode == FrameModeAssert {
				return ErrFrameTxBatchAssertNext
			}
		}
		// A frame belongs to a batch when it or its predecessor carries
		// ATOMIC_BATCH_FLAG. Batch frames must not carry an approval scope.
		if frame.Flags&FrameFlagAtomicBatch != 0 || (i > 0 && tx.Frames[i-1].Flags&FrameFlagAtomicBatch != 0) {
			if frame.Flags&FrameFlagApproveScopeMask != 0 {
				return ErrFrameTxBatchApproveScope
			}
		}
	}
	// Expiry verifier frames (EIP-8141): a VERIFY frame targeting
	// EXPIRY_VERIFIER carries strict structural constraints and at most one
	// may appear per transaction.
	expiryFrames := 0
	for _, frame := range tx.Frames {
		if frame.Mode == FrameModeVerify && frame.Target != nil && *frame.Target == ExpiryVerifierAddress {
			expiryFrames++
			if frame.Flags != 0 {
				return ErrExpiryFrameFlags
			}
			if frame.Value != nil && !frame.Value.IsZero() {
				return ErrExpiryFrameValue
			}
			if frame.Limits.State != 0 {
				return ErrExpiryFrameStateGas
			}
			if len(frame.Data) != ExpiryDataLength {
				return ErrExpiryFrameData
			}
		}
	}
	if expiryFrames > 1 {
		return ErrExpiryFrameMultiple
	}
	// Intrinsic and execution gas must fit the EIP-7825 transaction cap.
	intrinsic, floor, err := tx.IntrinsicGas()
	if err != nil {
		return err
	}
	if max(intrinsic+totalFrameExecutionGas, floor) > params.MaxTxGas {
		return ErrFrameTxGasLimitHigh
	}
	return nil
}

func add64(a, b uint64) (uint64, bool) {
	c := a + b
	return c, c < a
}

// --- Intrinsic gas (EIP-8141 "Gas Accounting") ---

// frameCalldataTokens returns the weighted token count of calldata per
// EIP-7976: zero bytes count 1 token, non-zero bytes 4.
func frameCalldataTokens(data []byte) uint64 {
	z := uint64(bytes.Count(data, []byte{0}))
	return z + (uint64(len(data))-z)*params.TxTokenPerNonZeroByte
}

// frameFloorTokens returns the EIP-7976 floor token count: 4 per byte.
func frameFloorTokens(data []byte) uint64 {
	return uint64(len(data)) * params.TxTokenPerNonZeroByte
}

// SignatureGas returns the protocol signature-validation gas cost of the
// entry's scheme, or false for an unknown scheme.
func (sig *FrameSignature) SignatureGas() (uint64, bool) {
	switch sig.Scheme {
	case FrameSigSchemeArbitrary:
		return FrameSigGasArbitrary, true
	case FrameSigSchemeSecp256k1:
		return FrameSigGasSecp256k1, true
	case FrameSigSchemeP256:
		return FrameSigGasP256, true
	}
	return 0, false
}

// IntrinsicGas computes the frame transaction's intrinsic gas and calldata
// floor gas (EIP-8141 "Gas Accounting"). Both are derivable from the
// transaction fields alone. An error is returned for unknown signature
// schemes (already excluded by ValidateStatic) or uint64 overflow.
func (tx *FrameTx) IntrinsicGas() (intrinsic uint64, floor uint64, err error) {
	var sigVerificationCost, frameDataCost, sigDataCost, valueTransferCost uint64
	for i := range tx.Signatures {
		sig := &tx.Signatures[i]
		sg, ok := sig.SignatureGas()
		if !ok {
			return 0, 0, ErrFrameSigScheme
		}
		sigVerificationCost, _ = add64(sigVerificationCost, sg)
		// Bytes elided from the canonical signature hash are priced as if all
		// non-zero, so their cost is a function of len(signature) alone.
		var sigTokens uint64
		if len(sig.Msg) == 0 {
			sigTokens = frameFloorTokens(sig.Signature)
		} else {
			sigTokens = frameCalldataTokens(sig.Signature)
		}
		sigDataCost, _ = add64(sigDataCost, frameCalldataTokens(sig.Signer)*params.TxStandardTokenCost)
		sigDataCost, _ = add64(sigDataCost, frameCalldataTokens(sig.Msg)*params.TxStandardTokenCost)
		sigDataCost, _ = add64(sigDataCost, sigTokens*params.TxStandardTokenCost)
	}
	for i := range tx.Frames {
		f := &tx.Frames[i]
		frameDataCost, _ = add64(frameDataCost, frameCalldataTokens(f.Data)*params.TxStandardTokenCost)
		// value_cost: TX_VALUE_COST for each value-carrying frame whose target
		// is neither nil nor the sender.
		if f.Value != nil && !f.Value.IsZero() && f.Target != nil && *f.Target != tx.Sender {
			valueTransferCost, _ = add64(valueTransferCost, params.TxValueCost2780)
		}
	}
	intrinsic = uint64(FrameTxIntrinsicCost)
	intrinsic, _ = add64(intrinsic, uint64(len(tx.Frames))*FrameTxPerFrameCost)
	intrinsic, _ = add64(intrinsic, frameDataCost)
	intrinsic, _ = add64(intrinsic, sigDataCost)
	intrinsic, _ = add64(intrinsic, sigVerificationCost)
	intrinsic, _ = add64(intrinsic, valueTransferCost)

	var floorTokens uint64
	for i := range tx.Frames {
		floorTokens, _ = add64(floorTokens, frameFloorTokens(tx.Frames[i].Data))
	}
	for i := range tx.Signatures {
		sig := &tx.Signatures[i]
		floorTokens, _ = add64(floorTokens, frameFloorTokens(sig.Signer))
		floorTokens, _ = add64(floorTokens, frameFloorTokens(sig.Msg))
		floorTokens, _ = add64(floorTokens, frameFloorTokens(sig.Signature))
	}
	floor = uint64(FrameTxIntrinsicCost)
	floor, _ = add64(floor, uint64(len(tx.Frames))*FrameTxPerFrameCost)
	floor, _ = add64(floor, sigVerificationCost)
	floor, _ = add64(floor, valueTransferCost)
	floor, _ = add64(floor, params.TxCostFloorPerToken7976*floorTokens)
	return intrinsic, floor, nil
}

// MaxGas computes the transaction's total gas limit (EIP-8141):
//
//	standard_gas_limit = intrinsic + sum(execution) + sum(state)
//	max_gas = max(standard_gas_limit, calldata_floor_gas + sum(state))
func (tx *FrameTx) MaxGas() uint64 {
	intrinsic, floor, err := tx.IntrinsicGas()
	if err != nil {
		return 0
	}
	var execution, state uint64
	for _, f := range tx.Frames {
		execution, _ = add64(execution, f.Limits.Execution)
		state, _ = add64(state, f.Limits.State)
	}
	standard, _ := add64(intrinsic, execution)
	standard, _ = add64(standard, state)
	floorTotal, _ := add64(floor, state)
	return max(standard, floorTotal)
}

// BlobGas returns the total blob gas of the transaction.
func (tx *FrameTx) blobGas() uint64 {
	return uint64(len(tx.BlobHashes)) * params.BlobTxBlobGasPerBlob
}

// --- Signature validation (EIP-8141 "Signature Validation") ---

var (
	ErrFrameSigMalformed = errors.New("malformed frame signature")
	ErrFrameSigInvalid   = errors.New("invalid frame signature")
)

// ResolvedSigner returns the signer address the protocol assigns to the
// signature entry: sig.signer when present, otherwise tx.sender. The second
// return value reports whether resolution is defined for the scheme
// (ARBITRARY entries have no resolved signer).
func (tx *FrameTx) ResolvedSigner(sig *FrameSignature) (common.Address, bool) {
	if sig.Scheme == FrameSigSchemeArbitrary {
		return common.Address{}, false
	}
	if len(sig.Signer) == common.AddressLength {
		return common.BytesToAddress(sig.Signer), true
	}
	return tx.Sender, true
}

// ValidateSignature validates one signature entry against the canonical
// signature hash, following EIP-8141 validate_signature. Callers must have
// run ValidateStatic first (message-length rules are assumed here).
func (tx *FrameTx) ValidateSignature(sig *FrameSignature, sigHash common.Hash) bool {
	msg := sigHash
	if len(sig.Msg) == common.HashLength {
		if bytes.Equal(sig.Msg, make([]byte, common.HashLength)) {
			return false
		}
		msg = common.BytesToHash(sig.Msg)
	}
	if len(sig.Signer) != 0 && len(sig.Signer) != common.AddressLength {
		return false
	}
	switch sig.Scheme {
	case FrameSigSchemeSecp256k1:
		if len(sig.Signature) != 65 {
			return false
		}
		v := sig.Signature[0]
		r := new(big.Int).SetBytes(sig.Signature[1:33])
		s := new(big.Int).SetBytes(sig.Signature[33:65])
		// v is the recovery id (0 or 1); r and s must be canonical with
		// low-s per EIP-2 so each signature has one encoding.
		halfN := new(big.Int).Rsh(Secp256k1N, 1)
		if v > 1 || r.Sign() <= 0 || r.Cmp(Secp256k1N) >= 0 || s.Sign() <= 0 || s.Cmp(halfN) > 0 {
			return false
		}
		// ecrecover
		sigBytes := make([]byte, 65)
		copy(sigBytes[0:32], sig.Signature[1:33])
		copy(sigBytes[32:64], sig.Signature[33:65])
		sigBytes[64] = v
		pub, err := crypto.Ecrecover(msg[:], sigBytes)
		if err != nil || len(pub) == 0 {
			return false
		}
		var recovered common.Address
		copy(recovered[:], crypto.Keccak256(pub[1:])[12:])
		resolved, _ := tx.ResolvedSigner(sig)
		return resolved == recovered
	case FrameSigSchemeP256:
		if len(sig.Signature) != 128 {
			return false
		}
		r := new(big.Int).SetBytes(sig.Signature[0:32])
		s := new(big.Int).SetBytes(sig.Signature[32:64])
		qx := sig.Signature[64:96]
		qy := sig.Signature[96:128]
		halfN := new(big.Int).Rsh(Secp256r1N, 1)
		if r.Sign() <= 0 || r.Cmp(Secp256r1N) >= 0 || s.Sign() <= 0 || s.Cmp(halfN) > 0 {
			return false
		}
		resolved, _ := tx.ResolvedSigner(sig)
		var derived common.Address
		copy(derived[:], crypto.Keccak256(append(qx, qy...))[12:])
		if resolved != derived {
			return false
		}
		return P256Verify(msg[:], r, s, qx, qy)
	case FrameSigSchemeArbitrary:
		return len(sig.Signer) == 0
	}
	return false
}

// ValidateSignatures validates every protocol-validated signature in the
// transaction (EIP-8141: before any frame is executed). It returns the
// canonical signature hash on success.
func (tx *FrameTx) ValidateSignatures() (common.Hash, error) {
	sigHash := tx.SigHash()
	for i := range tx.Signatures {
		if !tx.ValidateSignature(&tx.Signatures[i], sigHash) {
			return common.Hash{}, ErrFrameSigInvalid
		}
	}
	return sigHash, nil
}

// P256Verify executes the RIP-7212 P256VERIFY verification over the digest,
// reusing the same curve implementation as the EVM precompile
// (crypto/secp256r1).
func P256Verify(msg []byte, r, s *big.Int, qx, qy []byte) bool {
	return secp256r1.Verify(msg, r, s, new(big.Int).SetBytes(qx), new(big.Int).SetBytes(qy))
}
