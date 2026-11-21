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

// EIP-7906 transaction state-diff construction for the TXTRACE and
// EVENTDATACOPY opcodes.
//
// The diff is the net difference between the transaction prestate and the
// state at the time of the opcode call. It is derived entirely from data the
// client already records per transaction (spec: "TXTRACE reads data that the
// client already holds"): the state journal carries every mutation with its
// previous value, the per-account mutation stash holds the pre-tx nonce and
// code (maintained for the EIP-7928 block-level access list), and the tx logs
// hold the emitted events. Reverted writes vanish from the journal with the
// rollback, and writes that restore the original value collapse via the
// net-change comparison, so the surviving entries describe exactly the
// transaction's net diff.
//
// The diff is built lazily on first use and cached on the StateDB for the
// remainder of the transaction. That is sound because the opcodes are valid
// only inside ASSERT frames, which form a read-only trailing suffix of the
// transaction: no state writes or logs can occur while the suffix executes,
// and an ASSERT failure ends frame execution, so no read ever observes a
// stale cache.

package state

import (
	"bytes"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
)

// TxTraceBalanceChange is one entry of the TXTRACE balances table.
type TxTraceBalanceChange struct {
	Address common.Address
	Before  *uint256.Int
	After   *uint256.Int
}

// TxTraceSlotChange is one entry of the TXTRACE storage table.
type TxTraceSlotChange struct {
	Address common.Address
	Key     common.Hash
	Before  common.Hash
	After   common.Hash
}

// TxTraceCodeChange is one entry of the TXTRACE code table (code hashes).
type TxTraceCodeChange struct {
	Address common.Address
	Before  common.Hash
	After   common.Hash
}

// TxTraceNonceChange is one entry of the TXTRACE nonce table.
type TxTraceNonceChange struct {
	Address common.Address
	Before  uint64
	After   uint64
}

// TxTraceDiff is the transaction state diff exposed by EIP-7906. The tables
// are sorted ascending by address (uint160); the storage table is additionally
// sorted by slot key (uint256) within each address. Events are in emission
// order, matching their global log index within the transaction.
type TxTraceDiff struct {
	Balances []TxTraceBalanceChange
	Slots    []TxTraceSlotChange
	Codes    []TxTraceCodeChange
	Nonces   []TxTraceNonceChange
	Events   []*types.Log
}

// TxTraceDiff returns the cached transaction state diff, building it on first
// call. See the file-level comment for the cache-validity invariant.
func (s *StateDB) TxTraceDiff() *TxTraceDiff {
	if s.txTraceDiff != nil {
		return s.txTraceDiff
	}
	s.txTraceDiff = s.buildTxTraceDiff()
	return s.txTraceDiff
}

// buildTxTraceDiff computes the transaction state diff from the journal and
// the live state.
func (s *StateDB) buildTxTraceDiff() *TxTraceDiff {
	d := &TxTraceDiff{}

	// Balances: first previous value per account among the live journal
	// entries is the pre-transaction balance (reverted entries are gone; a
	// post-revert rewrite re-journals against the reverted — i.e. original —
	// value). Compare against the current balance for the net change.
	balanceBefore := make(map[common.Address]*uint256.Int)
	for _, e := range s.journal.entries {
		if bc, ok := e.(balanceChange); ok {
			if _, seen := balanceBefore[bc.account]; !seen {
				balanceBefore[bc.account] = bc.prev
			}
		}
	}
	for addr, before := range balanceBefore {
		after := s.GetBalance(addr)
		if after.Cmp(before) != 0 {
			d.Balances = append(d.Balances, TxTraceBalanceChange{
				Address: addr,
				Before:  new(uint256.Int).Set(before),
				After:   new(uint256.Int).Set(after),
			})
		}
	}

	// Nonces and codes: the journal mutation stash holds the pre-transaction
	// values, cleared on full revert and re-stashed on re-mutation.
	for addr, ms := range s.journal.mutations {
		if ms.nonceSet {
			if after := s.GetNonce(addr); after != ms.nonce {
				d.Nonces = append(d.Nonces, TxTraceNonceChange{Address: addr, Before: ms.nonce, After: after})
			}
		}
		if ms.codeSet {
			before := types.EmptyCodeHash
			if len(ms.code) != 0 {
				before = crypto.Keccak256Hash(ms.code)
			}
			after := s.GetCodeHash(addr)
			if after == (common.Hash{}) {
				// A non-existent account counts as having the empty-code hash
				// (the account can be observed mid-transaction only before
				// end-of-transaction deletions).
				after = types.EmptyCodeHash
			}
			if before != after {
				d.Codes = append(d.Codes, TxTraceCodeChange{Address: addr, Before: before, After: after})
			}
		}
	}

	// Storage slots: first original (committed, pre-transaction) value per
	// (address, slot) among live journal entries; compare against current.
	slotBefore := make(map[AddressSlot]common.Hash)
	for _, e := range s.journal.entries {
		if sc, ok := e.(storageChange); ok {
			key := AddressSlot{Address: sc.account, Slot: sc.key}
			if _, seen := slotBefore[key]; !seen {
				slotBefore[key] = sc.origvalue
			}
		}
	}
	for key, before := range slotBefore {
		if after := s.GetState(key.Address, key.Slot); after != before {
			d.Slots = append(d.Slots, TxTraceSlotChange{
				Address: key.Address,
				Key:     key.Slot,
				Before:  before,
				After:   after,
			})
		}
	}

	// Events: the transaction's logs in emission order. This includes logs
	// the protocol emits on the transaction's behalf (EIP-7708 transfer logs).
	d.Events = s.logs[s.thash]

	// Deterministic enumeration: address-ascending tables, slots by key
	// within an address; events keep emission order.
	sort.Slice(d.Balances, func(i, j int) bool {
		return bytes.Compare(d.Balances[i].Address[:], d.Balances[j].Address[:]) < 0
	})
	sort.Slice(d.Codes, func(i, j int) bool {
		return bytes.Compare(d.Codes[i].Address[:], d.Codes[j].Address[:]) < 0
	})
	sort.Slice(d.Nonces, func(i, j int) bool {
		return bytes.Compare(d.Nonces[i].Address[:], d.Nonces[j].Address[:]) < 0
	})
	sort.Slice(d.Slots, func(i, j int) bool {
		if c := bytes.Compare(d.Slots[i].Address[:], d.Slots[j].Address[:]); c != 0 {
			return c < 0
		}
		return bytes.Compare(d.Slots[i].Key[:], d.Slots[j].Key[:]) < 0
	})
	return d
}
