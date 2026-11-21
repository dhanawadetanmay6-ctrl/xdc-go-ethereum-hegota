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

package txpool

import (
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/core/types"
)

// Public-mempool validation budget constants (EIP-8141 "Constants").
const (
	// MaxVerifyGas bounds the work a node expends validating signatures and
	// simulating the validation prefix of one frame transaction
	// (MAX_VERIFY_GAS).
	MaxVerifyGas = 100_000
	// MaxVerifyStateGas bounds the state gas budgeted across the validation
	// prefix (MAX_VERIFY_STATE_GAS).
	MaxVerifyStateGas = 500_000
)

// FramePrefixShape identifies one of the four validation prefixes recognized
// by the EIP-8141 public mempool policy.
type FramePrefixShape int

const (
	// FramePrefixUnrecognized marks a frame sequence matching none of the
	// recognized validation prefixes.
	FramePrefixUnrecognized FramePrefixShape = iota
	// FramePrefixSelfVerify is [self_verify].
	FramePrefixSelfVerify
	// FramePrefixDeploySelfVerify is [deploy, self_verify].
	FramePrefixDeploySelfVerify
	// FramePrefixOnlyVerifyPay is [only_verify, pay].
	FramePrefixOnlyVerifyPay
	// FramePrefixDeployOnlyVerifyPay is [deploy, only_verify, pay].
	FramePrefixDeployOnlyVerifyPay
)

func (s FramePrefixShape) String() string {
	switch s {
	case FramePrefixSelfVerify:
		return "self_verify"
	case FramePrefixDeploySelfVerify:
		return "deploy+self_verify"
	case FramePrefixOnlyVerifyPay:
		return "only_verify+pay"
	case FramePrefixDeployOnlyVerifyPay:
		return "deploy+only_verify+pay"
	default:
		return "unrecognized"
	}
}

// Errors returned by the frame-transaction public-mempool policy.
var (
	ErrFramePrefixUnrecognizedPrefix = errors.New("validation prefix is not one of the four recognized prefixes")
	ErrFramePrefixVerifyAfterPrefix  = errors.New("VERIFY frame after validation prefix")
	ErrFramePrefixBatchInPrefix      = errors.New("atomic batch flag in validation prefix")
	ErrFramePrefixVerifyGas          = errors.New("validation prefix exceeds MAX_VERIFY_GAS")
	ErrFramePrefixVerifyStateGas     = errors.New("validation prefix exceeds MAX_VERIFY_STATE_GAS")
	ErrRecentRootFrameMultiple       = errors.New("multiple recent root verifier frames")
	ErrRecentRootFramePosition       = errors.New("recent root verifier frame not immediately after optional expiry verifier")
	ErrRecentRootFrameMalformed      = errors.New("VERIFY frame to RECENT_ROOT_ADDRESS does not match the canonical shape")
)

// ClassifyFrameValidationPrefix determines the validation prefix of a frame
// transaction under the EIP-8141 public-mempool grammar, applying EIP-8272's
// skip semantics: an optional leading expiry_verify frame and an optional
// recent_root_verify frame are skipped before matching the four recognized
// shapes (the deploy-first rule applies after the skips).
//
// It returns the matched shape, the prefix length (frames[0:prefixLen],
// including any skipped protocol verifier frames — they precede the frame
// that sets the payer), the index of the expiry verifier frame (-1), and the
// index of the recent root verifier frame (-1).
func ClassifyFrameValidationPrefix(ft *types.FrameTx) (FramePrefixShape, int, int, int) {
	frames := ft.Frames
	i := 0
	expiryIdx, rrIdx := -1, -1
	if i < len(frames) && types.IsExpiryVerifyFrame(&frames[i]) {
		expiryIdx = i
		i++
	}
	if i < len(frames) && types.IsRecentRootVerifyFrame(&frames[i]) {
		rrIdx = i
		i++
	}
	rest := frames[i:]
	is := func(pred func(*types.Frame) bool, k int) bool {
		return k < len(rest) && pred(&rest[k])
	}
	senderTarget := func(f *types.Frame) bool { return f.Target == nil || *f.Target == ft.Sender }
	selfVerify := func(f *types.Frame) bool {
		return f.Mode == types.FrameModeVerify && f.Flags == types.FrameFlagApproveScopeMask && senderTarget(f)
	}
	onlyVerify := func(f *types.Frame) bool {
		return f.Mode == types.FrameModeVerify && f.Flags == types.FrameFlagApproveExecution && senderTarget(f)
	}
	pay := func(f *types.Frame) bool {
		return f.Mode == types.FrameModeVerify && f.Flags == types.FrameFlagApprovePayment
	}
	deploy := func(f *types.Frame) bool {
		return f.Mode == types.FrameModeDefault && f.Flags == 0
	}
	switch {
	case is(selfVerify, 0):
		return FramePrefixSelfVerify, i + 1, expiryIdx, rrIdx
	case is(deploy, 0) && is(selfVerify, 1):
		return FramePrefixDeploySelfVerify, i + 2, expiryIdx, rrIdx
	case is(onlyVerify, 0) && is(pay, 1):
		return FramePrefixOnlyVerifyPay, i + 2, expiryIdx, rrIdx
	case is(deploy, 0) && is(onlyVerify, 1) && is(pay, 2):
		return FramePrefixDeployOnlyVerifyPay, i + 3, expiryIdx, rrIdx
	}
	return FramePrefixUnrecognized, 0, expiryIdx, rrIdx
}

// frameTxHasRecentRootTarget reports whether any frame is a VERIFY frame
// targeting RECENT_ROOT_ADDRESS (whether or not it matches the canonical
// recent root verifier shape).
func frameTxHasRecentRootTarget(ft *types.FrameTx) bool {
	for i := range ft.Frames {
		f := &ft.Frames[i]
		if f.Mode == types.FrameModeVerify && f.Target != nil && *f.Target == types.RecentRootAddress {
			return true
		}
	}
	return false
}

// ValidateFrameRecentRootPolicy enforces the EIP-8272 public-mempool rules
// for frame transactions containing a VERIFY frame to RECENT_ROOT_ADDRESS.
// The function is stateless by construction: it runs before any sender state
// is read (EIP-8272: "Clients MUST check this frame shape and data encoding
// before reading sender state").
//
// Rules enforced (EIP-8272 "Public mempool handling" + EIP-8141 structural
// rules as extended by the skip semantics):
//
//  1. Any VERIFY frame to RECENT_ROOT_ADDRESS must exactly match the
//     canonical recent root verifier frame shape.
//  2. At most one recent root verifier frame per transaction.
//  3. It must appear immediately after an optional expiry_verify frame and
//     before every other frame.
//  4. The validation prefix (after skipping both optional protocol verifier
//     frames) must match one of the four recognized EIP-8141 prefixes.
//  5. No frame in the validation prefix may carry ATOMIC_BATCH_FLAG; no
//     VERIFY frame may follow the prefix.
//  6. Signature-validation cost plus the prefix's limits.execution sum must
//     not exceed MAX_VERIFY_GAS; the prefix's limits.state sum must not
//     exceed MAX_VERIFY_STATE_GAS.
func ValidateFrameRecentRootPolicy(ft *types.FrameTx) error {
	if !frameTxHasRecentRootTarget(ft) {
		return nil // not an EIP-8272 transaction; unchanged admission policy
	}
	// Rule 1: every VERIFY frame to RECENT_ROOT_ADDRESS must be canonical.
	for i := range ft.Frames {
		f := &ft.Frames[i]
		if f.Mode == types.FrameModeVerify && f.Target != nil && *f.Target == types.RecentRootAddress && !types.IsRecentRootVerifyFrame(f) {
			return fmt.Errorf("%w: frame %d", ErrRecentRootFrameMalformed, i)
		}
	}
	// Rule 2+3: exactly one, at the skip position.
	rrIdx := types.RecentRootVerifyFrameIndexes(ft)
	if len(rrIdx) > 1 {
		return ErrRecentRootFrameMultiple
	}
	shape, prefixLen, _, rrSkip := ClassifyFrameValidationPrefix(ft)
	if len(rrIdx) == 0 || rrIdx[0] != rrSkip {
		return ErrRecentRootFramePosition
	}
	// Rule 4: recognized prefix after the skips.
	if shape == FramePrefixUnrecognized {
		return ErrFramePrefixUnrecognizedPrefix
	}
	// Rule 5: batch flags inside the prefix, VERIFY after the prefix.
	for i := 0; i < prefixLen; i++ {
		if ft.Frames[i].Flags&types.FrameFlagAtomicBatch != 0 {
			return fmt.Errorf("%w: frame %d", ErrFramePrefixBatchInPrefix, i)
		}
	}
	for i := prefixLen; i < len(ft.Frames); i++ {
		if ft.Frames[i].Mode == types.FrameModeVerify {
			return fmt.Errorf("%w: frame %d", ErrFramePrefixVerifyAfterPrefix, i)
		}
	}
	// Rule 6: validation budget. Signature validation counts as intrinsic
	// cost against MAX_VERIFY_GAS (EIP-8141).
	var sigCost uint64
	for i := range ft.Signatures {
		gas, ok := ft.Signatures[i].SignatureGas()
		if !ok {
			return types.ErrFrameSigScheme
		}
		sigCost += gas
	}
	var execSum, stateSum uint64
	for i := 0; i < prefixLen; i++ {
		execSum += ft.Frames[i].Limits.Execution
		stateSum += ft.Frames[i].Limits.State
	}
	if sigCost+execSum > MaxVerifyGas {
		return fmt.Errorf("%w: sig %d + exec %d > %d", ErrFramePrefixVerifyGas, sigCost, execSum, MaxVerifyGas)
	}
	if stateSum > MaxVerifyStateGas {
		return fmt.Errorf("%w: state %d > %d", ErrFramePrefixVerifyStateGas, stateSum, MaxVerifyStateGas)
	}
	return nil
}
