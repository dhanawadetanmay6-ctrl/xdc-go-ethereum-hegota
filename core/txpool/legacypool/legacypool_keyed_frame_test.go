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
	"crypto/ecdsa"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/holiman/uint256"
)

// keyedTestChainConfig activates Amsterdam + Bogota + the EIP-8250 keyed
// nonce fork at genesis time.
func keyedTestChainConfig(withKeyedFork bool) *params.ChainConfig {
	cpy := *params.TestChainConfig
	cpy.AmsterdamTime = new(uint64)
	cpy.BogotaTime = new(uint64)
	if withKeyedFork {
		cpy.KeyedNonceTime = new(uint64)
	}
	return &cpy
}

// setupKeyedPool creates a pool with an empty state and the given config.
func setupKeyedPool(t *testing.T, cfg *params.ChainConfig) *LegacyPool {
	t.Helper()
	statedb, _ := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	blockchain := newTestBlockChain(cfg, 10000000, statedb, new(event.Feed))
	pool := New(testTxPoolConfig, blockchain)
	if err := pool.Init(testTxPoolConfig.PriceLimit, blockchain.CurrentBlock(), newReserver()); err != nil {
		t.Fatalf("pool init: %v", err)
	}
	<-pool.initDoneCh
	t.Cleanup(func() { pool.Close() })
	return pool
}

// keyedFrameTxExec is keyedFrameTx with an explicit verify-frame execution
// limit (lets tests build same-identity transactions with distinct hashes).
func keyedFrameTxExec(t *testing.T, key *ecdsa.PrivateKey, seq uint64, tip uint64, maxFee uint64, exec uint64, keys ...uint64) *types.Transaction {
	t.Helper()
	sender := crypto.PubkeyToAddress(key.PublicKey)
	nk := make([]uint256.Int, len(keys))
	for i, k := range keys {
		nk[i].SetUint64(k)
	}
	tx := types.NewTx(&types.FrameTx{
		ChainID:   uint256.MustFromBig(params.TestChainConfig.ChainID),
		NonceKeys: nk,
		NonceSeq:  seq,
		Sender:    sender,
		Frames: []types.Frame{{
			Mode:   types.FrameModeVerify,
			Flags:  types.FrameFlagApproveScopeMask,
			Target: nil, // self verify, resolves to sender
			Limits: types.FrameGasLimits{Execution: exec, State: 100_000},
			Value:  new(uint256.Int),
		}},
		Signatures: []types.FrameSignature{{
			Scheme:    types.FrameSigSchemeSecp256k1,
			Signature: make([]byte, 65), // length placeholder, replaced by SignFrameTx
		}},
		Fees: types.FrameFees{
			MaxFee:               uint256.NewInt(maxFee),
			MaxPriorityFeePerGas: uint256.NewInt(tip),
		},
	})
	signed, err := types.SignFrameTx(key, tx)
	if err != nil {
		t.Fatalf("SignFrameTx: %v", err)
	}
	return signed
}

// keyedFrameTx builds a minimal signed keyed-form frame transaction: a single
// self-verify frame approving both scopes, the given nonce keys/sequence, and
// the given priority fee tip.
func keyedFrameTx(t *testing.T, key *ecdsa.PrivateKey, seq uint64, tip uint64, keys ...uint64) *types.Transaction {
	t.Helper()
	return keyedFrameTxExec(t, key, seq, tip, 1e15, 50_000, keys...)
}

// legacyFormFrameTx builds a signed pre-EIP-8250 (legacy payload form) frame
// transaction by hand-assembling the 7-element RLP payload.
func legacyFormFrameTx(t *testing.T, key *ecdsa.PrivateKey, nonce uint64) *types.Transaction {
	t.Helper()
	sender := crypto.PubkeyToAddress(key.PublicKey)
	frames := []types.Frame{{
		Mode:   types.FrameModeVerify,
		Flags:  types.FrameFlagApproveScopeMask,
		Target: nil,
		Limits: types.FrameGasLimits{Execution: 50_000, State: 100_000},
		Value:  new(uint256.Int),
	}}
	sigs := []types.FrameSignature{{
		Scheme:    types.FrameSigSchemeSecp256k1,
		Signature: make([]byte, 65),
	}}
	fees := types.FrameFees{
		MaxFee:               uint256.NewInt(1e15),
		MaxPriorityFeePerGas: uint256.NewInt(2),
	}
	enc := func(v any) []byte {
		b, err := rlp.EncodeToBytes(v)
		if err != nil {
			t.Fatalf("rlp encode: %v", err)
		}
		return b
	}
	body := append([]byte{}, enc(uint256.MustFromBig(params.TestChainConfig.ChainID))...)
	body = append(body, enc(nonce)...)
	body = append(body, enc(sender)...)
	body = append(body, enc(frames)...)
	body = append(body, enc(sigs)...)
	body = append(body, enc(fees)...)
	body = append(body, enc([]common.Hash{})...)
	if len(body) >= 256 {
		t.Fatal("fixture payload too large for the assumed header form")
	}
	payload := append([]byte{0xf8, byte(len(body))}, body...)
	raw := append([]byte{types.FrameTxType}, payload...)
	tx := new(types.Transaction)
	if err := tx.UnmarshalBinary(raw); err != nil {
		t.Fatalf("legacy form decode: %v", err)
	}
	signed, err := types.SignFrameTx(key, tx)
	if err != nil {
		t.Fatalf("SignFrameTx (legacy form): %v", err)
	}
	return signed
}

// setKeyedSlot writes a NONCE_MANAGER nonce slot directly into the pool state.
func setKeyedSlot(pool *LegacyPool, sender common.Address, key, value uint64) {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	pool.currentState.SetState(types.NonceManagerAddress,
		types.NonceSlot(sender, uint256.NewInt(key)),
		common.Hash(uint256.NewInt(value).Bytes32()))
}

func keyedCount(pool *LegacyPool) int {
	pool.mu.RLock()
	defer pool.mu.RUnlock()
	return len(pool.keyedFrame)
}

// TestKeyedFramePoolAdmission verifies a valid keyed frame transaction is
// admitted into the side pool and surfaced to the miner via Pending.
func TestKeyedFramePoolAdmission(t *testing.T) {
	pool := setupKeyedPool(t, keyedTestChainConfig(true))
	key, _ := crypto.GenerateKey()
	sender := crypto.PubkeyToAddress(key.PublicKey)
	testAddBalance(pool, sender, big.NewInt(1e18))

	tx := keyedFrameTx(t, key, 0, 2, 7)
	if errs := pool.Add([]*types.Transaction{tx}, true); errs[0] != nil {
		t.Fatalf("keyed frame tx rejected: %v", errs[0])
	}
	if got := pool.Get(tx.Hash()); got == nil {
		t.Fatal("keyed tx missing from lookup")
	}
	if n := keyedCount(pool); n != 1 {
		t.Fatalf("keyed side pool size %d, want 1", n)
	}
	// The keyed transaction must be miner-visible via Pending, appended to
	// the sender's list.
	pending, count := pool.Pending(txpool.PendingFilter{})
	if count != 1 || len(pending[sender]) != 1 || pending[sender][0].Hash != tx.Hash() {
		t.Fatalf("pending = %v (count %d), want the keyed tx for %v", pending, count, sender)
	}
	// Tip filtering applies to keyed transactions too.
	filtered, count := pool.Pending(txpool.PendingFilter{MinTip: uint256.NewInt(100)})
	if count != 0 || len(filtered) != 0 {
		t.Fatalf("tip-filtered pending = %v (count %d), want empty", filtered, count)
	}
	// Account-nonce accounting must be untouched by the keyed transaction.
	if n := pool.Nonce(sender); n != 0 {
		t.Fatalf("pending nonce %d, want 0 (keyed txs do not advance account nonce)", n)
	}
}

// TestKeyedFramePoolReplacement verifies the EIP-8250 mempool identity rule:
// same (sender, nonce_keys, nonce_seq) replaces with a fee bump; a different
// identity is rejected (one pending keyed frame tx per sender).
func TestKeyedFramePoolReplacement(t *testing.T) {
	pool := setupKeyedPool(t, keyedTestChainConfig(true))
	key, _ := crypto.GenerateKey()
	sender := crypto.PubkeyToAddress(key.PublicKey)
	testAddBalance(pool, sender, big.NewInt(1e18))

	tx1 := keyedFrameTx(t, key, 0, 2, 7)
	if errs := pool.Add([]*types.Transaction{tx1}, true); errs[0] != nil {
		t.Fatalf("initial add: %v", errs[0])
	}
	// Different identity (different key) must not displace the pending one.
	tx2 := keyedFrameTx(t, key, 0, 100, 8)
	if errs := pool.Add([]*types.Transaction{tx2}, true); !errors.Is(errs[0], ErrKeyedFramePending) {
		t.Fatalf("different identity: got %v, want ErrKeyedFramePending", errs[0])
	}
	// Same identity without a fee bump must not replace (distinct hash via a
	// different execution limit, same fees and identity).
	tx3 := keyedFrameTxExec(t, key, 0, 2, 1e15, 50_001, 7)
	if errs := pool.Add([]*types.Transaction{tx3}, true); !errors.Is(errs[0], txpool.ErrReplaceUnderpriced) {
		t.Fatalf("same identity, no bump: got %v, want ErrReplaceUnderpriced", errs[0])
	}
	// Same identity with a sufficient bump replaces (testTxPoolConfig
	// PriceBump = 10; tip 2 -> 3 and budget 1e15 -> 2e15 clear it on both
	// the tip and the derived fee cap).
	tx4 := keyedFrameTxExec(t, key, 0, 3, 2e15, 50_000, 7)
	if errs := pool.Add([]*types.Transaction{tx4}, true); errs[0] != nil {
		t.Fatalf("bumped replacement: %v", errs[0])
	}
	if pool.Get(tx1.Hash()) != nil {
		t.Fatal("old keyed tx still in lookup after replacement")
	}
	if pool.Get(tx4.Hash()) == nil {
		t.Fatal("replacement keyed tx missing from lookup")
	}
	if n := keyedCount(pool); n != 1 {
		t.Fatalf("keyed side pool size %d, want 1 after replacement", n)
	}
}

// TestKeyedFramePoolRevalidation verifies that a pending keyed frame
// transaction is evicted when any of its selected nonce sequences moves
// (EIP-8250: nodes revalidate when selected nonce sequences change).
func TestKeyedFramePoolRevalidation(t *testing.T) {
	pool := setupKeyedPool(t, keyedTestChainConfig(true))
	key, _ := crypto.GenerateKey()
	sender := crypto.PubkeyToAddress(key.PublicKey)
	testAddBalance(pool, sender, big.NewInt(1e18))

	tx := keyedFrameTx(t, key, 0, 2, 7, 9)
	if errs := pool.Add([]*types.Transaction{tx}, true); errs[0] != nil {
		t.Fatalf("add: %v", errs[0])
	}
	// A state change on an UNRELATED key keeps the transaction valid.
	setKeyedSlot(pool, sender, 5, 1)
	pool.mu.Lock()
	pool.demoteUnexecutables()
	pool.mu.Unlock()
	if pool.Get(tx.Hash()) == nil {
		t.Fatal("tx evicted although its selected sequences did not change")
	}
	// Moving a SELECTED key (as inclusion or another keyed tx would) evicts.
	setKeyedSlot(pool, sender, 9, 1)
	pool.mu.Lock()
	pool.demoteUnexecutables()
	pool.mu.Unlock()
	if pool.Get(tx.Hash()) != nil {
		t.Fatal("tx not evicted after a selected nonce sequence changed")
	}
	if n := keyedCount(pool); n != 0 {
		t.Fatalf("keyed side pool size %d, want 0", n)
	}
	// A single-use keyed tx is evicted once the key is used.
	txSU := keyedFrameTx(t, key, ^uint64(0), 2, 11)
	if errs := pool.Add([]*types.Transaction{txSU}, true); errs[0] != nil {
		t.Fatalf("single-use add: %v", errs[0])
	}
	setKeyedSlot(pool, sender, 11, 1)
	pool.mu.Lock()
	pool.demoteUnexecutables()
	pool.mu.Unlock()
	if pool.Get(txSU.Hash()) != nil {
		t.Fatal("single-use keyed tx not evicted after key consumption")
	}
}

// TestKeyedFramePoolStatefulAdmission verifies the per-key sequence check at
// admission: a transaction whose selected key does not sit at nonce_seq is
// rejected (replay and skip-ahead are both invalid).
func TestKeyedFramePoolStatefulAdmission(t *testing.T) {
	pool := setupKeyedPool(t, keyedTestChainConfig(true))
	key, _ := crypto.GenerateKey()
	sender := crypto.PubkeyToAddress(key.PublicKey)
	testAddBalance(pool, sender, big.NewInt(1e18))

	setKeyedSlot(pool, sender, 7, 3)
	// Replay: seq below the current value.
	if errs := pool.Add([]*types.Transaction{keyedFrameTx(t, key, 2, 2, 7)}, true); !errors.Is(errs[0], txpool.ErrKeyedFrameNonce) {
		t.Fatalf("replay: got %v, want ErrKeyedFrameNonce", errs[0])
	}
	// Skip-ahead: seq above the current value.
	if errs := pool.Add([]*types.Transaction{keyedFrameTx(t, key, 4, 2, 7)}, true); !errors.Is(errs[0], txpool.ErrKeyedFrameNonce) {
		t.Fatalf("skip-ahead: got %v, want ErrKeyedFrameNonce", errs[0])
	}
	// Exact match admitted.
	if errs := pool.Add([]*types.Transaction{keyedFrameTx(t, key, 3, 2, 7)}, true); errs[0] != nil {
		t.Fatalf("matching seq rejected: %v", errs[0])
	}
}

// TestKeyedFramePoolForkGate verifies the payload-form gate at admission:
// before the keyed-nonce fork, the keyed payload form is rejected; the legacy
// payload form is valid.
func TestKeyedFramePoolForkGate(t *testing.T) {
	pool := setupKeyedPool(t, keyedTestChainConfig(false)) // Bogota only, no keyed fork
	key, _ := crypto.GenerateKey()
	sender := crypto.PubkeyToAddress(key.PublicKey)
	testAddBalance(pool, sender, big.NewInt(1e18))

	if errs := pool.Add([]*types.Transaction{keyedFrameTx(t, key, 0, 2, 7)}, true); !errors.Is(errs[0], types.ErrFrameTxKeyedBeforeFork) {
		t.Fatalf("keyed form pre-fork: got %v, want ErrFrameTxKeyedBeforeFork", errs[0])
	}
	// The keyed payload form with empty keys is also rejected pre-fork.
	if errs := pool.Add([]*types.Transaction{keyedFrameTx(t, key, 0, 2)}, true); !errors.Is(errs[0], types.ErrFrameTxKeyedBeforeFork) {
		t.Fatalf("keyed form (empty keys) pre-fork: got %v, want ErrFrameTxKeyedBeforeFork", errs[0])
	}
	// The legacy payload form is admitted pre-fork.
	legacy := legacyFormFrameTx(t, key, 0)
	if errs := pool.Add([]*types.Transaction{legacy}, true); errs[0] != nil {
		t.Fatalf("legacy form pre-fork rejected: %v", errs[0])
	}
	if pool.Get(legacy.Hash()) == nil {
		t.Fatal("legacy form tx missing from pool")
	}
}

// TestKeyedFramePoolForkBoundaryEviction verifies EIP-8250 activation
// semantics: pre-fork frame transactions do not survive the fork boundary and
// are evicted from the mempool on the first head past it.
func TestKeyedFramePoolForkBoundaryEviction(t *testing.T) {
	forkTime := uint64(1000)
	cpy := *params.TestChainConfig
	cpy.AmsterdamTime = new(uint64)
	cpy.BogotaTime = new(uint64)
	cpy.KeyedNonceTime = &forkTime
	pool := setupKeyedPool(t, &cpy)
	key, _ := crypto.GenerateKey()
	sender := crypto.PubkeyToAddress(key.PublicKey)
	testAddBalance(pool, sender, big.NewInt(1e18))

	// Pre-fork: the legacy payload form is admitted.
	legacy := legacyFormFrameTx(t, key, 0)
	if errs := pool.Add([]*types.Transaction{legacy}, true); errs[0] != nil {
		t.Fatalf("legacy form pre-fork rejected: %v", errs[0])
	}

	// Cross the boundary: reset to a head at the keyed-nonce fork time.
	// (Call reset directly: runReorg's basefee recomputation needs a full
	// header chain the test blockchain does not provide.)
	oldHead := pool.currentHead.Load()
	newHead := *oldHead
	newHead.Number = new(big.Int).Add(oldHead.Number, common.Big1)
	newHead.ParentHash = oldHead.Hash()
	newHead.Time = forkTime
	pool.mu.Lock()
	pool.reset(oldHead, &newHead)
	pool.mu.Unlock()

	if pool.Get(legacy.Hash()) != nil {
		t.Fatal("legacy payload form survived the keyed-nonce fork boundary")
	}
}

// TestKeyedFramePoolInsufficientFunds verifies the worst-case funds check
// applies to keyed frame transactions.
func TestKeyedFramePoolInsufficientFunds(t *testing.T) {
	pool := setupKeyedPool(t, keyedTestChainConfig(true))
	key, _ := crypto.GenerateKey()
	sender := crypto.PubkeyToAddress(key.PublicKey)
	testAddBalance(pool, sender, big.NewInt(10)) // way below MaxFee

	if errs := pool.Add([]*types.Transaction{keyedFrameTx(t, key, 0, 2, 7)}, true); !errors.Is(errs[0], core.ErrInsufficientFunds) {
		t.Fatalf("got %v, want ErrInsufficientFunds", errs[0])
	}
}
