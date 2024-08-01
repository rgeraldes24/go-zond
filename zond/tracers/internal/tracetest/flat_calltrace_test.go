package tracetest

import (
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/theQRL/go-zond/common"
	"github.com/theQRL/go-zond/common/hexutil"
	"github.com/theQRL/go-zond/core"
	"github.com/theQRL/go-zond/core/rawdb"
	"github.com/theQRL/go-zond/core/types"
	"github.com/theQRL/go-zond/core/vm"
	"github.com/theQRL/go-zond/crypto/pqcrypto"
	"github.com/theQRL/go-zond/params"
	"github.com/theQRL/go-zond/rlp"
	"github.com/theQRL/go-zond/tests"

	// Force-load the native, to trigger registration
	"github.com/theQRL/go-zond/zond/tracers"
)

// flatCallTrace is the result of a callTracerParity run.
type flatCallTrace struct {
	Action              flatCallTraceAction `json:"action"`
	BlockHash           common.Hash         `json:"-"`
	BlockNumber         uint64              `json:"-"`
	Error               string              `json:"error,omitempty"`
	Result              flatCallTraceResult `json:"result,omitempty"`
	Subtraces           int                 `json:"subtraces"`
	TraceAddress        []int               `json:"traceAddress"`
	TransactionHash     common.Hash         `json:"-"`
	TransactionPosition uint64              `json:"-"`
	Type                string              `json:"type"`
	Time                string              `json:"-"`
}

type flatCallTraceAction struct {
	Author         common.Address `json:"author,omitempty"`
	RewardType     string         `json:"rewardType,omitempty"`
	SelfDestructed common.Address `json:"address,omitempty"`
	Balance        hexutil.Big    `json:"balance,omitempty"`
	CallType       string         `json:"callType,omitempty"`
	CreationMethod string         `json:"creationMethod,omitempty"`
	From           common.Address `json:"from,omitempty"`
	Gas            hexutil.Uint64 `json:"gas,omitempty"`
	Init           hexutil.Bytes  `json:"init,omitempty"`
	Input          hexutil.Bytes  `json:"input,omitempty"`
	RefundAddress  common.Address `json:"refundAddress,omitempty"`
	To             common.Address `json:"to,omitempty"`
	Value          hexutil.Big    `json:"value,omitempty"`
}

type flatCallTraceResult struct {
	Address common.Address `json:"address,omitempty"`
	Code    hexutil.Bytes  `json:"code,omitempty"`
	GasUsed hexutil.Uint64 `json:"gasUsed,omitempty"`
	Output  hexutil.Bytes  `json:"output,omitempty"`
}

// flatCallTracerTest defines a single test to check the call tracer against.
type flatCallTracerTest struct {
	Genesis      core.Genesis    `json:"genesis"`
	Context      callContext     `json:"context"`
	Input        string          `json:"input"`
	TracerConfig json.RawMessage `json:"tracerConfig"`
	Result       []flatCallTrace `json:"result"`
}

func flatCallTracerTestRunner(tracerName string, filename string, dirPath string, t testing.TB) error {
	// Call tracer test found, read if from disk
	blob, err := os.ReadFile(filepath.Join("testdata", dirPath, filename))
	if err != nil {
		return fmt.Errorf("failed to read testcase: %v", err)
	}
	test := new(flatCallTracerTest)
	if err := json.Unmarshal(blob, test); err != nil {
		return fmt.Errorf("failed to parse testcase: %v", err)
	}
	// Configure a blockchain with the given prestate
	tx := new(types.Transaction)
	if err := rlp.DecodeBytes(common.FromHex(test.Input), tx); err != nil {
		return fmt.Errorf("failed to parse testcase input: %v", err)
	}
	signer := types.MakeSigner(test.Genesis.Config)
	origin, _ := signer.Sender(tx)
	txContext := vm.TxContext{
		Origin:   origin,
		GasPrice: tx.GasPrice(),
	}
	context := vm.BlockContext{
		CanTransfer: core.CanTransfer,
		Transfer:    core.Transfer,
		Coinbase:    test.Context.Miner,
		BlockNumber: new(big.Int).SetUint64(uint64(test.Context.Number)),
		Time:        uint64(test.Context.Time),
		GasLimit:    uint64(test.Context.GasLimit),
		BaseFee:     big.NewInt(params.InitialBaseFee),
	}
	triedb, _, statedb := tests.MakePreState(rawdb.NewMemoryDatabase(), test.Genesis.Alloc, false, rawdb.HashScheme)
	defer triedb.Close()

	// Create the tracer, the EVM environment and run it
	tracer, err := tracers.DefaultDirectory.New(tracerName, new(tracers.Context), test.TracerConfig)
	if err != nil {
		return fmt.Errorf("failed to create call tracer: %v", err)
	}
	evm := vm.NewEVM(context, txContext, statedb, test.Genesis.Config, vm.Config{Tracer: tracer})

	msg, err := core.TransactionToMessage(tx, signer, nil)
	if err != nil {
		return fmt.Errorf("failed to prepare transaction for tracing: %v", err)
	}
	st := core.NewStateTransition(evm, msg, new(core.GasPool).AddGas(tx.Gas()))

	if _, err = st.TransitionDb(); err != nil {
		return fmt.Errorf("failed to execute transaction: %v", err)
	}

	// Retrieve the trace result and compare against the etalon
	res, err := tracer.GetResult()
	if err != nil {
		return fmt.Errorf("failed to retrieve trace result: %v", err)
	}
	ret := make([]flatCallTrace, 0)
	if err := json.Unmarshal(res, &ret); err != nil {
		return fmt.Errorf("failed to unmarshal trace result: %v", err)
	}
	if !jsonEqualFlat(ret, test.Result) {
		t.Logf("tracer name: %s", tracerName)

		// uncomment this for easier debugging
		// have, _ := json.MarshalIndent(ret, "", " ")
		// want, _ := json.MarshalIndent(test.Result, "", " ")
		// t.Logf("trace mismatch: \nhave %+v\nwant %+v", string(have), string(want))

		// uncomment this for harder debugging <3 meowsbits
		// lines := deep.Equal(ret, test.Result)
		// for _, l := range lines {
		// 	t.Logf("%s", l)
		// 	t.FailNow()
		// }

		t.Fatalf("trace mismatch: \nhave %+v\nwant %+v", ret, test.Result)
	}
	return nil
}

// Iterates over all the input-output datasets in the tracer parity test harness and
// runs the Native tracer against them.
func TestFlatCallTracerNative(t *testing.T) {

	key, err := pqcrypto.HexToDilithium("12345678")
	if err != nil {
		log.Fatal(err)
	}
	signer := types.LatestSigner(&params.ChainConfig{ChainID: big.NewInt(3)})
	// to := common.HexToAddress("0x269296dddce321a6bcbaa2f0181127593d732cba")
	tx := types.NewTx(&types.DynamicFeeTx{
		ChainID: signer.ChainID(),
		Nonce:   9,
		// Value:     big.NewInt(1050000000000000000),
		Value: common.Big0,
		// To:        &to,
		Data:      common.Hex2Bytes("606060405260405160208061077c83398101604052808051906020019091905050600160009054906101000a900473ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff163373ffffffffffffffffffffffffffffffffffffffff161415151561007d57600080fd5b336000806101000a81548173ffffffffffffffffffffffffffffffffffffffff021916908373ffffffffffffffffffffffffffffffffffffffff16021790555080600160006101000a81548173ffffffffffffffffffffffffffffffffffffffff021916908373ffffffffffffffffffffffffffffffffffffffff1602179055506001600460006101000a81548160ff02191690831515021790555050610653806101296000396000f300606060405260043610610083576000357c0100000000000000000000000000000000000000000000000000000000900463ffffffff16806305e4382a146100855780631c02708d146100ae5780632e1a7d4d146100c35780635114cb52146100e6578063a37dda2c146100fe578063ae200e7914610153578063b5769f70146101a8575b005b341561009057600080fd5b6100986101d1565b6040518082815260200191505060405180910390f35b34156100b957600080fd5b6100c16101d7565b005b34156100ce57600080fd5b6100e460048080359060200190919050506102eb565b005b6100fc6004808035906020019091905050610513565b005b341561010957600080fd5b6101116105d6565b604051808273ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff16815260200191505060405180910390f35b341561015e57600080fd5b6101666105fc565b604051808273ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff16815260200191505060405180910390f35b34156101b357600080fd5b6101bb610621565b6040518082815260200191505060405180910390f35b60025481565b60011515600460009054906101000a900460ff1615151415156101f957600080fd5b6000809054906101000a900473ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff163373ffffffffffffffffffffffffffffffffffffffff1614806102a15750600160009054906101000a900473ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff163373ffffffffffffffffffffffffffffffffffffffff16145b15156102ac57600080fd5b6000600460006101000a81548160ff0219169083151502179055506003543073ffffffffffffffffffffffffffffffffffffffff163103600281905550565b6000809054906101000a900473ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff163373ffffffffffffffffffffffffffffffffffffffff1614806103935750600160009054906101000a900473ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff163373ffffffffffffffffffffffffffffffffffffffff16145b151561039e57600080fd5b6000809054906101000a900473ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff163373ffffffffffffffffffffffffffffffffffffffff16141561048357600060025411801561040757506002548111155b151561041257600080fd5b80600254036002819055506000809054906101000a900473ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff166108fc829081150290604051600060405180830381858888f19350505050151561047e57600080fd5b610510565b600060035411801561049757506003548111155b15156104a257600080fd5b8060035403600381905550600160009054906101000a900473ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff166108fc829081150290604051600060405180830381858888f19350505050151561050f57600080fd5b5b50565b60011515600460009054906101000a900460ff16151514151561053557600080fd5b6000809054906101000a900473ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff163373ffffffffffffffffffffffffffffffffffffffff1614801561059657506003548160035401115b80156105bd575080600354013073ffffffffffffffffffffffffffffffffffffffff163110155b15156105c857600080fd5b806003540160038190555050565b600160009054906101000a900473ffffffffffffffffffffffffffffffffffffffff1681565b6000809054906101000a900473ffffffffffffffffffffffffffffffffffffffff1681565b600354815600a165627a7a72305820c3b849e8440987ce43eae3097b77672a69234d516351368b03fe5b7de03807910029000000000000000000000000c65e620a3a55451316168d57e268f5702ef56a11"),
		GasFeeCap: big.NewInt(50000000000),
		Gas:       1000000,
	})
	signedTx, err := types.SignTx(tx, signer, key)
	if err != nil {
		log.Fatal(err)
	}
	rawTx, err := rlp.EncodeToBytes(signedTx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(common.Bytes2Hex(rawTx))

	testFlatCallTracer("flatCallTracer", "call_tracer_flat", t)
}

func testFlatCallTracer(tracerName string, dirPath string, t *testing.T) {
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

			err := flatCallTracerTestRunner(tracerName, file.Name(), dirPath, t)
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

// jsonEqual is similar to reflect.DeepEqual, but does a 'bounce' via json prior to
// comparison
func jsonEqualFlat(x, y interface{}) bool {
	xTrace := new([]flatCallTrace)
	yTrace := new([]flatCallTrace)
	if xj, err := json.Marshal(x); err == nil {
		json.Unmarshal(xj, xTrace)
	} else {
		return false
	}
	if yj, err := json.Marshal(y); err == nil {
		json.Unmarshal(yj, yTrace)
	} else {
		return false
	}
	return reflect.DeepEqual(xTrace, yTrace)
}

func BenchmarkFlatCallTracer(b *testing.B) {
	files, err := filepath.Glob("testdata/call_tracer_flat/*.json")
	if err != nil {
		b.Fatalf("failed to read testdata: %v", err)
	}

	for _, file := range files {
		filename := strings.TrimPrefix(file, "testdata/call_tracer_flat/")
		b.Run(camel(strings.TrimSuffix(filename, ".json")), func(b *testing.B) {
			for n := 0; n < b.N; n++ {
				err := flatCallTracerTestRunner("flatCallTracer", filename, "call_tracer_flat", b)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
