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

// Validation-prefix grammar and EIP-8272 public-mempool policy tests.

package txpool

import (
	"errors"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"
)

// --- Frame fixtures ---------------------------------------------------------

func rrData(n int) []byte { return make([]byte, n*types.RecentRootTupleBytes) }

func rrFrame(exec uint64) types.Frame {
	return types.Frame{
		Mode:   types.FrameModeVerify,
		Flags:  0,
		Target: &[]common.Address{types.RecentRootAddress}[0],
		Limits: types.FrameGasLimits{Execution: exec},
		Value:  new(uint256.Int),
		Data:   rrData(1),
	}
}

func expiryFrame() types.Frame {
	return types.Frame{
		Mode:   types.FrameModeVerify,
		Flags:  0,
		Target: &[]common.Address{types.ExpiryVerifierAddress}[0],
		Limits: types.FrameGasLimits{Execution: 1000},
		Value:  new(uint256.Int),
		Data:   make([]byte, types.ExpiryDataLength),
	}
}

func selfVerifyFrame(sender common.Address) types.Frame {
	return types.Frame{
		Mode:   types.FrameModeVerify,
		Flags:  types.FrameFlagApproveScopeMask,
		Limits: types.FrameGasLimits{Execution: 30_000},
		Value:  new(uint256.Int),
	}
}

func onlyVerifyFrame(sender common.Address) types.Frame {
	return types.Frame{
		Mode:   types.FrameModeVerify,
		Flags:  types.FrameFlagApproveExecution,
		Limits: types.FrameGasLimits{Execution: 30_000},
		Value:  new(uint256.Int),
	}
}

func payFrame() types.Frame {
	return types.Frame{
		Mode:   types.FrameModeVerify,
		Flags:  types.FrameFlagApprovePayment,
		Target: &[]common.Address{common.HexToAddress("0xpay")}[0],
		Limits: types.FrameGasLimits{Execution: 30_000},
		Value:  new(uint256.Int),
	}
}

func deployFrame() types.Frame {
	return types.Frame{
		Mode:   types.FrameModeDefault,
		Flags:  0,
		Target: &[]common.Address{common.HexToAddress("0xfac70")}[0],
		Limits: types.FrameGasLimits{Execution: 30_000},
		Value:  new(uint256.Int),
	}
}

func userOpFrame() types.Frame {
	return types.Frame{
		Mode:   types.FrameModeSender,
		Flags:  0,
		Target: &[]common.Address{common.HexToAddress("0xbeef")}[0],
		Limits: types.FrameGasLimits{Execution: 21_000},
		Value:  uint256.NewInt(1),
	}
}

func mkFT(frames ...types.Frame) *types.FrameTx {
	return &types.FrameTx{
		ChainID: uint256.NewInt(1),
		Sender:  framePoolSender,
		Frames:  frames,
		Signatures: []types.FrameSignature{{
			Scheme:    types.FrameSigSchemeSecp256k1,
			Signature: make([]byte, 65),
		}},
		Fees: types.FrameFees{MaxFee: uint256.NewInt(1e15), MaxPriorityFeePerGas: uint256.NewInt(1)},
	}
}

// --- Shape matrix: 4 prefixes × {none, rr, expiry, expiry+rr} ----------------

func TestClassifyFrameValidationPrefix(t *testing.T) {
	self := selfVerifyFrame(framePoolSender)
	only := onlyVerifyFrame(framePoolSender)
	pay := payFrame()
	dep := deployFrame()
	rr := rrFrame(5000)
	exp := expiryFrame()

	shapes := []struct {
		name    string
		body    []types.Frame
		want    FramePrefixShape
		bodyLen int
	}{
		{"self_verify", []types.Frame{self}, FramePrefixSelfVerify, 1},
		{"deploy+self_verify", []types.Frame{dep, self}, FramePrefixDeploySelfVerify, 2},
		{"only_verify+pay", []types.Frame{only, pay}, FramePrefixOnlyVerifyPay, 2},
		{"deploy+only_verify+pay", []types.Frame{dep, only, pay}, FramePrefixDeployOnlyVerifyPay, 3},
	}
	for _, s := range shapes {
		for _, variant := range []struct {
			name   string
			prefix []types.Frame
			expIdx int
			rrIdx  int
		}{
			{"none", nil, -1, -1},
			{"rr", []types.Frame{rr}, -1, 0},
			{"expiry", []types.Frame{exp}, 0, -1},
			{"expiry+rr", []types.Frame{exp, rr}, 0, 1},
		} {
			t.Run(s.name+"/"+variant.name, func(t *testing.T) {
				frames := append(append([]types.Frame{}, variant.prefix...), s.body...)
				ft := mkFT(frames...)
				shape, prefixLen, expIdx, rrIdx := ClassifyFrameValidationPrefix(ft)
				if shape != s.want {
					t.Fatalf("shape = %v, want %v", shape, s.want)
				}
				if prefixLen != len(variant.prefix)+s.bodyLen {
					t.Fatalf("prefixLen = %d, want %d", prefixLen, len(variant.prefix)+s.bodyLen)
				}
				if expIdx != variant.expIdx || rrIdx != variant.rrIdx {
					t.Fatalf("expiry/rr indexes = %d/%d, want %d/%d", expIdx, rrIdx, variant.expIdx, variant.rrIdx)
				}
				if err := ValidateFrameRecentRootPolicy(mkFT(append(append([]types.Frame{}, variant.prefix...), s.body...)...)); variant.rrIdx >= 0 && err != nil {
					t.Fatalf("policy rejected valid %s: %v", variant.name, err)
				}
			})
		}
	}
}

// --- Structural rejections ---------------------------------------------------

func TestValidateFrameRecentRootPolicyRejections(t *testing.T) {
	self := selfVerifyFrame(framePoolSender)
	rr := rrFrame(5000)
	exp := expiryFrame()

	cases := []struct {
		name   string
		frames []types.Frame
		want   error
	}{
		// Reversed protocol verifiers: the rr frame is consumed at index 0,
		// leaving [expiry, self_verify] which matches no recognized prefix.
		// Either policy rejection is correct; the position check fires when
		// the rr frame is not at the skip position.
		{"reversed protocol verifiers", []types.Frame{rr, exp, self}, nil},
		{"duplicate recent root", []types.Frame{rr, rr, self}, ErrRecentRootFrameMultiple},
		{"recent root after self_verify", []types.Frame{self, rr}, ErrRecentRootFramePosition},
		{"recent root after deploy", []types.Frame{deployFrame(), rr, self}, ErrRecentRootFramePosition},
		{"recent root alone", []types.Frame{rr}, ErrFramePrefixUnrecognizedPrefix},
		{"recent root + user_op only", []types.Frame{rr, userOpFrame()}, ErrFramePrefixUnrecognizedPrefix},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateFrameRecentRootPolicy(mkFT(c.frames...))
			if err == nil {
				t.Fatal("accepted, want rejection")
			}
			if c.want != nil && !errors.Is(err, c.want) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
		})
	}

	// Malformed VERIFY frames to RECENT_ROOT_ADDRESS (not recent_root_verify).
	malformed := []struct {
		name   string
		mutate func(f *types.Frame)
	}{
		{"flags=1", func(f *types.Frame) { f.Flags = 1 }},
		{"flags=3 (self_verify shape)", func(f *types.Frame) { f.Flags = types.FrameFlagApproveScopeMask }},
		{"nonzero value", func(f *types.Frame) { f.Value = uint256.NewInt(1) }},
		{"nonzero state limit", func(f *types.Frame) { f.Limits.State = 1 }},
		{"71-byte data", func(f *types.Frame) { f.Data = make([]byte, 71) }},
		{"73-byte data", func(f *types.Frame) { f.Data = make([]byte, 73) }},
		{"17 tuples", func(f *types.Frame) { f.Data = rrData(17) }},
		{"empty data", func(f *types.Frame) { f.Data = nil }},
		{"64-byte write encoding", func(f *types.Frame) { f.Data = make([]byte, 64) }},
	}
	for _, m := range malformed {
		t.Run("malformed/"+m.name, func(t *testing.T) {
			f := rrFrame(5000)
			m.mutate(&f)
			err := ValidateFrameRecentRootPolicy(mkFT(f, self))
			if !errors.Is(err, ErrRecentRootFrameMalformed) {
				t.Fatalf("got %v, want ErrRecentRootFrameMalformed", err)
			}
		})
	}

	// VERIFY frame after the validation prefix.
	t.Run("VERIFY after prefix", func(t *testing.T) {
		tail := rrFrame(5000)
		tail.Target = &[]common.Address{common.HexToAddress("0x1234")}[0] // not RR, still VERIFY
		err := ValidateFrameRecentRootPolicy(mkFT(rr, self, tail))
		if !errors.Is(err, ErrFramePrefixVerifyAfterPrefix) {
			t.Fatalf("got %v", err)
		}
	})
	// Atomic batch flag inside the validation prefix.
	t.Run("batch in prefix", func(t *testing.T) {
		batched := selfVerifyFrame(framePoolSender)
		batched.Flags |= types.FrameFlagAtomicBatch
		// note: flags 0x7 != 0x3 breaks self_verify shape too; use rr-frame-free
		// variant to isolate the batch rule: batch flag on the rr frame itself
		// breaks its shape first. Put batch on a trailing deploy-shaped frame
		// inside prefix instead.
		_ = batched
		dep := deployFrame()
		dep.Flags = types.FrameFlagAtomicBatch
		// [rr, deploy(batch), self]: batch on deploy inside prefix. The batch
		// flag also violates deploy's flags==0 shape → unrecognized wins over
		// batch. Accept either policy error, but it must reject.
		if err := ValidateFrameRecentRootPolicy(mkFT(rr, dep, self)); err == nil {
			t.Fatal("batch in prefix accepted")
		}
	})
}

// --- MAX_VERIFY_GAS / MAX_VERIFY_STATE_GAS boundaries -------------------------

func TestValidateFrameRecentRootGasBudgets(t *testing.T) {
	// secp256k1 signature cost.
	sigCost := uint64(types.FrameSigGasSecp256k1) // 2800

	// Exactly at MAX_VERIFY_GAS: sig + rr + self = 100_000.
	atLimit := func(deltaExec int64) *types.FrameTx {
		rr := rrFrame(10_000)
		self := selfVerifyFrame(framePoolSender)
		self.Limits.Execution = uint64(int64(MaxVerifyGas-sigCost) - int64(rr.Limits.Execution) + deltaExec)
		return mkFT(rr, self)
	}
	if err := ValidateFrameRecentRootPolicy(atLimit(0)); err != nil {
		t.Fatalf("MAX_VERIFY_GAS exact boundary rejected: %v", err)
	}
	if err := ValidateFrameRecentRootPolicy(atLimit(1)); !errors.Is(err, ErrFramePrefixVerifyGas) {
		t.Fatalf("MAX_VERIFY_GAS+1: got %v", err)
	}

	// MAX_VERIFY_STATE_GAS: state gas on the self_verify frame (allowed for
	// APPROVE account creation), exactly at and over the cap.
	atStateLimit := func(delta int64) *types.FrameTx {
		rr := rrFrame(10_000)
		self := selfVerifyFrame(framePoolSender)
		self.Limits.State = uint64(int64(MaxVerifyStateGas) + delta)
		return mkFT(rr, self)
	}
	if err := ValidateFrameRecentRootPolicy(atStateLimit(0)); err != nil {
		t.Fatalf("MAX_VERIFY_STATE_GAS exact boundary rejected: %v", err)
	}
	if err := ValidateFrameRecentRootPolicy(atStateLimit(1)); !errors.Is(err, ErrFramePrefixVerifyStateGas) {
		t.Fatalf("MAX_VERIFY_STATE_GAS+1: got %v", err)
	}

	// The recent root frame's own limits.execution counts toward
	// MAX_VERIFY_GAS: an oversized rr frame alone can blow the budget.
	rr := rrFrame(MaxVerifyGas - sigCost + 1)
	if err := ValidateFrameRecentRootPolicy(mkFT(rr, selfVerifyFrame(framePoolSender))); !errors.Is(err, ErrFramePrefixVerifyGas) {
		t.Fatalf("rr frame counting: got %v", err)
	}
}

// --- Stateless-before-stateful ordering --------------------------------------

// TestRecentRootPolicyRunsBeforeSenderState proves the structural policy is
// evaluated by the stateless admission path: a malformed recent-root frame tx
// is rejected by ValidateTransaction (no state access anywhere in that call).
func TestRecentRootPolicyRunsBeforeSenderState(t *testing.T) {
	cfg := framePoolChainConfig()
	head := framePoolHead(cfg)
	signer := types.LatestSigner(cfg)

	// Malformed (flags=1) VERIFY to RECENT_ROOT_ADDRESS; everything else valid.
	bad := rrFrame(5000)
	bad.Flags = 1
	ft := mkFT(bad, selfVerifyFrame(framePoolSender))
	tx := types.NewTx(ft)
	signed, err := types.SignFrameTx(framePoolKey, tx)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	err = ValidateTransaction(signed, head, signer, framePoolOpts(cfg))
	if !errors.Is(err, ErrRecentRootFrameMalformed) {
		t.Fatalf("got %v, want ErrRecentRootFrameMalformed", err)
	}

	// A canonical recent-root frame tx passes the stateless gate.
	good, err := types.SignFrameTx(framePoolKey, types.NewTx(mkFT(rrFrame(5000), selfVerifyFrame(framePoolSender), userOpFrame())))
	if err != nil {
		t.Fatalf("sign good: %v", err)
	}
	if err := ValidateTransaction(good, head, signer, framePoolOpts(cfg)); err != nil {
		t.Fatalf("valid recent-root frame tx rejected statelessly: %v", err)
	}
	// Sanity: error text must not depend on state contents.
	if strings.Contains(errString(ValidateTransaction(signed, head, signer, framePoolOpts(cfg))), "balance") {
		t.Fatal("stateful check fired before structural policy")
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
