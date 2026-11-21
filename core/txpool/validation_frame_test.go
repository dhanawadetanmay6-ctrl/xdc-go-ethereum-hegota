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

// Pool-admission tests for EIP-8141 frame transactions.

package txpool

import (
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

var (
	framePoolKey, _ = crypto.HexToECDSA("45a915e4d060149eb4365960e6a7a45f334393093061116b197e3240065ff2d8")
	framePoolSender = crypto.PubkeyToAddress(framePoolKey.PublicKey)
)

// framePoolChainConfig returns a test config with Amsterdam+Bogota active.
func framePoolChainConfig() *params.ChainConfig {
	cfg := *params.MergedTestChainConfig
	cfg.AmsterdamTime = new(uint64)
	cfg.BogotaTime = new(uint64)
	return &cfg
}

// framePoolHead returns a head header on which Bogota rules are active.
func framePoolHead(cfg *params.ChainConfig) *types.Header {
	return &types.Header{
		Number:     big.NewInt(1),
		GasLimit:   30_000_000,
		Time:       1,
		Difficulty: big.NewInt(0), // post-merge
		BaseFee:    big.NewInt(1_000_000_000),
	}
}

// mkPoolFrameTx builds a signed, valid self-verify+transfer frame tx.
func mkPoolFrameTx(t *testing.T, cfg *params.ChainConfig, nonce uint64, fee *uint256.Int) *types.Transaction {
	t.Helper()
	receiver := common.HexToAddress("0xbeef")
	ft := &types.FrameTx{
		ChainID: uint256.MustFromBig(cfg.ChainID),
		Nonce:   nonce,
		Sender:  framePoolSender,
		Frames: []types.Frame{
			{Mode: types.FrameModeVerify, Flags: types.FrameFlagApproveScopeMask,
				Limits: types.FrameGasLimits{Execution: 50_000}, Value: new(uint256.Int)},
			{Mode: types.FrameModeSender, Target: &receiver,
				Limits: types.FrameGasLimits{Execution: 100_000, State: 200_000}, Value: uint256.NewInt(1)},
		},
		Signatures: []types.FrameSignature{{Scheme: types.FrameSigSchemeSecp256k1, Signature: make([]byte, 65)}},
		Fees: types.FrameFees{
			MaxFee:               fee,
			MaxPriorityFeePerGas: uint256.NewInt(1_000_000_000),
		},
	}
	tx := types.NewTx(ft)
	signed, err := types.SignFrameTx(framePoolKey, tx)
	if err != nil {
		t.Fatalf("SignFrameTx: %v", err)
	}
	return signed
}

func framePoolOpts(cfg *params.ChainConfig) *ValidationOptions {
	return &ValidationOptions{
		Config:       cfg,
		Accept:       0xFF,
		MaxSize:      128 * 1024,
		MaxBlobCount: 6,
		MinTip:       big.NewInt(0),
	}
}

func TestValidateFrameTransaction(t *testing.T) {
	cfg := framePoolChainConfig()
	head := framePoolHead(cfg)
	signer := types.LatestSigner(cfg)

	// Positive: valid frame tx is admitted.
	maxGas := func(tx *types.Transaction) uint64 { return tx.FrameTxData().MaxGas() }
	good := mkPoolFrameTx(t, cfg, 0, uint256.NewInt(1e15))
	if err := ValidateTransaction(good, head, signer, framePoolOpts(cfg)); err != nil {
		t.Fatalf("valid frame tx rejected: %v", err)
	}
	_ = maxGas

	// Negative: Bogota not active.
	cfgNoBogota := *cfg
	cfgNoBogota.BogotaTime = nil
	if err := ValidateTransaction(good, head, signer, framePoolOpts(&cfgNoBogota)); !strings.Contains(err.Error(), "not yet in Bogota") {
		t.Fatalf("pre-Bogota: got %v", err)
	}

	// Negative: wrong chain id.
	bad := mkPoolFrameTx(t, cfg, 0, uint256.NewInt(1e15))
	bad.FrameTxData().ChainID = uint256.NewInt(999_999)
	// chain id changed after signing: signature invalid too, but chain check fires first
	if err := ValidateTransaction(bad, head, signer, framePoolOpts(cfg)); err == nil {
		t.Fatal("wrong chain id accepted")
	}

	// Negative: static violation (VERIFY after batch flag).
	staticBad := mkPoolFrameTx(t, cfg, 0, uint256.NewInt(1e15))
	ftx := staticBad.FrameTxData()
	ftx.Frames[0].Flags |= types.FrameFlagAtomicBatch // VERIFY with batch flag: invalid
	if err := ValidateTransaction(staticBad, head, signer, framePoolOpts(cfg)); err == nil {
		t.Fatal("static-invalid frame tx accepted")
	}

	// Negative: broken signature (flip a byte; sighash unchanged as length is fixed).
	sigBad := mkPoolFrameTx(t, cfg, 0, uint256.NewInt(1e15))
	sigBad.FrameTxData().Signatures[0].Signature[10] ^= 0xff
	if err := ValidateTransaction(sigBad, head, signer, framePoolOpts(cfg)); err == nil {
		t.Fatal("bad-signature frame tx accepted")
	}

	// Negative: blob-carrying frame tx (no blobpool support).
	blobbed := mkPoolFrameTx(t, cfg, 0, uint256.NewInt(1e15))
	blobbed.FrameTxData().BlobHashes = []common.Hash{common.HexToHash("0x01")}
	if err := ValidateTransaction(blobbed, head, signer, framePoolOpts(cfg)); err == nil || !strings.Contains(err.Error(), "blob") {
		t.Fatalf("blob-carrying frame tx: got %v", err)
	}

	// Negative: execution reservation exceeds block gas limit.
	lowHead := &types.Header{Number: big.NewInt(1), GasLimit: 1000, Time: 1, Difficulty: big.NewInt(0), BaseFee: big.NewInt(1)}
	if err := ValidateTransaction(good, lowHead, signer, framePoolOpts(cfg)); err == nil {
		t.Fatal("oversized reservation accepted")
	}

	// Negative: minimum tip not met.
	tipOpts := framePoolOpts(cfg)
	tipOpts.MinTip = big.NewInt(10_000_000_000) // 10 gwei > tx's 1 gwei
	if err := ValidateTransaction(good, head, signer, tipOpts); err == nil {
		t.Fatal("sub-minimum-tip frame tx accepted")
	}
}

func TestValidateFrameTransactionWithState(t *testing.T) {
	cfg := framePoolChainConfig()
	signer := types.LatestSigner(cfg)

	mkState := func(nonce uint64, balance *uint256.Int) *state.StateDB {
		sdb, _ := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
		sdb.CreateAccount(framePoolSender)
		if balance != nil {
			sdb.AddBalance(framePoolSender, balance, tracing.BalanceChangeUnspecified)
		}
		if nonce != 0 {
			sdb.SetNonce(framePoolSender, nonce, tracing.NonceChangeGenesis)
		}
		sdb.Finalise(true)
		return sdb
	}
	// Positive: funds cover maxFee + value.
	opts := ValidationOptionsWithState{
		State:               mkState(0, uint256.NewInt(1e18)),
		ExistingExpenditure: func(addr common.Address) *big.Int { return new(big.Int) },
		ExistingCost:        func(addr common.Address, nonce uint64) *big.Int { return nil },
	}
	good := mkPoolFrameTx(t, cfg, 0, uint256.NewInt(1e15))
	if err := ValidateTransactionWithState(good, signer, &opts); err != nil {
		t.Fatalf("stateful validation rejected funded frame tx: %v", err)
	}
	// Negative: maxFee + value beyond balance.
	opts2 := opts
	opts2.State = mkState(0, uint256.NewInt(1e18))
	poor := mkPoolFrameTx(t, cfg, 0, uint256.NewInt(2e18))
	if err := ValidateTransactionWithState(poor, signer, &opts2); err == nil {
		t.Fatal("unfunded frame tx accepted")
	}
	// Negative: nonce too low (state nonce ahead).
	opts3 := opts
	opts3.State = mkState(5, uint256.NewInt(1e18))
	if err := ValidateTransactionWithState(good, signer, &opts3); err == nil {
		t.Fatal("stale-nonce frame tx accepted")
	}
}

// TestValidateFrameTransactionFloorReservation: the execution-dimension block
// reservation is max(intrinsic + sum(execution), calldata_floor_gas)
// (EIP-8141 "Block-level gas accounting"). A transaction whose calldata floor
// exceeds the block gas limit is inadmissible even when the intrinsic plus
// execution sum fits.
func TestValidateFrameTransactionFloorReservation(t *testing.T) {
	cfg := framePoolChainConfig()
	signer := types.LatestSigner(cfg)

	receiver := common.HexToAddress("0xbeef")
	bigData := make([]byte, 4000) // all-zero: 64 gas/byte at the floor
	ft := &types.FrameTx{
		ChainID: uint256.MustFromBig(cfg.ChainID),
		Nonce:   0,
		Sender:  framePoolSender,
		Frames: []types.Frame{
			{Mode: types.FrameModeVerify, Flags: types.FrameFlagApproveScopeMask,
				Limits: types.FrameGasLimits{Execution: 50_000}, Value: new(uint256.Int)},
			{Mode: types.FrameModeSender, Target: &receiver,
				Limits: types.FrameGasLimits{Execution: 10_000}, Value: new(uint256.Int), Data: bigData},
		},
		Signatures: []types.FrameSignature{{Scheme: types.FrameSigSchemeSecp256k1, Signature: make([]byte, 65)}},
		Fees: types.FrameFees{
			MaxFee:               uint256.NewInt(1e15),
			MaxPriorityFeePerGas: uint256.NewInt(1_000_000_000),
		},
	}
	tx, err := types.SignFrameTx(framePoolKey, types.NewTx(ft))
	if err != nil {
		t.Fatalf("SignFrameTx: %v", err)
	}
	intrinsic, floor, err := tx.FrameTxData().IntrinsicGas()
	if err != nil {
		t.Fatal(err)
	}
	// The block limit sits between the two reservation candidates: the
	// intrinsic+execution sum fits, the calldata floor does not.
	execSum := intrinsic + 60_000
	if !(execSum <= 200_000 && floor > 200_000) {
		t.Fatalf("test setup: intrinsic+exec %d, floor %d", execSum, floor)
	}
	head := &types.Header{Number: big.NewInt(1), GasLimit: 200_000, Time: 1, Difficulty: big.NewInt(0), BaseFee: big.NewInt(1)}
	if err := ValidateTransaction(tx, head, signer, framePoolOpts(cfg)); err == nil {
		t.Fatal("floor-dominated frame tx admitted below its calldata floor")
	}
}
