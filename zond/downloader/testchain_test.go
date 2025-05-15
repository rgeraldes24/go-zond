// Copyright 2018 The go-ethereum Authors
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

package downloader

import (
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/theQRL/go-zond/common"
	"github.com/theQRL/go-zond/consensus/beacon"
	"github.com/theQRL/go-zond/core"
	"github.com/theQRL/go-zond/core/rawdb"
	"github.com/theQRL/go-zond/core/types"
	"github.com/theQRL/go-zond/core/vm"
	"github.com/theQRL/go-zond/crypto/pqcrypto"
	"github.com/theQRL/go-zond/params"
	"github.com/theQRL/go-zond/trie"
)

// Test chain parameters.
var (
	testKey, _  = pqcrypto.HexToMLDSA87("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
	testAddress = pqcrypto.MLDSA87ToAddress(testKey)
	testDB      = rawdb.NewMemoryDatabase()

	testGspec = &core.Genesis{
		Config:  params.TestChainConfig,
		Alloc:   core.GenesisAlloc{testAddress: {Balance: big.NewInt(1000000000000000)}},
		BaseFee: big.NewInt(params.InitialBaseFee),
	}
	testGenesis = testGspec.MustCommit(testDB, trie.NewDatabase(testDB, trie.HashDefaults))
)

// The common prefix of all test chains:
var testChainBase *testChain

var pregenerated bool

func init() {
	// Reduce some of the parameters to make the tester faster
	blockCacheMaxItems = 1024
	fsHeaderSafetyNet = 256
	fsHeaderContCheck = 500 * time.Millisecond

	testChainBase = newTestChain(blockCacheMaxItems+200, testGenesis)

	// Generate the test peers used by the tests to avoid overloading during testing.
	// These seemingly random chains are used in various downloader tests. We're just
	// pre-generating them here.
	chains := []*testChain{
		testChainBase,
		testChainBase.shorten(1),
		testChainBase.shorten(blockCacheMaxItems - 15),
		testChainBase.shorten((blockCacheMaxItems - 15) / 2),
		testChainBase.shorten(blockCacheMaxItems - 15 - 5),
		testChainBase.shorten(MaxHeaderFetch),
		testChainBase.shorten(800),
		testChainBase.shorten(800 / 2),
		testChainBase.shorten(800 / 3),
		testChainBase.shorten(800 / 4),
		testChainBase.shorten(800 / 5),
		testChainBase.shorten(800 / 6),
		testChainBase.shorten(800 / 7),
		testChainBase.shorten(800 / 8),
		testChainBase.shorten(3*fsHeaderSafetyNet + 256 + fsMinFullBlocks),
		testChainBase.shorten(fsMinFullBlocks + 256 - 1),
	}
	var wg sync.WaitGroup
	wg.Add(len(chains))
	for _, chain := range chains {
		go func(blocks []*types.Block) {
			newTestBlockchain(blocks)
			wg.Done()
		}(chain.blocks[1:])
	}
	wg.Wait()

	// Mark the chains pregenerated. Generating a new one will lead to a panic.
	pregenerated = true
}

type testChain struct {
	blocks []*types.Block
}

// newTestChain creates a blockchain of the given length.
func newTestChain(length int, genesis *types.Block) *testChain {
	tc := &testChain{
		blocks: []*types.Block{genesis},
	}
	tc.generate(length-1, 0, genesis)
	return tc
}

// shorten creates a copy of the chain with the given length. It panics if the
// length is longer than the number of available blocks.
func (tc *testChain) shorten(length int) *testChain {
	if length > len(tc.blocks) {
		panic(fmt.Errorf("can't shorten test chain to %d blocks, it's only %d blocks long", length, len(tc.blocks)))
	}
	return tc.copy(length)
}

func (tc *testChain) copy(newlen int) *testChain {
	if newlen > len(tc.blocks) {
		newlen = len(tc.blocks)
	}
	cpy := &testChain{
		blocks: append([]*types.Block{}, tc.blocks[:newlen]...),
	}
	return cpy
}

// generate creates a chain of n blocks starting at and including parent.
// the returned hash chain is ordered head->parent. In addition, every 22th block
// contains a transaction and every 5th an uncle to allow testing correct block
// reassembly.
func (tc *testChain) generate(n int, seed byte, parent *types.Block) {
	blocks, _ := core.GenerateChain(testGspec.Config, parent, beacon.NewFaker(), testDB, n, func(i int, block *core.BlockGen) {
		block.SetCoinbase(common.Address{seed})
		// Include transactions to the miner to make blocks more interesting.
		if parent == tc.blocks[0] && i%22 == 0 {
			signer := types.MakeSigner(params.TestChainConfig)

			tx, err := types.SignTx(types.NewTx(&types.DynamicFeeTx{Nonce: block.TxNonce(testAddress), To: &common.Address{seed}, Value: big.NewInt(1000), Gas: params.TxGas, GasFeeCap: block.BaseFee(), Data: nil}), signer, testKey)
			if err != nil {
				panic(err)
			}
			block.AddTx(tx)
		}
	})
	tc.blocks = append(tc.blocks, blocks...)
}

var (
	testBlockchains     = make(map[common.Hash]*testBlockchain)
	testBlockchainsLock sync.Mutex
)

type testBlockchain struct {
	chain *core.BlockChain
	gen   sync.Once
}

// newTestBlockchain creates a blockchain database built by running the given blocks,
// either actually running them, or reusing a previously created one. The returned
// chains are *shared*, so *do not* mutate them.
func newTestBlockchain(blocks []*types.Block) *core.BlockChain {
	// Retrieve an existing database, or create a new one
	head := testGenesis.Hash()
	if len(blocks) > 0 {
		head = blocks[len(blocks)-1].Hash()
	}
	testBlockchainsLock.Lock()
	if _, ok := testBlockchains[head]; !ok {
		testBlockchains[head] = new(testBlockchain)
	}
	tbc := testBlockchains[head]
	testBlockchainsLock.Unlock()

	// Ensure that the database is generated
	tbc.gen.Do(func() {
		if pregenerated {
			panic("Requested chain generation outside of init")
		}
		chain, err := core.NewBlockChain(rawdb.NewMemoryDatabase(), nil, testGspec, beacon.NewFaker(), vm.Config{}, nil)
		if err != nil {
			panic(err)
		}
		if n, err := chain.InsertChain(blocks); err != nil {
			panic(fmt.Sprintf("block %d: %v", n, err))
		}
		tbc.chain = chain
	})
	return tbc.chain
}
