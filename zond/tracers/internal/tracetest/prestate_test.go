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

// TODO(rgeraldes24): for the 'create_existing_contract' testcase check why the nonce: 1 is added
// Do same test in go-ethereum for post shanghai to see if its expected
func TestPrestateTracer(t *testing.T) {
	testPrestateDiffTracer("prestateTracer", "prestate_tracer", t)
}

// TODO(rgeraldes24): for the 'create' testcase check why the nonce: 1 is added and the miner does not show up
// Do same test in go-ethereum for post shanghai to see if its expected
func TestPrestateWithDiffModeTracer(t *testing.T) {
	/*
		key, err := pqcrypto.HexToDilithium("12345678")
		if err != nil {
			log.Fatal(err)
		}
		signer := types.LatestSigner(&params.ChainConfig{ChainID: big.NewInt(1)})
		// to := common.HexToAddress("0x82effbaaaf28614e55b2ba440fb198e0e5789b0f")
		tx := types.NewTx(&types.DynamicFeeTx{
			ChainID: signer.ChainID(),
			Nonce:   747,
			Value:   common.Big0,
			// To:        &to,
			Data:      common.Hex2Bytes("60606040526040516102b43803806102b48339016040526060805160600190602001505b5b33600060006101000a81548173ffffffffffffffffffffffffffffffffffffffff021916908302179055505b806001600050908051906020019082805482825590600052602060002090601f01602090048101928215609e579182015b82811115609d5782518260005055916020019190600101906081565b5b50905060c5919060a9565b8082111560c1576000818150600090555060010160a9565b5090565b50505b506101dc806100d86000396000f30060606040526000357c01000000000000000000000000000000000000000000000000000000009004806341c0e1b514610044578063cfae32171461005157610042565b005b61004f6004506100ca565b005b61005c60045061015e565b60405180806020018281038252838181518152602001915080519060200190808383829060006004602084601f0104600302600f01f150905090810190601f1680156100bc5780820380516001836020036101000a031916815260200191505b509250505060405180910390f35b600060009054906101000a900473ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff163373ffffffffffffffffffffffffffffffffffffffff16141561015b57600060009054906101000a900473ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff16ff5b5b565b60206040519081016040528060008152602001506001600050805480601f016020809104026020016040519081016040528092919081815260200182805480156101cd57820191906000526020600020905b8154815290600101906020018083116101b057829003601f168201915b505050505090506101d9565b9056000000000000000000000000000000000000000000000000000000000000002000000000000000000000000000000000000000000000000000000000000001ee7b225f6964223a225a473466784a7245323639384866623839222c22666f726d5f736f75726365223a22434c54523031222c22636f6d6d69746d656e745f64617465223a22222c22626f72726f7765725f6e616d65223a22222c22626f72726f7765725f616464726573735f6c696e6531223a22222c22626f72726f7765725f616464726573735f6c696e6532223a22222c22626f72726f7765725f636f6e74616374223a22222c22626f72726f7765725f7374617465223a22222c22626f72726f7765725f74797065223a22222c2270726f70657274795f61646472657373223a22222c226c6f616e5f616d6f756e745f7772697474656e223a22222c226c6f616e5f616d6f756e74223a22222c224c54565f7772697474656e223a22222c224c5456223a22222c2244534352223a22222c2270726f70657274795f74797065223a22222c2270726f70657274795f6465736372697074696f6e223a22222c226c656e646572223a22222c2267756172616e746f7273223a22222c226c696d69746564223a22222c226361705f616d6f756e74223a22222c226361705f70657263656e745f7772697474656e223a22222c226361705f70657263656e74616765223a22222c227465726d5f7772697474656e223a22222c227465726d223a22222c22657874656e64223a22227d000000000000000000000000000000000000"),
			GasFeeCap: big.NewInt(50000000000),
			Gas:       1000000,
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
	*/

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
