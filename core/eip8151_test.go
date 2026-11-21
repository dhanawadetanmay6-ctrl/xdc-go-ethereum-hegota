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
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

var (
	eip8151CorePrecompile = common.BytesToAddress([]byte{0x01})
	eip8151CoreRecovered  = common.HexToAddress("0xceaccac640adf55b2028469bd36ba501f28b699d")
	eip8151CoreInput      = common.Hex2Bytes("38d18acb67d25c8bb9942764b62f18e17054f66a817bd4295423adf9ed98873e000000000000000000000000000000000000000000000000000000000000001b38d18acb67d25c8bb9942764b62f18e17054f66a817bd4295423adf9ed98873e789d1dd423d25f0772d2748d60f7e4b81bb14d086eba8e8e8efb6dcff8a4ae02")
)

func newEIP8151BALTestEnv(extra types.GenesisAlloc) *balTestEnv {
	env := newBALTestEnv(extra)
	env.cfg.BogotaTime = new(uint64)
	env.signer = types.LatestSigner(env.cfg)
	return env
}

// eip8151CoreCallerCode places the ecRecover input in memory and performs a
// STATICCALL with a fixed gas allowance. It either stops normally or reverts
// after the precompile returns.
func eip8151CoreCallerCode(input []byte, gas uint64, revert bool) []byte {
	input = common.RightPadBytes(input, 128)
	var code []byte
	for i := 0; i < 4; i++ {
		code = append(code, 0x7f)
		code = append(code, input[i*32:(i+1)*32]...)
		code = append(code, 0x60, byte(i*32), 0x52)
	}
	code = append(code,
		0x60, 0x00, // retSize
		0x60, 0x00, // retOffset
		0x60, 0x80, // argsSize
		0x60, 0x00, // argsOffset
		0x73, // PUSH20 ecRecover
	)
	code = append(code, eip8151CorePrecompile.Bytes()...)
	code = append(code, 0x61, byte(gas>>8), byte(gas), 0xfa, 0x50) // PUSH2 gas; STATICCALL; POP
	if revert {
		return append(code, 0x60, 0x00, 0x60, 0x00, 0xfd) // REVERT(0, 0)
	}
	return append(code, 0x00)
}

func TestEIP8151BlockAccessList(t *testing.T) {
	t.Run("delegation read does not load target", func(t *testing.T) {
		delegate := common.HexToAddress("0x1000000000000000000000000000000000000001")
		env := newEIP8151BALTestEnv(types.GenesisAlloc{
			eip8151CoreRecovered: {Code: types.AddressToDelegation(delegate), Balance: common.Big0},
			delegate:             {Code: []byte{0xfe}, Balance: common.Big0},
		})
		access, receipts := env.run(t, func(g *BlockGen) {
			g.AddTx(env.tx(0, &eip8151CorePrecompile, big.NewInt(0), 100_000, 0, eip8151CoreInput))
		})
		if len(receipts) != 1 || receipts[0].Status != types.ReceiptStatusSuccessful {
			t.Fatalf("unexpected receipts: %+v", receipts)
		}
		assertEmpty(t, assertPresent(t, access, eip8151CoreRecovered))
		assertAbsent(t, access, delegate)
	})

	t.Run("rejected code retains empty read", func(t *testing.T) {
		env := newEIP8151BALTestEnv(types.GenesisAlloc{
			eip8151CoreRecovered: {Code: []byte{0xfe}, Balance: common.Big0},
		})
		access, receipts := env.run(t, func(g *BlockGen) {
			g.AddTx(env.tx(0, &eip8151CorePrecompile, big.NewInt(0), 100_000, 0, eip8151CoreInput))
		})
		if len(receipts) != 1 || receipts[0].Status != types.ReceiptStatusSuccessful {
			t.Fatalf("rejected code must not fail the call: %+v", receipts)
		}
		assertEmpty(t, assertPresent(t, access, eip8151CoreRecovered))
	})

	t.Run("recovery failure makes no account read", func(t *testing.T) {
		invalid := bytes.Clone(eip8151CoreInput)
		invalid[63] = 0
		env := newEIP8151BALTestEnv(types.GenesisAlloc{
			eip8151CoreRecovered: {Code: []byte{0xfe}, Balance: common.Big0},
		})
		access, receipts := env.run(t, func(g *BlockGen) {
			g.AddTx(env.tx(0, &eip8151CorePrecompile, big.NewInt(0), 100_000, 0, invalid))
		})
		if len(receipts) != 1 || receipts[0].Status != types.ReceiptStatusSuccessful {
			t.Fatalf("recovery failure must not fail the call: %+v", receipts)
		}
		assertAbsent(t, access, eip8151CoreRecovered)
	})

	t.Run("code check out of gas makes no account read", func(t *testing.T) {
		caller := common.HexToAddress("0xca11e7")
		env := newEIP8151BALTestEnv(types.GenesisAlloc{
			caller: {Code: eip8151CoreCallerCode(eip8151CoreInput, 3000, false), Balance: common.Big0},
		})
		access, receipts := env.run(t, func(g *BlockGen) {
			g.AddTx(env.tx(0, &caller, big.NewInt(0), 1_000_000, 0, nil))
		})
		if len(receipts) != 1 || receipts[0].Status != types.ReceiptStatusSuccessful {
			t.Fatalf("outer caller must survive child OOG: %+v", receipts)
		}
		assertAbsent(t, access, eip8151CoreRecovered)
	})

	t.Run("outer revert retains account read", func(t *testing.T) {
		caller := common.HexToAddress("0xca11e7")
		env := newEIP8151BALTestEnv(types.GenesisAlloc{
			caller: {Code: eip8151CoreCallerCode(eip8151CoreInput, 10_000, true), Balance: common.Big0},
		})
		access, receipts := env.run(t, func(g *BlockGen) {
			g.AddTx(env.tx(0, &caller, big.NewInt(0), 1_000_000, 0, nil))
		})
		if len(receipts) != 1 || receipts[0].Status != types.ReceiptStatusFailed {
			t.Fatalf("outer caller should have reverted: %+v", receipts)
		}
		assertEmpty(t, assertPresent(t, access, eip8151CoreRecovered))
	})
}
