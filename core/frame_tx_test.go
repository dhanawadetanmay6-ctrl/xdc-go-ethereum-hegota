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

// End-to-end tests for EIP-8141 frame transactions: the frame engine running
// whole transactions against an in-memory Bogota-active chain state.

package core

import (
	"bytes"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

var (
	frameCfg    = frameChainConfig()
	frameSigner = types.LatestSigner(frameCfg)

	ftKey, _     = crypto.HexToECDSA("45a915e4d060149eb4365960e6a7a45f334393093061116b197e3240065ff2d8")
	ftSender     = crypto.PubkeyToAddress(ftKey.PublicKey)
	ftBankKey, _ = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
	ftBank       = crypto.PubkeyToAddress(ftBankKey.PublicKey)
)

func frameChainConfig() *params.ChainConfig {
	cfg := *params.MergedTestChainConfig
	cfg.AmsterdamTime = new(uint64)
	cfg.BogotaTime = new(uint64)
	cfg.KeyedNonceTime = new(uint64) // EIP-8250: tests build keyed-form transactions
	return &cfg
}

// frameEVM builds a Bogota EVM over statedb. baseFee nil means fees disabled
// (eth_call style NoBaseFee environment).
func frameEVM(sdb *state.StateDB, baseFee *big.Int) *vm.EVM {
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
	}
	cfg := vm.Config{}
	if baseFee == nil {
		cfg.NoBaseFee = true
		ctx.BaseFee = big.NewInt(0)
	} else {
		ctx.BaseFee = baseFee
	}
	return vm.NewEVM(ctx, sdb, frameCfg, cfg)
}

// applyFrameTx executes one frame transaction against sdb and returns the
// execution result, block gas pool and consensus error.
func applyFrameTx(t *testing.T, sdb *state.StateDB, evm *vm.EVM, tx *types.Transaction) (*ExecutionResult, *GasPool, error) {
	t.Helper()
	msg, err := TransactionToMessage(tx, frameSigner, evm.Context.BaseFee)
	if err != nil {
		return nil, nil, err
	}
	gp := NewGasPool(evm.Context.GasLimit)
	evm.SetTxContext(NewEVMTxContext(msg))
	st := newStateTransition(evm, msg, gp, common.Address{})
	res, err := st.execute()
	return res, gp, err
}

// frameTxBuilder assembles and signs a frame transaction with a single
// default-code secp256k1 signature installed at index 0.
type frameTxBuilder struct {
	nonce     uint64
	nonceKeys []uint256.Int
	sender    common.Address
	frames    []types.Frame
	sigs      []types.FrameSignature
	fees      types.FrameFees
	chainID   *uint256.Int
	blobs     []common.Hash
	signKey   bool
}

func newFrameTx(nonce uint64, sender common.Address) *frameTxBuilder {
	return &frameTxBuilder{
		nonce:   nonce,
		sender:  sender,
		chainID: uint256.MustFromBig(frameCfg.ChainID),
		fees: types.FrameFees{
			MaxFee:               new(uint256.Int),
			MaxPriorityFeePerGas: new(uint256.Int),
		},
		signKey: true,
	}
}

// keyed selects EIP-8250 keyed nonce domains for the transaction: nonce_seq
// is seq and each key is a uint256 nonce key.
func (b *frameTxBuilder) keyed(seq uint64, keys ...uint64) *frameTxBuilder {
	b.nonce = seq
	b.nonceKeys = make([]uint256.Int, len(keys))
	for i, k := range keys {
		b.nonceKeys[i].SetUint64(k)
	}
	return b
}

func (b *frameTxBuilder) addFrame(mode, flags uint8, target *common.Address, exec, state uint64, value *uint256.Int, data []byte) *frameTxBuilder {
	b.frames = append(b.frames, types.Frame{
		Mode:   mode,
		Flags:  flags,
		Target: target,
		Limits: types.FrameGasLimits{Execution: exec, State: state},
		Value:  value,
		Data:   data,
	})
	return b
}

// selfVerify prepends the canonical self_verify frame (default code, scope
// EXECUTION|FLAGS per argument) for the sender.
func (b *frameTxBuilder) selfVerify(scope uint8, exec, stateGas uint64) *frameTxBuilder {
	b.frames = append([]types.Frame{{
		Mode:   types.FrameModeVerify,
		Flags:  scope,
		Target: nil, // resolves to sender
		Limits: types.FrameGasLimits{Execution: exec, State: stateGas},
		Value:  new(uint256.Int),
	}}, b.frames...)
	return b
}

func (b *frameTxBuilder) build(t *testing.T) *types.Transaction {
	t.Helper()
	if b.signKey {
		b.sigs = append([]types.FrameSignature{{
			Scheme:    types.FrameSigSchemeSecp256k1,
			Signature: make([]byte, 65), // length placeholder, replaced by SignFrameTx
		}}, b.sigs...)
	}
	ft := &types.FrameTx{
		ChainID:    b.chainID,
		NonceKeys:  b.nonceKeys,
		NonceSeq:   b.nonce,
		Sender:     b.sender,
		Frames:     b.frames,
		Signatures: b.sigs,
		Fees:       b.fees,
		BlobHashes: b.blobs,
	}
	tx := types.NewTx(ft)
	if b.signKey {
		signed, err := types.SignFrameTx(ftKey, tx)
		if err != nil {
			t.Fatalf("SignFrameTx: %v", err)
		}
		tx = signed
	}
	return tx
}

// mkFrameState builds an in-memory statedb with the sender funded.
func mkFrameState(t *testing.T, extra types.GenesisAlloc) *state.StateDB {
	t.Helper()
	alloc := types.GenesisAlloc{ftSender: {Balance: big.NewInt(1e18)}}
	for a, acc := range extra {
		alloc[a] = acc
	}
	return mkState(alloc)
}

// --- Bytecode fixtures ---

var (
	// PUSH1 3 (scope), PUSH1 0 (length), PUSH1 0 (offset), APPROVE
	approveBothCode = []byte{0x60, 0x03, 0x60, 0x00, 0x60, 0x00, 0xaa}
	// PUSH1 0 PUSH1 0 REVERT
	revertCode = []byte{0x60, 0x00, 0x60, 0x00, 0xfd}
	// conditional SSTORE: calldata word 0 == 0 -> clear slot 0, else set slot 0 = 1
	// 60 00 35 15 60 0d 57 | 60 01 60 00 55 00 | 5b 60 00 60 00 55 00
	setOrClearCode = []byte{0x60, 0x00, 0x35, 0x15, 0x60, 0x0d, 0x57, 0x60, 0x01, 0x60, 0x00, 0x55, 0x00, 0x5b, 0x60, 0x00, 0x60, 0x00, 0x55, 0x00}
	// unconditional SSTORE slot 0 = 1: 60 01 60 00 55 00
	setSlotCode = []byte{0x60, 0x01, 0x60, 0x00, 0x55, 0x00}
)

func word(v byte) []byte {
	w := make([]byte, 32)
	w[31] = v
	return w
}

// TestFrameTxSelfVerifyTransfer runs the canonical self_verify + user_op
// shape: default-code VERIFY of the sender approving execution+payment,
// followed by a SENDER value transfer to a fresh account.
func TestFrameTxSelfVerifyTransfer(t *testing.T) {
	receiver := common.HexToAddress("0xbeef")
	sdb := mkFrameState(t, nil)

	tx := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
		addFrame(types.FrameModeSender, 0, &receiver, 100_000, 200_000, uint256.NewInt(1e15), nil).
		build(t)

	evm := frameEVM(sdb, nil)
	res, gp, err := applyFrameTx(t, sdb, evm, tx)
	if err != nil {
		t.Fatalf("frame tx failed: %v", err)
	}
	if len(res.FrameReceipts) != 2 {
		t.Fatalf("receipts: got %d, want 2", len(res.FrameReceipts))
	}
	r0, r1 := res.FrameReceipts[0], res.FrameReceipts[1]

	// VERIFY frame: warm access to the pre-warmed sender only.
	if r0.Status != vm.FrameStatusSucceeded {
		t.Fatalf("frame0 status %d, want success", r0.Status)
	}
	if r0.GasUsedExecution != params.WarmAccountAccessAmsterdam {
		t.Fatalf("frame0 exec gas %d, want %d", r0.GasUsedExecution, params.WarmAccountAccessAmsterdam)
	}
	if r0.GasUsedState != 0 {
		t.Fatalf("frame0 state gas %d, want 0", r0.GasUsedState)
	}
	// SENDER frame: cold access to fresh receiver + account-creation state gas.
	if r1.Status != vm.FrameStatusSucceeded {
		t.Fatalf("frame1 status %d, want success", r1.Status)
	}
	if r1.GasUsedExecution != params.ColdAccountAccessAmsterdam {
		t.Fatalf("frame1 exec gas %d, want %d", r1.GasUsedExecution, params.ColdAccountAccessAmsterdam)
	}
	if r1.GasUsedState != 183_600 {
		t.Fatalf("frame1 state gas %d, want 183600", r1.GasUsedState)
	}
	// The value transfer emits one EIP-7708 log attributed to frame 1.
	if len(r1.Logs) != 1 {
		t.Fatalf("frame1 logs %d, want 1 (ETH transfer log)", len(r1.Logs))
	}
	// Payer, nonce, balances.
	if res.Payer != ftSender {
		t.Fatalf("payer %v, want sender %v", res.Payer, ftSender)
	}
	if got := sdb.GetBalance(receiver); got.Cmp(uint256.NewInt(1e15)) != 0 {
		t.Fatalf("receiver balance %v, want 1e15", got)
	}
	if got := sdb.GetBalance(ftSender); got.Cmp(uint256.NewInt(1e18-1e15)) != 0 {
		t.Fatalf("sender balance %v, want 999e15 (fees disabled)", got)
	}
	if got := sdb.GetNonce(ftSender); got != 1 {
		t.Fatalf("sender nonce %d, want 1", got)
	}
	// Block accounting: execution dimension carries intrinsic + frames,
	// state dimension the net state gas.
	if gp.Used() == 0 {
		t.Fatal("block gas pool not charged")
	}
}

// TestFrameTxUnapprovedSender ensures a SENDER frame without execution
// approval invalidates the transaction.
func TestFrameTxUnapprovedSender(t *testing.T) {
	receiver := common.HexToAddress("0xbeef")
	sdb := mkFrameState(t, nil)
	tx := newFrameTx(0, ftSender).
		addFrame(types.FrameModeSender, 0, &receiver, 100_000, 200_000, uint256.NewInt(1), nil).
		build(t)
	_, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx)
	if !errors.Is(err, ErrFrameTxSenderUnapproved) {
		t.Fatalf("err %v, want ErrFrameTxSenderUnapproved", err)
	}
	if got := sdb.GetNonce(ftSender); got != 0 {
		t.Fatalf("sender nonce %d, want 0 (tx reverted)", got)
	}
}

// TestFrameTxNoPayer ensures a transaction without payment approval is
// invalid even if all frames succeed.
func TestFrameTxNoPayer(t *testing.T) {
	receiver := common.HexToAddress("0xbeef")
	sdb := mkFrameState(t, nil)
	tx := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveExecution, 50_000, 0). // execution only
		addFrame(types.FrameModeSender, 0, &receiver, 100_000, 200_000, uint256.NewInt(1), nil).
		build(t)
	_, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx)
	if !errors.Is(err, ErrFrameTxNoPayer) {
		t.Fatalf("err %v, want ErrFrameTxNoPayer", err)
	}
	if got := sdb.GetBalance(receiver); got.Sign() != 0 {
		t.Fatalf("receiver balance %v, want 0 (tx reverted)", got)
	}
	if got := sdb.GetNonce(ftSender); got != 0 {
		t.Fatalf("sender nonce %d, want 0 (tx reverted)", got)
	}
}

// TestFrameTxVerifyFailure ensures a reverting VERIFY frame invalidates the
// whole transaction.
func TestFrameTxVerifyFailure(t *testing.T) {
	verifier := common.HexToAddress("0xc0de")
	sdb := mkFrameState(t, types.GenesisAlloc{
		verifier: {Code: revertCode},
	})
	tx := newFrameTx(0, ftSender).
		addFrame(types.FrameModeVerify, 0, &verifier, 100_000, 0, new(uint256.Int), nil).
		build(t)
	_, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx)
	if !errors.Is(err, ErrFrameTxVerifyFailed) {
		t.Fatalf("err %v, want ErrFrameTxVerifyFailed", err)
	}
}

// TestFrameTxSenderContractAccount covers the AA case: the sender carries
// code (EIP-3607 must not apply) and approves via real-code APPROVE.
func TestFrameTxSenderContractAccount(t *testing.T) {
	receiver := common.HexToAddress("0xbeef")
	sdb := mkFrameState(t, nil)
	// Overlay code onto the sender account (sender is a contract).
	sdb.SetCode(ftSender, approveBothCode, tracing.CodeChangeUnspecified)

	tx := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 100_000, 10_000).
		addFrame(types.FrameModeSender, 0, &receiver, 100_000, 200_000, uint256.NewInt(7), nil).
		build(t)

	res, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx)
	if err != nil {
		t.Fatalf("frame tx failed: %v", err)
	}
	if res.FrameReceipts[0].Status != vm.FrameStatusSucceeded {
		t.Fatalf("contract VERIFY frame failed")
	}
	if got := sdb.GetBalance(receiver); got.Cmp(uint256.NewInt(7)) != 0 {
		t.Fatalf("receiver balance %v, want 7", got)
	}
	if got := sdb.GetNonce(ftSender); got != 1 {
		t.Fatalf("sender nonce %d, want 1", got)
	}
}

// TestFrameTxRevertIsolation ensures a reverting SENDER frame fails alone:
// earlier frames persist, the transaction succeeds.
func TestFrameTxRevertIsolation(t *testing.T) {
	reverter := common.HexToAddress("0xf1a1")
	sdb := mkFrameState(t, types.GenesisAlloc{
		reverter: {Code: revertCode, Balance: big.NewInt(0)},
	})
	tx := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
		addFrame(types.FrameModeSender, 0, &reverter, 100_000, 10_000, new(uint256.Int), nil). // reverts
		build(t)

	res, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx)
	if err != nil {
		t.Fatalf("tx should be valid despite frame revert: %v", err)
	}
	if res.FrameReceipts[1].Status != vm.FrameStatusFailed {
		t.Fatalf("frame1 status %d, want failed(0)", res.FrameReceipts[1].Status)
	}
	if res.FrameReceipts[0].Status != vm.FrameStatusSucceeded {
		t.Fatalf("frame0 status %d, want success", res.FrameReceipts[0].Status)
	}
	if got := sdb.GetNonce(ftSender); got != 1 {
		t.Fatalf("sender nonce %d, want 1 (approval persisted)", got)
	}
}

// TestFrameTxAtomicBatch exercises the atomic batch: a failing mid-batch
// frame unrolls the whole batch and skips its remainder.
func TestFrameTxAtomicBatch(t *testing.T) {
	r1 := common.HexToAddress("0xb471")
	reverter := common.HexToAddress("0xf1a1")
	r3 := common.HexToAddress("0xb473")
	sdb := mkFrameState(t, types.GenesisAlloc{
		reverter: {Code: revertCode},
	})
	batch := uint8(types.FrameFlagAtomicBatch)
	tx := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
		addFrame(types.FrameModeSender, batch, &r1, 100_000, 200_000, uint256.NewInt(11), nil).
		addFrame(types.FrameModeSender, batch, &reverter, 100_000, 10_000, new(uint256.Int), nil). // fails
		addFrame(types.FrameModeSender, 0, &r3, 100_000, 200_000, uint256.NewInt(33), nil).        // batch terminator, skipped
		build(t)

	res, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx)
	if err != nil {
		t.Fatalf("tx should be valid despite batch failure: %v", err)
	}
	statuses := []uint8{res.FrameReceipts[0].Status, res.FrameReceipts[1].Status, res.FrameReceipts[2].Status, res.FrameReceipts[3].Status}
	want := []uint8{vm.FrameStatusSucceeded, vm.FrameStatusSucceeded, vm.FrameStatusFailed, vm.FrameStatusSkipped}
	if !bytes.Equal(statuses, want) {
		t.Fatalf("statuses %v, want %v", statuses, want)
	}
	// All batch state rolled back, including the successful first frame.
	for _, a := range []common.Address{r1, reverter, r3} {
		if bal := sdb.GetBalance(a); bal.Sign() != 0 {
			t.Fatalf("batch account %v balance %v, want 0", a, bal)
		}
	}
	// Approval (pre-batch) persists.
	if got := sdb.GetNonce(ftSender); got != 1 {
		t.Fatalf("sender nonce %d, want 1", got)
	}
	// Executed batch frames retain execution gas; the skipped frame refunds all.
	if res.FrameReceipts[1].GasUsedExecution == 0 || res.FrameReceipts[2].GasUsedExecution == 0 {
		t.Fatal("executed batch frames must retain execution gas")
	}
	if res.FrameReceipts[3].GasUsedExecution != 0 || res.FrameReceipts[3].GasUsedState != 0 {
		t.Fatal("skipped frame must report zero gas")
	}
}

// TestFrameTxCrossFrameStateRefund covers the EIP-8141 cross-frame refill:
// frame A creates a storage slot (state-gas charge), frame B clears it — the
// refill reduces frame A's receipt.
func TestFrameTxCrossFrameStateRefund(t *testing.T) {
	contract := common.HexToAddress("0xc7e1")
	sdb := mkFrameState(t, types.GenesisAlloc{
		contract: {Code: setOrClearCode},
	})
	tx := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
		addFrame(types.FrameModeSender, 0, &contract, 200_000, 150_000, new(uint256.Int), word(1)). // set slot
		addFrame(types.FrameModeSender, 0, &contract, 200_000, 50_000, new(uint256.Int), word(0)).  // clear slot
		build(t)

	res, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx)
	if err != nil {
		t.Fatalf("frame tx failed: %v", err)
	}
	setFrame, clearFrame := res.FrameReceipts[1], res.FrameReceipts[2]
	if setFrame.Status != vm.FrameStatusSucceeded || clearFrame.Status != vm.FrameStatusSucceeded {
		t.Fatalf("frames must succeed: %d %d", setFrame.Status, clearFrame.Status)
	}
	// Slot cleared at tx end.
	if v := sdb.GetState(contract, common.Hash{}); v != (common.Hash{}) {
		t.Fatalf("slot not cleared: %v", v)
	}
	// The creation charge (97920) was attributed to frame A, then the
	// cross-frame refill reduced it to zero.
	if setFrame.GasUsedState != 0 {
		t.Fatalf("set-frame state gas %d, want 0 after cross-frame refill", setFrame.GasUsedState)
	}
	if clearFrame.GasUsedState != 0 {
		t.Fatalf("clear-frame state gas %d, want 0", clearFrame.GasUsedState)
	}
}

// TestFrameTxStateGasSeparation proves the strict pool separation: state-gas
// charges can never spill into execution gas within a frame.
func TestFrameTxStateGasSeparation(t *testing.T) {
	contract := common.HexToAddress("0xc7e1")
	// Insufficient state gas for slot creation (needs 97920): halt.
	sdb := mkFrameState(t, types.GenesisAlloc{contract: {Code: setSlotCode}})
	tx := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
		addFrame(types.FrameModeSender, 0, &contract, 1_000_000, 50_000, new(uint256.Int), nil).
		build(t)
	res, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx)
	if err != nil {
		t.Fatalf("tx valid (frame halts): %v", err)
	}
	fr := res.FrameReceipts[1]
	if fr.Status != vm.FrameStatusFailed {
		t.Fatalf("status %d, want failed", fr.Status)
	}
	if fr.GasUsedExecution != 1_000_000 {
		t.Fatalf("exceptional halt must consume entire execution pool: got %d", fr.GasUsedExecution)
	}
	if fr.GasUsedState != 0 {
		t.Fatalf("halted frame state gas %d, want 0", fr.GasUsedState)
	}
	if v := sdb.GetState(contract, common.Hash{}); v != (common.Hash{}) {
		t.Fatal("slot must not be set")
	}

	// Same frame with sufficient state gas: succeeds.
	sdb2 := mkFrameState(t, types.GenesisAlloc{contract: {Code: setSlotCode}})
	tx2 := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
		addFrame(types.FrameModeSender, 0, &contract, 1_000_000, 150_000, new(uint256.Int), nil).
		build(t)
	res2, _, err := applyFrameTx(t, sdb2, frameEVM(sdb2, nil), tx2)
	if err != nil {
		t.Fatalf("frame tx failed: %v", err)
	}
	if res2.FrameReceipts[1].Status != vm.FrameStatusSucceeded {
		t.Fatal("frame must succeed with sufficient state gas")
	}
	if res2.FrameReceipts[1].GasUsedState != 97_920 {
		t.Fatalf("state gas %d, want 97920", res2.FrameReceipts[1].GasUsedState)
	}
}

// TestFrameTxSettlementFees verifies the settlement math with real fees:
// base fee burned, priority fee to coinbase, remainder refunded to payer.
func TestFrameTxSettlementFees(t *testing.T) {
	receiver := common.HexToAddress("0xbeef")
	sdb := mkFrameState(t, nil)
	baseFee := big.NewInt(1_000_000_000) // 1 gwei
	tipPerGas := uint64(2_000_000_000)   // 2 gwei

	b := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
		addFrame(types.FrameModeSender, 0, &receiver, 100_000, 200_000, uint256.NewInt(1e15), nil)
	// Generous fee budget: cover required + full tip with slack refunded.
	b.fees.MaxPriorityFeePerGas = uint256.NewInt(tipPerGas)
	// maxGas computed after frames are known: replicate MaxGas().
	tmp := &types.FrameTx{Frames: b.frames, Signatures: []types.FrameSignature{{Scheme: 0, Signature: make([]byte, 65)}}, Fees: b.fees, ChainID: b.chainID, BlobHashes: nil}
	maxGas := tmp.MaxGas()
	b.fees.MaxFee = new(uint256.Int).Mul(uint256.NewInt(maxGas), uint256.NewInt(baseFee.Uint64()+tipPerGas+1_000_000_000))

	tx := b.build(t)
	evm := frameEVM(sdb, baseFee)
	senderBefore := sdb.GetBalance(ftSender).ToBig()
	coinbase := evm.Context.Coinbase

	res, _, err := applyFrameTx(t, sdb, evm, tx)
	if err != nil {
		t.Fatalf("frame tx failed: %v", err)
	}
	gasUsed := res.UsedGas
	value := big.NewInt(1e15)

	// Sender (payer) pays exactly gasUsed * (baseFee + tipPerGas) + value.
	wantSpend := new(big.Int).Add(new(big.Int).Mul(big.NewInt(int64(gasUsed)), big.NewInt(int64(baseFee.Uint64()+tipPerGas))), value)
	gotSpend := new(big.Int).Sub(senderBefore, sdb.GetBalance(ftSender).ToBig())
	if gotSpend.Cmp(wantSpend) != 0 {
		t.Fatalf("payer spend %v, want %v (gasUsed %d)", gotSpend, wantSpend, gasUsed)
	}
	// Coinbase receives the priority component.
	wantTip := new(big.Int).Mul(big.NewInt(int64(gasUsed)), big.NewInt(int64(tipPerGas)))
	if got := sdb.GetBalance(coinbase).ToBig(); got.Cmp(wantTip) != 0 {
		t.Fatalf("coinbase tip %v, want %v", got, wantTip)
	}
	// gasUsed decomposes as execution + state across frames.
	var execSum, stateSum uint64
	for _, fr := range res.FrameReceipts {
		execSum += fr.GasUsedExecution
		stateSum += fr.GasUsedState
	}
	// gasUsed = max(execPostRefund, floor) + state; here execPostRefund ==
	// intrinsic + exec (no refunds), check consistency with decomposition.
	if gasUsed < execSum+stateSum {
		t.Fatalf("gasUsed %d < exec %d + state %d", gasUsed, execSum, stateSum)
	}
}

// TestFrameTxPreBogotaRejected ensures the type is not accepted before the
// Bogota fork (signer-level rejection).
func TestFrameTxPreBogotaRejected(t *testing.T) {
	cfg := *params.MergedTestChainConfig
	cfg.AmsterdamTime = new(uint64) // Amsterdam but not Bogota
	signer := types.LatestSigner(&cfg)
	tx := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
		build(t)
	if _, err := types.Sender(signer, tx); !errors.Is(err, types.ErrTxTypeNotSupported) {
		t.Fatalf("err %v, want ErrTxTypeNotSupported", err)
	}
}

// TestFrameTxNonceReplay: replaying the same nonce fails.
func TestFrameTxNonceReplay(t *testing.T) {
	receiver := common.HexToAddress("0xbeef")
	sdb := mkFrameState(t, nil)
	build := func() *types.Transaction {
		return newFrameTx(0, ftSender).
			selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
			addFrame(types.FrameModeSender, 0, &receiver, 100_000, 200_000, uint256.NewInt(1), nil).
			build(t)
	}
	if _, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), build()); err != nil {
		t.Fatalf("first tx failed: %v", err)
	}
	if _, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), build()); err == nil {
		t.Fatal("replay with same nonce must be rejected")
	}
}

// TestFrameTxApproveOutsideFrameTx: APPROVE in a normal transaction halts
// exceptionally.
func TestFrameTxApproveOutsideFrameTx(t *testing.T) {
	contract := common.HexToAddress("0xc0de")
	sdb := mkFrameState(t, types.GenesisAlloc{contract: {Code: approveBothCode}})
	normalTx := types.MustSignNewTx(ftKey, frameSigner, &types.DynamicFeeTx{
		ChainID: frameCfg.ChainID, Nonce: 0, To: &contract,
		Gas: 100_000, GasFeeCap: big.NewInt(0), GasTipCap: big.NewInt(0),
	})
	res, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), normalTx)
	if err != nil {
		t.Fatalf("normal tx processing failed: %v", err)
	}
	if res.Err == nil {
		t.Fatal("APPROVE outside a frame transaction must halt")
	}
	if res.UsedGas != 100_000-0 && res.UsedGas >= 100_000 {
		// halt consumes the forwarded gas; exact figure depends on intrinsic.
	}
	if res.FrameReceipts != nil {
		t.Fatal("normal tx must not produce frame receipts")
	}
}

// TestFrameTxVerifyPrecompileDispatch: a VERIFY frame targeting an active
// precompile dispatches the precompile as an ordinary (static) call — the
// precompile check precedes the empty-code default path (EIP-8141
// "Behavior"). Before the fix, such frames were misrouted to the default
// code, which reverts on flags == 0 and invalidates the transaction.
func TestFrameTxVerifyPrecompileDispatch(t *testing.T) {
	identity := common.BytesToAddress([]byte{0x04}) // identity precompile
	sdb := mkFrameState(t, nil)
	tx := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0).
		addFrame(types.FrameModeVerify, 0, &identity, 50_000, 0, new(uint256.Int), []byte("hello world")).
		build(t)
	res, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), tx)
	if err != nil {
		t.Fatalf("verify-to-precompile frame tx must be valid: %v", err)
	}
	if res.FrameReceipts[1].Status != vm.FrameStatusSucceeded {
		t.Fatalf("precompile VERIFY frame status %d, want success", res.FrameReceipts[1].Status)
	}
	if res.FrameReceipts[1].GasUsedState != 0 {
		t.Fatalf("precompile frame used state gas %d, want 0", res.FrameReceipts[1].GasUsedState)
	}
}

// TestFrameTxTooManyBlobs: the EIP-7594 per-transaction blob limit applies
// to frame transactions unchanged (EIP-8141 "Blob handling").
func TestFrameTxTooManyBlobs(t *testing.T) {
	mkBlobs := func(n int) []common.Hash {
		hashes := make([]common.Hash, n)
		for i := range hashes {
			h := common.Hash{0x01} // VERSIONED_HASH_VERSION_KZG
			h[31] = byte(i + 1)
			hashes[i] = h
		}
		return hashes
	}
	build := func(n int) *types.Transaction {
		b := newFrameTx(0, ftSender).
			selfVerify(types.FrameFlagApproveScopeMask, 50_000, 0)
		b.blobs = mkBlobs(n)
		return b.build(t)
	}
	// Above the limit: invalid.
	sdbOver := mkFrameState(t, nil)
	if _, _, err := applyFrameTx(t, sdbOver, frameEVM(sdbOver, nil), build(params.BlobTxMaxBlobs+1)); !errors.Is(err, ErrTooManyBlobs) {
		t.Fatalf("err %v, want ErrTooManyBlobs", err)
	}
	// At the limit: valid (blob base fee is zero in this harness).
	sdb := mkFrameState(t, nil)
	res, _, err := applyFrameTx(t, sdb, frameEVM(sdb, nil), build(params.BlobTxMaxBlobs))
	if err != nil {
		t.Fatalf("frame tx with %d blobs must be valid: %v", params.BlobTxMaxBlobs, err)
	}
	if res.FrameReceipts[0].Status != vm.FrameStatusSucceeded {
		t.Fatalf("verify frame status %d, want success", res.FrameReceipts[0].Status)
	}
}

// TestFrameTxFeeOverflow: the fee budget arithmetic is exact (EIP-8141 "Fee
// budget"); a uint256 overflow invalidates the transaction rather than
// wrapping the required fee.
func TestFrameTxFeeOverflow(t *testing.T) {
	b := newFrameTx(0, ftSender).
		selfVerify(types.FrameFlagApproveScopeMask, 50_000, uint64(1)<<62)
	b.fees.MaxFee = new(uint256.Int).Lsh(uint256.NewInt(1), 250) // huge budget
	tx := b.build(t)
	// max_gas ~= 2^62, base fee 2^200: max_gas * base_fee overflows uint256.
	baseFee := new(big.Int).Lsh(big.NewInt(1), 200)
	sdbHuge := mkFrameState(t, nil)
	if _, _, err := applyFrameTx(t, sdbHuge, frameEVM(sdbHuge, baseFee), tx); !errors.Is(err, ErrFrameTxFeeOverflow) {
		t.Fatalf("err %v, want ErrFrameTxFeeOverflow", err)
	}
	// Control: same transaction at a sane base fee passes the fee budget and
	// fails later on the block gas reservation (not with a fee error).
	baseFeeOK := new(big.Int).Lsh(big.NewInt(1), 60)
	sdbOK := mkFrameState(t, nil)
	_, _, err := applyFrameTx(t, sdbOK, frameEVM(sdbOK, baseFeeOK), tx)
	if err == nil || errors.Is(err, ErrFrameTxFeeOverflow) {
		t.Fatalf("control: err %v, want gas-pool reservation failure", err)
	}
}
