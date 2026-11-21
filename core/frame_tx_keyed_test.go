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
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU Lesser
// General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

// EIP-8250 keyed nonce execution tests.

package core

import (
	"bytes"
	"errors"
	"math"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/beacon"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/holiman/uint256"
)

// nonceManagerAlloc deploys the NONCE_MANAGER system contract per the
// EIP-8250 activation precondition (nonce 1, NONCE_MANAGER_CODE).
var nonceManagerAlloc = types.GenesisAlloc{
	types.NonceManagerAddress: {Nonce: 1, Code: types.NonceManagerCode},
}

// mkKeyedState builds the standard funded-sender test state with the
// NONCE_MANAGER contract deployed.
func mkKeyedState(t *testing.T, extra types.GenesisAlloc) *state.StateDB {
	t.Helper()
	alloc := types.GenesisAlloc{}
	for a, acc := range nonceManagerAlloc {
		alloc[a] = acc
	}
	for a, acc := range extra {
		alloc[a] = acc
	}
	return mkFrameState(t, alloc)
}

// nonceSlotValue reads the keyed nonce sequence stored for (sender, key).
func nonceSlotValue(sdb *state.StateDB, sender common.Address, key uint64) *big.Int {
	return sdb.GetState(types.NonceManagerAddress, types.NonceSlot(sender, uint256.NewInt(key))).Big()
}

// bogotaOnlyCfg activates Amsterdam+Bogota but not the keyed nonce fork.
var bogotaOnlyCfg = func() *params.ChainConfig {
	cfg := *params.MergedTestChainConfig
	cfg.AmsterdamTime = new(uint64)
	cfg.BogotaTime = new(uint64)
	return &cfg
}()

// bogotaOnlyEVM mirrors frameEVM with the keyed nonce fork disabled.
func bogotaOnlyEVM(sdb *state.StateDB) *vm.EVM {
	ctx := vm.BlockContext{
		CanTransfer:      CanTransfer,
		Transfer:         Transfer,
		GetHash:          func(uint64) common.Hash { return common.Hash{} },
		BlockNumber:      big.NewInt(0),
		Random:           &common.Hash{},
		Difficulty:       big.NewInt(0),
		BlobBaseFee:      big.NewInt(0),
		GasLimit:         60_000_000,
		CostPerStateByte: params.CostPerStateByte,
		BaseFee:          big.NewInt(0),
	}
	return vm.NewEVM(ctx, sdb, bogotaOnlyCfg, vm.Config{NoBaseFee: true})
}

// rlpListWrap concatenates pre-encoded RLP elements into an RLP list.
func rlpListWrap(t *testing.T, elems ...[]byte) []byte {
	t.Helper()
	var body []byte
	for _, e := range elems {
		body = append(body, e...)
	}
	if len(body) < 56 {
		return append([]byte{0xc0 + byte(len(body))}, body...)
	}
	var lb []byte
	for n := len(body); n > 0; n >>= 8 {
		lb = append([]byte{byte(n)}, lb...)
	}
	out := append([]byte{0xf7 + byte(len(lb))}, lb...)
	return append(out, body...)
}

// buildLegacyFrameTx assembles a pre-EIP-8250 (scalar nonce) wire-form frame
// transaction and signs it. Legacy transactions only exist on the wire; this
// helper crafts the payload manually.
func buildLegacyFrameTx(t *testing.T, nonce uint64, frames []types.Frame) *types.Transaction {
	t.Helper()
	chainID := uint256.MustFromBig(frameCfg.ChainID)
	sigs := []types.FrameSignature{{Scheme: types.FrameSigSchemeSecp256k1, Signature: make([]byte, 65)}}
	fees := types.FrameFees{MaxFee: new(uint256.Int), MaxPriorityFeePerGas: new(uint256.Int)}
	enc := func(v any) []byte {
		b, err := rlp.EncodeToBytes(v)
		if err != nil {
			t.Fatalf("rlp encode: %v", err)
		}
		return b
	}
	payload := rlpListWrap(t,
		enc(chainID), enc(nonce), enc(ftSender), enc(frames), enc(sigs), enc(&fees), enc([]common.Hash{}),
	)
	tx := new(types.Transaction)
	if err := tx.UnmarshalBinary(append([]byte{types.FrameTxType}, payload...)); err != nil {
		t.Fatalf("legacy payload decode: %v", err)
	}
	if !tx.FrameTxData().IsLegacyPayload() {
		t.Fatal("decoded transaction is not marked legacy payload")
	}
	signed, err := types.SignFrameTx(ftKey, tx)
	if err != nil {
		t.Fatalf("SignFrameTx: %v", err)
	}
	return signed
}

// TestFrameTxKeyedNonceBasic: a first-use keyed transaction consumes its key
// (slot 0 -> 1), leaves the legacy account nonce untouched, and pays fees.
func TestFrameTxKeyedNonceBasic(t *testing.T) {
	receiver := common.HexToAddress("0xbeef")
	sdb := mkKeyedState(t, nil)
	tx := newFrameTx(0, ftSender).keyed(0, 7).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 100_000).
		addFrame(types.FrameModeSender, 0, &receiver, 100_000, 200_000, uint256.NewInt(1e15), nil).
		build(t)
	res, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx)
	if err != nil {
		t.Fatalf("keyed frame tx must be valid: %v", err)
	}
	if got := nonceSlotValue(sdb, ftSender, 7); got.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("key 7 slot: got %v, want 1", got)
	}
	if nonce := sdb.GetNonce(ftSender); nonce != 0 {
		t.Fatalf("legacy nonce changed: got %d, want 0", nonce)
	}
	if res.Payer != ftSender {
		t.Fatalf("payer %v, want sender", res.Payer)
	}
	if res.FrameReceipts[0].Status != vm.FrameStatusSucceeded ||
		res.FrameReceipts[1].Status != vm.FrameStatusSucceeded {
		t.Fatalf("frame statuses %d/%d, want success/success",
			res.FrameReceipts[0].Status, res.FrameReceipts[1].Status)
	}
	// First use of one key: 64 * 1530 = 97920 state gas on the approving frame.
	if got := res.FrameReceipts[0].GasUsedState; got != 97_920 {
		t.Fatalf("frame0 state gas %d, want 97920 (first use)", got)
	}
}

// TestFrameTxKeyedNonceSequentialAndReplay: sequential consumption advances
// the slot; replays and skipped sequences are rejected at stateful validity.
func TestFrameTxKeyedNonceSequentialAndReplay(t *testing.T) {
	sdb := mkKeyedState(t, nil)

	apply := func(seq uint64) error {
		tx := newFrameTx(0, ftSender).keyed(seq, 42).
			selfVerify(types.FrameFlagApproveScopeMask, 50_000, 100_000).
			build(t)
		_, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx)
		return err
	}
	if err := apply(0); err != nil {
		t.Fatalf("seq 0: %v", err)
	}
	if got := nonceSlotValue(sdb, ftSender, 42); got.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("after seq 0: slot %v, want 1", got)
	}
	// Reuse of an already-consumed key is a replay.
	if err := apply(0); !errors.Is(err, ErrFrameTxNonceSeq) {
		t.Fatalf("replay seq 0: got %v, want ErrFrameTxNonceSeq", err)
	}
	// Skipping a sequence is rejected: seq must equal the stored value.
	if err := apply(2); !errors.Is(err, ErrFrameTxNonceSeq) {
		t.Fatalf("skip seq 2: got %v, want ErrFrameTxNonceSeq", err)
	}
	if err := apply(1); err != nil {
		t.Fatalf("seq 1: %v", err)
	}
	if got := nonceSlotValue(sdb, ftSender, 42); got.Cmp(big.NewInt(2)) != 0 {
		t.Fatalf("after seq 1: slot %v, want 2", got)
	}
	// The legacy account nonce never moved.
	if nonce := sdb.GetNonce(ftSender); nonce != 0 {
		t.Fatalf("legacy nonce changed: got %d, want 0", nonce)
	}
}

// TestFrameTxKeyedNonceMultiKeyAtomic: one transaction consumes several keys
// atomically; a second transaction overlapping any consumed key is invalid.
func TestFrameTxKeyedNonceMultiKeyAtomic(t *testing.T) {
	sdb := mkKeyedState(t, nil)
	tx := newFrameTx(0, ftSender).keyed(0, 3, 9).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 250_000).
		build(t)
	if _, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx); err != nil {
		t.Fatalf("multi-key tx: %v", err)
	}
	for _, k := range []uint64{3, 9} {
		if got := nonceSlotValue(sdb, ftSender, k); got.Cmp(big.NewInt(1)) != 0 {
			t.Fatalf("key %d slot: got %v, want 1", k, got)
		}
	}
	// Overlap on key 9 alone invalidates the second transaction.
	tx2 := newFrameTx(0, ftSender).keyed(0, 9).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 100_000).
		build(t)
	if _, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx2); !errors.Is(err, ErrFrameTxNonceSeq) {
		t.Fatalf("overlapping tx: got %v, want ErrFrameTxNonceSeq", err)
	}
	// Key 3 continues independently.
	tx3 := newFrameTx(0, ftSender).keyed(1, 3).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 100_000).
		build(t)
	if _, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx3); err != nil {
		t.Fatalf("key 3 seq 1: %v", err)
	}
}

// TestFrameTxKeyedNonceSingleUse: nonce_seq == MAX_NONCE_SEQ requires unused
// keys and exhausts them permanently.
func TestFrameTxKeyedNonceSingleUse(t *testing.T) {
	sdb := mkKeyedState(t, nil)
	tx := newFrameTx(0, ftSender).keyed(types.MaxNonceSeq, 5).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 100_000).
		build(t)
	if _, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx); err != nil {
		t.Fatalf("single-use tx: %v", err)
	}
	if got := nonceSlotValue(sdb, ftSender, 5); got.Cmp(new(big.Int).SetUint64(types.MaxNonceSeq)) != 0 {
		t.Fatalf("key 5 slot: got %v, want MAX_NONCE_SEQ", got)
	}
	// Single-use replay fails: the key is no longer unused.
	txReplay := newFrameTx(0, ftSender).keyed(types.MaxNonceSeq, 5).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 100_000).
		build(t)
	if _, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), txReplay); !errors.Is(err, ErrFrameTxNonceSeq) {
		t.Fatalf("single-use replay: got %v, want ErrFrameTxNonceSeq", err)
	}
	// A sequential use also fails: the stored value never equals any
	// nonce_seq except MAX, which is itself invalid for sequential form.
	txSeq := newFrameTx(0, ftSender).keyed(0, 5).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 100_000).
		build(t)
	if _, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), txSeq); !errors.Is(err, ErrFrameTxNonceSeq) {
		t.Fatalf("sequential on exhausted key: got %v, want ErrFrameTxNonceSeq", err)
	}
}

// TestFrameTxKeyedNonceDisjointIndependent: transactions on disjoint key sets
// from the same sender are replay-independent and both valid in one block.
func TestFrameTxKeyedNonceDisjointIndependent(t *testing.T) {
	sdb := mkKeyedState(t, nil)
	for _, key := range []uint64{100, 200} {
		tx := newFrameTx(0, ftSender).keyed(0, key).
			selfVerify(types.FrameFlagApproveScopeMask, 50_000, 100_000).
			build(t)
		if _, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx); err != nil {
			t.Fatalf("key %d: %v", key, err)
		}
	}
	for _, key := range []uint64{100, 200} {
		if got := nonceSlotValue(sdb, ftSender, key); got.Cmp(big.NewInt(1)) != 0 {
			t.Fatalf("key %d slot: got %v, want 1", key, got)
		}
	}
}

// TestFrameTxKeyedNonceStateGasHalt: the approval transition halts when the
// frame's state gas pool cannot cover the first-use charge, leaving the
// transaction without a payer (invalid).
func TestFrameTxKeyedNonceStateGasHalt(t *testing.T) {
	sdb := mkKeyedState(t, nil)
	tx := newFrameTx(0, ftSender).keyed(0, 7).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 97_919). // one short of 97920
		build(t)
	if _, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx); !errors.Is(err, ErrFrameTxVerifyFailed) {
		t.Fatalf("got %v, want ErrFrameTxVerifyFailed (approval halted, verify frame fails)", err)
	}
	if got := nonceSlotValue(sdb, ftSender, 7); got.Sign() != 0 {
		t.Fatalf("key 7 consumed despite halt: %v", got)
	}
}

// TestFrameTxKeyedNonceTXPARAM: transaction introspection of the EIP-8250
// fields from within a frame.
func TestFrameTxKeyedNonceTXPARAM(t *testing.T) {
	// Contract: store TXPARAM(0x01), TXPARAM(0x0D), TXPARAM(0x0E),
	// TXPARAM(0x0F), TXPARAM(0x10) into slots 0..4.
	var code []byte
	for i, p := range []byte{0x01, 0x0d, 0x0e, 0x0f, 0x10} {
		code = append(code, 0x60, p, 0xb0, 0x60, byte(i), 0x55)
	}
	code = append(code, 0x00)
	reader := common.HexToAddress("0x1234")
	// Pre-seed both keyed nonce slots at 3 so the sequential form seq=3 is
	// statefully valid (and consumption writes 4).
	be32 := func(v uint64) common.Hash { return common.Hash(uint256.NewInt(v).Bytes32()) }
	nm := nonceManagerAlloc[types.NonceManagerAddress]
	nm.Storage = map[common.Hash]common.Hash{
		types.NonceSlot(ftSender, uint256.NewInt(42)):   be32(3),
		types.NonceSlot(ftSender, uint256.NewInt(1042)): be32(3),
	}
	sdb := mkKeyedState(t, types.GenesisAlloc{
		reader:                    {Balance: big.NewInt(1), Code: code},
		types.NonceManagerAddress: nm,
	})
	tx := newFrameTx(0, ftSender).keyed(3, 42, 1042).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 200_000).
		addFrame(types.FrameModeDefault, 0, &reader, 150_000, 500_000, new(uint256.Int), nil).
		build(t)
	res, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx)
	if err != nil {
		t.Fatalf("txparam tx: %v", err)
	}
	if res.FrameReceipts[1].Status != vm.FrameStatusSucceeded {
		t.Fatalf("reader frame status %d", res.FrameReceipts[1].Status)
	}
	wantSeq := new(big.Int).SetUint64(3)
	if got := sdb.GetState(reader, common.Hash{}).Big(); got.Cmp(wantSeq) != 0 {
		t.Fatalf("TXPARAM(0x01) = %v, want nonce_seq 3", got)
	}
	if got := sdb.GetState(reader, common.Hash{31: 1}).Big(); got.Sign() != 0 {
		t.Fatalf("TXPARAM(0x0D) = %v, want legacy nonce 0", got)
	}
	if got := sdb.GetState(reader, common.Hash{31: 2}).Big(); got.Cmp(big.NewInt(2)) != 0 {
		t.Fatalf("TXPARAM(0x0E) = %v, want key count 2", got)
	}
	wantHash := types.NonceKeysHash([]uint256.Int{*uint256.NewInt(42), *uint256.NewInt(1042)})
	if got := sdb.GetState(reader, common.Hash{31: 3}); got != wantHash {
		t.Fatalf("TXPARAM(0x0F) = %v, want %v", got, wantHash)
	}
	if got := sdb.GetState(reader, common.Hash{31: 4}).Big(); got.Cmp(big.NewInt(42)) != 0 {
		t.Fatalf("TXPARAM(0x10) = %v, want key0 42", got)
	}
}

// TestFrameTxKeyedNonceLegacyCreateInterplay (EIP-8250 nonce consumption):
// for the legacy domain the payment approval increments the sender's CURRENT
// account nonce. An earlier CREATE at the sender moves it first, so the
// approval observes and increments the moved value.
func TestFrameTxKeyedNonceLegacyCreateInterplay(t *testing.T) {
	// CREATE(value 0, offset 0, size 0); APPROVE scope 2 (execution); STOP
	senderCode := []byte{0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0xf0, 0x60, 0x02, 0x60, 0x00, 0x60, 0x00, 0xaa, 0x00}
	// APPROVE with scope 1 (payment only) — the verifier contract.
	verifier := common.HexToAddress("0x9999")
	approvePaymentCode := []byte{0x60, 0x01, 0x60, 0x00, 0x60, 0x00, 0xaa}
	sdb := mkKeyedState(t, types.GenesisAlloc{
		ftSender: {Balance: big.NewInt(1e18), Code: senderCode},
		verifier: {Balance: big.NewInt(1), Code: approvePaymentCode},
	})
	tx := newFrameTx(0, ftSender).
		addFrame(types.FrameModeDefault, types.FrameFlagApproveExecution, &ftSender, 100_000, 200_000, new(uint256.Int), nil).
		addFrame(types.FrameModeVerify, types.FrameFlagApprovePayment, &verifier, 50_000, 50_000, new(uint256.Int), nil).
		build(t)
	res, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx)
	if err != nil {
		t.Fatalf("create-interplay tx: %v", err)
	}
	if res.FrameReceipts[0].Status != vm.FrameStatusSucceeded ||
		res.FrameReceipts[1].Status != vm.FrameStatusSucceeded {
		t.Fatalf("frame statuses %d/%d", res.FrameReceipts[0].Status, res.FrameReceipts[1].Status)
	}
	// CREATE bumped 0 -> 1; approval consumed the CURRENT nonce: 1 -> 2.
	if nonce := sdb.GetNonce(ftSender); nonce != 2 {
		t.Fatalf("sender nonce: got %d, want 2 (CREATE + increment-current)", nonce)
	}
	if res.Payer != verifier {
		t.Fatalf("payer %v, want verifier", res.Payer)
	}
}

// TestFrameTxKeyedNonceLegacyOverflowHalt (EIP-8250): when the sender's
// legacy nonce reaches MAX_NONCE_SEQ before approval (via CREATE), the
// payment-scoped APPROVE halts exceptionally with no approval effects, so
// the VERIFY frame fails and the transaction is invalid.
func TestFrameTxKeyedNonceLegacyOverflowHalt(t *testing.T) {
	senderCode := []byte{0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0xf0, 0x60, 0x02, 0x60, 0x00, 0x60, 0x00, 0xaa, 0x00}
	verifier := common.HexToAddress("0x9999")
	approvePaymentCode := []byte{0x60, 0x01, 0x60, 0x00, 0x60, 0x00, 0xaa}
	sdb := mkKeyedState(t, types.GenesisAlloc{
		ftSender: {Balance: big.NewInt(1e18), Code: senderCode, Nonce: math.MaxUint64 - 1},
		verifier: {Balance: big.NewInt(1), Code: approvePaymentCode},
	})
	// nonce_seq == pre-state legacy nonce == MaxUint64-1.
	tx := newFrameTx(math.MaxUint64-1, ftSender).
		addFrame(types.FrameModeDefault, types.FrameFlagApproveExecution, &ftSender, 100_000, 200_000, new(uint256.Int), nil).
		addFrame(types.FrameModeVerify, types.FrameFlagApprovePayment, &verifier, 50_000, 50_000, new(uint256.Int), nil).
		build(t)
	if _, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx); !errors.Is(err, ErrFrameTxVerifyFailed) {
		t.Fatalf("got %v, want ErrFrameTxVerifyFailed (approval halted on nonce overflow)", err)
	}
	// The transaction is invalid: all its effects, including the earlier
	// frame's CREATE nonce bump, are discarded (EIP-8250: "Clients MUST
	// discard all effects of an invalid transaction").
	if nonce := sdb.GetNonce(ftSender); nonce != math.MaxUint64-1 {
		t.Fatalf("sender nonce: got %d, want MAX-1 (invalid tx fully rolled back)", nonce)
	}
}

// TestFrameTxKeyedNonceBlockAccessList: keyed nonce slots are recorded in the
// EIP-7928 block access list as storage changes of NONCE_MANAGER (and never
// as storage reads).
func TestFrameTxKeyedNonceBlockAccessList(t *testing.T) {
	sdb := mkKeyedState(t, nil)
	tx := newFrameTx(0, ftSender).keyed(0, 7).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 100_000).
		build(t)
	// Simulate block position 7 for the BAL index.
	sdb.SetTxContext(tx.Hash(), 0, 7)
	if _, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx); err != nil {
		t.Fatalf("keyed tx: %v", err)
	}
	accessList := sdb.Finalise(false)
	if accessList == nil {
		t.Fatal("no block access list constructed (Amsterdam rules expected)")
	}
	acc, ok := accessList.Accounts[types.NonceManagerAddress]
	if !ok {
		t.Fatal("NONCE_MANAGER missing from block access list")
	}
	slot := types.NonceSlot(ftSender, uint256.NewInt(7))
	writes, ok := acc.StorageWrites[slot]
	if !ok {
		t.Fatalf("nonce slot %v missing from storage changes", slot)
	}
	if got, ok := writes[7]; !ok || got != common.Hash(uint256.NewInt(1).Bytes32()) {
		t.Fatalf("storage change at index 7: got %v (present %v), want 1", got, ok)
	}
	if _, ok := acc.StorageReads[slot]; ok {
		t.Fatal("nonce slot appeared in storage reads; a valid tx writes every slot it reads")
	}
}

// TestFrameTxKeyedNonceForkGating: the payload form must match the fork.
func TestFrameTxKeyedNonceForkGating(t *testing.T) {
	selfVerifyFrames := []types.Frame{{
		Mode:   types.FrameModeVerify,
		Flags:  types.FrameFlagApproveScopeMask,
		Limits: types.FrameGasLimits{Execution: 50_000},
		Value:  new(uint256.Int),
	}}

	// Under the keyed nonce fork the legacy payload form is rejected.
	sdb := mkKeyedState(t, nil)
	legacyTx := buildLegacyFrameTx(t, 0, selfVerifyFrames)
	if _, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), legacyTx); !errors.Is(err, types.ErrFrameTxLegacyKeyedFork) {
		t.Fatalf("legacy form under keyed fork: got %v, want ErrFrameTxLegacyKeyedFork", err)
	}

	// Pre-fork (Bogota only): the keyed payload form is rejected...
	sdb2 := mkKeyedState(t, nil)
	keyedTx := newFrameTx(0, ftSender).keyed(0, 7).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 100_000).
		build(t)
	if _, _, err := applyFrameTx(t, sdb2, bogotaOnlyEVM(sdb2), keyedTx); !errors.Is(err, types.ErrFrameTxKeyedBeforeFork) {
		t.Fatalf("keyed form pre-fork: got %v, want ErrFrameTxKeyedBeforeFork", err)
	}

	// ...and the legacy payload form executes with EIP-8141 semantics.
	sdb3 := mkKeyedState(t, nil)
	legacyTx2 := buildLegacyFrameTx(t, 0, selfVerifyFrames)
	res, _, err := applyFrameTx(t, sdb3, bogotaOnlyEVM(sdb3), legacyTx2)
	if err != nil {
		t.Fatalf("legacy form pre-fork must be valid: %v", err)
	}
	if res.FrameReceipts[0].Status != vm.FrameStatusSucceeded {
		t.Fatalf("legacy verify frame status %d", res.FrameReceipts[0].Status)
	}
	if nonce := sdb3.GetNonce(ftSender); nonce != 1 {
		t.Fatalf("legacy nonce after pre-fork tx: got %d, want 1", nonce)
	}
	// Legacy form keeps its wire encoding (hash stability): re-encoding the
	// decoded transaction must reproduce the original bytes.
	enc1, err := legacyTx2.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	tx2 := new(types.Transaction)
	if err := tx2.UnmarshalBinary(enc1); err != nil {
		t.Fatal(err)
	}
	enc2, err := tx2.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(enc1, enc2) || tx2.Hash() != legacyTx2.Hash() {
		t.Fatal("legacy payload re-encode changed bytes/hash")
	}
}

// TestFrameTxKeyedNoncePersistsThroughReverts (EIP-8250: "Nonce consumption
// persists through later-frame reverts and EIP-8141 atomic-batch rollback
// because it is part of payment approval"): the keyed nonce write committed
// by the approving frame survives both a later plain revert and an atomic
// batch unroll, while the batch's own effects are rolled back.
func TestFrameTxKeyedNoncePersistsThroughReverts(t *testing.T) {
	// (a) Later plain revert.
	reverter := common.HexToAddress("0xf1a1")
	sdb := mkKeyedState(t, types.GenesisAlloc{
		reverter: {Code: []byte{0x60, 0x00, 0x60, 0x00, 0xfd}},
	})
	tx := newFrameTx(0, ftSender).keyed(0, 11).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 100_000).
		addFrame(types.FrameModeDefault, 0, &reverter, 50_000, 0, new(uint256.Int), nil).
		build(t)
	res, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx)
	if err != nil {
		t.Fatalf("tx should be valid despite later revert: %v", err)
	}
	if res.FrameReceipts[1].Status != vm.FrameStatusFailed {
		t.Fatalf("frame1 status %d, want failed", res.FrameReceipts[1].Status)
	}
	if got := nonceSlotValue(sdb, ftSender, 11); got.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("key 11 slot %v, want 1 (consumption persists through revert)", got)
	}
	if nonce := sdb.GetNonce(ftSender); nonce != 0 {
		t.Fatalf("legacy nonce changed: %d", nonce)
	}

	// (b) Atomic batch rollback: batch effects unroll, consumption persists.
	r1 := common.HexToAddress("0xb471")
	r3 := common.HexToAddress("0xb473")
	sdb2 := mkKeyedState(t, types.GenesisAlloc{
		reverter: {Code: []byte{0x60, 0x00, 0x60, 0x00, 0xfd}},
	})
	batch := uint8(types.FrameFlagAtomicBatch)
	tx2 := newFrameTx(0, ftSender).keyed(0, 12).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 100_000).
		addFrame(types.FrameModeSender, batch, &r1, 100_000, 200_000, uint256.NewInt(11), nil).
		addFrame(types.FrameModeSender, batch, &reverter, 100_000, 10_000, new(uint256.Int), nil). // fails: unrolls batch
		addFrame(types.FrameModeSender, 0, &r3, 100_000, 200_000, uint256.NewInt(33), nil).        // batch terminator, skipped
		build(t)
	res2, _, err := applyFrameTx(t, sdb2, frameEVM(sdb2, nil), tx2)
	if err != nil {
		t.Fatalf("tx should be valid despite batch rollback: %v", err)
	}
	statuses := []uint8{res2.FrameReceipts[0].Status, res2.FrameReceipts[1].Status, res2.FrameReceipts[2].Status, res2.FrameReceipts[3].Status}
	want := []uint8{vm.FrameStatusSucceeded, vm.FrameStatusSucceeded, vm.FrameStatusFailed, vm.FrameStatusSkipped}
	if !bytes.Equal(statuses, want) {
		t.Fatalf("statuses %v, want %v", statuses, want)
	}
	// All batch state rolled back, including the successful first frame.
	for _, a := range []common.Address{r1, reverter, r3} {
		if bal := sdb2.GetBalance(a); bal.Sign() != 0 {
			t.Fatalf("batch account %v balance %v, want 0", a, bal)
		}
	}
	// The keyed nonce consumption (committed by the pre-batch approval)
	// persists through the batch rollback.
	if got := nonceSlotValue(sdb2, ftSender, 12); got.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("key 12 slot %v, want 1 (consumption persists through batch rollback)", got)
	}
}

// TestFrameTxKeyedNonceSixteenKeys exercises the MAX_NONCE_KEYS boundary: a
// transaction selecting exactly 16 keys is valid and consumes all of them
// (17+ is rejected at decode/static level; see TestFrameTxKeyedDecodeRejections).
func TestFrameTxKeyedNonceSixteenKeys(t *testing.T) {
	sdb := mkKeyedState(t, nil)
	keys := make([]uint64, 16)
	for i := range keys {
		keys[i] = uint64(i + 1)
	}
	tx := newFrameTx(0, ftSender).keyed(0, keys...).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 100_000+16*types.KeyedNonceFirstUseStateGas(params.CostPerStateByte)).
		build(t)
	if _, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx); err != nil {
		t.Fatalf("16-key tx must be valid: %v", err)
	}
	for _, k := range keys {
		if got := nonceSlotValue(sdb, ftSender, k); got.Cmp(big.NewInt(1)) != 0 {
			t.Fatalf("key %d slot %v, want 1", k, got)
		}
	}
	if nonce := sdb.GetNonce(ftSender); nonce != 0 {
		t.Fatalf("legacy nonce changed: %d", nonce)
	}
}

// TestFrameTxKeyedNonceSkipNonceChecks covers the eth_call/simulation path:
// with SkipNonceChecks the keyed sequence checks are skipped (the transaction
// still executes and consumes; the pre-state legacy nonce is still captured
// for TXPARAM(0x0D)).
func TestFrameTxKeyedNonceSkipNonceChecks(t *testing.T) {
	sdb := mkKeyedState(t, nil)
	// seq 5 on an unused slot would be statefully invalid; simulation skips
	// the check.
	tx := newFrameTx(5, ftSender).keyed(5, 7).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 100_000).
		build(t)
	evm := frameEVM(sdb, nil)
	msg, err := TransactionToMessage(tx, frameSigner, evm.Context.BaseFee)
	if err != nil {
		t.Fatalf("message conversion: %v", err)
	}
	msg.SkipNonceChecks = true
	gp := NewGasPool(evm.Context.GasLimit)
	evm.SetTxContext(NewEVMTxContext(msg))
	res, err := newStateTransition(evm, msg, gp, common.Address{}).execute()
	if err != nil {
		t.Fatalf("simulation must skip keyed nonce checks: %v", err)
	}
	if res.FrameReceipts[0].Status != vm.FrameStatusSucceeded {
		t.Fatalf("verify frame failed in simulation")
	}
	// Consumption still writes seq+1.
	if got := nonceSlotValue(sdb, ftSender, 7); got.Cmp(big.NewInt(6)) != 0 {
		t.Fatalf("slot %v, want 6 (seq 5 consumed)", got)
	}
	if nonce := sdb.GetNonce(ftSender); nonce != 0 {
		t.Fatalf("legacy nonce changed: %d", nonce)
	}
}

// TestFrameTxKeyedNonceManagerAbsent characterizes behavior when the chain
// state does NOT contain the NONCE_MANAGER deployment: the protocol never
// checks for the deployment (EIP-8250: "Clients MUST NOT check for
// NONCE_MANAGER at the fork boundary"), so slots are ordinary storage on a
// freshly created account. Activation on a real chain still requires the
// deployment precondition (spec: the state MUST already contain it); this
// test only documents the engine's behavior if it is missing.
func TestFrameTxKeyedNonceManagerAbsent(t *testing.T) {
	sdb := mkState(types.GenesisAlloc{ftSender: {Balance: big.NewInt(1e18)}}) // no NONCE_MANAGER
	if sdb.Exist(types.NonceManagerAddress) {
		t.Fatal("precondition: NONCE_MANAGER must be absent in this test")
	}
	tx := newFrameTx(0, ftSender).keyed(0, 3).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 100_000).
		build(t)
	if _, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx); err != nil {
		t.Fatalf("keyed tx must work without the deployment (no boundary check): %v", err)
	}
	if got := nonceSlotValue(sdb, ftSender, 3); got.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("slot %v, want 1", got)
	}
	// The account now exists (storage writes created it) but has no code:
	// ordinary calls to it remain no-ops, so nonce-slot spoofing via SSTORE
	// is impossible either way.
	if code := sdb.GetCode(types.NonceManagerAddress); len(code) != 0 {
		t.Fatalf("unexpected code at NONCE_MANAGER: %x", code)
	}
}

// TestFrameTxKeyedNonceBlockIntegration runs a keyed frame transaction
// through full block generation and verifies receipts and the block access
// list: NONCE_MANAGER must appear with the consumed slot in storage_changes
// at the transaction's index (and never in storage_reads).
func TestFrameTxKeyedNonceBlockIntegration(t *testing.T) {
	receiver := common.HexToAddress("0xbeef")
	cfg := frameChainConfig()
	alloc := types.GenesisAlloc{ftSender: {Balance: big.NewInt(1e18)}}
	alloc[types.NonceManagerAddress] = types.Account{Nonce: 1, Code: types.NonceManagerCode}
	gspec := &Genesis{Config: cfg, GasLimit: 60_000_000, Alloc: alloc}

	b := newFrameTx(0, ftSender).keyed(0, 7).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 100_000).
		addFrame(types.FrameModeSender, 0, &receiver, 100_000, 200_000, uint256.NewInt(1e15), nil)
	b.fees.MaxPriorityFeePerGas = uint256.NewInt(1_000_000_000)
	b.fees.MaxFee = uint256.NewInt(30_000_000_000_000_000)
	frameTx := b.build(t)

	_, blocks, receipts := GenerateChainWithGenesis(gspec, beacon.New(ethash.NewFaker()), 1, func(i int, gen *BlockGen) {
		gen.SetPoS()
		gen.AddTx(frameTx)
	})
	if len(blocks) != 1 || len(blocks[0].Transactions()) != 1 {
		t.Fatalf("expected 1 block with 1 tx, got %d/%d", len(blocks), len(blocks[0].Transactions()))
	}
	block := blocks[0]
	if root := types.DeriveSha(receipts[0], trie.NewStackTrie(nil)); root != block.ReceiptHash() {
		t.Fatalf("receipt root mismatch: derived %v, header %v", root, block.ReceiptHash())
	}
	genReceipt := receipts[0][0]
	if len(genReceipt.FrameReceipts) != 2 ||
		genReceipt.FrameReceipts[0].Status != types.FrameStatusSucceeded ||
		genReceipt.FrameReceipts[1].Status != types.FrameStatusSucceeded {
		t.Fatalf("unexpected frame receipts: %+v", genReceipt.FrameReceipts)
	}
	// The keyed first-use charge (64 bytes * 1530) lands on the approving
	// frame's state-gas receipt.
	if got := genReceipt.FrameReceipts[0].GasUsedState; got != 97_920 {
		t.Fatalf("frame0 state gas %d, want 97920 (keyed first use)", got)
	}
	// Block access list: the consumed slot must be a storage change of
	// NONCE_MANAGER at the transaction's index (1; 0 is reserved for pre-block
	// system calls), never a storage read.
	al := block.AccessList()
	if al == nil {
		t.Fatal("generated block carries no access list")
	}
	slot := new(uint256.Int).SetBytes(types.NonceSlot(ftSender, uint256.NewInt(7)).Bytes())
	found := false
	for _, acct := range *al {
		if acct.Address != types.NonceManagerAddress {
			continue
		}
		for _, sc := range acct.StorageChanges {
			if sc.Slot != nil && sc.Slot.Cmp(slot) == 0 {
				found = true
				if len(sc.SlotChanges) != 1 || sc.SlotChanges[0].BlockAccessIndex != 1 ||
					common.Hash(sc.SlotChanges[0].PostValue.Bytes32()) != common.Hash(uint256.NewInt(1).Bytes32()) {
					t.Fatalf("unexpected slot changes: %+v", sc.SlotChanges)
				}
			}
		}
		for _, rd := range acct.StorageReads {
			if rd != nil && rd.Cmp(slot) == 0 {
				t.Fatal("keyed nonce slot must never appear in storage_reads")
			}
		}
	}
	if !found {
		t.Fatal("NONCE_MANAGER keyed slot missing from block access list storage_changes")
	}
}
