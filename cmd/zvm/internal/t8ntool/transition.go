// Copyright 2020 The go-ethereum Authors
// This file is part of go-ethereum.
//
// go-ethereum is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// go-ethereum is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with go-ethereum. If not, see <http://www.gnu.org/licenses/>.

package t8ntool

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path"
	"strings"

	"github.com/theQRL/go-qrllib/crypto/ml_dsa_87"
	"github.com/theQRL/go-zond/common"
	"github.com/theQRL/go-zond/common/hexutil"
	"github.com/theQRL/go-zond/consensus/misc/eip1559"
	"github.com/theQRL/go-zond/core"
	"github.com/theQRL/go-zond/core/state"
	"github.com/theQRL/go-zond/core/types"
	"github.com/theQRL/go-zond/core/vm"
	"github.com/theQRL/go-zond/crypto/pqcrypto"
	"github.com/theQRL/go-zond/log"
	"github.com/theQRL/go-zond/params"
	"github.com/theQRL/go-zond/rlp"
	"github.com/theQRL/go-zond/tests"
	"github.com/theQRL/go-zond/zond/tracers/logger"
	"github.com/urfave/cli/v2"
)

const (
	ErrorZVM              = 2
	ErrorConfig           = 3
	ErrorMissingBlockhash = 4

	ErrorJson = 10
	ErrorIO   = 11
	ErrorRlp  = 12

	stdinSelector = "stdin"
)

type NumberedError struct {
	errorCode int
	err       error
}

func NewError(errorCode int, err error) *NumberedError {
	return &NumberedError{errorCode, err}
}

func (n *NumberedError) Error() string {
	return fmt.Sprintf("ERROR(%d): %v", n.errorCode, n.err.Error())
}

func (n *NumberedError) ExitCode() int {
	return n.errorCode
}

// compile-time conformance test
var (
	_ cli.ExitCoder = (*NumberedError)(nil)
)

type input struct {
	Alloc core.GenesisAlloc `json:"alloc,omitempty"`
	Env   *stEnv            `json:"env,omitempty"`
	Txs   []*txWithKey      `json:"txs,omitempty"`
	TxRlp string            `json:"txsRlp,omitempty"`
}

func Transition(ctx *cli.Context) error {
	// Configure the go-ethereum logger
	glogger := log.NewGlogHandler(log.StreamHandler(os.Stderr, log.TerminalFormat(false)))
	glogger.Verbosity(log.Lvl(ctx.Int(VerbosityFlag.Name)))
	log.Root().SetHandler(glogger)

	var (
		err    error
		tracer vm.ZVMLogger
	)
	var getTracer func(txIndex int, txHash common.Hash) (vm.ZVMLogger, error)

	baseDir, err := createBasedir(ctx)
	if err != nil {
		return NewError(ErrorIO, fmt.Errorf("failed creating output basedir: %v", err))
	}
	if ctx.Bool(TraceFlag.Name) {
		// Configure the ZVM logger
		logConfig := &logger.Config{
			DisableStack:     ctx.Bool(TraceDisableStackFlag.Name),
			EnableMemory:     ctx.Bool(TraceEnableMemoryFlag.Name),
			EnableReturnData: ctx.Bool(TraceEnableReturnDataFlag.Name),
			Debug:            true,
		}
		var prevFile *os.File
		// This one closes the last file
		defer func() {
			if prevFile != nil {
				prevFile.Close()
			}
		}()
		getTracer = func(txIndex int, txHash common.Hash) (vm.ZVMLogger, error) {
			if prevFile != nil {
				prevFile.Close()
			}
			traceFile, err := os.Create(path.Join(baseDir, fmt.Sprintf("trace-%d-%v.jsonl", txIndex, txHash.String())))
			if err != nil {
				return nil, NewError(ErrorIO, fmt.Errorf("failed creating trace-file: %v", err))
			}
			prevFile = traceFile
			return logger.NewJSONLogger(logConfig, traceFile), nil
		}
	} else {
		getTracer = func(txIndex int, txHash common.Hash) (tracer vm.ZVMLogger, err error) {
			return nil, nil
		}
	}
	// We need to load three things: alloc, env and transactions. May be either in
	// stdin input or in files.
	// Check if anything needs to be read from stdin
	var (
		prestate Prestate
		txs      types.Transactions // txs to apply
		allocStr = ctx.String(InputAllocFlag.Name)

		envStr    = ctx.String(InputEnvFlag.Name)
		txStr     = ctx.String(InputTxsFlag.Name)
		inputData = &input{}
	)
	// Figure out the prestate alloc
	if allocStr == stdinSelector || envStr == stdinSelector || txStr == stdinSelector {
		decoder := json.NewDecoder(os.Stdin)
		if err := decoder.Decode(inputData); err != nil {
			return NewError(ErrorJson, fmt.Errorf("failed unmarshaling stdin: %v", err))
		}
	}
	if allocStr != stdinSelector {
		if err := readFile(allocStr, "alloc", &inputData.Alloc); err != nil {
			return err
		}
	}
	prestate.Pre = inputData.Alloc

	// Set the block environment
	if envStr != stdinSelector {
		var env stEnv
		if err := readFile(envStr, "env", &env); err != nil {
			return err
		}
		inputData.Env = &env
	}
	prestate.Env = *inputData.Env

	vmConfig := vm.Config{
		Tracer: tracer,
	}
	// Construct the chainconfig
	var chainConfig *params.ChainConfig
	if cConf, extraEips, err := tests.GetChainConfig(ctx.String(ForknameFlag.Name)); err != nil {
		return NewError(ErrorConfig, fmt.Errorf("failed constructing chain configuration: %v", err))
	} else {
		chainConfig = cConf
		vmConfig.ExtraEips = extraEips
	}
	// Set the chain id
	chainConfig.ChainID = big.NewInt(ctx.Int64(ChainIDFlag.Name))

	if txs, err = loadTransactions(txStr, inputData, chainConfig); err != nil {
		return err
	}
	if err := applyLondonChecks(&prestate.Env, chainConfig); err != nil {
		return err
	}
	if err := applyShanghaiChecks(&prestate.Env, chainConfig); err != nil {
		return err
	}
	if err := applyMergeChecks(&prestate.Env, chainConfig); err != nil {
		return err
	}
	// Run the test and aggregate the result
	s, result, err := prestate.Apply(vmConfig, chainConfig, txs, ctx.Int64(RewardFlag.Name), getTracer)
	if err != nil {
		return err
	}
	body, _ := rlp.EncodeToBytes(txs)
	// Dump the excution result
	collector := make(Alloc)
	s.DumpToCollector(collector, nil)
	return dispatchOutput(ctx, baseDir, result, collector, body)
}

// txWithKey is a helper-struct, to allow us to use the types.Transaction along with
// a `seed`-field, for input
type txWithKey struct {
	key *ml_dsa_87.MLDSA87
	tx  *types.Transaction
}

func (t *txWithKey) UnmarshalJSON(input []byte) error {
	// Read the metadata, if present
	type txMetadata struct {
		Seed *string `json:"seed"`
	}
	var data txMetadata
	if err := json.Unmarshal(input, &data); err != nil {
		return err
	}
	if data.Seed != nil {
		sd := *data.Seed
		if mlDSA87Key, err := pqcrypto.HexToMLDSA87(sd[2:]); err != nil {
			return err
		} else {
			t.key = mlDSA87Key
		}
	}
	// Now, read the transaction itself
	var tx types.Transaction
	if err := json.Unmarshal(input, &tx); err != nil {
		return err
	}
	t.tx = &tx
	return nil
}

// signUnsignedTransactions converts the input txs to canonical transactions.
//
// The transactions can have two forms, either
//  1. unsigned or
//  2. signed
//
// For (1), signature, need so be zero, and the `seed` needs to be set.
// If so, we sign it here and now, with the given `seed`
// If the condition above is not met, then it's considered a signed transaction.
//
// To manage this, we read the transactions twice, first trying to read the seeds,
// and secondly to read them with the standard tx json format

func signUnsignedTransactions(txs []*txWithKey, signer types.Signer) (types.Transactions, error) {
	var signedTxs []*types.Transaction
	for i, tx := range txs {
		var (
			signature = tx.tx.RawSignatureValue()
			signed    *types.Transaction
			err       error
		)
		if tx.key == nil || len(signature) != 0 {
			// Already signed
			signedTxs = append(signedTxs, tx.tx)
			continue
		}
		signed, err = types.SignTx(tx.tx, signer, tx.key)
		if err != nil {
			return nil, NewError(ErrorJson, fmt.Errorf("tx %d: failed to sign tx: %v", i, err))
		}
		signedTxs = append(signedTxs, signed)
	}
	return signedTxs, nil
}

func loadTransactions(txStr string, inputData *input, chainConfig *params.ChainConfig) (types.Transactions, error) {
	var txsWithKeys []*txWithKey
	var signed types.Transactions
	if txStr != stdinSelector {
		data, err := os.ReadFile(txStr)
		if err != nil {
			return nil, NewError(ErrorIO, fmt.Errorf("failed reading txs file: %v", err))
		}
		if strings.HasSuffix(txStr, ".rlp") { // A file containing an rlp list
			var body hexutil.Bytes
			if err := json.Unmarshal(data, &body); err != nil {
				return nil, err
			}
			// Already signed transactions
			if err := rlp.DecodeBytes(body, &signed); err != nil {
				return nil, err
			}
			return signed, nil
		}
		if err := json.Unmarshal(data, &txsWithKeys); err != nil {
			return nil, NewError(ErrorJson, fmt.Errorf("failed unmarshaling txs-file: %v", err))
		}
	} else {
		if len(inputData.TxRlp) > 0 {
			// Decode the body of already signed transactions
			body := common.FromHex(inputData.TxRlp)
			// Already signed transactions
			if err := rlp.DecodeBytes(body, &signed); err != nil {
				return nil, err
			}
			return signed, nil
		}
		// JSON encoded transactions
		txsWithKeys = inputData.Txs
	}
	// We may have to sign the transactions.
	signer := types.MakeSigner(chainConfig)
	return signUnsignedTransactions(txsWithKeys, signer)
}

func applyLondonChecks(env *stEnv, chainConfig *params.ChainConfig) error {
	// Sanity check, to not `panic` in state_transition
	if env.BaseFee != nil {
		// Already set, base fee has precedent over parent base fee.
		return nil
	}
	if env.ParentBaseFee == nil || env.Number == 0 {
		return NewError(ErrorConfig, errors.New("EIP-1559 config but missing 'parentBaseFee' in env section"))
	}
	env.BaseFee = eip1559.CalcBaseFee(chainConfig, &types.Header{
		Number:   new(big.Int).SetUint64(env.Number - 1),
		BaseFee:  env.ParentBaseFee,
		GasUsed:  env.ParentGasUsed,
		GasLimit: env.ParentGasLimit,
	})
	return nil
}

func applyShanghaiChecks(env *stEnv, chainConfig *params.ChainConfig) error {
	if env.Withdrawals == nil {
		return NewError(ErrorConfig, errors.New("shanghai config but missing 'withdrawals' in env section"))
	}
	return nil
}

func applyMergeChecks(env *stEnv, chainConfig *params.ChainConfig) error {
	if env.Random == nil {
		return NewError(ErrorConfig, errors.New("post-merge requires currentRandom to be defined in env"))
	}
	return nil
}

type Alloc map[common.Address]core.GenesisAccount

func (g Alloc) OnRoot(common.Hash) {}

func (g Alloc) OnAccount(addr *common.Address, dumpAccount state.DumpAccount) {
	if addr == nil {
		return
	}
	balance, _ := new(big.Int).SetString(dumpAccount.Balance, 10)
	var storage map[common.Hash]common.Hash
	if dumpAccount.Storage != nil {
		storage = make(map[common.Hash]common.Hash)
		for k, v := range dumpAccount.Storage {
			storage[k] = common.HexToHash(v)
		}
	}
	genesisAccount := core.GenesisAccount{
		Code:    dumpAccount.Code,
		Storage: storage,
		Balance: balance,
		Nonce:   dumpAccount.Nonce,
	}
	g[*addr] = genesisAccount
}

// saveFile marshals the object to the given file
func saveFile(baseDir, filename string, data interface{}) error {
	b, err := json.MarshalIndent(data, "", " ")
	if err != nil {
		return NewError(ErrorJson, fmt.Errorf("failed marshalling output: %v", err))
	}
	location := path.Join(baseDir, filename)
	if err = os.WriteFile(location, b, 0644); err != nil {
		return NewError(ErrorIO, fmt.Errorf("failed writing output: %v", err))
	}
	log.Info("Wrote file", "file", location)
	return nil
}

// dispatchOutput writes the output data to either stderr or stdout, or to the specified
// files
func dispatchOutput(ctx *cli.Context, baseDir string, result *ExecutionResult, alloc Alloc, body hexutil.Bytes) error {
	stdOutObject := make(map[string]interface{})
	stdErrObject := make(map[string]interface{})
	dispatch := func(baseDir, fName, name string, obj interface{}) error {
		switch fName {
		case "stdout":
			stdOutObject[name] = obj
		case "stderr":
			stdErrObject[name] = obj
		case "":
			// don't save
		default: // save to file
			if err := saveFile(baseDir, fName, obj); err != nil {
				return err
			}
		}
		return nil
	}
	if err := dispatch(baseDir, ctx.String(OutputAllocFlag.Name), "alloc", alloc); err != nil {
		return err
	}
	if err := dispatch(baseDir, ctx.String(OutputResultFlag.Name), "result", result); err != nil {
		return err
	}
	if err := dispatch(baseDir, ctx.String(OutputBodyFlag.Name), "body", body); err != nil {
		return err
	}
	if len(stdOutObject) > 0 {
		b, err := json.MarshalIndent(stdOutObject, "", "  ")
		if err != nil {
			return NewError(ErrorJson, fmt.Errorf("failed marshalling output: %v", err))
		}
		os.Stdout.Write(b)
		os.Stdout.WriteString("\n")
	}
	if len(stdErrObject) > 0 {
		b, err := json.MarshalIndent(stdErrObject, "", "  ")
		if err != nil {
			return NewError(ErrorJson, fmt.Errorf("failed marshalling output: %v", err))
		}
		os.Stderr.Write(b)
		os.Stderr.WriteString("\n")
	}
	return nil
}
