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

package vm

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

// enable8298 registers the EIP-8298 SETCODEFROM instruction (0xa7) in the
// Bogota instruction set.
//
// All gas is charged inside the handler (constantGas zero, no dynamicGas):
// EIP-8298 prescribes a strictly staged charging order that the interpreter's
// fixed constant-then-dynamic-then-execute sequence cannot express:
//
//  1. static-context and system-contract halts happen BEFORE
//     SETCODEFROM_SOURCE_GAS is charged (source is not accessed);
//  2. SETCODEFROM_SOURCE_GAS = SOURCE_ACCOUNT_ACCESS_COST + WARM_ACCESS is
//     charged per attempt; only then is the source warmed (valid or not);
//  3. for a valid source, SETCODEFROM_CURRENT_ACCESS_GAS = WARM_ACCESS is
//     charged before the code-hash comparison;
//  4. if the hashes differ, SETCODEFROM_WRITE_GAS = ACCOUNT_WRITE is charged
//     before the update.
//
// SETCODEFROM_STATE_GAS is 0: adoption never deposits code and never charges
// code-deposit gas (also not for the surrounding contract creation).
func enable8298(jt *JumpTable) {
	jt[SETCODEFROM] = &operation{
		execute:  opSetCodeFrom,
		minStack: minStack(1, 1),
		maxStack: maxStack(1, 1),
	}
}

// opSetCodeFrom implements SETCODEFROM (0xa7).
//
// Stack: [..., source] -> [..., success], where success is 1 if source is a
// valid source account and the current execution-environment account's code
// hash was set to source's code hash (or already equalled it), and 0 if the
// source is invalid (no state change).
func opSetCodeFrom(pc *uint64, evm *EVM, scope *ScopeContext) ([]byte, error) {
	// Restriction halts precede any gas charge: in a static context, or when
	// the current account is a system contract (i.e. the protocol invoked the
	// current frame from SYSTEM_ADDRESS), the instruction halts without
	// charging SETCODEFROM_SOURCE_GAS and without accessing the source.
	if evm.readOnly {
		return nil, ErrWriteProtection
	}
	if isSystemCall(scope.Contract.Caller()) {
		return nil, ErrSetCodeFromSystemContract
	}

	source := common.Address(scope.Stack.peek().Bytes20())

	// Charge SETCODEFROM_SOURCE_GAS. The source account must not be accessed
	// before this charge succeeds (and is excluded from the BAL if it fails).
	sourceAccess := uint64(params.WarmAccountAccessAmsterdam)
	cold := !evm.StateDB.AddressInAccessList(source)
	if cold {
		sourceAccess = params.ColdAccountAccessAmsterdam
	}
	sourceGas := sourceAccess + uint64(params.WarmAccountAccessAmsterdam)
	if !scope.Contract.chargeRegular(sourceGas, evm.Config.Tracer, tracing.GasChangeCallCodeStorage) {
		return nil, ErrOutOfGas
	}
	// The source is warmed once the source gas is charged, whether or not the
	// source turns out to be valid.
	if cold {
		evm.StateDB.AddAddressToAccessList(source)
	}

	// Validate the source account. The source's code is read raw: an EIP-7702
	// delegation is never resolved, so a delegated source's target is neither
	// accessed, charged nor warmed. A source created in the current
	// transaction (EIP-6780 definition: the account SELFDESTRUCT could still
	// delete) is invalid, as is a precompile or any account without deployed
	// code (EMPTYCODEHASH).
	valid := false
	var (
		sourceHash common.Hash
		sourceCode []byte
	)
	if evm.StateDB.Exist(source) && !evm.StateDB.IsNewContract(source) {
		sourceHash = evm.StateDB.GetCodeHash(source)
		if sourceHash != (common.Hash{}) && sourceHash != types.EmptyCodeHash {
			sourceCode = evm.StateDB.GetCode(source)
			if len(sourceCode) > 0 && sourceCode[0] != 0xEF {
				valid = true
			}
		}
	}
	if !valid {
		// Invalid source: push 0, make no state change, and charge neither
		// the current-account access gas nor the write gas.
		scope.Stack.peek().Clear()
		return nil, nil
	}

	// Valid source: charge SETCODEFROM_CURRENT_ACCESS_GAS before comparing
	// the source code hash with the current account's code hash.
	if !scope.Contract.chargeRegular(uint64(params.WarmAccountAccessAmsterdam), evm.Config.Tracer, tracing.GasChangeCallCodeStorage) {
		return nil, ErrOutOfGas
	}
	self := scope.Contract.Address()
	if evm.StateDB.GetCodeHash(self) == sourceHash {
		// Same code hash: success without the write charge.
		scope.Stack.peek().SetOne()
		return nil, nil
	}

	// Charge SETCODEFROM_WRITE_GAS before updating the code hash.
	if !scope.Contract.chargeRegular(uint64(params.AccountWriteAmsterdam), evm.Config.Tracer, tracing.GasChangeCallCodeStorage) {
		return nil, ErrOutOfGas
	}
	// Adopt: the current account's code hash now references the source's
	// existing bytecode. No bytecode is deposited for the account; the blob
	// remains available via the shared content-addressed code store even if
	// the source later changes its code.
	evm.StateDB.SetCodeHashReference(self, sourceHash, sourceCode)
	scope.Stack.peek().SetOne()
	return nil, nil
}
