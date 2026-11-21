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

package vm

import (
	"errors"
	"math"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"
)

// EIP-8141 introspection parameter identifiers (spec tables).
const (
	txParamType          = 0x00
	txParamNonce         = 0x01
	txParamSender        = 0x02
	txParamMaxPrioFee    = 0x03
	txParamMaxFee        = 0x04
	txParamBlobCount     = 0x07
	txParamSigHash       = 0x08
	txParamFrameCount    = 0x09
	txParamCurrentFrame  = 0x0a
	txParamSigCount      = 0x0b
	txParamStateGasLeft  = 0x0c
)

const (
	frameParamResolvedTarget = 0x00
	frameParamExecGasLimit   = 0x01
	frameParamMode           = 0x02
	frameParamFlags          = 0x03
	frameParamDataLength     = 0x04
	frameParamStatus         = 0x05
	frameParamAllowedScope   = 0x06
	frameParamAtomicBatch    = 0x07
	frameParamValue          = 0x08
	frameParamStateGasLimit  = 0x09
	frameParamExecGasUsed    = 0x0a
	frameParamStateGasUsed   = 0x0b
)

const (
	sigParamResolvedSigner = 0x00
	sigParamScheme         = 0x01
	sigParamMsg            = 0x02
	sigParamSignatureLen   = 0x03
)

// enable8141 activates the EIP-8141 frame-transaction instructions.
// The opcodes are registered in the Bogota instruction set but halt
// exceptionally when executed outside a frame transaction.
func enable8141(jt *JumpTable) {
	jt[APPROVE] = &operation{
		execute:    opApprove,
		dynamicGas: gasReturn, // memory expansion only, matching RETURN
		minStack:   minStack(3, 0),
		maxStack:   maxStack(3, 0),
		memorySize: memoryReturn,
	}
	jt[TXPARAM] = &operation{
		execute:     opTxParam,
		constantGas: GasQuickStep,
		minStack:    minStack(1, 1),
		maxStack:    maxStack(1, 1),
	}
	jt[FRAMEDATALOAD] = &operation{
		execute:     opFrameDataLoad,
		constantGas: GasFastestStep,
		minStack:    minStack(2, 1),
		maxStack:    maxStack(2, 1),
	}
	jt[FRAMEDATACOPY] = &operation{
		execute:     opFrameDataCopy,
		constantGas: GasFastestStep,
		dynamicGas:  gasCallDataCopy,
		minStack:    minStack(4, 0),
		maxStack:    maxStack(4, 0),
		memorySize:  memoryCallDataCopy,
	}
	jt[FRAMEPARAM] = &operation{
		execute:     opFrameParam,
		constantGas: GasQuickStep,
		minStack:    minStack(2, 1),
		maxStack:    maxStack(2, 1),
	}
	jt[SIGPARAM] = &operation{
		execute:     opSigParam,
		constantGas: GasQuickStep,
		minStack:    minStack(2, 1),
		maxStack:    maxStack(2, 1),
	}
	jt[SIGDATACOPY] = &operation{
		execute:     opSigDataCopy,
		constantGas: GasFastestStep,
		dynamicGas:  gasCallDataCopy,
		minStack:    minStack(4, 0),
		maxStack:    maxStack(4, 0),
		memorySize:  memoryCallDataCopy,
	}
}

// frameAt returns the frame at index i, halting on out-of-bounds.
func (fc *FrameTxContext) frameAt(i *uint256.Int) (*types.Frame, error) {
	if !i.IsUint64() || i.Uint64() >= uint64(len(fc.Tx.Frames)) {
		return nil, errFrameTxOnly
	}
	return &fc.Tx.Frames[i.Uint64()], nil
}

// opTxParam implements TXPARAM (0xb0): transaction-scoped introspection.
// Stack: [param] -> [value].
func opTxParam(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	fc := evm.frameCtx
	if fc == nil {
		return nil, errFrameTxOnly
	}
	param := scope.Stack.pop()
	if !param.IsUint64() {
		return nil, errFrameTxOnly
	}
	var val uint256.Int
	switch param.Uint64() {
	case txParamType:
		val.SetUint64(types.FrameTxType)
	case txParamNonce:
		val.SetUint64(fc.Tx.Nonce)
	case txParamSender:
		val.SetBytes(fc.Tx.Sender.Bytes())
	case txParamMaxPrioFee:
		val.Set(fc.Tx.Fees.MaxPriorityFeePerGas)
	case txParamMaxFee:
		val.Set(fc.Tx.Fees.MaxFee)
	case txParamBlobCount:
		val.SetUint64(uint64(len(fc.Tx.BlobHashes)))
	case txParamSigHash:
		val.SetBytes32(fc.SigHash.Bytes())
	case txParamFrameCount:
		val.SetUint64(uint64(len(fc.Tx.Frames)))
	case txParamCurrentFrame:
		val.SetUint64(uint64(fc.CurrentFrame))
	case txParamSigCount:
		val.SetUint64(uint64(len(fc.Tx.Signatures)))
	case txParamStateGasLeft:
		val.SetUint64(scope.Contract.Gas.StateGas)
	default:
		return nil, errFrameTxOnly
	}
	scope.Stack.push(&val)
	return nil, nil
}

// opFrameDataLoad implements FRAMEDATALOAD (0xb1).
// Stack: [offset(top), frameIndex] -> [word].
func opFrameDataLoad(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	fc := evm.frameCtx
	if fc == nil {
		return nil, errFrameTxOnly
	}
	offset := scope.Stack.pop()
	frameIdx := scope.Stack.pop()
	frame, err := fc.frameAt(&frameIdx)
	if err != nil {
		return nil, err
	}
	var val uint256.Int
	if off, overflow := offset.Uint64WithOverflow(); !overflow {
		val.SetBytes(getData(frame.Data, off, 32))
	}
	scope.Stack.push(&val)
	return nil, nil
}

// opFrameDataCopy implements FRAMEDATACOPY (0xb2).
// Stack: [memOffset(top), dataOffset, length, frameIndex].
func opFrameDataCopy(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	fc := evm.frameCtx
	if fc == nil {
		return nil, errFrameTxOnly
	}
	memOffset, dataOffset, length := scope.Stack.pop3()
	frameIdx := scope.Stack.pop()
	frame, err := fc.frameAt(&frameIdx)
	if err != nil {
		return nil, err
	}
	dataOffset64, overflow := dataOffset.Uint64WithOverflow()
	if overflow {
		dataOffset64 = math.MaxUint64
	}
	// These values are checked for overflow during gas cost calculation
	memOffset64 := memOffset.Uint64()
	length64 := length.Uint64()
	scope.Memory.Set(memOffset64, length64, getData(frame.Data, dataOffset64, length64))
	return nil, nil
}

// opFrameParam implements FRAMEPARAM (0xb3): frame-scoped introspection.
// Stack: [frameIndex(top), param] -> [value].
func opFrameParam(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	fc := evm.frameCtx
	if fc == nil {
		return nil, errFrameTxOnly
	}
	frameIdx := scope.Stack.pop()
	param := scope.Stack.pop()
	frame, err := fc.frameAt(&frameIdx)
	if err != nil {
		return nil, err
	}
	if !param.IsUint64() {
		return nil, errFrameTxOnly
	}
	idx := frameIdx.Uint64()
	var val uint256.Int
	switch param.Uint64() {
	case frameParamResolvedTarget:
		target := fc.Tx.Sender
		if frame.Target != nil {
			target = *frame.Target
		}
		val.SetBytes(target.Bytes())
	case frameParamExecGasLimit:
		val.SetUint64(frame.Limits.Execution)
	case frameParamMode:
		val.SetUint64(uint64(frame.Mode))
	case frameParamFlags:
		val.SetUint64(uint64(frame.Flags))
	case frameParamDataLength:
		val.SetUint64(uint64(len(frame.Data)))
	case frameParamStatus, frameParamExecGasUsed, frameParamStateGasUsed:
		// Defined only for completed frames; current/future halts.
		if idx >= uint64(fc.CurrentFrame) || !fc.FrameFinalized[idx] {
			return nil, errFrameTxOnly
		}
		switch param.Uint64() {
		case frameParamStatus:
			val.SetUint64(uint64(fc.FrameStatuses[idx]))
		case frameParamExecGasUsed:
			val.SetUint64(fc.FrameGasUsedExecution[idx])
		default: // frameParamStateGasUsed
			// The journaled receipt value, reflecting later refills.
			sdb, fs := fc.frameDB(evm)
			if fs == nil {
				return nil, errFrameTxOnly
			}
			v := fs.ReceiptStateGas[idx]
			if v < 0 {
				return nil, errFrameTxOnly // cannot happen, but never underflow
			}
			val.SetUint64(uint64(v))
			_ = sdb
		}
	case frameParamAllowedScope:
		val.SetUint64(uint64(frame.Flags & types.FrameFlagApproveScopeMask))
	case frameParamAtomicBatch:
		val.SetUint64(uint64((frame.Flags >> 2) & 0x01))
	case frameParamValue:
		if frame.Value != nil {
			val.Set(frame.Value)
		}
	case frameParamStateGasLimit:
		val.SetUint64(frame.Limits.State)
	default:
		return nil, errFrameTxOnly
	}
	scope.Stack.push(&val)
	return nil, nil
}

// opSigParam implements SIGPARAM (0xb4): signature-scoped introspection.
// Stack: [signatureIndex(top), param] -> [value].
func opSigParam(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	fc := evm.frameCtx
	if fc == nil {
		return nil, errFrameTxOnly
	}
	sigIdx := scope.Stack.pop()
	param := scope.Stack.pop()
	if !sigIdx.IsUint64() || sigIdx.Uint64() >= uint64(len(fc.Tx.Signatures)) || !param.IsUint64() {
		return nil, errFrameTxOnly
	}
	sig := &fc.Tx.Signatures[sigIdx.Uint64()]
	var val uint256.Int
	switch param.Uint64() {
	case sigParamResolvedSigner:
		// ARBITRARY entries have no resolved signer.
		if sig.Scheme == types.FrameSigSchemeArbitrary {
			return nil, errFrameTxOnly
		}
		val.SetBytes(fc.SigResolvedSigners[sigIdx.Uint64()].Bytes())
	case sigParamScheme:
		val.SetUint64(uint64(sig.Scheme))
	case sigParamMsg:
		val.SetBytes(sig.Msg)
	case sigParamSignatureLen:
		// Only ARBITRARY entries expose their raw signature, including length.
		if sig.Scheme != types.FrameSigSchemeArbitrary {
			return nil, errFrameTxOnly
		}
		val.SetUint64(uint64(len(sig.Signature)))
	default:
		return nil, errFrameTxOnly
	}
	scope.Stack.push(&val)
	return nil, nil
}

// opSigDataCopy implements SIGDATACOPY (0xb5): copies an ARBITRARY signature's
// raw bytes into memory. Protocol-validated schemes halt.
// Stack: [memOffset(top), dataOffset, length, signatureIndex].
func opSigDataCopy(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	fc := evm.frameCtx
	if fc == nil {
		return nil, errFrameTxOnly
	}
	memOffset, dataOffset, length := scope.Stack.pop3()
	sigIdx := scope.Stack.pop()
	if !sigIdx.IsUint64() || sigIdx.Uint64() >= uint64(len(fc.Tx.Signatures)) {
		return nil, errFrameTxOnly
	}
	sig := &fc.Tx.Signatures[sigIdx.Uint64()]
	if sig.Scheme != types.FrameSigSchemeArbitrary {
		return nil, errFrameTxOnly
	}
	dataOffset64, overflow := dataOffset.Uint64WithOverflow()
	if overflow {
		dataOffset64 = math.MaxUint64
	}
	memOffset64 := memOffset.Uint64()
	length64 := length.Uint64()
	scope.Memory.Set(memOffset64, length64, getData(sig.Signature, dataOffset64, length64))
	return nil, nil
}

var _ = common.Address{} // keep common import when unused paths compile

// errApproveHalt requests an exceptional halt of the current call frame from
// within APPROVE (e.g. the sender-creation state-gas charge exceeding the
// frame's state pool).
var errApproveHalt = errors.New("approve: out of state gas")

// opApprove implements the EIP-8141 APPROVE instruction (0xaa).
//
// Stack (top first): offset, length, scope.
//
// APPROVE exits the current EVM call frame successfully (RETURN semantics:
// the memory region [offset, offset+length) becomes the call frame's return
// data) and updates the transaction-scoped approval context according to
// scope. Outside a frame transaction it halts exceptionally. Scope or target
// violations revert the current call frame; exhaustion of the frame's state
// gas pool halts it.
func opApprove(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	fc := evm.frameCtx
	if fc == nil {
		return nil, errFrameTxOnly
	}
	offset, length := scope.Stack.pop2()
	scopeVal := scope.Stack.pop()
	ret := scope.Memory.GetCopy(offset.Uint64(), length.Uint64())
	if !scopeVal.IsUint64() {
		return nil, ErrExecutionReverted
	}
	revert, halt := fc.Approve(evm, &scope.Contract.Gas, scope.Contract.Address(), scopeVal.Uint64())
	switch {
	case halt:
		return nil, errApproveHalt
	case revert:
		return nil, ErrExecutionReverted
	}
	return ret, errStopToken
}
