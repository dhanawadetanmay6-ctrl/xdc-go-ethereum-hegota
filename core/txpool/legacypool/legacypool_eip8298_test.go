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

// EIP-8298 mempool tests: an account that adopts regular deployed code (e.g.
// via SETCODEFROM) can no longer originate ECDSA transactions (EIP-3607).
// The pool must reject new such transactions at admission and evict pending
// ones on head-change revalidation; EIP-8141 frame transactions stay valid.

package legacypool

import (
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// TestSetCodeFromPoolAdmissionRejectsCodedAccounts checks that new ECDSA
// transactions from an account with regular deployed code are rejected at
// admission, while delegated (EIP-7702) and frame transactions stay allowed.
func TestSetCodeFromPoolAdmissionRejectsCodedAccounts(t *testing.T) {
	t.Parallel()

	pool, key := setupPool()
	defer pool.Close()
	from := crypto.PubkeyToAddress(key.PublicKey)
	testAddBalance(pool, from, big.NewInt(0xffffffffffffff))

	// Baseline: plain ECDSA tx from an empty account is admissible.
	if err := pool.addRemoteSync(transaction(0, 100000, key)); err != nil {
		t.Fatalf("baseline ECDSA admission failed: %v", err)
	}

	// Account adopts regular code: new ECDSA transactions must be rejected.
	pool.mu.Lock()
	pool.currentState.SetCode(from, []byte{0x60, 0x00, 0x55, 0x00}, tracing.CodeChangeUnspecified)
	pool.mu.Unlock()
	if err := pool.addRemoteSync(transaction(1, 100000, key)); !errors.Is(err, core.ErrSenderNoEOA) {
		t.Fatalf("admission of ECDSA tx from coded account: err = %v, want %v", err, core.ErrSenderNoEOA)
	}

	// EIP-7702 delegated accounts (indicator, not regular code) remain
	// admissible (subject to the pre-existing delegation limit). The baseline
	// tx at nonce 0 is still pending, so replace it with a bumped price.
	pool.mu.Lock()
	pool.currentState.SetCode(from, types.AddressToDelegation(common.BytesToAddress([]byte{0x42})), tracing.CodeChangeUnspecified)
	pool.mu.Unlock()
	if err := pool.addRemoteSync(pricedTransaction(0, 100000, big.NewInt(2), key)); err != nil {
		t.Fatalf("delegated account replacement should stay admissible: %v", err)
	}
}

// TestSetCodeFromPoolEvictsPendingOnHeadChange checks that pending ECDSA
// transactions of an account that gains regular code are evicted during
// demoteUnexecutables, while frame transactions are preserved.
//
// The frame transaction sits at nonce 0 (the front of the list): evicting
// the ECDSA transaction at nonce 1 must not gap-demote it. (An ECDSA eviction
// below a frame transaction's nonce legitimately demotes the frame tx to the
// queue: the legacy nonce domain of a coded account advances only via frame
// transactions, so such a gap can only be filled by a new frame transaction.)
func TestSetCodeFromPoolEvictsPendingOnHeadChange(t *testing.T) {
	t.Parallel()

	pool, key := setupPool()
	defer pool.Close()
	from := crypto.PubkeyToAddress(key.PublicKey)
	testAddBalance(pool, from, big.NewInt(0xffffffffffffff))

	// Inject a frame transaction for the account directly into pending at
	// nonce 0 (bypassing signature admission; demoteUnexecutables must not
	// evict it).
	frameTx := types.NewTx(&types.FrameTx{
		ChainID:  uint256.MustFromBig(params.TestChainConfig.ChainID),
		Sender:   from,
		NonceSeq: 0,
		Frames: []types.Frame{{
			Mode:   types.FrameModeVerify,
			Flags:  types.FrameFlagApproveScopeMask,
			Limits: types.FrameGasLimits{Execution: 100_000, State: 100_000},
			Value:  new(uint256.Int),
		}},
		Signatures: []types.FrameSignature{{
			Scheme:    types.FrameSigSchemeSecp256k1,
			Signature: make([]byte, 65),
		}},
		Fees: types.FrameFees{
			MaxFee:               uint256.NewInt(10),
			MaxPriorityFeePerGas: uint256.NewInt(1),
		},
	})
	pool.mu.Lock()
	if pool.pending[from] == nil {
		pool.pending[from] = newList(true)
	}
	if added, _ := pool.pending[from].Add(frameTx, pool.config.PriceBump); !added {
		pool.mu.Unlock()
		t.Fatal("failed to inject frame tx")
	}
	pool.all.Add(frameTx)
	pool.pendingNonces.set(from, frameTx.Nonce()+1)
	pool.mu.Unlock()

	// ECDSA transaction at nonce 1 (nonce 0 filled by the frame tx).
	tx1 := transaction(1, 100000, key)
	if err := pool.addRemoteSync(tx1); err != nil {
		t.Fatal(err)
	}
	if pending, _ := pool.stats(); pending != 2 {
		t.Fatalf("pending = %d, want 2", pending)
	}

	// The account adopts regular code.
	pool.mu.Lock()
	pool.currentState.SetCode(from, []byte{0x60, 0x00, 0x55, 0x00}, tracing.CodeChangeUnspecified)
	pool.demoteUnexecutables()
	pendingList := pool.pending[from]
	pool.mu.Unlock()

	if pendingList == nil || !pendingList.Contains(0) {
		t.Fatal("frame transaction must survive the code-adoption eviction")
	}
	if pendingList.Contains(1) {
		t.Fatal("ECDSA transaction of code-adopted account must be evicted")
	}
	if pool.all.Get(frameTx.Hash()) == nil {
		t.Fatal("frame transaction must remain in the lookup")
	}
	if pool.all.Get(tx1.Hash()) != nil {
		t.Fatal("evicted ECDSA transaction must leave the lookup")
	}
}
