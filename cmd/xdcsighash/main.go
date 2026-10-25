package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"golang.org/x/crypto/sha3"
)

func postRPC(url, payload string) ([]byte, error) {
	resp, err := http.Post(url, "application/json", bytes.NewBufferString(payload))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

type rpcBlock struct {
	Number           string `json:"number"`
	Hash             string `json:"hash"`
	ParentHash       string `json:"parentHash"`
	Sha3Uncles       string `json:"sha3Uncles"`
	Miner            string `json:"miner"`
	StateRoot        string `json:"stateRoot"`
	TxRoot           string `json:"transactionsRoot"`
	ReceiptsRoot     string `json:"receiptsRoot"`
	LogsBloom        string `json:"logsBloom"`
	Difficulty       string `json:"difficulty"`
	GasLimit         string `json:"gasLimit"`
	GasUsed          string `json:"gasUsed"`
	Timestamp        string `json:"timestamp"`
	ExtraData        string `json:"extraData"`
	MixHash          string `json:"mixHash"`
	Nonce            string `json:"nonce"`
	Validators       string `json:"validators"`
	Penalties        string `json:"penalties"`
	Validator        string `json:"validator"`
	BaseFeePerGas    string `json:"baseFeePerGas"`
}

func hx(s string) []byte {
	s = strings.TrimPrefix(s, "0x")
	if len(s)%2 == 1 { s = "0" + s }
	b, err := hex.DecodeString(s)
	if err != nil { panic(err) }
	return b
}

func hxBig(s string) *big.Int {
	s = strings.TrimPrefix(s, "0x")
	if s == "" { return big.NewInt(0) }
	n, ok := new(big.Int).SetString(s, 16)
	if !ok { panic("bad hex " + s) }
	return n
}

func keccak256(b []byte) []byte {
	h := sha3.NewLegacyKeccak256()
	h.Write(b)
	return h.Sum(nil)
}

func main() {
	rpcURL := os.Args[1]
	blockNo := os.Args[2] // e.g. 0x2c6f
	payload := fmt.Sprintf(`{"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["%s",false],"id":1}`, blockNo)
	out, err := postRPC(rpcURL, payload)
	if err != nil { panic(err) }
	var res struct{ Result rpcBlock `json:"result"` }
	if err := json.Unmarshal(out, &res); err != nil { panic(err) }
	b := res.Result

	// nethermind ComputeV2SigHash: RLP list in this exact order
	// [parentHash, unclesHash, beneficiary, stateRoot, txRoot, receiptsRoot, bloom(diff-enc),
	//  difficulty, number, gasLimit, gasUsed, timestamp, extraData(raw), mixHash, nonce(8B),
	//  validators(raw), penalties(raw), (baseFee if nonzero)]
	extra := hx(b.ExtraData)
	validators := hx(b.Validators)   // hex string of raw bytes; "0x" -> empty
	penalties := hx(b.Penalties)
	nonceB := hx(b.Nonce) // 8 bytes

	// bloom is a 256-byte value encoded as RLP bytes
	bloom := hx(b.LogsBloom)

	enc := func(v interface{}) []byte {
		e, err := rlp.EncodeToBytes(v)
		if err != nil { panic(err) }
		return e
	}
	var buf []byte
	buf = append(buf, enc(hx(b.ParentHash))...)      // 32B -> RLP bytes
	buf = append(buf, enc(hx(b.Sha3Uncles))...)
	buf = append(buf, enc(hx(b.Miner))...)           // address 20B
	buf = append(buf, enc(hx(b.StateRoot))...)
	buf = append(buf, enc(hx(b.TxRoot))...)
	buf = append(buf, enc(hx(b.ReceiptsRoot))...)
	buf = append(buf, enc(bloom)...)
	buf = append(buf, enc(hxBig(b.Difficulty))...)
	buf = append(buf, enc(hxBig(b.Number))...)
	buf = append(buf, enc(hxBig(b.GasLimit))...)
	buf = append(buf, enc(hxBig(b.GasUsed))...)
	buf = append(buf, enc(hxBig(b.Timestamp))...)
	buf = append(buf, enc(extra)...)
	buf = append(buf, enc(hx(b.MixHash))...)
	buf = append(buf, enc(nonceB)...)                 // 8B -> RLP bytes
	buf = append(buf, enc(validators)...)
	buf = append(buf, enc(penalties)...)
	if b.BaseFeePerGas != "" && hxBig(b.BaseFeePerGas).Sign() != 0 {
		buf = append(buf, enc(hxBig(b.BaseFeePerGas))...)
	}
	// wrap in list
	var payloadLen = len(buf)
	var hdr []byte
	if payloadLen <= 55 {
		hdr = []byte{byte(0xc0 + payloadLen)}
	} else {
		lb := big.NewInt(int64(payloadLen)).Bytes()
		hdr = append([]byte{byte(0xf7 + len(lb))}, lb...)
	}
	sighash := keccak256(append(hdr, buf...))
	fmt.Printf("sighash: 0x%x\n", sighash)

	sig := hx(b.Validator)
	fmt.Printf("validator sig len: %d\n", len(sig))
	if len(sig) != 65 { panic("sig len") }
	// nethermind RecoverAddress: v handling — check both raw and -27
	for _, adj := range []int{0, 27} {
		s := make([]byte, 65)
		copy(s, sig)
		s[64] = sig[64] - byte(adj)
		pub, err := crypto.Ecrecover(sighash, s)
		if err != nil { fmt.Printf("adj=%d err %v\n", adj, err); continue }
		addr := common.BytesToAddress(keccak256(pub[1:])[12:])
		fmt.Printf("adj=%d recovered signer: %s\n", adj, addr.Hex())
	}
	fmt.Println("beneficiary:", b.Miner)
}
