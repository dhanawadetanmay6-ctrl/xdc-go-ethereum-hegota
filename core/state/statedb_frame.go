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

package state

import (
	"github.com/ethereum/go-ethereum/common"
)

// FrameState carries the EIP-8141 frame-transaction execution state that must
// roll back together with ordinary state changes (EIP-8141 "Cross-frame
// interactions": state-gas ownership records, frame receipts' gas_used.state,
// and the approval context (payer, sender_approved) are journaled alongside
// the state changes that produced them).
//
// A *FrameState is attached to the StateDB for the duration of one frame
// transaction's execution (StateDB.SetFrameState) and is nil otherwise. All
// mutations MUST go through the StateDB journaling methods below so that
// EVM call-frame reverts, frame reverts, and atomic-batch unrolls restore
// them automatically via RevertToSnapshot.
type FrameState struct {
	// Payer is the account that approved payment (APPROVE_PAYMENT scope).
	Payer   common.Address
	PayerSet bool
	// SenderApproved records APPROVE_EXECUTION having been granted.
	SenderApproved bool

	// ReceiptStateGas[i] is the final attributed state gas of frame i
	// (gas_used.state in its frame receipt). Written (journaled) at frame
	// exit and possibly reduced later by cross-frame state-gas refills.
	// Entries are only meaningful after the frame has finalized.
	ReceiptStateGas []int64

	// StateGasOwners maps (address, slot) to the index of the frame that paid
	// the outstanding EIP-8037 SSTORE slot-creation charge. Only SSTORE
	// creation charges have cross-frame refill potential (EIP-8141: "Only one
	// refill can cross a frame boundary, an SSTORE clearing a slot that an
	// earlier frame created").
	StateGasOwners map[AddressSlot]int
}

// AddressSlot keys the StateGasOwners map.
type AddressSlot struct {
	Address common.Address
	Slot    common.Hash
}

// NewFrameState creates the per-transaction frame execution state for a
// transaction with the given number of frames.
func NewFrameState(numFrames int) *FrameState {
	return &FrameState{
		ReceiptStateGas: make([]int64, numFrames),
		StateGasOwners:  make(map[AddressSlot]int),
	}
}

func (f *FrameState) copy() *FrameState {
	cpy := &FrameState{
		Payer:          f.Payer,
		PayerSet:       f.PayerSet,
		SenderApproved: f.SenderApproved,
		ReceiptStateGas: append([]int64(nil), f.ReceiptStateGas...),
		StateGasOwners:  make(map[AddressSlot]int, len(f.StateGasOwners)),
	}
	for k, v := range f.StateGasOwners {
		cpy.StateGasOwners[k] = v
	}
	return cpy
}

// --- journal entries ---

type frameApprovalChange struct {
	prevPayer    common.Address
	prevPayerSet bool
	prevApproved bool
}

func (ch frameApprovalChange) revert(s *StateDB) {
	if s.frameState == nil {
		return
	}
	s.frameState.Payer = ch.prevPayer
	s.frameState.PayerSet = ch.prevPayerSet
	s.frameState.SenderApproved = ch.prevApproved
}

func (ch frameApprovalChange) mutation() (common.Address, journalMutationKind, bool) {
	return common.Address{}, journalMutationKindNone, false
}

func (ch frameApprovalChange) copy() journalEntry { return ch }

type frameReceiptStateGasChange struct {
	frame int
	prev  int64
}

func (ch frameReceiptStateGasChange) revert(s *StateDB) {
	if s.frameState == nil || ch.frame >= len(s.frameState.ReceiptStateGas) {
		return
	}
	s.frameState.ReceiptStateGas[ch.frame] = ch.prev
}

func (ch frameReceiptStateGasChange) mutation() (common.Address, journalMutationKind, bool) {
	return common.Address{}, journalMutationKindNone, false
}

func (ch frameReceiptStateGasChange) copy() journalEntry { return ch }

type frameOwnerChange struct {
	key         AddressSlot
	prevOwner   int
	prevExisted bool
}

func (ch frameOwnerChange) revert(s *StateDB) {
	if s.frameState == nil {
		return
	}
	if !ch.prevExisted {
		delete(s.frameState.StateGasOwners, ch.key)
		return
	}
	s.frameState.StateGasOwners[ch.key] = ch.prevOwner
}

func (ch frameOwnerChange) mutation() (common.Address, journalMutationKind, bool) {
	return common.Address{}, journalMutationKindNone, false
}

func (ch frameOwnerChange) copy() journalEntry { return ch }

// --- StateDB wiring ---

// SetFrameState attaches (or detaches, with nil) the frame-transaction
// execution state. Called by the frame transition engine at transaction
// boundaries; not journaled (a tx-boundary operation).
func (s *StateDB) SetFrameState(fs *FrameState) {
	s.frameState = fs
}

// FrameState returns the attached frame-transaction execution state, or nil
// when the current transaction is not a frame transaction.
func (s *StateDB) FrameState() *FrameState {
	return s.frameState
}

// SetFrameApproval journals and applies a change to the approval context.
func (s *StateDB) SetFrameApproval(payer common.Address, payerSet, approved bool) {
	if s.frameState == nil {
		return
	}
	s.journal.append(frameApprovalChange{
		prevPayer:    s.frameState.Payer,
		prevPayerSet: s.frameState.PayerSet,
		prevApproved: s.frameState.SenderApproved,
	})
	s.frameState.Payer = payer
	s.frameState.PayerSet = payerSet
	s.frameState.SenderApproved = approved
}

// SetFrameReceiptStateGas journals and sets the attributed state gas of a
// frame's receipt.
func (s *StateDB) SetFrameReceiptStateGas(frame int, value int64) {
	if s.frameState == nil || frame >= len(s.frameState.ReceiptStateGas) {
		return
	}
	s.journal.append(frameReceiptStateGasChange{frame: frame, prev: s.frameState.ReceiptStateGas[frame]})
	s.frameState.ReceiptStateGas[frame] = value
}

// SetFrameStateGasOwner journals and records the owning frame of an
// outstanding SSTORE state-gas charge for (addr, slot).
func (s *StateDB) SetFrameStateGasOwner(addr common.Address, slot common.Hash, frame int) {
	if s.frameState == nil {
		return
	}
	key := AddressSlot{Address: addr, Slot: slot}
	prev, existed := s.frameState.StateGasOwners[key]
	s.journal.append(frameOwnerChange{key: key, prevOwner: prev, prevExisted: existed})
	s.frameState.StateGasOwners[key] = frame
}

// ClearFrameStateGasOwner journals and removes the ownership record for
// (addr, slot) (a refill fired or the frame creating it reverted).
func (s *StateDB) ClearFrameStateGasOwner(addr common.Address, slot common.Hash) {
	if s.frameState == nil {
		return
	}
	key := AddressSlot{Address: addr, Slot: slot}
	prev, existed := s.frameState.StateGasOwners[key]
	if !existed {
		return
	}
	s.journal.append(frameOwnerChange{key: key, prevOwner: prev, prevExisted: true})
	delete(s.frameState.StateGasOwners, key)
}

// FrameStateGasOwner returns the frame owning the outstanding SSTORE
// state-gas charge for (addr, slot), if any.
func (s *StateDB) FrameStateGasOwner(addr common.Address, slot common.Hash) (int, bool) {
	if s.frameState == nil {
		return 0, false
	}
	owner, ok := s.frameState.StateGasOwners[AddressSlot{Address: addr, Slot: slot}]
	return owner, ok
}

// ResetTransientStorage discards all transient storage (EIP-1153). The frame
// transition engine calls this between frames (EIP-8141: "Discard the TSTORE
// and TLOAD transient storage between frames.").
func (s *StateDB) ResetTransientStorage() {
	s.transientStorage = newTransientStorage()
}
