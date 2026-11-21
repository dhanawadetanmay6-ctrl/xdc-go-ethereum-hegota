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

package core

import (
	"errors"
	"fmt"
	"os"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// Errors returned by frame transaction processing. A non-nil error from the
// frame engine marks the transaction INVALID (not merely reverted): it cannot
// be included in a block.
var (
	ErrFrameTxNotSupported     = errors.New("frame transaction not supported before Bogota")
	ErrFrameTxStaticInvalid    = errors.New("frame transaction failed static validation")
	ErrFrameTxChainID          = errors.New("frame transaction chain ID mismatch")
	ErrFrameTxFeeCapTooLow     = errors.New("frame transaction max_fee below required fee")
	ErrFrameTxSenderUnapproved = errors.New("SENDER frame without execution approval")
	ErrFrameTxVerifyFailed     = errors.New("VERIFY frame failed")
	ErrFrameTxNoPayer          = errors.New("frame transaction never approved payment")
	ErrFrameTxNonceSeq         = errors.New("frame transaction keyed nonce sequence mismatch")
	ErrFrameTxStateDB          = errors.New("frame transaction requires *state.StateDB")
	ErrFrameTxFeeOverflow      = errors.New("frame transaction fee computation overflows uint256")
)

// errTxInvalid wraps frame-level failures that invalidate the whole
// transaction (as opposed to ordinary frame reverts).
type errTxInvalid struct{ inner error }

func (e errTxInvalid) Error() string { return e.inner.Error() }
func (e errTxInvalid) Unwrap() error { return e.inner }

var debugFrameTx = os.Getenv("DEBUG_FRAMETX") != ""

// frameEngine executes an EIP-8141 frame transaction. It is invoked from
// stateTransition.execute when the message carries a frame transaction.
type frameEngine struct {
	st    *stateTransition
	tx    *types.FrameTx
	sdb   *state.StateDB
	evm   *vm.EVM
	fc    *vm.FrameTxContext
	rules params.Rules

	// Computed at setup:
	intrinsic        uint64
	floor            uint64
	maxGas           uint64
	maxCost          *uint256.Int
	gasPrice         *uint256.Int
	blobGas          uint64
	standardGasLimit uint64

	// precompiles is the active precompile set at the current fork. An
	// active precompile dispatches as an ordinary call in every mode
	// (EIP-8141: the precompile check precedes the empty-code default
	// path), so VERIFY frames to precompiles must not take the default
	// code route.
	precompiles map[common.Address]bool

	// Per-frame receipts (log slices attached at frame exit).
	receipts []*types.FrameReceipt
}

// executeFrameTx runs the frame transaction carried by the state transition's
// message (EIP-8141 "Behavior"). It returns a consensus error if the
// transaction is invalid.
func (st *stateTransition) executeFrameTx(rules params.Rules) (*ExecutionResult, error) {
	tx := st.msg.FrameTx
	if !rules.IsBogota {
		return nil, ErrFrameTxNotSupported
	}
	sdb, ok := st.state.(*state.StateDB)
	if !ok {
		return nil, ErrFrameTxStateDB
	}
	e := &frameEngine{st: st, tx: tx, sdb: sdb, evm: st.evm, rules: rules}
	return e.execute()
}

func (e *frameEngine) execute() (*ExecutionResult, error) {
	tx := e.tx
	msg := e.st.msg

	// --- Static (stateless) validity, chain binding, nonce, signatures ---
	if err := tx.ValidateStatic(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrFrameTxStaticInvalid, err)
	}
	// EIP-8250 activation: the payload form must match the fork.
	if err := tx.ValidateFork(e.rules.IsKeyedNonce); err != nil {
		return nil, err
	}
	if chainID := e.evm.ChainConfig().ChainID; chainID != nil && tx.ChainID.ToBig().Cmp(chainID) != 0 {
		return nil, fmt.Errorf("%w: have %v want %v", ErrFrameTxChainID, tx.ChainID, chainID)
	}
	// EIP-8250 stateful validity, evaluated against the transaction's actual
	// pre-state before any frame executes. The legacy account nonce is
	// captured unconditionally for TXPARAM(0x0D).
	legacyNonce := e.sdb.GetNonce(tx.Sender)
	if !msg.SkipNonceChecks {
		if len(tx.NonceKeys) == 0 {
			// Legacy domain: tx.nonce_seq == state[tx.sender].nonce.
			if legacyNonce != tx.NonceSeq {
				return nil, fmt.Errorf("%w: address %v, tx: %d state: %d", ErrNonceTooLow, tx.Sender.Hex(), tx.NonceSeq, legacyNonce)
			}
			if legacyNonce+1 < legacyNonce {
				return nil, fmt.Errorf("%w: address %v, nonce: %d", ErrNonceMax, tx.Sender.Hex(), legacyNonce)
			}
		} else {
			// Keyed domains: every selected key must sit at nonce_seq (or
			// be unused for the single-use form).
			for i := range tx.NonceKeys {
				cur := e.sdb.GetState(types.NonceManagerAddress, types.NonceSlot(tx.Sender, &tx.NonceKeys[i]))
				if tx.NonceSeq == types.MaxNonceSeq {
					if cur != (common.Hash{}) {
						return nil, fmt.Errorf("%w: address %v, single-use key %v already used", ErrFrameTxNonceSeq, tx.Sender.Hex(), tx.NonceKeys[i].Dec())
					}
				} else if cur != common.Hash(uint256.NewInt(tx.NonceSeq).Bytes32()) {
					return nil, fmt.Errorf("%w: address %v, key %v, tx seq: %d state: %v", ErrFrameTxNonceSeq, tx.Sender.Hex(), tx.NonceKeys[i].Dec(), tx.NonceSeq, cur.Big())
				}
			}
		}
	}
	// The EIP-7594 per-transaction blob limit applies to frame transactions
	// unchanged (EIP-8141 "Blob handling").
	if e.rules.IsOsaka && len(tx.BlobHashes) > params.BlobTxMaxBlobs {
		return nil, ErrTooManyBlobs
	}
	// Compute the canonical transaction signature hash and validate every
	// protocol-validated signature before any frame is executed.
	sigHash, err := tx.ValidateSignatures()
	if err != nil {
		return nil, err
	}

	// --- Gas limits, fee budget ---
	if err := e.computeGas(); err != nil {
		return nil, err
	}

	// Reserve the worst case against the block's two gas dimensions.
	executionReservation := max(e.intrinsic+e.sumExecution(), e.floor)
	stateReservation := e.sumState()
	if err := e.st.gp.CheckGasAmsterdam(executionReservation, stateReservation); err != nil {
		return nil, err
	}

	// --- Transaction environment ---
	// EIP-2929/3651 warm set: tx.sender, coinbase, precompiles. Frame targets
	// are not warmed; there is no access list (EIP-8141 cross-frame rules).
	activePrecompiles := vm.ActivePrecompiles(e.rules)
	e.sdb.Prepare(e.rules, tx.Sender, e.evm.Context.Coinbase, nil, activePrecompiles, nil)
	e.sdb.ResetTransientStorage()
	e.precompiles = make(map[common.Address]bool, len(activePrecompiles))
	for _, addr := range activePrecompiles {
		e.precompiles[addr] = true
	}

	// Frame execution state (journaled) + vm context.
	fs := state.NewFrameState(len(tx.Frames))
	e.sdb.SetFrameState(fs)
	defer e.sdb.SetFrameState(nil)
	e.fc = &vm.FrameTxContext{
		Tx:                    tx,
		SigHash:               sigHash,
		MaxCost:               e.maxCost,
		FrameStatuses:         make([]uint8, len(tx.Frames)),
		FrameGasUsedExecution: make([]uint64, len(tx.Frames)),
		FrameFinalized:        make([]bool, len(tx.Frames)),
		SigResolvedSigners:    make([]common.Address, len(tx.Signatures)),
		SigResolvedOK:         make([]bool, len(tx.Signatures)),
		KeyedNonce:            e.rules.IsKeyedNonce,
		LegacyNonce:           legacyNonce,
	}
	for i := range tx.Signatures {
		e.fc.SigResolvedSigners[i], e.fc.SigResolvedOK[i] = tx.ResolvedSigner(&tx.Signatures[i])
	}
	e.evm.SetFrameContext(e.fc)
	defer e.evm.SetFrameContext(nil)

	// GASPRICE for the whole transaction: base_fee + max_priority_fee/max_gas.
	e.evm.SetTxContext(NewEVMTxContext(msg))
	e.evm.TxContext.Origin = tx.Sender
	e.evm.TxContext.GasPrice = e.gasPrice
	e.evm.TxContext.BlobHashes = tx.BlobHashes

	refundStart := e.sdb.GetRefund()
	txSnapshot := e.sdb.Snapshot()
	e.receipts = make([]*types.FrameReceipt, len(tx.Frames))

	// --- Frame loop ---
	if err := e.runFrames(fs, txSnapshot); err != nil {
		return nil, err
	}

	// --- Payer check, settlement ---
	if !fs.PayerSet {
		e.sdb.RevertToSnapshot(txSnapshot)
		return nil, ErrFrameTxNoPayer
	}
	return e.settle(fs, refundStart)
}

// computeGas computes the intrinsic/floor/max gas and the fee budget.
func (e *frameEngine) computeGas() error {
	tx := e.tx
	intrinsic, floor, err := tx.IntrinsicGas()
	if err != nil {
		return err
	}
	e.intrinsic, e.floor = intrinsic, floor
	e.maxGas = tx.MaxGas()
	e.standardGasLimit = intrinsic + e.sumExecution() + e.sumState()
	e.blobGas = uint64(len(tx.BlobHashes)) * params.BlobTxBlobGasPerBlob

	e.maxCost = new(uint256.Int)
	e.gasPrice = new(uint256.Int)
	if e.evm.Config.NoBaseFee && tx.Fees.MaxFee.IsZero() && tx.Fees.MaxPriorityFeePerGas.IsZero() {
		// Simulation paths (eth_call) with fee fields zeroed.
		return nil
	}
	if e.evm.Context.BaseFee == nil {
		return errors.New("frame transaction requires base fee (London+)")
	}
	baseFee, overflow := uint256.FromBig(e.evm.Context.BaseFee)
	if overflow {
		return fmt.Errorf("invalid baseFee: %v", e.evm.Context.BaseFee)
	}
	blobBaseFee := new(uint256.Int)
	if e.blobGas > 0 {
		if e.evm.Context.BlobBaseFee == nil {
			return errors.New("missing blob base fee")
		}
		blobBaseFee, overflow = uint256.FromBig(e.evm.Context.BlobBaseFee)
		if overflow {
			return fmt.Errorf("invalid blobBaseFee: %v", e.evm.Context.BlobBaseFee)
		}
	}
	// required_max_fee = max_gas * base_fee + blob_gas * blob_base_fee.
	// The spec's arithmetic is exact; any uint256 overflow invalidates the
	// transaction rather than wrapping the budget.
	required := new(uint256.Int).SetUint64(e.maxGas)
	if _, overflow := required.MulOverflow(required, baseFee); overflow {
		return fmt.Errorf("%w: max_gas * base_fee", ErrFrameTxFeeOverflow)
	}
	blobFee := new(uint256.Int).SetUint64(e.blobGas)
	if _, overflow := blobFee.MulOverflow(blobFee, blobBaseFee); overflow {
		return fmt.Errorf("%w: blob_gas * blob_base_fee", ErrFrameTxFeeOverflow)
	}
	if _, overflow := required.AddOverflow(required, blobFee); overflow {
		return fmt.Errorf("%w: required_max_fee", ErrFrameTxFeeOverflow)
	}
	if tx.Fees.MaxFee.Cmp(required) < 0 {
		return fmt.Errorf("%w: max_fee %v, required %v", ErrFrameTxFeeCapTooLow, tx.Fees.MaxFee, required)
	}
	// max_priority_fee = min(max_priority_fee_per_gas * max_gas, max_fee - required)
	maxPriority := new(uint256.Int).SetUint64(e.maxGas)
	if _, overflow := maxPriority.MulOverflow(maxPriority, tx.Fees.MaxPriorityFeePerGas); overflow {
		return fmt.Errorf("%w: max_priority_fee_per_gas * max_gas", ErrFrameTxFeeOverflow)
	}
	if headroom := new(uint256.Int).Sub(tx.Fees.MaxFee, required); headroom.Cmp(maxPriority) < 0 {
		maxPriority = headroom
	}
	// max_cost = required + max_priority_fee
	e.maxCost.Add(required, maxPriority)
	// GASPRICE = base_fee + max_priority_fee // max_gas
	if e.maxGas > 0 {
		perGas := new(uint256.Int).Div(maxPriority, uint256.NewInt(e.maxGas))
		e.gasPrice.Add(baseFee, perGas)
	} else {
		e.gasPrice.Set(baseFee)
	}
	return nil
}

func (e *frameEngine) sumExecution() uint64 {
	var sum uint64
	for _, f := range e.tx.Frames {
		sum += f.Limits.Execution
	}
	return sum
}

func (e *frameEngine) sumState() uint64 {
	var sum uint64
	for _, f := range e.tx.Frames {
		sum += f.Limits.State
	}
	return sum
}

// runFrames executes the frame loop (EIP-8141 "Behavior"), including atomic
// batch handling. A non-nil return marks the transaction invalid.
func (e *frameEngine) runFrames(fs *state.FrameState, txSnapshot int) error {
	tx := e.tx
	var (
		batchStart    = -1
		batchSnapshot int
	)
	for i := 0; i < len(tx.Frames); i++ {
		frame := &tx.Frames[i]
		e.fc.CurrentFrame = i

		// Track atomic batch boundaries. A batch is a maximal contiguous
		// sequence [i, j] where frames i..j-1 carry ATOMIC_BATCH_FLAG and
		// frame j does not.
		if frame.Flags&types.FrameFlagAtomicBatch != 0 && batchStart == -1 {
			batchStart = i
			batchSnapshot = e.sdb.Snapshot()
		}
		inBatch := batchStart != -1

		frameSnapshot := e.sdb.Snapshot()
		logsBefore := len(e.sdb.Logs())

		// Execute the frame.
		vmerr := e.runFrame(fs, frame, i)

		// Attach the logs emitted by this frame on success; failed frames have
		// empty log lists (their logs were reverted with their state).
		if vmerr == nil {
			all := e.sdb.Logs()
			if len(all) > logsBefore {
				e.receipts[i].Logs = all[logsBefore:]
			}
		}

		if vmerr == nil {
			// Batch closes when its unflagged terminator completes.
			if inBatch && frame.Flags&types.FrameFlagAtomicBatch == 0 {
				batchStart = -1
			}
			continue
		}
		// Transaction-invalidating failures.
		if inv, ok := vmerr.(errTxInvalid); ok {
			e.sdb.RevertToSnapshot(txSnapshot)
			return inv.inner
		}
		// A failed VERIFY frame invalidates the whole transaction, unrolling
		// any effects of APPROVE.
		if frame.Mode == types.FrameModeVerify {
			e.sdb.RevertToSnapshot(txSnapshot)
			return fmt.Errorf("%w: frame %d: %v", ErrFrameTxVerifyFailed, i, vmerr)
		}
		if inBatch {
			// Unroll the atomic batch: restore the batch-entry state, mark
			// the remaining frames of the batch skipped, and jump past them.
			e.sdb.RevertToSnapshot(batchSnapshot)
			i = e.unrollBatch(batchStart, i)
			batchStart = -1
			continue
		}
		// Plain frame failure: revert state to frame entry (the EVM call
		// already reverted its own snapshot; this additionally restores the
		// entry access-charge warming and any default-code effects).
		e.sdb.RevertToSnapshot(frameSnapshot)
	}
	return nil
}

// runFrame executes a single frame and returns its frame-level error (nil on
// success). Errors of type errTxInvalid invalidate the whole transaction.
func (e *frameEngine) runFrame(fs *state.FrameState, frame *types.Frame, index int) error {
	tx := e.tx

	// Resolve target and set the frame caller.
	resolvedTarget := tx.Sender
	if frame.Target != nil {
		resolvedTarget = *frame.Target
	}
	e.fc.ResolvedTarget = resolvedTarget

	var caller common.Address
	switch frame.Mode {
	case types.FrameModeSender:
		// sender_approved must be true; if not, the transaction is invalid.
		if !fs.SenderApproved {
			return errTxInvalid{ErrFrameTxSenderUnapproved}
		}
		caller = tx.Sender
	default: // DEFAULT, VERIFY
		caller = types.EntryPoint
	}
	// ORIGIN returns the frame's caller throughout all call depths.
	e.evm.TxContext.Origin = caller

	// Discard transient storage between frames.
	e.sdb.ResetTransientStorage()

	// Frame gas pools: strict two-dimensional budgets.
	gas := vm.NewGasBudgetNoSpill(frame.Limits.Execution, frame.Limits.State)

	// Frame entry: charge the resolved target's warm/cold account access from
	// gas_left before the balance check and before dispatch.
	accessCost := uint64(params.ColdAccountAccessAmsterdam)
	if e.sdb.AddressInAccessList(resolvedTarget) {
		accessCost = uint64(params.WarmAccountAccessAmsterdam)
	}
	if _, ok := gas.ChargeRegular(accessCost); !ok {
		e.haltFrame(&gas, index)
		return vm.ErrOutOfGas
	}
	e.sdb.AddAddressToAccessList(resolvedTarget)

	// Balance check for value-carrying frames (value>0 implies SENDER mode,
	// so caller == tx.sender).
	if !frameValue(frame).IsZero() && !e.evm.Context.CanTransfer(e.sdb, caller, frameValue(frame)) {
		// As with an ordinary CALL, the frame reverts consuming the gas
		// charged so far.
		e.revertFrame(&gas, index)
		return vm.ErrExecutionReverted
	}
	// Value-carrying frame creating a new account: state-gas charge after the
	// balance check and before the frame's code executes (EIP-2780 semantics).
	if !frameValue(frame).IsZero() && e.sdb.Empty(resolvedTarget) {
		if !gas.ChargeStateStrict(uint64(params.AccountCreationSize) * e.evm.Context.CostPerStateByte) {
			e.haltFrame(&gas, index)
			return vm.ErrOutOfGas
		}
	}

	// Dispatch. An active precompile executes as an ordinary call in every
	// mode (EIP-8141): the precompile check precedes the empty-code
	// evaluation, so only non-precompile empty-code VERIFY targets take the
	// protocol default code.
	switch {
	case frame.Mode == types.FrameModeVerify && e.isDefaultCode(resolvedTarget) && !e.precompiles[resolvedTarget]:
		return e.runDefaultCodeVerify(&gas, frame, index)
	default:
		return e.callFrame(&gas, caller, resolvedTarget, frame, index)
	}
}

// callFrame dispatches the frame's top-level call through the EVM.
func (e *frameEngine) callFrame(gas *vm.GasBudget, caller, target common.Address, frame *types.Frame, index int) error {
	var (
		result vm.GasBudget
		err    error
	)
	if frame.Mode == types.FrameModeVerify {
		// VERIFY frames execute as STATICCALL.
		_, result, err = e.evm.StaticCall(caller, target, frame.Data, *gas)
	} else {
		_, result, err = e.evm.Call(caller, target, frame.Data, *gas, frameValue(frame))
	}
	if debugFrameTx && err != nil {
		println("frame", index, "vmerr:", err.Error())
	}
	e.absorbResult(result, err, index)
	return err
}

// isDefaultCode reports whether the resolved target executes the EIP-8141
// default code: it carries neither contract code nor an EIP-7702 delegation.
func (e *frameEngine) isDefaultCode(target common.Address) bool {
	code := e.sdb.GetCode(target)
	if _, delegated := types.ParseDelegation(code); delegated {
		return false
	}
	return len(code) == 0
}

// runDefaultCodeVerify executes the EIP-8141 default code for a VERIFY frame
// whose resolved target has no code.
func (e *frameEngine) runDefaultCodeVerify(gas *vm.GasBudget, frame *types.Frame, index int) error {
	tx := e.tx
	allowedScope := uint64(frame.Flags & types.FrameFlagApproveScopeMask)
	if allowedScope == 0 {
		e.revertFrame(gas, index)
		return vm.ErrExecutionReverted
	}
	// sig_index = 0 if execution approval is in scope, else 1.
	sigIndex := 1
	if allowedScope&types.FrameFlagApproveExecution != 0 {
		sigIndex = 0
	}
	if sigIndex >= len(tx.Signatures) {
		e.revertFrame(gas, index)
		return vm.ErrExecutionReverted
	}
	sig := &tx.Signatures[sigIndex]
	resolvedSigner, ok := tx.ResolvedSigner(sig)
	if sig.Scheme != types.FrameSigSchemeSecp256k1 || !ok || resolvedSigner != e.fc.ResolvedTarget || len(sig.Msg) != 0 {
		e.revertFrame(gas, index)
		return vm.ErrExecutionReverted
	}
	// Call APPROVE(allowed_scope). The default code draws no execution gas of
	// its own; its APPROVE may charge state gas for sender-account creation.
	revert, halt := e.fc.Approve(e.evm, gas, e.fc.ResolvedTarget, allowedScope)
	if halt {
		e.haltFrame(gas, index)
		return vm.ErrOutOfGas
	}
	if revert {
		e.revertFrame(gas, index)
		return vm.ErrExecutionReverted
	}
	e.absorbResult(gas.Exit(nil), nil, index)
	return nil
}

// absorbResult merges the post-execution budget into the frame accounting and
// finalizes the frame receipt. `result` must be in leftover form (post
// GasBudget.Exit).
func (e *frameEngine) absorbResult(result vm.GasBudget, vmerr error, index int) {
	frame := &e.tx.Frames[index]
	status := uint8(vm.FrameStatusSucceeded)
	if vmerr != nil {
		status = vm.FrameStatusFailed
	}
	execUsed := frame.Limits.Execution - result.RegularGas
	stateUsed := int64(frame.Limits.State) - int64(result.StateGas)
	e.writeReceipt(index, status, execUsed, stateUsed)
}

// revertFrame finalizes a frame that reverted without entering the EVM:
// remaining execution gas is returned, the state pool is restored.
func (e *frameEngine) revertFrame(gas *vm.GasBudget, index int) {
	e.absorbResult(gas.Exit(vm.ErrExecutionReverted), vm.ErrExecutionReverted, index)
}

// haltFrame finalizes a frame that halted exceptionally without entering the
// EVM: the entire execution pool is consumed, the state pool is restored.
func (e *frameEngine) haltFrame(gas *vm.GasBudget, index int) {
	e.absorbResult(gas.ExitHalt(), vm.ErrOutOfGas, index)
}

// writeReceipt records the finalized per-frame receipt fields. The attributed
// state gas is journaled (later refills may reduce it); the receipt struct's
// GasUsedState is filled from the journaled value at settlement.
func (e *frameEngine) writeReceipt(index int, status uint8, execUsed uint64, stateUsed int64) {
	e.fc.FrameStatuses[index] = status
	e.fc.FrameGasUsedExecution[index] = execUsed
	e.fc.FrameFinalized[index] = true
	e.sdb.SetFrameReceiptStateGas(index, stateUsed)
	e.receipts[index] = &types.FrameReceipt{
		Status:           status,
		GasUsedExecution: execUsed,
	}
}

// unrollBatch applies the atomic-batch receipt rules after a batch failed at
// frame failedIndex: executed in-batch frames retain status and execution gas
// with empty logs and zero state gas (the journaled batch revert already
// zeroed their attributed state gas and discarded their logs); the remaining
// frames of the batch are marked skipped (status 2) with zero gas.
// It returns the index of the batch's last frame (the caller skips it).
func (e *frameEngine) unrollBatch(batchStart, failedIndex int) int {
	tx := e.tx
	for i := batchStart; i <= failedIndex; i++ {
		e.receipts[i].Logs = nil
		e.sdb.SetFrameReceiptStateGas(i, 0)
	}
	end := failedIndex
	for i := failedIndex + 1; i < len(tx.Frames); i++ {
		e.fc.FrameStatuses[i] = vm.FrameStatusSkipped
		e.fc.FrameGasUsedExecution[i] = 0
		e.fc.FrameFinalized[i] = true
		e.receipts[i] = &types.FrameReceipt{Status: vm.FrameStatusSkipped}
		end = i
		if tx.Frames[i].Flags&types.FrameFlagAtomicBatch == 0 {
			break // end of the failed batch
		}
	}
	return end
}

// settle performs EIP-8141 transaction settlement: gas accounting, fee
// payment, and the ExecutionResult.
func (e *frameEngine) settle(fs *state.FrameState, refundStart uint64) (*ExecutionResult, error) {
	tx := e.tx

	// tx_unused_gas over all frames, using final attributed state gas.
	var txUnusedGas uint64
	for i, frame := range tx.Frames {
		stateUsed := fs.ReceiptStateGas[i]
		e.receipts[i].GasUsedState = uint64(stateUsed)
		txUnusedGas += (frame.Limits.Execution - e.fc.FrameGasUsedExecution[i]) + (frame.Limits.State - uint64(stateUsed))
	}
	gasUsedBeforeRefund := e.standardGasLimit - txUnusedGas

	// EIP-3529 refund, accumulated across frames into the tx-scoped counter.
	refund := e.sdb.GetRefund() - refundStart
	if maxRefund := gasUsedBeforeRefund / params.RefundQuotientEIP3529; refund > maxRefund {
		refund = maxRefund
	}
	gasUsedAfterRefund := gasUsedBeforeRefund - refund

	var txStateGas uint64
	for i := range tx.Frames {
		txStateGas += uint64(fs.ReceiptStateGas[i])
	}
	txExecutionGas := max(gasUsedAfterRefund-txStateGas, e.floor)
	gasUsed := txExecutionGas + txStateGas

	// Block-level accounting: execution dimension is charged pre-refund
	// (EIP-7778), the state dimension stays net.
	blockExecutionGas := max(gasUsedBeforeRefund-txStateGas, e.floor)
	if err := e.st.gp.ChargeGasAmsterdam(blockExecutionGas, txStateGas, gasUsed); err != nil {
		return nil, err
	}

	// Fee settlement (spec behavior): max_cost was collected from the payer at
	// approval time; refund the difference, credit the priority fee to the
	// coinbase; the base-fee portion stays collected (burned).
	payer := fs.Payer
	simulating := e.evm.Config.NoBaseFee && tx.Fees.MaxFee.IsZero() && tx.Fees.MaxPriorityFeePerGas.IsZero()
	if !simulating {
		baseFee, _ := uint256.FromBig(e.evm.Context.BaseFee)
		blobBaseFee := new(uint256.Int)
		if e.blobGas > 0 {
			blobBaseFee, _ = uint256.FromBig(e.evm.Context.BlobBaseFee)
		}
		// base_fee_paid = gas_used * base_fee + blob_gas * blob_base_fee
		baseFeePaid := new(uint256.Int).SetUint64(gasUsed)
		baseFeePaid.Mul(baseFeePaid, baseFee)
		blobPaid := new(uint256.Int).SetUint64(e.blobGas)
		blobPaid.Mul(blobPaid, blobBaseFee)
		baseFeePaid.Add(baseFeePaid, blobPaid)
		// priority_fee_paid = min(max_priority_fee_per_gas * gas_used, max_cost - base_fee_paid)
		priorityPaid := new(uint256.Int).SetUint64(gasUsed)
		priorityPaid.Mul(priorityPaid, tx.Fees.MaxPriorityFeePerGas)
		if diff := new(uint256.Int).Sub(e.maxCost, baseFeePaid); diff.Cmp(priorityPaid) < 0 {
			priorityPaid = diff
		}
		// payer_refund = max_cost - base_fee_paid - priority_fee_paid
		payerRefund := new(uint256.Int).Sub(e.maxCost, baseFeePaid)
		payerRefund.Sub(payerRefund, priorityPaid)
		if payerRefund.Sign() > 0 {
			e.sdb.AddBalance(payer, payerRefund, tracing.BalanceIncreaseGasReturn)
		}
		if priorityPaid.Sign() > 0 {
			e.sdb.AddBalance(e.evm.Context.Coinbase, priorityPaid, tracing.BalanceIncreaseRewardTransactionFee)
		}
	}

	return &ExecutionResult{
		UsedGas:       gasUsed,
		MaxUsedGas:    blockExecutionGas + txStateGas,
		Err:           nil,
		Payer:         payer,
		FrameReceipts: e.receipts,
	}, nil
}

// frameValue returns the frame's value, never nil.
func frameValue(f *types.Frame) *uint256.Int {
	if f.Value == nil {
		return new(uint256.Int)
	}
	return f.Value
}
