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

package legacypool

import (
	"errors"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
)

// EIP-8272 recent root dependency tracking for the public mempool.
//
// On admission of a frame transaction carrying a recent root verifier frame,
// the pool records:
//
//   - every distinct (storage_key, entry_hash) dependency of the frame's
//     tuples, indexed by storage key so a slot advance or reorganization does
//     not require scanning unrelated transactions;
//   - the first current_slot at which the transaction expires
//     (min over tuples of slot + RECENT_ROOT_LENGTH);
//   - the code of RECENT_ROOT_ADDRESS and the fork activation status as
//     implicit dependencies (revalidated for every recent-root transaction on
//     every head change).
//
// Records are removed when the transaction leaves the pool and replaced
// atomically with the transaction on replacement.

// recentRootDep is the dependency record of one pending transaction.
type recentRootDep struct {
	// tuples are the parsed references of the recent root verifier frame.
	tuples []types.RecentRootTuple
	// keys holds the distinct storage keys referenced by the tuples.
	keys []common.Hash
	// entryHash maps each storage key to the entry hash admitted against it.
	entryHash map[common.Hash]common.Hash
	// expirySlot is the first mempool current_slot at which the transaction
	// must be evicted: min(slot) + RECENT_ROOT_LENGTH over the tuples.
	expirySlot uint64
}

// rrDeps maps transaction hash to its recent root dependency record.
// rrByKey indexes storage key → set of transaction hashes referencing it.
//
// Both are guarded by pool.mu.

// currentSlotRecentRoot returns the mempool view of the current slot: one
// greater than the slotNumber of the latest canonical header (EIP-8272
// "Current slot"). It deliberately does not advance during empty slots. The
// second return value is false when the head carries no slot number
// (EIP-7843 inactive — frame transactions cannot be valid there anyway).
func (pool *LegacyPool) currentSlotRecentRoot() (uint64, bool) {
	head := pool.currentHead.Load()
	if head == nil || head.SlotNumber == nil {
		return 0, false
	}
	return *head.SlotNumber + 1, true
}

// validateRecentRootTx performs the stateful EIP-8272 admission checks for a
// frame transaction carrying a recent root verifier frame. Called from
// validateTx with pool.mu held; pool.currentState is the head state.
//
// Checks (EIP-8272 "Public mempool handling"):
//
//  1. The runtime code at RECENT_ROOT_ADDRESS exactly equals
//     RECENT_ROOT_CODE (else reject).
//  2. Every tuple satisfies slot < current_slot and
//     current_slot - slot <= RECENT_ROOT_USABLE_WINDOW, and the stored entry
//     hash matches (cheap direct evaluation).
//  3. The frame executes successfully under STATICCALL semantics with
//     BlockContext.SlotNum = current_slot and the EIP-8272 SLOTNUM/SLOAD
//     permissions scoped to the canonical top-level code, within the frame's
//     declared limits.execution (full EVM simulation with permission tracer).
func (pool *LegacyPool) validateRecentRootTx(tx *types.Transaction) error {
	ft := tx.FrameTxData()
	if ft == nil {
		return nil
	}
	idx := types.RecentRootVerifyFrameIndexes(ft)
	if len(idx) == 0 {
		// No canonical recent root verifier frame. Note: a malformed VERIFY
		// frame to RECENT_ROOT_ADDRESS is rejected statelessly by
		// ValidateFrameRecentRootPolicy before state is read.
		return nil
	}
	rules := pool.chainconfig.Rules(pool.currentHead.Load().Number, true, pool.currentHead.Load().Time)
	if !rules.IsBogota {
		return nil // pre-activation: stateless gate already rejected frame txs
	}
	head := pool.currentHead.Load()
	currentSlot, ok := pool.currentSlotRecentRoot()
	if !ok {
		return errors.New("recent root validation requires EIP-7843 slot number on head")
	}
	if !core.RecentRootCodeMatches(pool.currentState) {
		return core.ErrRecentRootCodeMismatch
	}
	frame := &ft.Frames[idx[0]]
	tuples, err := types.ParseRecentRootTuples(frame.Data)
	if err != nil {
		return err
	}
	if err := core.CheckRecentRootTuples(pool.currentState, currentSlot, tuples); err != nil {
		return err
	}
	if _, err := core.SimulateRecentRootVerifyFrame(pool.currentState, pool.chainconfig, head, ft.Sender, frame); err != nil {
		return err
	}
	return nil
}

// registerRecentRootDeps records the dependencies of an admitted transaction
// carrying a recent root verifier frame. No-op for other transactions.
// Callers must hold pool.mu and have inserted the transaction already.
func (pool *LegacyPool) registerRecentRootDeps(tx *types.Transaction) {
	hash := tx.Hash()
	pool.dropRecentRootDeps(hash) // idempotent re-registration
	ft := tx.FrameTxData()
	if ft == nil {
		return
	}
	idx := types.RecentRootVerifyFrameIndexes(ft)
	if len(idx) != 1 {
		return
	}
	tuples, err := types.ParseRecentRootTuples(ft.Frames[idx[0]].Data)
	if err != nil {
		return // unreachable: shape and encoding checked at admission
	}
	dep := &recentRootDep{
		tuples:    tuples,
		entryHash: make(map[common.Hash]common.Hash),
		expirySlot: func() uint64 {
			min := tuples[0].Slot
			for _, t := range tuples[1:] {
				if t.Slot < min {
					min = t.Slot
				}
			}
			return min + types.RecentRootLength
		}(),
	}
	seen := make(map[common.Hash]struct{})
	for i := range tuples {
		key := tuples[i].StorageKey()
		if _, ok := seen[key]; ok {
			continue // distinct keys only
		}
		seen[key] = struct{}{}
		dep.keys = append(dep.keys, key)
		dep.entryHash[key] = tuples[i].EntryHash()
		if pool.rrByKey[key] == nil {
			pool.rrByKey[key] = make(map[common.Hash]struct{})
		}
		pool.rrByKey[key][hash] = struct{}{}
	}
	pool.rrDeps[hash] = dep
}

// dropRecentRootDeps removes the dependency records of a transaction leaving
// the pool (eviction, replacement, inclusion). Callers must hold pool.mu.
func (pool *LegacyPool) dropRecentRootDeps(hash common.Hash) {
	dep, ok := pool.rrDeps[hash]
	if !ok {
		return
	}
	for _, key := range dep.keys {
		if set := pool.rrByKey[key]; set != nil {
			delete(set, hash)
			if len(set) == 0 {
				delete(pool.rrByKey, key)
			}
		}
	}
	delete(pool.rrDeps, hash)
}

// revalidateRecentRootFrames rechecks every pending transaction carrying a
// recent root verifier frame after a head change (slot advance or chain
// reorganization) and evicts those whose dependencies no longer hold.
//
// The sweep re-reads state at the new head, which inherently covers both
// reorganization branches: entries removed by rollback (with or without a
// replacement write) read as zero/changed on the new canonical branch, and
// writes added by the new branch appear. A change to the code of
// RECENT_ROOT_ADDRESS or a crossing of the fork activation boundary
// revalidates — and on failure evicts — every such transaction.
//
// Callers must hold pool.mu. Returns the number of evicted transactions.
func (pool *LegacyPool) revalidateRecentRootFrames(oldHead, newHead *types.Header) int {
	if len(pool.rrDeps) == 0 {
		return 0
	}
	// Activation flip: if Bogota (or the underlying Amsterdam/SLOTNUM stack)
	// toggled between the heads, every recent-root transaction is suspect.
	activationFlip := false
	if oldHead != nil && newHead != nil {
		oldRules := pool.chainconfig.Rules(oldHead.Number, true, oldHead.Time)
		newRules := pool.chainconfig.Rules(newHead.Number, true, newHead.Time)
		activationFlip = oldRules.IsBogota != newRules.IsBogota || oldRules.IsAmsterdam != newRules.IsAmsterdam
	}
	currentSlot, slotOK := pool.currentSlotRecentRoot()
	codeOK := core.RecentRootCodeMatches(pool.currentState)

	var evict []common.Hash
	for hash, dep := range pool.rrDeps {
		tx := pool.all.Get(hash)
		if tx == nil {
			// Transaction left the pool without a recorded cleanup; drop the
			// record now (defensive: all removal sites call dropRecentRootDeps).
			pool.dropRecentRootDeps(hash)
			continue
		}
		reason := ""
		switch {
		case activationFlip:
			reason = "fork activation change"
		case !slotOK:
			reason = "head without slot number"
		case !codeOK:
			reason = "recent root code changed"
		default:
			// Age predicates: evict if any tuple has slot >= current_slot, or
			// when current_slot reaches the recorded expiry slot
			// (current_slot - slot >= RECENT_ROOT_LENGTH).
			if currentSlot >= dep.expirySlot {
				reason = "recent root reference expired"
				break
			}
			for i := range dep.tuples {
				if dep.tuples[i].Slot >= currentSlot {
					reason = "recent root reference to current/future slot"
					break
				}
			}
			if reason != "" {
				break
			}
			// Storage predicates: every recorded (storage_key, entry_hash)
			// must still match at the new head.
			for _, key := range dep.keys {
				if pool.currentState.GetState(types.RecentRootAddress, key) != dep.entryHash[key] {
					reason = "recent root entry changed"
					break
				}
			}
		}
		if reason != "" {
			log.Trace("Evicting transaction on recent root dependency change", "hash", hash, "reason", reason)
			evict = append(evict, hash)
		}
	}
	for _, hash := range evict {
		pool.removeTx(hash, true, true)
	}
	return len(evict)
}

// recentRootPreInclusionCheck was moved to core.CheckRecentRootPreInclusion
// so both the pool and the miner share one implementation.
