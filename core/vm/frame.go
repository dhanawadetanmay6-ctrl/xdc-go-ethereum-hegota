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

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// errFrameTxOnly is the exceptional halt produced when a frame-transaction
// opcode executes outside the context of a frame transaction.
var errFrameTxOnly = errors.New("frame transaction opcode outside frame transaction")

// Frame receipt statuses (EIP-8141).
// Frame receipt status codes (EIP-8141), defined in core/types and
// re-exported here for the execution layer.
const (
	FrameStatusFailed    = types.FrameStatusFailed
	FrameStatusSucceeded = types.FrameStatusSucceeded
	FrameStatusSkipped   = types.FrameStatusSkipped
)

// FrameTxContext carries the EIP-8141 frame-transaction execution state that
// EVM code observes or mutates through the frame opcodes (APPROVE and the
// introspection opcodes). It is attached to the EVM (SetFrameContext) only
// while a frame transaction executes.
//
// The journaled parts of the execution state (approval context, per-frame
// attributed state gas, state-gas ownership) live in state.FrameState on the
// StateDB; this context carries the immutable transaction view plus the
// finalized per-frame results the introspection opcodes read.
type FrameTxContext struct {
	Tx      *types.FrameTx
	SigHash common.Hash

	// CurrentFrame is the index of the frame currently executing.
	CurrentFrame int
	// ResolvedTarget is the current frame's resolved target
	// (frame.target, or tx.sender when nil).
	ResolvedTarget common.Address
	// MaxCost is the wei amount collected from the payer on payment approval
	// (EIP-8141 max_cost).
	MaxCost *uint256.Int

	// FrameStatuses[i] records the finalized status of frame i
	// (0 failure, 1 success, 2 skipped). Set by the engine at frame exit;
	// defined for completed frames only.
	FrameStatuses []uint8
	// FrameGasUsedExecution[i] records the finalized execution gas used by
	// frame i. Immutable after frame exit.
	FrameGasUsedExecution []uint64
	// FrameFinalized reports whether frame i has completed (its receipt
	// fields are readable by FRAMEPARAM).
	FrameFinalized []bool

	// SigResolvedSigners caches the per-signature resolved signer; entry i is
	// meaningful only when SigResolvedOK[i] is true (ARBITRARY entries have
	// no resolved signer).
	SigResolvedSigners []common.Address
	SigResolvedOK      []bool
}

// frameStateDB is the StateDB surface the frame opcodes need. *state.StateDB
// satisfies it; keeping it an interface avoids widening vm.StateDB.
type frameStateDB interface {
	FrameState() *state.FrameState
	SetFrameApproval(payer common.Address, payerSet, approved bool)
	SetFrameReceiptStateGas(frame int, value int64)
	SetFrameStateGasOwner(addr common.Address, slot common.Hash, frame int)
	ClearFrameStateGasOwner(addr common.Address, slot common.Hash)
	FrameStateGasOwner(addr common.Address, slot common.Hash) (int, bool)
	GetBalance(common.Address) *uint256.Int
	SubBalance(common.Address, *uint256.Int, tracing.BalanceChangeReason) uint256.Int
	GetNonce(common.Address) uint64
	SetNonce(common.Address, uint64, tracing.NonceChangeReason)
	Empty(common.Address) bool
}

// frameDB returns the journaled frame execution state, or nil when the
// StateDB implementation does not carry one (consensus-impacting paths always
// run against *state.StateDB).
func (fc *FrameTxContext) frameDB(evm *EVM) (frameStateDB, *state.FrameState) {
	sdb, ok := evm.StateDB.(frameStateDB)
	if !ok {
		return nil, nil
	}
	return sdb, sdb.FrameState()
}

// Approve applies the EIP-8141 APPROVE semantics for the executing frame.
// addr is the executing contract's own address (ADDRESS) — for real opcode
// execution that is contract.Address(); for default-code evaluation it is the
// frame's resolved target. gas is the executing call frame's gas budget, from
// which the sender-creation state-gas charge is drawn.
//
// The return value is (revert, halt): revert requests a REVERT-style exit of
// the current call frame; halt requests an exceptional halt. At most one is
// true, and both false means the approval was applied.
func (fc *FrameTxContext) Approve(evm *EVM, gas *GasBudget, addr common.Address, scope uint64) (revert bool, halt bool) {
	sdb, fs := fc.frameDB(evm)
	if fs == nil {
		// No journaled frame state: cannot apply approvals safely.
		return false, true
	}
	tx := fc.Tx
	// If ADDRESS != resolved_target, revert.
	if addr != fc.ResolvedTarget {
		return true, false
	}
	// Ensure that scope is one of the caller's allowed scopes in frame.flags.
	allowedScope := uint64(tx.Frames[fc.CurrentFrame].Flags & types.FrameFlagApproveScopeMask)
	if scope == 0 || scope&^uint64(types.FrameFlagApproveScopeMask) != 0 || scope&^allowedScope != 0 {
		return true, false
	}
	wantExecution := scope&types.FrameFlagApproveExecution != 0
	wantPayment := scope&types.FrameFlagApprovePayment != 0

	// APPROVE_EXECUTION checks.
	if wantExecution {
		if fs.SenderApproved {
			return true, false
		}
		if fc.ResolvedTarget != tx.Sender {
			return true, false
		}
	}
	// APPROVE_PAYMENT checks.
	if wantPayment {
		if fs.PayerSet {
			return true, false
		}
		if scope == types.FrameFlagApproveScopeMask {
			// APPROVE_EXECUTION_AND_PAYMENT additionally requires the target
			// to be the sender (covered above via wantExecution).
		}
		if bal := sdb.GetBalance(fc.ResolvedTarget); bal == nil || bal.Cmp(fc.MaxCost) < 0 {
			return true, false
		}
		if !fs.SenderApproved && !wantExecution {
			return true, false
		}
		// Sender account creation state-gas charge, drawn from the current
		// frame's state pool. Failure halts the call frame without applying
		// any approval effects.
		if sdb.Empty(tx.Sender) {
			cost := uint64(params.AccountCreationSize) * evm.Context.CostPerStateByte
			if !gas.ChargeStateStrict(cost) {
				return false, true
			}
		}
	}
	// Apply effects (journaled).
	if wantPayment {
		sdb.SetNonce(tx.Sender, sdb.GetNonce(tx.Sender)+1, tracing.NonceChangeEoACall)
		sdb.SubBalance(fc.ResolvedTarget, fc.MaxCost, tracing.BalanceDecreaseGasBuy)
		sdb.SetFrameApproval(fc.ResolvedTarget, true, true)
	} else {
		sdb.SetFrameApproval(fs.Payer, fs.PayerSet, true)
	}
	return false, false
}

// ChargeStateStrict deducts a state-gas charge honoring pool separation and
// reports whether the pool covered it. APPROVE's sender-creation charge can
// never spill into execution gas.
func (g *GasBudget) ChargeStateStrict(s uint64) bool {
	if g.StateGas < s {
		return false
	}
	g.StateGas -= s
	g.UsedStateGas += int64(s)
	return true
}
