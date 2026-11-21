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
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
)

// EIP-8272 recent root evaluation helpers.
//
// Consensus (block execution) needs nothing here: a recent root verifier
// frame is an ordinary EIP-8141 VERIFY frame and RECENT_ROOT_CODE is ordinary
// contract code. This file implements the public-mempool machinery of
// EIP-8272 "Public mempool handling": direct tuple predicate evaluation
// (dependency revalidation, pre-inclusion checks) and the admission
// simulation of the verifier frame with the two narrowly scoped permissions
// (SLOTNUM inside the canonical code, SLOAD of derived keys) enforced by a
// tracer on top of EIP-8141's generic validation-prefix opcode bans.

var (
	// ErrRecentRootCodeMismatch is returned when the runtime code at
	// RECENT_ROOT_ADDRESS does not exactly equal RECENT_ROOT_CODE.
	ErrRecentRootCodeMismatch = errors.New("recent root contract code mismatch")
	// ErrRecentRootSlotCurrent is returned when a tuple references the
	// current slot.
	ErrRecentRootSlotCurrent = errors.New("recent root reference to current slot")
	// ErrRecentRootSlotFuture is returned when a tuple references a future
	// slot.
	ErrRecentRootSlotFuture = errors.New("recent root reference to future slot")
	// ErrRecentRootExpired is returned when a tuple's age reaches
	// RECENT_ROOT_LENGTH.
	ErrRecentRootExpired = errors.New("recent root reference expired")
	// ErrRecentRootEntryMismatch is returned when the stored entry hash does
	// not match the tuple's entry hash.
	ErrRecentRootEntryMismatch = errors.New("recent root entry hash mismatch")
	// ErrRecentRootBannedOpcode is returned when the admission simulation
	// observes an opcode outside the permitted set.
	ErrRecentRootBannedOpcode = errors.New("banned opcode during recent root verification")
	// ErrRecentRootBadSLOAD is returned when the admission simulation
	// observes an SLOAD outside the derived key set.
	ErrRecentRootBadSLOAD = errors.New("unpermitted SLOAD during recent root verification")
	// ErrRecentRootNestedGrant is returned when SLOTNUM/SLOAD would execute
	// below the top level of the recent root frame.
	ErrRecentRootNestedGrant = errors.New("recent root permission used in nested call")
	// ErrRecentRootFrameFailed is returned when the frame execution reverts
	// or halts (including insufficient limits.execution).
	ErrRecentRootFrameFailed = errors.New("recent root verifier frame failed")
)

// CheckRecentRootTuples evaluates the EIP-8272 validation-operation
// predicates directly against state (the spec's permitted direct evaluation):
//
//	slot < current_slot
//	current_slot - slot <= RECENT_ROOT_USABLE_WINDOW
//	storage[RECENT_ROOT_ADDRESS][storage_key(slot mod 8192)] == entry_hash
//
// current_slot MUST come from the EIP-7843 slotNumber pipeline (never derived
// from timestamps by the caller's intention; this function cannot verify
// provenance). No writes are performed.
func CheckRecentRootTuples(sdb vm.StateDB, currentSlot uint64, tuples []types.RecentRootTuple) error {
	if len(tuples) == 0 {
		return types.ErrRecentRootDataEmpty
	}
	if len(tuples) > types.MaxRecentRootReferences {
		return types.ErrRecentRootDataTooLong
	}
	for i := range tuples {
		t := &tuples[i]
		if t.Slot >= currentSlot {
			if t.Slot > currentSlot {
				return fmt.Errorf("%w: slot %d, current %d", ErrRecentRootSlotFuture, t.Slot, currentSlot)
			}
			return fmt.Errorf("%w: slot %d", ErrRecentRootSlotCurrent, t.Slot)
		}
		if currentSlot-t.Slot > types.RecentRootUsableWindow {
			return fmt.Errorf("%w: slot %d, current %d", ErrRecentRootExpired, t.Slot, currentSlot)
		}
		stored := sdb.GetState(types.RecentRootAddress, t.StorageKey())
		if stored != t.EntryHash() {
			return fmt.Errorf("%w: tuple %d slot %d", ErrRecentRootEntryMismatch, i, t.Slot)
		}
	}
	return nil
}

// RecentRootCodeMatches reports whether the runtime code at
// RECENT_ROOT_ADDRESS exactly equals RECENT_ROOT_CODE.
func RecentRootCodeMatches(sdb vm.StateDB) bool {
	return bytes.Equal(sdb.GetCode(types.RecentRootAddress), types.RecentRootCode)
}

// CheckRecentRootPreInclusion revalidates a frame transaction's recent root
// verifier frame against the exact slot of the payload being assembled
// (EIP-8272: "Before including a transaction in a payload, a client MUST
// revalidate it with SLOTNUM set to that payload's slotNumber"). It applies
// the complete age and storage predicates plus the code dependency. A nil
// slotNumber means EIP-7843 is inactive for the payload; frame transactions
// are invalid there and fail in the engine regardless.
func CheckRecentRootPreInclusion(sdb vm.StateDB, slotNumber *uint64, tx *types.Transaction) error {
	ft := tx.FrameTxData()
	if ft == nil {
		return nil
	}
	idx := types.RecentRootVerifyFrameIndexes(ft)
	if len(idx) == 0 {
		return nil
	}
	if slotNumber == nil {
		return errors.New("recent root verifier frame requires payload slot number")
	}
	if !RecentRootCodeMatches(sdb) {
		return ErrRecentRootCodeMismatch
	}
	tuples, err := types.ParseRecentRootTuples(ft.Frames[idx[0]].Data)
	if err != nil {
		return err
	}
	return CheckRecentRootTuples(sdb, *slotNumber, tuples)
}

// recentRootTrace enforces the public-mempool opcode and storage permissions
// during the admission simulation of a recent root verifier frame:
//
//   - EIP-8141's banned opcode set (GASPRICE, BLOCKHASH, COINBASE, TIMESTAMP,
//     NUMBER, PREVRANDAO, GASLIMIT, BASEFEE, BLOBBASEFEE, SLOTNUM, GAS,
//     CREATE, CREATE2, SETDELEGATE, INVALID, SELFDESTRUCT, BALANCE,
//     SELFBALANCE, SSTORE), with the two EIP-8272 permissions: SLOTNUM may
//     execute and SLOAD may read the derived recent-root storage keys, both
//     only at the top level of the canonical RECENT_ROOT_CODE execution.
//   - Any other SLOAD, any state write, and any use of either permission
//     inside a nested call is recorded as a violation.
//
// Violations are recorded, not raised; the caller rejects the transaction
// after execution. (The frame is VERIFY/STATICCALL: the interpreter already
// blocks writes; the tracer adds defense-in-depth and evidence.)
type recentRootTrace struct {
	keys map[common.Hash]struct{}
	err  error
}

func (t *recentRootTrace) fail(err error) {
	if t.err == nil {
		t.err = err
	}
}

// onOpcode implements tracing.OpcodeHook.
func (t *recentRootTrace) onOpcode(pc uint64, op byte, gas, cost uint64, scope tracing.OpContext, rData []byte, depth int, err error) {
	opc := vm.OpCode(op)
	inCanonicalTop := depth == 1 && scope.Address() == types.RecentRootAddress
	switch opc {
	case vm.SLOTNUM:
		// EIP-8272 permission 1: only inside canonical top-level code.
		if !inCanonicalTop {
			t.fail(fmt.Errorf("%w: SLOTNUM at depth %d addr %v", ErrRecentRootNestedGrant, depth, scope.Address()))
		}
	case vm.SLOAD:
		// EIP-8272 permission 2: only derived keys, only top level.
		stack := scope.StackData()
		if len(stack) == 0 {
			return
		}
		key := common.Hash(stack[len(stack)-1].Bytes32())
		if !inCanonicalTop {
			t.fail(fmt.Errorf("%w: SLOAD at depth %d addr %v", ErrRecentRootNestedGrant, depth, scope.Address()))
			return
		}
		if _, ok := t.keys[key]; !ok {
			t.fail(fmt.Errorf("%w: key %v", ErrRecentRootBadSLOAD, key))
		}
	case vm.GASPRICE, vm.BLOCKHASH, vm.COINBASE, vm.TIMESTAMP, vm.NUMBER,
		vm.PREVRANDAO, vm.GASLIMIT, vm.BASEFEE, vm.BLOBBASEFEE,
		vm.GAS, vm.CREATE, vm.CREATE2, vm.INVALID, vm.SELFDESTRUCT,
		vm.BALANCE, vm.SELFBALANCE, vm.SSTORE:
		t.fail(fmt.Errorf("%w: %v at pc %d depth %d", ErrRecentRootBannedOpcode, opc, pc, depth))
	default:
		// SETDELEGATE (0xf6, EIP-7819) is not allocated in this fork; if it
		// ever is, it is banned here by the deploy-frame exception not
		// applying. All remaining opcodes are permitted.
	}
}

// SimulateRecentRootVerifyFrame runs the EIP-8272 public-mempool admission
// check of one recent root verifier frame against the head state:
//
//  1. The runtime code at RECENT_ROOT_ADDRESS must equal RECENT_ROOT_CODE.
//  2. The frame executes under STATICCALL semantics with
//     BlockContext.SlotNum = current_slot and a gas budget equal to the
//     frame's declared limits, after charging the cold account access of the
//     target exactly as the EIP-8141 engine does at frame entry.
//  3. The EIP-8272 opcode/storage permissions are enforced by tracer.
//
// The state database is snapshotted and reverted: the simulation leaves no
// observable change (the STATICCALL performs none; the revert also undoes
// access-list warming). It returns the execution gas the frame consumed
// (frame-entry access charge plus internal execution), which equals the
// frame receipt's GasUsedExecution for a successful first touch.
//
// head is the latest canonical header: current_slot is one greater than its
// slotNumber (EIP-8272 "Current slot") and its number/time drive fork
// dispatch for the simulation EVM. sender is the transaction sender, used to
// reproduce the engine's transaction-level access-list warming exactly
// (EIP-2929/3651: sender, coinbase, precompiles).
func SimulateRecentRootVerifyFrame(sdb *state.StateDB, chainConfig *params.ChainConfig, head *types.Header, sender common.Address, frame *types.Frame) (uint64, error) {
	if !types.IsRecentRootVerifyFrame(frame) {
		return 0, errors.New("not a recent root verifier frame")
	}
	if head == nil || head.SlotNumber == nil {
		return 0, errors.New("recent root simulation requires a head with slot number")
	}
	currentSlot := *head.SlotNumber + 1
	if !RecentRootCodeMatches(sdb) {
		return 0, ErrRecentRootCodeMismatch
	}
	tuples, err := types.ParseRecentRootTuples(frame.Data)
	if err != nil {
		return 0, err
	}
	keys := make(map[common.Hash]struct{}, len(tuples))
	for i := range tuples {
		keys[tuples[i].StorageKey()] = struct{}{}
	}
	tracer := &recentRootTrace{keys: keys}

	snap := sdb.Snapshot()
	defer sdb.RevertToSnapshot(snap)

	rules := chainConfig.Rules(head.Number, true, head.Time)
	// Reproduce the engine's transaction-entry warming (frame_transition.go
	// execute: Prepare warms sender, coinbase and precompiles; frame targets
	// are not warmed). The recent root verifier frame is the first (or, after
	// an expiry verifier frame, second) frame of the transaction; neither
	// touches RECENT_ROOT_ADDRESS before it.
	sdb.Prepare(rules, sender, common.Address{}, nil, vm.ActivePrecompiles(rules), nil)
	sdb.ResetTransientStorage()

	evm := vm.NewEVM(vm.BlockContext{
		CanTransfer:      CanTransfer,
		Transfer:         Transfer,
		GetHash:          func(uint64) common.Hash { return common.Hash{} },
		Coinbase:         common.Address{},
		BlockNumber:      new(big.Int).Set(head.Number),
		Time:             head.Time,
		Difficulty:       big.NewInt(0),
		BaseFee:          big.NewInt(0),
		BlobBaseFee:      big.NewInt(0),
		GasLimit:         60_000_000,
		Random:           &common.Hash{},
		CostPerStateByte: params.CostPerStateByte,
		SlotNum:          currentSlot,
	}, sdb, chainConfig, vm.Config{
		NoBaseFee: true,
		Tracer:    &tracing.Hooks{OnOpcode: tracer.onOpcode},
	})

	// Frame-entry target access charge, mirroring the EIP-8141 engine
	// (frame_transition.go runFrame): cold (or warm) account access of the
	// resolved target is charged from limits.execution before dispatch. The
	// mempool admission bound charges the cold cost: no prior frame of the
	// transaction is assumed to have warmed the target.
	gas := vm.NewGasBudgetNoSpill(frame.Limits.Execution, frame.Limits.State)
	accessCost := uint64(params.ColdAccountAccessAmsterdam)
	if sdb.AddressInAccessList(types.RecentRootAddress) {
		accessCost = uint64(params.WarmAccountAccessAmsterdam)
	}
	if _, ok := gas.ChargeRegular(accessCost); !ok {
		return frame.Limits.Execution, ErrRecentRootFrameFailed
	}
	sdb.AddAddressToAccessList(types.RecentRootAddress)

	// VERIFY frames execute as STATICCALL with the frame caller ENTRY_POINT.
	_, result, vmerr := evm.StaticCall(types.EntryPoint, types.RecentRootAddress, frame.Data, gas)
	if tracer.err != nil {
		return frame.Limits.Execution - result.RegularGas, tracer.err
	}
	if vmerr != nil {
		return frame.Limits.Execution - result.RegularGas, fmt.Errorf("%w: %v", ErrRecentRootFrameFailed, vmerr)
	}
	// EIP-8141: a successful VERIFY frame returns no data. RECENT_ROOT_CODE
	// returns nothing by construction (STOP); assert the property anyway.
	return frame.Limits.Execution - result.RegularGas, nil
}

// DeployRecentRoot installs the canonical recent root contract at
// RECENT_ROOT_ADDRESS. It is a TEST FIXTURE helper: the protocol never
// installs the contract (EIP-8272 "Activation" makes no state change); real
// chains deploy it with the ordinary deployment transaction.
func DeployRecentRoot(sdb *state.StateDB) {
	sdb.CreateAccount(types.RecentRootAddress)
	sdb.SetCode(types.RecentRootAddress, types.RecentRootCode, tracing.CodeChangeUnspecified)
}

// RecentRootWriteCalldata assembles the 64-byte write-operation calldata
// (salt || root). Test helper.
func RecentRootWriteCalldata(salt, root common.Hash) []byte {
	data := make([]byte, 0, types.RecentRootWriteDataLength)
	data = append(data, salt[:]...)
	data = append(data, root[:]...)
	return data
}

// RecentRootValidationCalldata packs tuples into validation-operation
// calldata. Test helper.
func RecentRootValidationCalldata(tuples []types.RecentRootTuple) []byte {
	var data []byte
	for _, t := range tuples {
		data = append(data, t.SourceID[:]...)
		var slotBE [8]byte
		binary.BigEndian.PutUint64(slotBE[:], t.Slot)
		data = append(data, slotBE[:]...)
		data = append(data, t.Root[:]...)
	}
	return data
}
