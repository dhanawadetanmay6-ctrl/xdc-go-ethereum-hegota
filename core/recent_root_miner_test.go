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

// EIP-8272 pre-inclusion validation tests (miner path): the payload's actual
// slotNumber governs, and an invalid transaction must not enter a block.

package core

import (
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
)

// rrPreInclusionTx builds a signed frame tx with a recent root verifier frame
// referencing the given tuples.
func rrPreInclusionTx(t *testing.T, nonce uint64, tuples []types.RecentRootTuple) *types.Transaction {
	t.Helper()
	return rrBuildOrdered(t, nonce, ftSender, []types.Frame{
		rrRecentRootFrame(200_000, RecentRootValidationCalldata(tuples)),
		rrSelfVerifyFrame(),
	})
}

func TestCheckRecentRootPreInclusion(t *testing.T) {
	setup := func(t *testing.T) (*state.StateDB, types.RecentRootTuple) {
		sdb := mkState(types.GenesisAlloc{ftSender: {Balance: big.NewInt(1e18)}})
		DeployRecentRoot(sdb)
		evm := rrEVM(sdb, 50)
		salt := common.HexToHash("0x01")
		root := common.HexToHash("0x02")
		if _, err := rrWrite(t, sdb, evm, rrSource, salt, root, 1_000_000); err != nil {
			t.Fatalf("write: %v", err)
		}
		return sdb, rrTuple(rrSource, salt, 50, root)
	}

	t.Run("valid at payload slot", func(t *testing.T) {
		sdb, tuple := setup(t)
		tx := rrPreInclusionTx(t, 0, []types.RecentRootTuple{tuple})
		slot := uint64(51)
		if err := CheckRecentRootPreInclusion(sdb, &slot, tx); err != nil {
			t.Fatalf("rejected: %v", err)
		}
	})
	t.Run("expired at payload slot", func(t *testing.T) {
		sdb, tuple := setup(t)
		tx := rrPreInclusionTx(t, 0, []types.RecentRootTuple{tuple})
		slot := uint64(50 + 8192) // payload slot: age 8192
		if err := CheckRecentRootPreInclusion(sdb, &slot, tx); !errors.Is(err, ErrRecentRootExpired) {
			t.Fatalf("got %v, want ErrRecentRootExpired", err)
		}
	})
	t.Run("invalidated between admission and inclusion", func(t *testing.T) {
		sdb, tuple := setup(t)
		tx := rrPreInclusionTx(t, 0, []types.RecentRootTuple{tuple})
		slot := uint64(51)
		// Entry removed after admission (state change before inclusion).
		sdb.SetState(types.RecentRootAddress, tuple.StorageKey(), common.Hash{})
		if err := CheckRecentRootPreInclusion(sdb, &slot, tx); !errors.Is(err, ErrRecentRootEntryMismatch) {
			t.Fatalf("got %v, want ErrRecentRootEntryMismatch", err)
		}
	})
	t.Run("missing contract code", func(t *testing.T) {
		sdb, tuple := setup(t)
		tx := rrPreInclusionTx(t, 0, []types.RecentRootTuple{tuple})
		sdb.SetCode(types.RecentRootAddress, nil, tracing.CodeChangeUnspecified)
		slot := uint64(51)
		if err := CheckRecentRootPreInclusion(sdb, &slot, tx); !errors.Is(err, ErrRecentRootCodeMismatch) {
			t.Fatalf("got %v, want ErrRecentRootCodeMismatch", err)
		}
	})
	t.Run("changed contract code", func(t *testing.T) {
		sdb, tuple := setup(t)
		tx := rrPreInclusionTx(t, 0, []types.RecentRootTuple{tuple})
		sdb.SetCode(types.RecentRootAddress, []byte{0x00}, tracing.CodeChangeUnspecified)
		slot := uint64(51)
		if err := CheckRecentRootPreInclusion(sdb, &slot, tx); !errors.Is(err, ErrRecentRootCodeMismatch) {
			t.Fatalf("got %v, want ErrRecentRootCodeMismatch", err)
		}
	})
	t.Run("nil payload slot", func(t *testing.T) {
		sdb, tuple := setup(t)
		tx := rrPreInclusionTx(t, 0, []types.RecentRootTuple{tuple})
		if err := CheckRecentRootPreInclusion(sdb, nil, tx); err == nil {
			t.Fatal("nil slot accepted")
		}
	})
	t.Run("non-frame tx passes through", func(t *testing.T) {
		sdb, _ := setup(t)
		legacy := types.NewTx(&types.LegacyTx{Nonce: 0, GasPrice: big.NewInt(1), Gas: 21000})
		if err := CheckRecentRootPreInclusion(sdb, nil, legacy); err != nil {
			t.Fatalf("legacy tx blocked: %v", err)
		}
	})
	t.Run("frame tx without recent root frame passes through", func(t *testing.T) {
		sdb, _ := setup(t)
		tx := rrBuildOrdered(t, 0, ftSender, []types.Frame{rrSelfVerifyFrame()})
		if err := CheckRecentRootPreInclusion(sdb, nil, tx); err != nil {
			t.Fatalf("plain frame tx blocked: %v", err)
		}
	})
}

// TestRecentRootPayloadSlotExpiryAtEngine proves the authoritative block-side
// guard: a frame transaction whose referenced root has expired (or was never
// written) at the payload's slotNumber is invalid and cannot enter a block.
func TestRecentRootPayloadSlotExpiryAtEngine(t *testing.T) {
	sdb := mkState(types.GenesisAlloc{
		ftSender: {Balance: big.NewInt(1e18)},
		rrSource: {Balance: big.NewInt(1e18)},
	})
	DeployRecentRoot(sdb)
	// Write during slot 50.
	evm := rrEVM(sdb, 50)
	salt := common.HexToHash("0x01")
	root := common.HexToHash("0x02")
	if _, err := rrWrite(t, sdb, evm, rrSource, salt, root, 1_000_000); err != nil {
		t.Fatalf("write: %v", err)
	}
	tuple := rrTuple(rrSource, salt, 50, root)

	// Payload at slot 51: valid, must execute.
	tx := rrPreInclusionTx(t, 0, []types.RecentRootTuple{tuple})
	evm.Context.SlotNum = 51
	res, _, err := applyFrameTx(t, sdb, evm, tx)
	if err != nil {
		t.Fatalf("payload slot 51 rejected: %v", err)
	}
	if res.FrameReceipts[0].Status != types.FrameStatusSucceeded {
		t.Fatal("recent root frame failed at payload slot 51")
	}

	// Payload at slot 50+8192: the same reference has aged out; the
	// transaction must be invalid (VERIFY frame reverts → tx invalid).
	sdb2 := mkState(types.GenesisAlloc{
		ftSender: {Balance: big.NewInt(1e18)},
		rrSource: {Balance: big.NewInt(1e18)},
	})
	DeployRecentRoot(sdb2)
	evm2 := rrEVM(sdb2, 50)
	if _, err := rrWrite(t, sdb2, evm2, rrSource, salt, root, 1_000_000); err != nil {
		t.Fatalf("write2: %v", err)
	}
	tx2 := rrPreInclusionTx(t, 0, []types.RecentRootTuple{tuple})
	evm2.Context.SlotNum = 50 + 8192
	if _, _, err := applyFrameTx(t, sdb2, evm2, tx2); err == nil {
		t.Fatal("expired reference accepted at payload slot")
	}

	// Payload on a branch where the write never happened (reorg removal).
	sdb3 := mkState(types.GenesisAlloc{
		ftSender: {Balance: big.NewInt(1e18)},
		rrSource: {Balance: big.NewInt(1e18)},
	})
	DeployRecentRoot(sdb3) // deployed, but no write
	evm3 := rrEVM(sdb3, 51)
	tx3 := rrPreInclusionTx(t, 0, []types.RecentRootTuple{tuple})
	if _, _, err := applyFrameTx(t, sdb3, evm3, tx3); err == nil {
		t.Fatal("reorg-removed root accepted at payload")
	}
}

// TestRecentRootEnginePayloadSlotFromHeaderOnly proves current_slot flows
// exclusively from the EIP-7843 header field: two payloads with identical
// timestamps but different slotNumbers behave differently.
func TestRecentRootEnginePayloadSlotFromHeaderOnly(t *testing.T) {
	sdb := mkState(types.GenesisAlloc{
		ftSender: {Balance: big.NewInt(1e18)},
		rrSource: {Balance: big.NewInt(1e18)},
	})
	DeployRecentRoot(sdb)
	evm := rrEVM(sdb, 60)
	salt := common.HexToHash("0x01")
	root := common.HexToHash("0x02")
	if _, err := rrWrite(t, sdb, evm, rrSource, salt, root, 1_000_000); err != nil {
		t.Fatalf("write: %v", err)
	}
	tuple := rrTuple(rrSource, salt, 60, root)
	tx := rrPreInclusionTx(t, 0, []types.RecentRootTuple{tuple})

	// Same timestamp, slot 61 → valid.
	evmA := frameEVM(sdb, nil)
	evmA.Context.Time = 999999
	evmA.Context.SlotNum = 61
	if _, _, err := applyFrameTx(t, sdb, evmA, tx); err != nil {
		t.Fatalf("slot 61: %v", err)
	}
	// Same timestamp, slot 60 (current-slot reference) → invalid.
	sdbB := mkState(types.GenesisAlloc{
		ftSender: {Balance: big.NewInt(1e18)},
		rrSource: {Balance: big.NewInt(1e18)},
	})
	DeployRecentRoot(sdbB)
	evmB := frameEVM(sdbB, nil)
	evmB.Context.Time = 999999
	if _, err := rrWrite(t, sdbB, evmB, rrSource, salt, root, 1_000_000); err != nil {
		t.Fatalf("write B: %v", err)
	}
	evmB.Context.SlotNum = 60
	if _, _, err := applyFrameTx(t, sdbB, evmB, tx); err == nil {
		t.Fatal("slot-from-timestamp behavior detected: current-slot reference accepted")
	}
}
