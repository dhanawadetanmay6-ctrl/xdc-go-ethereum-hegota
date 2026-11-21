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

// Static validity tests for the EIP-7906 ASSERT frame mode: mode acceptance,
// the contiguous-trailing-suffix rule, and the atomic-batch flag interactions.

package types

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
)

func TestFrameTxValidateStaticAssert(t *testing.T) {
	target := common.HexToAddress("0x000000000000000000000000000000000000dEaD")
	assertFrame := func() Frame {
		return Frame{Mode: FrameModeAssert, Flags: 0, Target: &target,
			Limits: FrameGasLimits{Execution: 50_000, State: 0}, Value: new(uint256.Int)}
	}

	tests := []struct {
		name    string
		mutate  func(*FrameTx)
		wantErr error
	}{
		// ASSERT mode is an accepted mode value (EIP-7906 amends the
		// EIP-8141 static mode constraint).
		{"assert mode accepted", func(tx *FrameTx) {
			tx.Frames = append(tx.Frames, assertFrame())
		}, nil},
		{"assert suffix of two", func(tx *FrameTx) {
			tx.Frames = append(tx.Frames, assertFrame(), assertFrame())
		}, nil},
		// Once any frame has mode ASSERT, every subsequent frame must too.
		{"assert not suffix", func(tx *FrameTx) {
			// ASSERT, SENDER, VERIFY — frames after an ASSERT frame are not ASSERT.
			tx.Frames = []Frame{assertFrame(), tx.Frames[1], tx.Frames[0]}
		}, ErrFrameTxAssertNotSuffix},
		{"assert then verify", func(tx *FrameTx) {
			// VERIFY, ASSERT, SENDER — SENDER after ASSERT violates the suffix rule.
			tx.Frames = []Frame{tx.Frames[0], assertFrame(), tx.Frames[1]}
		}, ErrFrameTxAssertNotSuffix},
		// The atomic batch flag is invalid on ASSERT frames and on a frame
		// directly before an ASSERT frame.
		{"batch flag on assert", func(tx *FrameTx) {
			f := assertFrame()
			f.Flags = FrameFlagAtomicBatch
			tx.Frames = append(tx.Frames, f, assertFrame())
		}, ErrFrameTxBatchAssert},
		{"batch flag before assert", func(tx *FrameTx) {
			tx.Frames[1].Flags |= FrameFlagAtomicBatch // SENDER frame flagged, followed by ASSERT
			tx.Frames = append(tx.Frames, assertFrame())
		}, ErrFrameTxBatchAssertNext},
		// Approval-scope flags are valid (but ineffective) on ASSERT frames.
		{"approval scope on assert", func(tx *FrameTx) {
			f := assertFrame()
			f.Flags = FrameFlagApprovePayment
			tx.Frames = append(tx.Frames, f)
		}, nil},
		// Value remains SENDER-only.
		{"value on assert", func(tx *FrameTx) {
			f := assertFrame()
			f.Value = uint256.NewInt(1)
			tx.Frames = append(tx.Frames, f)
		}, ErrFrameTxValueNotSender},
		// An ASSERT frame directly preceded by an unflagged batch terminator
		// is fine: the batch flag itself is what is restricted.
		{"batch closed before assert", func(tx *FrameTx) {
			second := Frame{Mode: FrameModeSender, Flags: FrameFlagAtomicBatch, Target: &target,
				Limits: FrameGasLimits{Execution: 21000, State: 0}, Value: new(uint256.Int)}
			third := Frame{Mode: FrameModeDefault, Flags: 0, Target: &target,
				Limits: FrameGasLimits{Execution: 21000, State: 0}, Value: new(uint256.Int)}
			tx.Frames = []Frame{tx.Frames[0], second, third, assertFrame()}
		}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tx := testFrameTx()
			tc.mutate(tx)
			err := tx.ValidateStatic()
			if tc.wantErr == nil && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantErr != nil && !errorsIs(err, tc.wantErr) {
				t.Fatalf("got error %v, want %v", err, tc.wantErr)
			}
		})
	}
}
