package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"
)

func main() {
	data, _ := os.ReadFile(os.Args[1])
	raw, err := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(string(data)), "0x"))
	if err != nil { panic(err) }
	var blk types.Block
	if err := rlp.DecodeBytes(raw, &blk); err != nil { panic(err) }
	fmt.Println("block", blk.NumberU64(), "hash", blk.Hash(), "txs", len(blk.Transactions()))
	for _, tx := range blk.Transactions() {
		v, r, s := tx.RawSignatureValues()
		fmt.Printf("tx %s nonce %d gas %d price %s to %s value %s v=%s r=%s s=%s\n",
			tx.Hash(), tx.Nonce(), tx.Gas(), tx.GasPrice(), tx.To(), tx.Value(), v, r, s)
	}
	_ = bytes.MinRead
}
