package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"
)

func main() {
	rpcURL, blockNo := os.Args[1], os.Args[2]
	payload := fmt.Sprintf(`{"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["%s",true],"id":1}`, blockNo)
	resp, err := http.Post(rpcURL, "application/json", bytes.NewBufferString(payload))
	if err != nil { panic(err) }
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil { panic(err) }
	var env struct {
		Result struct {
			Header       json.RawMessage     `json:"-"`
			Transactions []types.Transaction `json:"transactions"`
		} `json:"result"`
	}
	// unmarshal header from the whole result object
	var top struct{ Result json.RawMessage `json:"result"` }
	if err := json.Unmarshal(raw, &top); err != nil { panic(err) }
	var head types.Header
	if err := json.Unmarshal(top.Result, &head); err != nil { panic(err) }
	var txHolder struct {
		Transactions []*types.Transaction `json:"transactions"`
	}
	if err := json.Unmarshal(top.Result, &txHolder); err != nil { panic(err) }
	txs := make([]*types.Transaction, 0, len(txHolder.Transactions))
	txs = append(txs, txHolder.Transactions...)
	body := types.Body{Transactions: txs, Uncles: nil}
	blk := types.NewBlockWithHeader(&head).WithBody(body)
	var buf bytes.Buffer
	if err := rlp.Encode(&buf, blk); err != nil { panic(err) }
	fmt.Printf("0x%x\n", buf.Bytes())
	fmt.Fprintf(os.Stderr, "block %d hash %s txs %d\n", head.Number, head.Hash(), len(txs))
	_ = env
}
