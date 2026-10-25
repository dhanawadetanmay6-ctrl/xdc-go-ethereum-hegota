package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/ethereum/go-ethereum/consensus/XDPoS/utils"
	"github.com/ethereum/go-ethereum/core/types"
)

func getHex(rpcURL string, blockNo uint64) string {
	payload := fmt.Sprintf(`{"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["0x%x",false],"id":1}`, blockNo)
	resp, err := http.Post(rpcURL, "application/json", bytes.NewBufferString(payload))
	if err != nil { panic(err) }
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var env struct {
		Result struct {
			ExtraData string `json:"extraData"`
			Hash      string `json:"hash"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil { panic(err) }
	return env.Result.ExtraData
}

func main() {
	rpcURL := os.Args[1]
	for _, arg := range os.Args[2:] {
		n, _ := strconv.ParseUint(arg, 0, 64)
		extraHex := getHex(rpcURL, n)
		extra, err := hex.DecodeString(extraHex[2:])
		if err != nil { panic(err) }
		var ef types.ExtraFields_v2
		if err := utils.DecodeBytesExtraFields(extra, &ef); err != nil {
			fmt.Printf("block %d: decode error %v\n", n, err)
			continue
		}
		qc := ef.QuorumCert
		fmt.Printf("block %d: round=%d QC.proposed=(num=%d round=%d hash=%s) QC.gap=%d sigs=%d\n",
			n, ef.Round, qc.ProposedBlockInfo.Number, qc.ProposedBlockInfo.Round,
			qc.ProposedBlockInfo.Hash.Hex()[:18], qc.GapNumber, len(qc.Signatures))
	}
}
