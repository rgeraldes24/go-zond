// Copyright 2021 The go-ethereum Authors
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

package tracetest

import (
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theQRL/go-zond/common"
	"github.com/theQRL/go-zond/core"
	"github.com/theQRL/go-zond/core/rawdb"
	"github.com/theQRL/go-zond/core/types"
	"github.com/theQRL/go-zond/core/vm"
	"github.com/theQRL/go-zond/crypto/pqcrypto"
	"github.com/theQRL/go-zond/params"
	"github.com/theQRL/go-zond/tests"
	"github.com/theQRL/go-zond/zond/tracers"
)

// prestateTrace is the result of a prestateTrace run.
type prestateTrace = map[common.Address]*account

type account struct {
	Balance string                      `json:"balance"`
	Code    string                      `json:"code"`
	Nonce   uint64                      `json:"nonce"`
	Storage map[common.Hash]common.Hash `json:"storage"`
}

// testcase defines a single test to check the stateDiff tracer against.
type testcase struct {
	Genesis      *core.Genesis   `json:"genesis"`
	Context      *callContext    `json:"context"`
	Input        string          `json:"input"`
	TracerConfig json.RawMessage `json:"tracerConfig"`
	Result       interface{}     `json:"result"`
}

func TestPrestateTracer(t *testing.T) {
	testPrestateDiffTracer("prestateTracer", "prestate_tracer", t)
}

func TestPrestateWithDiffModeTracer(t *testing.T) {
	key, err := pqcrypto.HexToDilithium("12345678")
	if err != nil {
		log.Fatal(err)
	}
	signer := types.LatestSigner(&params.ChainConfig{ChainID: big.NewInt(12345)})
	// to := common.HexToAddress("0xFfa397285Ce46FB78C588a9e993286AaC68c37cD")
	tx := types.NewTx(&types.DynamicFeeTx{
		ChainID: signer.ChainID(),
		Nonce:   64,
		Value:   common.Big0,
		// To:      &to,
		Data:      common.Hex2Bytes("608060405234801561001057600080fd5b50610234806100206000396000f3fe608060405234801561001057600080fd5b50600436106100365760003560e01c806309ce9ccb1461003b5780633fb5c1cb14610059575b600080fd5b610043610075565b60405161005091906100e2565b60405180910390f35b610073600480360381019061006e919061012e565b61007b565b005b60005481565b80600081905550600a8111156100c6576040517f08c379a00000000000000000000000000000000000000000000000000000000081526004016100bd906101de565b60405180910390fd5b50565b6000819050919050565b6100dc816100c9565b82525050565b60006020820190506100f760008301846100d3565b92915050565b600080fd5b61010b816100c9565b811461011657600080fd5b50565b60008135905061012881610102565b92915050565b600060208284031215610144576101436100fd565b5b600061015284828501610119565b91505092915050565b600082825260208201905092915050565b7f4e756d6265722069732067726561746572207468616e2031302c207472616e7360008201527f616374696f6e2072657665727465642e00000000000000000000000000000000602082015250565b60006101c860308361015b565b91506101d38261016c565b604082019050919050565b600060208201905081810360008301526101f7816101bb565b905091905056fea264697066735822122069018995fecf03bda91a88b6eafe41641709dee8b4a706fe301c8a569fe8c1b364736f6c63430008130033"),
		GasFeeCap: big.NewInt(50000000000),
		// GasFeeCap: big.NewInt(51088069741),
		Gas: 176979,
		// Gas: 2100000,
	})
	signedTx, err := types.SignTx(tx, signer, key)
	if err != nil {
		log.Fatal(err)
	}
	rawTx, err := signedTx.MarshalBinary()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(common.Bytes2Hex(rawTx))

	testPrestateDiffTracer("prestateTracer", "prestate_tracer_with_diff_mode", t)
}

func testPrestateDiffTracer(tracerName string, dirPath string, t *testing.T) {
	files, err := os.ReadDir(filepath.Join("testdata", dirPath))
	if err != nil {
		t.Fatalf("failed to retrieve tracer test suite: %v", err)
	}
	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		file := file // capture range variable
		t.Run(camel(strings.TrimSuffix(file.Name(), ".json")), func(t *testing.T) {
			t.Parallel()

			var (
				test = new(testcase)
				tx   = new(types.Transaction)
			)
			// Call tracer test found, read if from disk
			if blob, err := os.ReadFile(filepath.Join("testdata", dirPath, file.Name())); err != nil {
				t.Fatalf("failed to read testcase: %v", err)
			} else if err := json.Unmarshal(blob, test); err != nil {
				t.Fatalf("failed to parse testcase: %v", err)
			}
			if err := tx.UnmarshalBinary(common.FromHex(test.Input)); err != nil {
				t.Fatalf("failed to parse testcase input: %v", err)
			}
			// Configure a blockchain with the given prestate
			var (
				signer    = types.MakeSigner(test.Genesis.Config)
				origin, _ = signer.Sender(tx)
				txContext = vm.TxContext{
					Origin:   origin,
					GasPrice: tx.GasPrice(),
				}
				context = vm.BlockContext{
					CanTransfer: core.CanTransfer,
					Transfer:    core.Transfer,
					Coinbase:    test.Context.Miner,
					BlockNumber: new(big.Int).SetUint64(uint64(test.Context.Number)),
					Time:        uint64(test.Context.Time),
					GasLimit:    uint64(test.Context.GasLimit),
					BaseFee:     test.Genesis.BaseFee,
				}
				triedb, _, statedb = tests.MakePreState(rawdb.NewMemoryDatabase(), test.Genesis.Alloc, false, rawdb.HashScheme)
			)
			defer triedb.Close()

			tracer, err := tracers.DefaultDirectory.New(tracerName, new(tracers.Context), test.TracerConfig)
			if err != nil {
				t.Fatalf("failed to create call tracer: %v", err)
			}
			evm := vm.NewEVM(context, txContext, statedb, test.Genesis.Config, vm.Config{Tracer: tracer})
			msg, err := core.TransactionToMessage(tx, signer, nil)
			if err != nil {
				t.Fatalf("failed to prepare transaction for tracing: %v", err)
			}
			st := core.NewStateTransition(evm, msg, new(core.GasPool).AddGas(tx.Gas()))
			if _, err = st.TransitionDb(); err != nil {
				t.Fatalf("failed to execute transaction: %v", err)
			}
			// Retrieve the trace result and compare against the expected
			res, err := tracer.GetResult()
			if err != nil {
				t.Fatalf("failed to retrieve trace result: %v", err)
			}
			want, err := json.Marshal(test.Result)
			if err != nil {
				t.Fatalf("failed to marshal test: %v", err)
			}
			if string(want) != string(res) {
				t.Fatalf("trace mismatch\n have: %v\n want: %v\n", string(res), string(want))
			}
		})
	}
}
