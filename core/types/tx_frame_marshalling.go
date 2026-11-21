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

// JSON marshalling support for EIP-8141 frame transactions.

package types

import (
	"encoding/json"
	"errors"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/holiman/uint256"
)

// frameJSON is the JSON representation of one frame.
type frameJSON struct {
	Mode          hexutil.Uint64  `json:"mode"`
	Flags         hexutil.Uint64  `json:"flags"`
	Target        *common.Address `json:"target"`
	GasLimit      hexutil.Uint64  `json:"gasLimit"`
	StateGasLimit hexutil.Uint64  `json:"stateGasLimit"`
	Value         *hexutil.Big    `json:"value"`
	Data          hexutil.Bytes   `json:"data"`
}

// MarshalJSON marshals a frame to JSON.
func (f Frame) MarshalJSON() ([]byte, error) {
	var value hexutil.Big
	if f.Value != nil {
		value = (hexutil.Big)(*f.Value.ToBig())
	}
	return json.Marshal(&frameJSON{
		Mode:          hexutil.Uint64(f.Mode),
		Flags:         hexutil.Uint64(f.Flags),
		Target:        f.Target,
		GasLimit:      hexutil.Uint64(f.Limits.Execution),
		StateGasLimit: hexutil.Uint64(f.Limits.State),
		Value:         &value,
		Data:          f.Data,
	})
}

// UnmarshalJSON unmarshals a frame from JSON.
func (f *Frame) UnmarshalJSON(input []byte) error {
	var dec frameJSON
	if err := json.Unmarshal(input, &dec); err != nil {
		return err
	}
	if dec.Value == nil {
		return errors.New("missing required field 'value' in frame")
	}
	f.Mode = uint8(dec.Mode)
	f.Flags = uint8(dec.Flags)
	f.Target = dec.Target
	f.Limits = FrameGasLimits{Execution: uint64(dec.GasLimit), State: uint64(dec.StateGasLimit)}
	f.Value = uint256.MustFromBig(dec.Value.ToInt())
	f.Data = dec.Data
	return nil
}

// frameSignatureJSON is the JSON representation of one signature entry.
type frameSignatureJSON struct {
	Scheme    hexutil.Uint64 `json:"scheme"`
	Signer    hexutil.Bytes  `json:"signer"`
	Msg       hexutil.Bytes  `json:"msg"`
	Signature hexutil.Bytes  `json:"signature"`
}

// MarshalJSON marshals a frame signature entry to JSON.
func (s FrameSignature) MarshalJSON() ([]byte, error) {
	return json.Marshal(&frameSignatureJSON{
		Scheme:    hexutil.Uint64(s.Scheme),
		Signer:    s.Signer,
		Msg:       s.Msg,
		Signature: s.Signature,
	})
}

// UnmarshalJSON unmarshals a frame signature entry from JSON.
func (s *FrameSignature) UnmarshalJSON(input []byte) error {
	var dec frameSignatureJSON
	if err := json.Unmarshal(input, &dec); err != nil {
		return err
	}
	s.Scheme = uint8(dec.Scheme)
	s.Signer = dec.Signer
	s.Msg = dec.Msg
	s.Signature = dec.Signature
	return nil
}
