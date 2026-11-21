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

// EIP-7906 transaction assertion instructions: TXTRACE (0xb6) and
// EVENTDATACOPY (0xb7). Both are valid only inside an ASSERT frame; executing
// them in any other context — legacy or EIP-1559 transactions, or any other
// EIP-8141 frame mode — is an exceptional halt.

package vm

import (
	"errors"

	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// errAssertFrameOnly is the exceptional halt produced when TXTRACE or
// EVENTDATACOPY executes outside an ASSERT frame.
var errAssertFrameOnly = errors.New("assertion opcode outside ASSERT frame")

// errTxTraceInput is the exceptional halt produced by an undefined TXTRACE
// param, a non-zero reserved index, or an out-of-range table index.
var errTxTraceInput = errors.New("assertion opcode: invalid selector or index")

// TXTRACE parameter identifiers (EIP-7906 spec table).
const (
	txTraceBalancesChanged = 0x00
	txTraceSlotsChanged    = 0x01
	txTraceCodesChanged    = 0x02
	txTraceNoncesChanged   = 0x03
	txTraceBalanceAddress  = 0x04
	txTraceBalanceBefore   = 0x05
	txTraceBalanceAfter    = 0x06
	txTraceSlotAddress     = 0x07
	txTraceSlotBefore      = 0x08
	txTraceSlotAfter       = 0x09
	txTraceSlotKey         = 0x0a
	txTraceCodeAddress     = 0x0b
	txTraceCodeBefore      = 0x0c
	txTraceCodeAfter       = 0x0d
	txTraceNonceAddress    = 0x0e
	txTraceNonceBefore     = 0x0f
	txTraceNonceAfter      = 0x10
	txTraceEventsCount     = 0x11
	txTraceEventAddress    = 0x12
	txTraceEventTopicCount = 0x13
	txTraceEventTopic0     = 0x14
	txTraceEventTopic1     = 0x15
	txTraceEventTopic2     = 0x16
	txTraceEventTopic3     = 0x17
	txTraceEventDataLen    = 0x18
	txTraceGasPreCharge    = 0x19
	txTraceGasPayer        = 0x1a
	txTraceMaxParam        = 0x1a
)

// txTraceStateDB is the StateDB surface the assertion opcodes need.
// *state.StateDB satisfies it; the narrow interface avoids widening
// vm.StateDB (same pattern as frameStateDB).
type txTraceStateDB interface {
	TxTraceDiff() *state.TxTraceDiff
	FrameState() *state.FrameState
}

// assertContext gates an assertion opcode: it must execute inside an ASSERT
// frame (any call depth of its subtree) of a frame transaction. Returns the
// transaction diff, the frame context, and the journaled frame state.
func assertContext(evm *EVM) (*state.TxTraceDiff, *FrameTxContext, *state.FrameState, error) {
	fc := evm.frameCtx
	if fc == nil {
		return nil, nil, nil, errAssertFrameOnly
	}
	if fc.Tx.Frames[fc.CurrentFrame].Mode != types.FrameModeAssert {
		return nil, nil, nil, errAssertFrameOnly
	}
	sdb, ok := evm.StateDB.(txTraceStateDB)
	if !ok {
		return nil, nil, nil, errAssertFrameOnly
	}
	fs := sdb.FrameState()
	if fs == nil {
		return nil, nil, nil, errAssertFrameOnly
	}
	return sdb.TxTraceDiff(), fc, fs, nil
}

// traceNoIndex enforces the reserved-zero rule of the count/wide params.
func traceNoIndex(in2 *uint256.Int) error {
	if !in2.IsZero() {
		return errTxTraceInput
	}
	return nil
}

// traceIndex validates an index operand against a table's entry count.
func traceIndex(in2 *uint256.Int, count int) (int, error) {
	if !in2.IsUint64() || in2.Uint64() >= uint64(count) {
		return 0, errTxTraceInput
	}
	return int(in2.Uint64()), nil
}

// opTxTrace implements TXTRACE (0xb6).
// Stack (top first): [param, in2] -> [value].
func opTxTrace(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	diff, fc, fs, err := assertContext(evm)
	if err != nil {
		return nil, err
	}
	param := scope.Stack.pop()
	in2 := scope.Stack.pop()
	// Selectors are checked as full 256-bit values, without truncation.
	if !param.IsUint64() || param.Uint64() > txTraceMaxParam {
		return nil, errTxTraceInput
	}
	var val uint256.Int
	switch param.Uint64() {
	case txTraceBalancesChanged:
		if err := traceNoIndex(&in2); err != nil {
			return nil, err
		}
		val.SetUint64(uint64(len(diff.Balances)))
	case txTraceSlotsChanged:
		if err := traceNoIndex(&in2); err != nil {
			return nil, err
		}
		val.SetUint64(uint64(len(diff.Slots)))
	case txTraceCodesChanged:
		if err := traceNoIndex(&in2); err != nil {
			return nil, err
		}
		val.SetUint64(uint64(len(diff.Codes)))
	case txTraceNoncesChanged:
		if err := traceNoIndex(&in2); err != nil {
			return nil, err
		}
		val.SetUint64(uint64(len(diff.Nonces)))
	case txTraceBalanceAddress, txTraceBalanceBefore, txTraceBalanceAfter:
		idx, err := traceIndex(&in2, len(diff.Balances))
		if err != nil {
			return nil, err
		}
		ch := diff.Balances[idx]
		switch param.Uint64() {
		case txTraceBalanceAddress:
			val.SetBytes(ch.Address.Bytes())
		case txTraceBalanceBefore:
			val.Set(ch.Before)
		default:
			val.Set(ch.After)
		}
	case txTraceSlotAddress, txTraceSlotBefore, txTraceSlotAfter, txTraceSlotKey:
		idx, err := traceIndex(&in2, len(diff.Slots))
		if err != nil {
			return nil, err
		}
		ch := diff.Slots[idx]
		switch param.Uint64() {
		case txTraceSlotAddress:
			val.SetBytes(ch.Address.Bytes())
		case txTraceSlotBefore:
			val.SetBytes32(ch.Before[:])
		case txTraceSlotAfter:
			val.SetBytes32(ch.After[:])
		default:
			val.SetBytes32(ch.Key[:])
		}
	case txTraceCodeAddress, txTraceCodeBefore, txTraceCodeAfter:
		idx, err := traceIndex(&in2, len(diff.Codes))
		if err != nil {
			return nil, err
		}
		ch := diff.Codes[idx]
		switch param.Uint64() {
		case txTraceCodeAddress:
			val.SetBytes(ch.Address.Bytes())
		case txTraceCodeBefore:
			val.SetBytes32(ch.Before[:])
		default:
			val.SetBytes32(ch.After[:])
		}
	case txTraceNonceAddress, txTraceNonceBefore, txTraceNonceAfter:
		idx, err := traceIndex(&in2, len(diff.Nonces))
		if err != nil {
			return nil, err
		}
		ch := diff.Nonces[idx]
		switch param.Uint64() {
		case txTraceNonceAddress:
			val.SetBytes(ch.Address.Bytes())
		case txTraceNonceBefore:
			val.SetUint64(ch.Before)
		default:
			val.SetUint64(ch.After)
		}
	case txTraceEventsCount:
		if err := traceNoIndex(&in2); err != nil {
			return nil, err
		}
		val.SetUint64(uint64(len(diff.Events)))
	case txTraceEventAddress:
		idx, err := traceIndex(&in2, len(diff.Events))
		if err != nil {
			return nil, err
		}
		val.SetBytes(diff.Events[idx].Address.Bytes())
	case txTraceEventTopicCount:
		idx, err := traceIndex(&in2, len(diff.Events))
		if err != nil {
			return nil, err
		}
		val.SetUint64(uint64(len(diff.Events[idx].Topics)))
	case txTraceEventTopic0, txTraceEventTopic1, txTraceEventTopic2, txTraceEventTopic3:
		idx, err := traceIndex(&in2, len(diff.Events))
		if err != nil {
			return nil, err
		}
		topic := param.Uint64() - txTraceEventTopic0
		if topic >= uint64(len(diff.Events[idx].Topics)) {
			return nil, errTxTraceInput
		}
		val.SetBytes32(diff.Events[idx].Topics[topic][:])
	case txTraceEventDataLen:
		idx, err := traceIndex(&in2, len(diff.Events))
		if err != nil {
			return nil, err
		}
		val.SetUint64(uint64(len(diff.Events[idx].Data)))
	case txTraceGasPreCharge:
		if err := traceNoIndex(&in2); err != nil {
			return nil, err
		}
		val.Set(fc.MaxCost)
	case txTraceGasPayer:
		if err := traceNoIndex(&in2); err != nil {
			return nil, err
		}
		// Zero address when no frame has approved payment yet (the
		// transaction is invalid in that case anyway).
		val.SetBytes(fs.Payer.Bytes())
	}
	scope.Stack.push(&val)
	return nil, nil
}

// opEventDataCopy implements EVENTDATACOPY (0xb7).
// Stack (top first): [event_index, memOffset, dataOffset, length] -> [].
func opEventDataCopy(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	diff, _, _, err := assertContext(evm)
	if err != nil {
		return nil, err
	}
	eventIdx := scope.Stack.pop()
	memOffset, dataOffset, length := scope.Stack.pop3()
	if !eventIdx.IsUint64() || eventIdx.Uint64() >= uint64(len(diff.Events)) {
		return nil, errTxTraceInput
	}
	data := diff.Events[eventIdx.Uint64()].Data
	// dataOffset + length is evaluated without 256-bit wraparound and must
	// not exceed the event's data length, even when length is zero.
	sum, overflow := new(uint256.Int).AddOverflow(dataOffset, length)
	if overflow || !sum.IsUint64() || sum.Uint64() > uint64(len(data)) {
		return nil, errTxTraceInput
	}
	// The source range is in-bounds: copy exactly (no zero-padding).
	scope.Memory.Set(memOffset.Uint64(), length.Uint64(), data[dataOffset.Uint64():sum.Uint64()])
	return nil, nil
}

// memoryEventDataCopy computes the memory expansion of EVENTDATACOPY: the
// destination region is [memOffset(top-1), memOffset+length(top-3)).
func memoryEventDataCopy(stack *Stack) (uint64, bool) {
	return calcMemSize64(stack.back(1), stack.back(3))
}

// gasEventDataCopy charges CALLDATACOPY-style variable gas for
// EVENTDATACOPY: memory expansion plus per-word copy cost, with the length
// operand at stack position back(3).
var gasEventDataCopy = memoryCopierGas(3)

// enable7906 activates the EIP-7906 transaction-assertion instructions. The
// opcodes are registered in the Bogota instruction set (the EIP-8141 frame
// machinery is Bogota-gated) but halt exceptionally outside ASSERT frames.
func enable7906(jt *JumpTable) {
	jt[TXTRACE] = &operation{
		execute:     opTxTrace,
		constantGas: params.WarmStorageReadCostEIP2929, // EIP-8038: WARM_ACCESS
		minStack:    minStack(2, 1),
		maxStack:    maxStack(2, 1),
	}
	jt[EVENTDATACOPY] = &operation{
		execute:     opEventDataCopy,
		constantGas: GasFastestStep,
		dynamicGas:  gasEventDataCopy,
		minStack:    minStack(4, 0),
		maxStack:    maxStack(4, 0),
		memorySize:  memoryEventDataCopy,
	}
}
