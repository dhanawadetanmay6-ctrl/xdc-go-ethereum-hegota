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

// EIP-8272 recent root public-mempool tests: admission, dependency tracking,
// slot advancement, reorganization, replacement and cleanup.

package legacypool

import (
	"crypto/ecdsa"
	"math/big"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// rrChain is a minimal BlockChain implementation carrying a settable head
// with EIP-7843 slot number and a shared head state.
type rrChain struct {
	config  *params.ChainConfig
	head    atomic.Pointer[types.Header]
	statedb *state.StateDB
	feed    *event.Feed
}

func (c *rrChain) Config() *params.ChainConfig { return c.config }
func (c *rrChain) CurrentBlock() *types.Header { return c.head.Load() }
func (c *rrChain) GetBlock(hash common.Hash, number uint64) *types.Block {
	return nil // reorg reinjection not exercised; sweeps are driven directly
}
func (c *rrChain) StateAt(header *types.Header) (*state.StateDB, error) { return c.statedb, nil }
func (c *rrChain) Genesis() *types.Block                                { return nil }
func (c *rrChain) SubscribeChainHeadEvent(ch chan<- core.ChainHeadEvent) event.Subscription {
	return c.feed.Subscribe(ch)
}

func rrChainConfig() *params.ChainConfig {
	cfg := *params.MergedTestChainConfig
	cfg.AmsterdamTime = new(uint64)
	cfg.BogotaTime = new(uint64)
	return &cfg
}

// rrHead builds a head header with the given slot number. Number/Time stay 0
// so Amsterdam/Bogota (time 0) are active.
func rrHead(slot uint64) *types.Header {
	return &types.Header{
		Number:     new(big.Int),
		Difficulty: new(big.Int),
		GasLimit:   30_000_000,
		BaseFee:    big.NewInt(1_000_000_000),
		SlotNumber: &slot,
	}
}

var (
	rrPoolKey, _   = crypto.HexToECDSA("8b3a350cf5c34c9194ef858e544b26a52b27f6d0b43a63b0b0b0b0b0b0b0b002")
	rrPoolSender   = crypto.PubkeyToAddress(rrPoolKey.PublicKey)
	rrWriterKey, _ = crypto.HexToECDSA("8b3a350cf5c34c9194ef858e544b26a52b27f6d0b43a63b0b0b0b0b0b0b0b003")
	rrWriter       = crypto.PubkeyToAddress(rrWriterKey.PublicKey)
)

// setupRecentRootPool creates a pool with a Bogota-active head at the given
// slot, the canonical contract deployed, and a funded frame-tx sender.
func setupRecentRootPool(t *testing.T, headSlot uint64) (*LegacyPool, *rrChain, *ecdsa.PrivateKey) {
	t.Helper()
	statedb, _ := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	statedb.CreateAccount(rrPoolSender)
	statedb.AddBalance(rrPoolSender, uint256.NewInt(1e18), tracing.BalanceChangeUnspecified)
	statedb.CreateAccount(rrWriter)
	statedb.AddBalance(rrWriter, uint256.NewInt(1e18), tracing.BalanceChangeUnspecified)
	statedb.CreateAccount(types.RecentRootAddress)
	statedb.SetCode(types.RecentRootAddress, types.RecentRootCode, tracing.CodeChangeUnspecified)
	statedb.Finalise(true)

	chain := &rrChain{config: rrChainConfig(), statedb: statedb, feed: new(event.Feed)}
	chain.head.Store(rrHead(headSlot))
	pool := New(testTxPoolConfig, chain)
	if err := pool.Init(testTxPoolConfig.PriceLimit, chain.CurrentBlock(), newReserver()); err != nil {
		t.Fatalf("pool init: %v", err)
	}
	<-pool.initDoneCh
	return pool, chain, rrPoolKey
}

// rrStoreRoot writes a recent root entry directly into state (equivalent to
// the contract's write operation at the given slot).
func rrStoreRoot(t *testing.T, sdb *state.StateDB, source common.Address, salt common.Hash, slot uint64, root common.Hash) types.RecentRootTuple {
	t.Helper()
	tuple := types.RecentRootTuple{
		SourceID: types.RecentRootSourceID(source, salt),
		Slot:     slot,
		Root:     root,
	}
	sdb.SetState(types.RecentRootAddress, tuple.StorageKey(), tuple.EntryHash())
	return tuple
}

// rrPoolTx builds a signed frame transaction [recent_root_verify, self_verify]
// with the given tuples, nonce and fee level.
func rrPoolTx(t *testing.T, key *ecdsa.PrivateKey, sender common.Address, nonce uint64, tuples []types.RecentRootTuple, maxFee int64, tip int64) *types.Transaction {
	t.Helper()
	data := make([]byte, 0, len(tuples)*types.RecentRootTupleBytes)
	for _, tup := range tuples {
		data = append(data, tup.SourceID[:]...)
		var slotBE [8]byte
		for i := 0; i < 8; i++ {
			slotBE[i] = byte(tup.Slot >> (56 - 8*i))
		}
		data = append(data, slotBE[:]...)
		data = append(data, tup.Root[:]...)
	}
	ft := &types.FrameTx{
		ChainID: uint256.MustFromBig(rrChainConfig().ChainID),
		Nonce:   nonce,
		Sender:  sender,
		Frames: []types.Frame{
			{
				Mode:   types.FrameModeVerify,
				Flags:  0,
				Target: &[]common.Address{types.RecentRootAddress}[0],
				Limits: types.FrameGasLimits{Execution: 20_000},
				Value:  new(uint256.Int),
				Data:   data,
			},
			{
				Mode:   types.FrameModeVerify,
				Flags:  types.FrameFlagApproveScopeMask,
				Limits: types.FrameGasLimits{Execution: 30_000},
				Value:  new(uint256.Int),
			},
		},
		Signatures: []types.FrameSignature{{
			Scheme:    types.FrameSigSchemeSecp256k1,
			Signature: make([]byte, 65),
		}},
		Fees: types.FrameFees{
			MaxFee:               uint256.NewInt(uint64(maxFee)),
			MaxPriorityFeePerGas: uint256.NewInt(uint64(tip)),
		},
	}
	signed, err := types.SignFrameTx(key, types.NewTx(ft))
	if err != nil {
		t.Fatalf("SignFrameTx: %v", err)
	}
	return signed
}

func rrAddSync(t *testing.T, pool *LegacyPool, tx *types.Transaction) error {
	t.Helper()
	errs := pool.Add([]*types.Transaction{tx}, true)
	return errs[0]
}

// TestPoolRecentRootAdmission: a transaction with a valid recent root
// verifier frame is admitted and its dependencies are registered.
func TestPoolRecentRootAdmission(t *testing.T) {
	pool, _, key := setupRecentRootPool(t, 100) // head slot 100 → current_slot 101
	salt := common.HexToHash("0x01")
	root := common.HexToHash("0x02")
	tuple := rrStoreRoot(t, pool.currentState, rrWriter, salt, 100, root)

	tx := rrPoolTx(t, key, rrPoolSender, 0, []types.RecentRootTuple{tuple}, 1e15, 1_000_000_000)
	if err := rrAddSync(t, pool, tx); err != nil {
		t.Fatalf("valid recent root tx rejected: %v", err)
	}
	if !pool.Has(tx.Hash()) {
		t.Fatal("tx not in pool")
	}
	// Dependency registration.
	dep, ok := pool.rrDeps[tx.Hash()]
	if !ok {
		t.Fatal("no dependency record registered")
	}
	if len(dep.keys) != 1 || dep.keys[0] != tuple.StorageKey() {
		t.Fatalf("dep keys %v", dep.keys)
	}
	if dep.entryHash[tuple.StorageKey()] != tuple.EntryHash() {
		t.Fatal("entry hash not recorded")
	}
	if dep.expirySlot != 100+types.RecentRootLength {
		t.Fatalf("expiry slot = %d, want %d", dep.expirySlot, 100+types.RecentRootLength)
	}
	if set := pool.rrByKey[tuple.StorageKey()]; set == nil {
		t.Fatal("storage key not indexed")
	} else if _, ok := set[tx.Hash()]; !ok {
		t.Fatal("tx missing from storage key index")
	}
}

// TestPoolRecentRootAdmissionRejections covers stateful rejection causes.
func TestPoolRecentRootAdmissionRejections(t *testing.T) {
	pool, _, key := setupRecentRootPool(t, 100)
	salt := common.HexToHash("0x01")
	root := common.HexToHash("0x02")
	tuple := rrStoreRoot(t, pool.currentState, rrWriter, salt, 100, root)

	cases := []struct {
		name   string
		mutate func() []types.RecentRootTuple
	}{
		{"invalid source id", func() []types.RecentRootTuple {
			return []types.RecentRootTuple{{SourceID: common.HexToHash("0xdead"), Slot: 100, Root: root}}
		}},
		{"invalid root", func() []types.RecentRootTuple {
			return []types.RecentRootTuple{rrTupleOf(tuple, common.HexToHash("0x03"))}
		}},
		// (The expired-reference boundary is covered by the dedicated pools
		// below: age 8192 rejects, age 8191 admits.)
		{"current slot", func() []types.RecentRootTuple {
			return []types.RecentRootTuple{rrTupleSlot(tuple, 101)}
		}},
		{"future slot", func() []types.RecentRootTuple {
			return []types.RecentRootTuple{rrTupleSlot(tuple, 102)}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tx := rrPoolTx(t, key, rrPoolSender, 0, c.mutate(), 1e15, 1_000_000_000)
			if err := rrAddSync(t, pool, tx); err == nil {
				t.Fatal("admitted, want rejection")
			} else {
				t.Logf("rejected: %v", err)
			}
		})
	}

	// Expired reference at the exact boundary: separate pool at head slot
	// 100+8191 (current_slot = 100+8192 → age 8192 → reject).
	pool2, _, key2 := setupRecentRootPool(t, 100+8191)
	rrStoreRoot(t, pool2.currentState, rrWriter, salt, 100, root)
	tx := rrPoolTx(t, key2, rrPoolSender, 0, []types.RecentRootTuple{tuple}, 1e15, 1_000_000_000)
	if err := rrAddSync(t, pool2, tx); err == nil {
		t.Fatal("age-8192 reference admitted")
	}

	// Age 8191 boundary admits.
	pool3, _, key3 := setupRecentRootPool(t, 100+8190)
	rrStoreRoot(t, pool3.currentState, rrWriter, salt, 100, root)
	tx3 := rrPoolTx(t, key3, rrPoolSender, 0, []types.RecentRootTuple{tuple}, 1e15, 1_000_000_000)
	if err := rrAddSync(t, pool3, tx3); err != nil {
		t.Fatalf("age-8191 reference rejected: %v", err)
	}

	// Missing contract code.
	pool4, _, key4 := setupRecentRootPool(t, 100)
	pool4.currentState.SetCode(types.RecentRootAddress, nil, tracing.CodeChangeUnspecified)
	rrStoreRoot(t, pool4.currentState, rrWriter, salt, 100, root)
	tx4 := rrPoolTx(t, key4, rrPoolSender, 0, []types.RecentRootTuple{tuple}, 1e15, 1_000_000_000)
	if err := rrAddSync(t, pool4, tx4); err == nil {
		t.Fatal("missing code admitted")
	}

	// Incorrect contract code.
	pool5, _, key5 := setupRecentRootPool(t, 100)
	pool5.currentState.SetCode(types.RecentRootAddress, []byte{0x00}, tracing.CodeChangeUnspecified)
	rrStoreRoot(t, pool5.currentState, rrWriter, salt, 100, root)
	tx5 := rrPoolTx(t, key5, rrPoolSender, 0, []types.RecentRootTuple{tuple}, 1e15, 1_000_000_000)
	if err := rrAddSync(t, pool5, tx5); err == nil {
		t.Fatal("wrong code admitted")
	}

	// Insufficient execution gas for the frame.
	pool6, _, key6 := setupRecentRootPool(t, 100)
	rrStoreRoot(t, pool6.currentState, rrWriter, salt, 100, root)
	tx6 := rrPoolTx(t, key6, rrPoolSender, 0, []types.RecentRootTuple{tuple}, 1e15, 1_000_000_000)
	tx6.FrameTxData().Frames[0].Limits.Execution = 100 // far below requirement
	// re-sign after mutation
	tx6, _ = resignFrameTx(t, key6, tx6)
	if err := rrAddSync(t, pool6, tx6); err == nil {
		t.Fatal("underfunded frame admitted")
	}
}

func rrTupleOf(t types.RecentRootTuple, root common.Hash) types.RecentRootTuple {
	t.Root = root
	return t
}

func rrTupleSlot(t types.RecentRootTuple, slot uint64) types.RecentRootTuple {
	t.Slot = slot
	return t
}

// resignFrameTx re-signs a mutated frame transaction.
func resignFrameTx(t *testing.T, key *ecdsa.PrivateKey, tx *types.Transaction) (*types.Transaction, error) {
	t.Helper()
	ft := tx.FrameTxData()
	// Rebuild to reset internal caches.
	ntx := types.NewTx(ft)
	signed, err := types.SignFrameTx(key, ntx)
	if err != nil {
		t.Fatalf("resign: %v", err)
	}
	return signed, nil
}

// TestPoolRecentRootMultipleDependencies: 16 tuples incl. duplicates produce
// distinct-key indexing; eviction of one entry invalidates the whole tx.
func TestPoolRecentRootMultipleDependencies(t *testing.T) {
	pool, _, key := setupRecentRootPool(t, 200)
	var tuples []types.RecentRootTuple
	for i := 0; i < 15; i++ {
		salt := common.BigToHash(big.NewInt(int64(i + 1)))
		tuples = append(tuples, rrStoreRoot(t, pool.currentState, rrWriter, salt, 200, common.BigToHash(big.NewInt(int64(i+100)))))
	}
	dup := tuples[0]
	tuples = append(tuples, dup) // duplicate valid tuple (checked independently)

	tx := rrPoolTx(t, key, rrPoolSender, 0, tuples, 1e15, 1_000_000_000)
	// Sixteen distinct cold storage keys need ~56k execution gas (measured:
	// 55,855 incl. the frame-entry cold account access).
	tx.FrameTxData().Frames[0].Limits.Execution = 60_000
	tx, _ = resignFrameTx(t, key, tx)
	if err := rrAddSync(t, pool, tx); err != nil {
		t.Fatalf("16-tuple tx rejected: %v", err)
	}
	dep := pool.rrDeps[tx.Hash()]
	if dep == nil {
		t.Fatal("no dep record")
	}
	if len(dep.keys) != 15 {
		t.Fatalf("distinct keys = %d, want 15 (duplicate must not double-register)", len(dep.keys))
	}
	if len(pool.rrByKey) != 15 {
		t.Fatalf("index keys = %d, want 15", len(pool.rrByKey))
	}
	// Invalidate one entry → sweep must evict.
	pool.currentState.SetState(types.RecentRootAddress, tuples[7].StorageKey(), common.Hash{})
	pool.mu.Lock()
	evicted := pool.revalidateRecentRootFrames(nil, nil)
	pool.mu.Unlock()
	if evicted != 1 {
		t.Fatalf("evicted = %d, want 1", evicted)
	}
	if pool.Has(tx.Hash()) {
		t.Fatal("tx survived entry invalidation")
	}
	if len(pool.rrDeps) != 0 || len(pool.rrByKey) != 0 {
		t.Fatalf("dependency records leaked: deps=%d keys=%d", len(pool.rrDeps), len(pool.rrByKey))
	}
}

// TestPoolRecentRootReplacement: a same-nonce replacement atomically swaps
// the dependency records.
func TestPoolRecentRootReplacement(t *testing.T) {
	pool, _, key := setupRecentRootPool(t, 300)
	salt1 := common.HexToHash("0x01")
	salt2 := common.HexToHash("0x02")
	tuple1 := rrStoreRoot(t, pool.currentState, rrWriter, salt1, 300, common.HexToHash("0x11"))
	tuple2 := rrStoreRoot(t, pool.currentState, rrWriter, salt2, 300, common.HexToHash("0x22"))

	tx1 := rrPoolTx(t, key, rrPoolSender, 0, []types.RecentRootTuple{tuple1}, 1e15, 1_000_000_000)
	if err := rrAddSync(t, pool, tx1); err != nil {
		t.Fatalf("tx1 rejected: %v", err)
	}
	// Promote to pending so the replacement path in add() fires.
	pool.mu.Lock()
	pool.promoteExecutables([]common.Address{rrPoolSender})
	pool.mu.Unlock()

	// Replacement: same nonce, higher fees (PriceBump on derived feeCap and tip).
	tx2 := rrPoolTx(t, key, rrPoolSender, 0, []types.RecentRootTuple{tuple2}, 12e14, 1_200_000_000)
	replaced, err := func() (bool, error) {
		pool.mu.Lock()
		defer pool.mu.Unlock()
		return pool.add(tx2)
	}()
	if err != nil {
		t.Fatalf("tx2 replacement rejected: %v", err)
	}
	if !replaced {
		t.Fatal("tx2 did not replace tx1")
	}
	if pool.Has(tx1.Hash()) {
		t.Fatal("tx1 still in pool")
	}
	if !pool.Has(tx2.Hash()) {
		t.Fatal("tx2 not in pool")
	}
	// Atomic dependency replacement: old records gone, new records present.
	if _, ok := pool.rrDeps[tx1.Hash()]; ok {
		t.Fatal("tx1 dependency record survived replacement")
	}
	if _, ok := pool.rrByKey[tuple1.StorageKey()]; ok {
		t.Fatal("tx1 storage key index survived replacement")
	}
	if dep := pool.rrDeps[tx2.Hash()]; dep == nil || len(dep.keys) != 1 || dep.keys[0] != tuple2.StorageKey() {
		t.Fatalf("tx2 dependency record wrong: %+v", dep)
	}
	if set := pool.rrByKey[tuple2.StorageKey()]; set == nil || len(set) != 1 {
		t.Fatal("tx2 storage key index wrong")
	}
}

// TestPoolRecentRootEvictionCleanup exercises removal paths: direct eviction,
// nonce-consumption demotion (inclusion equivalent), and revalidation
// eviction. Dependency records must be empty afterwards in every case.
func TestPoolRecentRootEvictionCleanup(t *testing.T) {
	setup := func(t *testing.T) (*LegacyPool, *types.Transaction, types.RecentRootTuple) {
		pool, _, key := setupRecentRootPool(t, 400)
		tuple := rrStoreRoot(t, pool.currentState, rrWriter, common.HexToHash("0x01"), 400, common.HexToHash("0x02"))
		tx := rrPoolTx(t, key, rrPoolSender, 0, []types.RecentRootTuple{tuple}, 1e15, 1_000_000_000)
		if err := rrAddSync(t, pool, tx); err != nil {
			t.Fatalf("admission: %v", err)
		}
		if len(pool.rrDeps) != 1 {
			t.Fatal("dep not registered")
		}
		return pool, tx, tuple
	}
	assertClean := func(t *testing.T, pool *LegacyPool, hash common.Hash) {
		t.Helper()
		if pool.Has(hash) {
			t.Fatal("tx still in pool")
		}
		if len(pool.rrDeps) != 0 || len(pool.rrByKey) != 0 {
			t.Fatalf("deps leaked: rrDeps=%d rrByKey=%d", len(pool.rrDeps), len(pool.rrByKey))
		}
	}

	t.Run("removeTx eviction", func(t *testing.T) {
		pool, tx, _ := setup(t)
		pool.mu.Lock()
		pool.removeTx(tx.Hash(), true, true)
		pool.mu.Unlock()
		assertClean(t, pool, tx.Hash())
	})

	t.Run("inclusion via nonce consumption (demoteUnexecutables)", func(t *testing.T) {
		pool, tx, _ := setup(t)
		// Simulate inclusion: the sender's nonce advanced.
		pool.currentState.SetNonce(rrPoolSender, 1, tracing.NonceChangeUnspecified)
		pool.mu.Lock()
		pool.demoteUnexecutables()
		pool.mu.Unlock()
		assertClean(t, pool, tx.Hash())
	})

	t.Run("revalidation eviction", func(t *testing.T) {
		pool, tx, tuple := setup(t)
		pool.currentState.SetState(types.RecentRootAddress, tuple.StorageKey(), common.Hash{})
		pool.mu.Lock()
		pool.revalidateRecentRootFrames(nil, nil)
		pool.mu.Unlock()
		assertClean(t, pool, tx.Hash())
	})

	t.Run("queue truncation", func(t *testing.T) {
		pool, _, key := setupRecentRootPool(t, 400)
		tuple := rrStoreRoot(t, pool.currentState, rrWriter, common.HexToHash("0x01"), 400, common.HexToHash("0x02"))
		// A nonce-1 tx is gapped (sender nonce 0) and stays in the queue.
		tx := rrPoolTx(t, key, rrPoolSender, 1, []types.RecentRootTuple{tuple}, 1e15, 1_000_000_000)
		if err := rrAddSync(t, pool, tx); err != nil {
			t.Fatalf("admission: %v", err)
		}
		if len(pool.rrDeps) != 1 {
			t.Fatal("dep not registered")
		}
		pool.mu.Lock()
		pool.config.GlobalQueue = 0
		pool.queue.config.GlobalQueue = 0 // queue keeps its own config copy
		pool.truncateQueue()
		pool.mu.Unlock()
		assertClean(t, pool, tx.Hash())
	})
}

// TestPoolRecentRootSlotAdvancement: pending transactions are evicted exactly
// when a tuple's age reaches RECENT_ROOT_LENGTH.
func TestPoolRecentRootSlotAdvancement(t *testing.T) {
	pool, chain, key := setupRecentRootPool(t, 500)
	tuple := rrStoreRoot(t, pool.currentState, rrWriter, common.HexToHash("0x01"), 500, common.HexToHash("0x02"))
	tx := rrPoolTx(t, key, rrPoolSender, 0, []types.RecentRootTuple{tuple}, 1e15, 1_000_000_000)
	if err := rrAddSync(t, pool, tx); err != nil {
		t.Fatalf("admission: %v", err)
	}

	sweep := func(slot uint64) int {
		head := rrHead(slot)
		pool.mu.Lock()
		defer pool.mu.Unlock()
		old := pool.currentHead.Load()
		chain.head.Store(head)
		pool.currentHead.Store(head)
		return pool.revalidateRecentRootFrames(old, head)
	}

	// Slot 500+8190 (current 500+8191): still valid.
	if evicted := sweep(500 + 8190); evicted != 0 {
		t.Fatalf("evicted at age 8191: %d", evicted)
	}
	if !pool.Has(tx.Hash()) {
		t.Fatal("tx evicted too early")
	}
	// Slot 500+8191 (current 500+8192): age 8192 → expired.
	if evicted := sweep(500 + 8191); evicted != 1 {
		t.Fatalf("not evicted at age 8192: %d", evicted)
	}
	if pool.Has(tx.Hash()) {
		t.Fatal("expired tx still in pool")
	}
	if len(pool.rrDeps) != 0 {
		t.Fatal("dep record leaked after expiry eviction")
	}
}

// TestPoolRecentRootReorganization covers reorg handling on both branches:
// a root removed by rollback (with no replacement write) evicts; a root that
// also exists on the new branch keeps the transaction.
func TestPoolRecentRootReorganization(t *testing.T) {
	pool, chain, key := setupRecentRootPool(t, 600)
	salt := common.HexToHash("0x01")
	root := common.HexToHash("0x02")
	tuple := rrStoreRoot(t, pool.currentState, rrWriter, salt, 600, root)
	tx := rrPoolTx(t, key, rrPoolSender, 0, []types.RecentRootTuple{tuple}, 1e15, 1_000_000_000)
	if err := rrAddSync(t, pool, tx); err != nil {
		t.Fatalf("admission: %v", err)
	}

	// Branch A → branch B: the write never happened on B (entry rolled back,
	// no replacement write on the added branch).
	pool.mu.Lock()
	oldHead := pool.currentHead.Load()
	newHead := rrHead(601)
	chain.head.Store(newHead)
	pool.currentHead.Store(newHead)
	pool.currentState.SetState(types.RecentRootAddress, tuple.StorageKey(), common.Hash{})
	evicted := pool.revalidateRecentRootFrames(oldHead, newHead)
	pool.mu.Unlock()
	if evicted != 1 {
		t.Fatalf("removed-branch root: evicted=%d, want 1", evicted)
	}

	// Re-admission works after re-receiving the transaction (spec: evicted
	// transactions are not resurrected automatically).
	rrStoreRoot(t, pool.currentState, rrWriter, salt, 601, root)
	tuple2 := rrTupleSlot(tuple, 601)
	tx2 := rrPoolTx(t, key, rrPoolSender, 1, []types.RecentRootTuple{tuple2}, 1e15, 1_000_000_000)
	pool.currentState.SetNonce(rrPoolSender, 1, tracing.NonceChangeUnspecified)
	if err := rrAddSync(t, pool, tx2); err != nil {
		t.Fatalf("re-admission after reorg failed: %v", err)
	}

	// Branch B → branch C where the same entry exists: no eviction.
	pool.mu.Lock()
	oldHead = pool.currentHead.Load()
	newHead = rrHead(602)
	chain.head.Store(newHead)
	pool.currentHead.Store(newHead)
	// entry for slot 601 still present (state unchanged)
	evicted = pool.revalidateRecentRootFrames(oldHead, newHead)
	pool.mu.Unlock()
	if evicted != 0 {
		t.Fatalf("restored/preserved root evicted tx: %d", evicted)
	}
	if !pool.Has(tx2.Hash()) {
		t.Fatal("tx evicted despite intact root")
	}
}

// TestPoolRecentRootCodeDependency: a change to the code (or its removal) at
// RECENT_ROOT_ADDRESS revalidates — and evicts — every pending recent-root
// transaction.
func TestPoolRecentRootCodeDependency(t *testing.T) {
	pool, _, key := setupRecentRootPool(t, 700)
	tuple := rrStoreRoot(t, pool.currentState, rrWriter, common.HexToHash("0x01"), 700, common.HexToHash("0x02"))
	tx := rrPoolTx(t, key, rrPoolSender, 0, []types.RecentRootTuple{tuple}, 1e15, 1_000_000_000)
	if err := rrAddSync(t, pool, tx); err != nil {
		t.Fatalf("admission: %v", err)
	}
	// Code change (e.g. reorg removed the deployment transaction).
	pool.currentState.SetCode(types.RecentRootAddress, nil, tracing.CodeChangeUnspecified)
	pool.mu.Lock()
	evicted := pool.revalidateRecentRootFrames(nil, nil)
	pool.mu.Unlock()
	if evicted != 1 || pool.Has(tx.Hash()) {
		t.Fatalf("code removal: evicted=%d has=%v", evicted, pool.Has(tx.Hash()))
	}
}

// TestPoolRecentRootActivationFlip: reorganization crossing back before
// activation evicts all recent-root transactions.
func TestPoolRecentRootActivationFlip(t *testing.T) {
	// Custom config: Amsterdam/Bogota at time 100.
	cfg := *params.MergedTestChainConfig
	cfg.AmsterdamTime = new(uint64)
	*cfg.AmsterdamTime = 100
	cfg.BogotaTime = new(uint64)
	*cfg.BogotaTime = 100

	statedb, _ := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	statedb.CreateAccount(rrPoolSender)
	statedb.AddBalance(rrPoolSender, uint256.NewInt(1e18), tracing.BalanceChangeUnspecified)
	statedb.CreateAccount(types.RecentRootAddress)
	statedb.SetCode(types.RecentRootAddress, types.RecentRootCode, tracing.CodeChangeUnspecified)
	statedb.Finalise(true)

	chain := &rrChain{config: &cfg, statedb: statedb, feed: new(event.Feed)}
	head := rrHead(800)
	head.Time = 100 // active
	chain.head.Store(head)
	pool := New(testTxPoolConfig, chain)
	if err := pool.Init(testTxPoolConfig.PriceLimit, chain.CurrentBlock(), newReserver()); err != nil {
		t.Fatalf("init: %v", err)
	}
	<-pool.initDoneCh

	tuple := rrStoreRoot(t, pool.currentState, rrWriter, common.HexToHash("0x01"), 800, common.HexToHash("0x02"))
	tx := rrPoolTx(t, rrPoolKey, rrPoolSender, 0, []types.RecentRootTuple{tuple}, 1e15, 1_000_000_000)
	if err := rrAddSync(t, pool, tx); err != nil {
		t.Fatalf("admission: %v", err)
	}

	// Reorg crosses back before activation: new head at time 99.
	newHead := rrHead(801)
	newHead.Time = 99
	pool.mu.Lock()
	old := pool.currentHead.Load()
	chain.head.Store(newHead)
	pool.currentHead.Store(newHead)
	evicted := pool.revalidateRecentRootFrames(old, newHead)
	pool.mu.Unlock()
	if evicted != 1 || pool.Has(tx.Hash()) {
		t.Fatalf("activation flip: evicted=%d has=%v", evicted, pool.Has(tx.Hash()))
	}
}

// TestPoolRecentRootAdmissionBeforeDeployment: a recent root verifier frame
// before the deployment transaction is included is rejected (default code
// would revert).
func TestPoolRecentRootAdmissionBeforeDeployment(t *testing.T) {
	pool, _, key := setupRecentRootPool(t, 900)
	pool.currentState.SetCode(types.RecentRootAddress, nil, tracing.CodeChangeUnspecified) // not deployed
	tuple := types.RecentRootTuple{SourceID: common.HexToHash("0x01"), Slot: 899, Root: common.HexToHash("0x02")}
	tx := rrPoolTx(t, key, rrPoolSender, 0, []types.RecentRootTuple{tuple}, 1e15, 1_000_000_000)
	if err := rrAddSync(t, pool, tx); err == nil {
		t.Fatal("pre-deployment tx admitted")
	}
}

// TestPoolRecentRootIndexOnlyTouchesRelatedTxs: the revalidation sweep must
// not evict unrelated transactions (no full-pool scan semantics).
func TestPoolRecentRootIndexOnlyTouchesRelatedTxs(t *testing.T) {
	pool, _, key := setupRecentRootPool(t, 1000)
	tuple := rrStoreRoot(t, pool.currentState, rrWriter, common.HexToHash("0x01"), 1000, common.HexToHash("0x02"))
	tx := rrPoolTx(t, key, rrPoolSender, 0, []types.RecentRootTuple{tuple}, 1e15, 1_000_000_000)
	if err := rrAddSync(t, pool, tx); err != nil {
		t.Fatalf("admission: %v", err)
	}
	// Unrelated legacy tx from another funded account.
	otherKey, _ := crypto.GenerateKey()
	other := crypto.PubkeyToAddress(otherKey.PublicKey)
	pool.currentState.CreateAccount(other)
	pool.currentState.AddBalance(other, uint256.NewInt(1e18), tracing.BalanceChangeUnspecified)
	legacy := pricedTransaction(0, 100000, big.NewInt(1_000_000_000), otherKey)
	if err := rrAddSync(t, pool, legacy); err != nil {
		t.Fatalf("legacy admission: %v", err)
	}
	// Sweep with no changes: nothing evicted.
	pool.mu.Lock()
	evicted := pool.revalidateRecentRootFrames(nil, nil)
	pool.mu.Unlock()
	if evicted != 0 {
		t.Fatalf("clean sweep evicted %d", evicted)
	}
	if !pool.Has(tx.Hash()) || !pool.Has(legacy.Hash()) {
		t.Fatal("clean sweep removed transactions")
	}
	// Invalidate the root: only the recent-root tx is evicted.
	pool.currentState.SetState(types.RecentRootAddress, tuple.StorageKey(), common.Hash{})
	pool.mu.Lock()
	evicted = pool.revalidateRecentRootFrames(nil, nil)
	pool.mu.Unlock()
	if evicted != 1 {
		t.Fatalf("sweep evicted %d, want 1", evicted)
	}
	if !pool.Has(legacy.Hash()) {
		t.Fatal("unrelated legacy tx evicted by recent-root sweep")
	}
}
