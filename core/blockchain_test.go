// Copyright 2014 The go-ethereum Authors
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

package core

import (
	"fmt"
	"math/big"
	"math/rand"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/theQRL/go-zond/common"
	"github.com/theQRL/go-zond/consensus"
	"github.com/theQRL/go-zond/consensus/beacon"
	"github.com/theQRL/go-zond/core/rawdb"
	"github.com/theQRL/go-zond/core/state"
	"github.com/theQRL/go-zond/core/types"
	"github.com/theQRL/go-zond/core/vm"
	"github.com/theQRL/go-zond/crypto"
	"github.com/theQRL/go-zond/crypto/pqcrypto"
	"github.com/theQRL/go-zond/params"
	"github.com/theQRL/go-zond/trie"
	"github.com/theQRL/go-zond/zond/tracers/logger"
	"github.com/theQRL/go-zond/zonddb"
)

// So we can deterministically seed different blockchains
var (
	canonicalSeed = 1
	forkSeed      = 2
)

// newCanonical creates a chain database, and injects a deterministic canonical
// chain. Depending on the full flag, if creates either a full block chain or a
// header only chain. The database and genesis specification for block generation
// are also returned in case more test blocks are needed later.
func newCanonical(engine consensus.Engine, n int, full bool, scheme string) (zonddb.Database, *Genesis, *BlockChain, error) {
	var (
		genesis = &Genesis{
			BaseFee: big.NewInt(params.InitialBaseFee),
			Config:  params.AllBeaconProtocolChanges,
		}
	)
	// Initialize a fresh chain with only a genesis block
	blockchain, _ := NewBlockChain(rawdb.NewMemoryDatabase(), DefaultCacheConfigWithScheme(scheme), genesis, engine, vm.Config{}, nil)

	// Create and inject the requested chain
	if n == 0 {
		return rawdb.NewMemoryDatabase(), genesis, blockchain, nil
	}
	if full {
		// Full block-chain requested
		genDb, blocks := makeBlockChainWithGenesis(genesis, n, engine, canonicalSeed)
		_, err := blockchain.InsertChain(blocks)
		return genDb, genesis, blockchain, err
	}
	// Header-only chain requested
	genDb, headers := makeHeaderChainWithGenesis(genesis, n, engine, canonicalSeed)
	_, err := blockchain.InsertHeaderChain(headers)
	return genDb, genesis, blockchain, err
}

func newGplanck(n int64) *big.Int {
	return new(big.Int).Mul(big.NewInt(n), big.NewInt(params.GPlanck))
}

// Test fork of length N starting from block i
func testFork(t *testing.T, blockchain *BlockChain, i, n int, full bool, scheme string) {
	// Copy old chain up to #i into a new db
	genDb, _, blockchain2, err := newCanonical(beacon.NewFaker(), i, full, scheme)
	if err != nil {
		t.Fatal("could not make new canonical in testFork", err)
	}
	defer blockchain2.Stop()

	// Assert the chains have the same header/block at #i
	var hash1, hash2 common.Hash
	if full {
		hash1 = blockchain.GetBlockByNumber(uint64(i)).Hash()
		hash2 = blockchain2.GetBlockByNumber(uint64(i)).Hash()
	} else {
		hash1 = blockchain.GetHeaderByNumber(uint64(i)).Hash()
		hash2 = blockchain2.GetHeaderByNumber(uint64(i)).Hash()
	}
	if hash1 != hash2 {
		t.Errorf("chain content mismatch at %d: have hash %v, want hash %v", i, hash2, hash1)
	}
	// Extend the newly created chain
	var (
		blockChainB  []*types.Block
		headerChainB []*types.Header
	)
	if full {
		blockChainB = makeBlockChain(blockchain2.chainConfig, blockchain2.GetBlockByHash(blockchain2.CurrentBlock().Hash()), n, beacon.NewFaker(), genDb, forkSeed)
		if _, err := blockchain2.InsertChain(blockChainB); err != nil {
			t.Fatalf("failed to insert forking chain: %v", err)
		}
	} else {
		headerChainB = makeHeaderChain(blockchain2.chainConfig, blockchain2.CurrentHeader(), n, beacon.NewFaker(), genDb, forkSeed)
		if _, err := blockchain2.InsertHeaderChain(headerChainB); err != nil {
			t.Fatalf("failed to insert forking chain: %v", err)
		}
	}
}

// testBlockChainImport tries to process a chain of blocks, writing them into
// the database if successful.
func testBlockChainImport(chain types.Blocks, blockchain *BlockChain) error {
	for _, block := range chain {
		// Try and process the block
		err := blockchain.engine.VerifyHeader(blockchain, block.Header())
		if err == nil {
			err = blockchain.validator.ValidateBody(block)
		}
		if err != nil {
			if err == ErrKnownBlock {
				continue
			}
			return err
		}
		statedb, err := state.New(blockchain.GetBlockByHash(block.ParentHash()).Root(), blockchain.stateCache, nil)
		if err != nil {
			return err
		}
		receipts, _, usedGas, err := blockchain.processor.Process(block, statedb, vm.Config{})
		if err != nil {
			blockchain.reportBlock(block, receipts, err)
			return err
		}
		err = blockchain.validator.ValidateState(block, statedb, receipts, usedGas)
		if err != nil {
			blockchain.reportBlock(block, receipts, err)
			return err
		}

		blockchain.chainmu.MustLock()
		rawdb.WriteBlock(blockchain.db, block)
		statedb.Commit(block.NumberU64(), false)
		blockchain.chainmu.Unlock()
	}
	return nil
}

// testHeaderChainImport tries to process a chain of header, writing them into
// the database if successful.
func testHeaderChainImport(chain []*types.Header, blockchain *BlockChain) error {
	for _, header := range chain {
		// Try and validate the header
		if err := blockchain.engine.VerifyHeader(blockchain, header); err != nil {
			return err
		}
		// Manually insert the header into the database, but don't reorganise (allows subsequent testing)
		blockchain.chainmu.MustLock()
		rawdb.WriteHeader(blockchain.db, header)
		blockchain.chainmu.Unlock()
	}
	return nil
}
func TestLastBlock(t *testing.T) {
	testLastBlock(t, rawdb.HashScheme)
	testLastBlock(t, rawdb.PathScheme)
}

func testLastBlock(t *testing.T, scheme string) {
	genDb, _, blockchain, err := newCanonical(beacon.NewFaker(), 0, true, scheme)
	if err != nil {
		t.Fatalf("failed to create pristine chain: %v", err)
	}
	defer blockchain.Stop()

	blocks := makeBlockChain(blockchain.chainConfig, blockchain.GetBlockByHash(blockchain.CurrentBlock().Hash()), 1, beacon.NewFullFaker(), genDb, 0)
	if _, err := blockchain.InsertChain(blocks); err != nil {
		t.Fatalf("Failed to insert block: %v", err)
	}
	if blocks[len(blocks)-1].Hash() != rawdb.ReadHeadBlockHash(blockchain.db) {
		t.Fatalf("Write/Get HeadBlockHash failed")
	}
}

// Test inserts the blocks/headers after the fork choice rule is changed.
// The chain is reorged to whatever specified.
func testInsertAfterMerge(t *testing.T, blockchain *BlockChain, i, n int, full bool, scheme string) {
	// Copy old chain up to #i into a new db
	genDb, _, blockchain2, err := newCanonical(beacon.NewFaker(), i, full, scheme)
	if err != nil {
		t.Fatal("could not make new canonical in testFork", err)
	}
	defer blockchain2.Stop()

	// Assert the chains have the same header/block at #i
	var hash1, hash2 common.Hash
	if full {
		hash1 = blockchain.GetBlockByNumber(uint64(i)).Hash()
		hash2 = blockchain2.GetBlockByNumber(uint64(i)).Hash()
	} else {
		hash1 = blockchain.GetHeaderByNumber(uint64(i)).Hash()
		hash2 = blockchain2.GetHeaderByNumber(uint64(i)).Hash()
	}
	if hash1 != hash2 {
		t.Errorf("chain content mismatch at %d: have hash %v, want hash %v", i, hash2, hash1)
	}

	// Extend the newly created chain
	if full {
		blockChainB := makeBlockChain(blockchain2.chainConfig, blockchain2.GetBlockByHash(blockchain2.CurrentBlock().Hash()), n, beacon.NewFaker(), genDb, forkSeed)
		if _, err := blockchain2.InsertChain(blockChainB); err != nil {
			t.Fatalf("failed to insert forking chain: %v", err)
		}
		if blockchain2.CurrentBlock().Number.Uint64() != blockChainB[len(blockChainB)-1].NumberU64() {
			t.Fatalf("failed to reorg to the given chain")
		}
		if blockchain2.CurrentBlock().Hash() != blockChainB[len(blockChainB)-1].Hash() {
			t.Fatalf("failed to reorg to the given chain")
		}
	} else {
		headerChainB := makeHeaderChain(blockchain2.chainConfig, blockchain2.CurrentHeader(), n, beacon.NewFaker(), genDb, forkSeed)
		if _, err := blockchain2.InsertHeaderChain(headerChainB); err != nil {
			t.Fatalf("failed to insert forking chain: %v", err)
		}
		if blockchain2.CurrentHeader().Number.Uint64() != headerChainB[len(headerChainB)-1].Number.Uint64() {
			t.Fatalf("failed to reorg to the given chain")
		}
		if blockchain2.CurrentHeader().Hash() != headerChainB[len(headerChainB)-1].Hash() {
			t.Fatalf("failed to reorg to the given chain")
		}
	}
}

// Tests that given a starting canonical chain of a given size, it can be extended
// with various length chains.
func TestExtendCanonicalHeaders(t *testing.T) {
	testExtendCanonical(t, false, rawdb.HashScheme)
	testExtendCanonical(t, false, rawdb.PathScheme)
}
func TestExtendCanonicalBlocks(t *testing.T) {
	testExtendCanonical(t, true, rawdb.HashScheme)
	testExtendCanonical(t, true, rawdb.PathScheme)
}

func testExtendCanonical(t *testing.T, full bool, scheme string) {
	length := 5

	// Make first chain starting from genesis
	_, _, processor, err := newCanonical(beacon.NewFaker(), length, full, scheme)
	if err != nil {
		t.Fatalf("failed to make new canonical chain: %v", err)
	}
	defer processor.Stop()

	// Start fork from current height
	testFork(t, processor, length, 1, full, scheme)
	testFork(t, processor, length, 2, full, scheme)
	testFork(t, processor, length, 5, full, scheme)
	testFork(t, processor, length, 10, full, scheme)
}

// Tests that given a starting canonical chain of a given size, it can be extended
// with various length chains.
func TestExtendCanonicalHeadersAfterMerge(t *testing.T) {
	testExtendCanonicalAfterMerge(t, false, rawdb.HashScheme)
	testExtendCanonicalAfterMerge(t, false, rawdb.PathScheme)
}
func TestExtendCanonicalBlocksAfterMerge(t *testing.T) {
	testExtendCanonicalAfterMerge(t, true, rawdb.HashScheme)
	testExtendCanonicalAfterMerge(t, true, rawdb.PathScheme)
}

func testExtendCanonicalAfterMerge(t *testing.T, full bool, scheme string) {
	length := 5

	// Make first chain starting from genesis
	_, _, processor, err := newCanonical(beacon.NewFaker(), length, full, scheme)
	if err != nil {
		t.Fatalf("failed to make new canonical chain: %v", err)
	}
	defer processor.Stop()

	testInsertAfterMerge(t, processor, length, 1, full, scheme)
	testInsertAfterMerge(t, processor, length, 10, full, scheme)
}

// Tests that given a starting canonical chain of a given size, creating shorter
// forks do not take canonical ownership.
func TestShorterForkHeaders(t *testing.T) {
	testShorterFork(t, false, rawdb.HashScheme)
	testShorterFork(t, false, rawdb.PathScheme)
}
func TestShorterForkBlocks(t *testing.T) {
	testShorterFork(t, true, rawdb.HashScheme)
	testShorterFork(t, true, rawdb.PathScheme)
}

func testShorterFork(t *testing.T, full bool, scheme string) {
	length := 10

	// Make first chain starting from genesis
	_, _, processor, err := newCanonical(beacon.NewFaker(), length, full, scheme)
	if err != nil {
		t.Fatalf("failed to make new canonical chain: %v", err)
	}
	defer processor.Stop()

	// Sum of numbers must be less than `length` for this to be a shorter fork
	testFork(t, processor, 0, 3, full, scheme)
	testFork(t, processor, 0, 7, full, scheme)
	testFork(t, processor, 1, 1, full, scheme)
	testFork(t, processor, 1, 7, full, scheme)
	testFork(t, processor, 5, 3, full, scheme)
	testFork(t, processor, 5, 4, full, scheme)
}

// Tests that given a starting canonical chain of a given size, creating shorter
// forks do not take canonical ownership.
func TestShorterForkHeadersAfterMerge(t *testing.T) {
	testShorterForkAfterMerge(t, false, rawdb.HashScheme)
	testShorterForkAfterMerge(t, false, rawdb.PathScheme)
}
func TestShorterForkBlocksAfterMerge(t *testing.T) {
	testShorterForkAfterMerge(t, true, rawdb.HashScheme)
	testShorterForkAfterMerge(t, true, rawdb.PathScheme)
}

func testShorterForkAfterMerge(t *testing.T, full bool, scheme string) {
	length := 10

	// Make first chain starting from genesis
	_, _, processor, err := newCanonical(beacon.NewFaker(), length, full, scheme)
	if err != nil {
		t.Fatalf("failed to make new canonical chain: %v", err)
	}
	defer processor.Stop()

	testInsertAfterMerge(t, processor, 0, 3, full, scheme)
	testInsertAfterMerge(t, processor, 0, 7, full, scheme)
	testInsertAfterMerge(t, processor, 1, 1, full, scheme)
	testInsertAfterMerge(t, processor, 1, 7, full, scheme)
	testInsertAfterMerge(t, processor, 5, 3, full, scheme)
	testInsertAfterMerge(t, processor, 5, 4, full, scheme)
}

// Tests that given a starting canonical chain of a given size, creating longer
// forks do take canonical ownership.
func TestLongerForkHeaders(t *testing.T) {
	testLongerFork(t, false, rawdb.HashScheme)
	testLongerFork(t, false, rawdb.PathScheme)
}
func TestLongerForkBlocks(t *testing.T) {
	testLongerFork(t, true, rawdb.HashScheme)
	testLongerFork(t, true, rawdb.PathScheme)
}

func testLongerFork(t *testing.T, full bool, scheme string) {
	length := 10

	// Make first chain starting from genesis
	_, _, processor, err := newCanonical(beacon.NewFaker(), length, full, scheme)
	if err != nil {
		t.Fatalf("failed to make new canonical chain: %v", err)
	}
	defer processor.Stop()

	testInsertAfterMerge(t, processor, 0, 11, full, scheme)
	testInsertAfterMerge(t, processor, 0, 15, full, scheme)
	testInsertAfterMerge(t, processor, 1, 10, full, scheme)
	testInsertAfterMerge(t, processor, 1, 12, full, scheme)
	testInsertAfterMerge(t, processor, 5, 6, full, scheme)
	testInsertAfterMerge(t, processor, 5, 8, full, scheme)
}

// Tests that given a starting canonical chain of a given size, creating longer
// forks do take canonical ownership.
func TestLongerForkHeadersAfterMerge(t *testing.T) {
	testLongerForkAfterMerge(t, false, rawdb.HashScheme)
	testLongerForkAfterMerge(t, false, rawdb.PathScheme)
}
func TestLongerForkBlocksAfterMerge(t *testing.T) {
	testLongerForkAfterMerge(t, true, rawdb.HashScheme)
	testLongerForkAfterMerge(t, true, rawdb.PathScheme)
}

func testLongerForkAfterMerge(t *testing.T, full bool, scheme string) {
	length := 10

	// Make first chain starting from genesis
	_, _, processor, err := newCanonical(beacon.NewFaker(), length, full, scheme)
	if err != nil {
		t.Fatalf("failed to make new canonical chain: %v", err)
	}
	defer processor.Stop()

	testInsertAfterMerge(t, processor, 0, 11, full, scheme)
	testInsertAfterMerge(t, processor, 0, 15, full, scheme)
	testInsertAfterMerge(t, processor, 1, 10, full, scheme)
	testInsertAfterMerge(t, processor, 1, 12, full, scheme)
	testInsertAfterMerge(t, processor, 5, 6, full, scheme)
	testInsertAfterMerge(t, processor, 5, 8, full, scheme)
}

// Tests that given a starting canonical chain of a given size, creating equal
// forks do take canonical ownership.
func TestEqualForkHeaders(t *testing.T) {
	testEqualFork(t, false, rawdb.HashScheme)
	testEqualFork(t, false, rawdb.PathScheme)
}
func TestEqualForkBlocks(t *testing.T) {
	testEqualFork(t, true, rawdb.HashScheme)
	testEqualFork(t, true, rawdb.PathScheme)
}

func testEqualFork(t *testing.T, full bool, scheme string) {
	length := 10

	// Make first chain starting from genesis
	_, _, processor, err := newCanonical(beacon.NewFaker(), length, full, scheme)
	if err != nil {
		t.Fatalf("failed to make new canonical chain: %v", err)
	}
	defer processor.Stop()

	// Sum of numbers must be equal to `length` for this to be an equal fork
	testFork(t, processor, 0, 10, full, scheme)
	testFork(t, processor, 1, 9, full, scheme)
	testFork(t, processor, 2, 8, full, scheme)
	testFork(t, processor, 5, 5, full, scheme)
	testFork(t, processor, 6, 4, full, scheme)
	testFork(t, processor, 9, 1, full, scheme)
}

// Tests that given a starting canonical chain of a given size, creating equal
// forks do take canonical ownership.
func TestEqualForkHeadersAfterMerge(t *testing.T) {
	testEqualForkAfterMerge(t, false, rawdb.HashScheme)
	testEqualForkAfterMerge(t, false, rawdb.PathScheme)
}
func TestEqualForkBlocksAfterMerge(t *testing.T) {
	testEqualForkAfterMerge(t, true, rawdb.HashScheme)
	testEqualForkAfterMerge(t, true, rawdb.PathScheme)
}

func testEqualForkAfterMerge(t *testing.T, full bool, scheme string) {
	length := 10

	// Make first chain starting from genesis
	_, _, processor, err := newCanonical(beacon.NewFaker(), length, full, scheme)
	if err != nil {
		t.Fatalf("failed to make new canonical chain: %v", err)
	}
	defer processor.Stop()

	testInsertAfterMerge(t, processor, 0, 10, full, scheme)
	testInsertAfterMerge(t, processor, 1, 9, full, scheme)
	testInsertAfterMerge(t, processor, 2, 8, full, scheme)
	testInsertAfterMerge(t, processor, 5, 5, full, scheme)
	testInsertAfterMerge(t, processor, 6, 4, full, scheme)
	testInsertAfterMerge(t, processor, 9, 1, full, scheme)
}

// Tests that chains missing links do not get accepted by the processor.
func TestBrokenHeaderChain(t *testing.T) {
	testBrokenChain(t, false, rawdb.HashScheme)
	testBrokenChain(t, false, rawdb.PathScheme)
}
func TestBrokenBlockChain(t *testing.T) {
	testBrokenChain(t, true, rawdb.HashScheme)
	testBrokenChain(t, true, rawdb.PathScheme)
}

func testBrokenChain(t *testing.T, full bool, scheme string) {
	// Make chain starting from genesis
	genDb, _, blockchain, err := newCanonical(beacon.NewFaker(), 10, full, scheme)
	if err != nil {
		t.Fatalf("failed to make new canonical chain: %v", err)
	}
	defer blockchain.Stop()

	// Create a forked chain, and try to insert with a missing link
	if full {
		chain := makeBlockChain(blockchain.chainConfig, blockchain.GetBlockByHash(blockchain.CurrentBlock().Hash()), 5, beacon.NewFaker(), genDb, forkSeed)[1:]
		if err := testBlockChainImport(chain, blockchain); err == nil {
			t.Errorf("broken block chain not reported")
		}
	} else {
		chain := makeHeaderChain(blockchain.chainConfig, blockchain.CurrentHeader(), 5, beacon.NewFaker(), genDb, forkSeed)[1:]
		if err := testHeaderChainImport(chain, blockchain); err == nil {
			t.Errorf("broken header chain not reported")
		}
	}
}

// Tests that reorganising a long difficult chain after a short easy one
// overwrites the canonical numbers and links in the database.
func TestReorgLongHeaders(t *testing.T) {
	testReorgLong(t, false, rawdb.HashScheme)
	testReorgLong(t, false, rawdb.PathScheme)
}
func TestReorgLongBlocks(t *testing.T) {
	testReorgLong(t, true, rawdb.HashScheme)
	testReorgLong(t, true, rawdb.PathScheme)
}

func testReorgLong(t *testing.T, full bool, scheme string) {
	testReorg(t, []int64{0, 0, -9}, []int64{0, 0, 0, -9}, full, scheme)
}

// Tests that reorganising a short difficult chain after a long easy one
// overwrites the canonical numbers and links in the database.
func TestReorgShortHeaders(t *testing.T) {
	testReorgShort(t, false, rawdb.HashScheme)
	testReorgShort(t, false, rawdb.PathScheme)
}

func TestReorgShortBlocks(t *testing.T) {
	testReorgShort(t, true, rawdb.HashScheme)
	testReorgShort(t, true, rawdb.PathScheme)
}

func testReorgShort(t *testing.T, full bool, scheme string) {
	// Create a long easy chain vs. a short heavy one. Due to difficulty adjustment
	// we need a fairly long chain of blocks with different difficulties for a short
	// one to become heavier than a long one. The 96 is an empirical value.
	easy := make([]int64, 96)
	for i := 0; i < len(easy); i++ {
		easy[i] = 60
	}
	diff := make([]int64, len(easy)-1)
	for i := 0; i < len(diff); i++ {
		diff[i] = -9
	}
	testReorg(t, easy, diff, full, scheme)
}

func testReorg(t *testing.T, first, second []int64, full bool, scheme string) {
	// Create a pristine chain and database
	genDb, _, blockchain, err := newCanonical(beacon.NewFaker(), 0, full, scheme)
	if err != nil {
		t.Fatalf("failed to create pristine chain: %v", err)
	}
	defer blockchain.Stop()

	// Insert an easy and a difficult chain afterwards
	easyBlocks, _ := GenerateChain(params.TestChainConfig, blockchain.GetBlockByHash(blockchain.CurrentBlock().Hash()), beacon.NewFaker(), genDb, len(first), func(i int, b *BlockGen) {
		b.OffsetTime(first[i])
	})
	diffBlocks, _ := GenerateChain(params.TestChainConfig, blockchain.GetBlockByHash(blockchain.CurrentBlock().Hash()), beacon.NewFaker(), genDb, len(second), func(i int, b *BlockGen) {
		b.OffsetTime(second[i])
	})
	if full {
		if _, err := blockchain.InsertChain(easyBlocks); err != nil {
			t.Fatalf("failed to insert easy chain: %v", err)
		}
		if _, err := blockchain.InsertChain(diffBlocks); err != nil {
			t.Fatalf("failed to insert difficult chain: %v", err)
		}
	} else {
		easyHeaders := make([]*types.Header, len(easyBlocks))
		for i, block := range easyBlocks {
			easyHeaders[i] = block.Header()
		}
		diffHeaders := make([]*types.Header, len(diffBlocks))
		for i, block := range diffBlocks {
			diffHeaders[i] = block.Header()
		}
		if _, err := blockchain.InsertHeaderChain(easyHeaders); err != nil {
			t.Fatalf("failed to insert easy chain: %v", err)
		}
		if _, err := blockchain.InsertHeaderChain(diffHeaders); err != nil {
			t.Fatalf("failed to insert difficult chain: %v", err)
		}
	}
	// Check that the chain is valid number and link wise
	if full {
		prev := blockchain.CurrentBlock()
		for block := blockchain.GetBlockByNumber(blockchain.CurrentBlock().Number.Uint64() - 1); block.NumberU64() != 0; prev, block = block.Header(), blockchain.GetBlockByNumber(block.NumberU64()-1) {
			if prev.ParentHash != block.Hash() {
				t.Errorf("parent block hash mismatch: have %x, want %x", prev.ParentHash, block.Hash())
			}
		}
	} else {
		prev := blockchain.CurrentHeader()
		for header := blockchain.GetHeaderByNumber(blockchain.CurrentHeader().Number.Uint64() - 1); header.Number.Uint64() != 0; prev, header = header, blockchain.GetHeaderByNumber(header.Number.Uint64()-1) {
			if prev.ParentHash != header.Hash() {
				t.Errorf("parent header hash mismatch: have %x, want %x", prev.ParentHash, header.Hash())
			}
		}
	}
}

// Tests chain insertions in the face of one entity containing an invalid nonce.
func TestHeadersInsertNonceError(t *testing.T) {
	testInsertNonceError(t, false, rawdb.HashScheme)
	testInsertNonceError(t, false, rawdb.PathScheme)
}
func TestBlocksInsertNonceError(t *testing.T) {
	testInsertNonceError(t, true, rawdb.HashScheme)
	testInsertNonceError(t, true, rawdb.PathScheme)
}

func testInsertNonceError(t *testing.T, full bool, scheme string) {
	doTest := func(i int) {
		// Create a pristine chain and database
		genDb, _, blockchain, err := newCanonical(beacon.NewFaker(), 0, full, scheme)
		if err != nil {
			t.Fatalf("failed to create pristine chain: %v", err)
		}
		defer blockchain.Stop()

		// Create and insert a chain with a failing nonce
		var (
			failAt  int
			failRes int
			failNum uint64
		)
		if full {
			blocks := makeBlockChain(blockchain.chainConfig, blockchain.GetBlockByHash(blockchain.CurrentBlock().Hash()), i, beacon.NewFaker(), genDb, 0)

			failAt = rand.Int() % len(blocks)
			failNum = blocks[failAt].NumberU64()

			blockchain.engine = beacon.NewFakeFailer(failNum)
			failRes, err = blockchain.InsertChain(blocks)
		} else {
			headers := makeHeaderChain(blockchain.chainConfig, blockchain.CurrentHeader(), i, beacon.NewFaker(), genDb, 0)

			failAt = rand.Int() % len(headers)
			failNum = headers[failAt].Number.Uint64()

			blockchain.engine = beacon.NewFakeFailer(failNum)
			blockchain.hc.engine = blockchain.engine
			failRes, err = blockchain.InsertHeaderChain(headers)
		}
		// Check that the returned error indicates the failure
		if failRes != failAt {
			t.Errorf("test %d: failure (%v) index mismatch: have %d, want %d", i, err, failRes, failAt)
		}
		// Check that all blocks after the failing block have been inserted
		for j := 0; j < i-failAt; j++ {
			if full {
				if block := blockchain.GetBlockByNumber(failNum + uint64(j)); block != nil {
					t.Errorf("test %d: invalid block in chain: %v", i, block)
				}
			} else {
				if header := blockchain.GetHeaderByNumber(failNum + uint64(j)); header != nil {
					t.Errorf("test %d: invalid header in chain: %v", i, header)
				}
			}
		}
	}
	for i := 1; i < 25 && !t.Failed(); i++ {
		doTest(i)
	}
}

// Tests that fast importing a block chain produces the same chain data as the
// classical full block processing.
func TestFastVsFullChains(t *testing.T) {
	testFastVsFullChains(t, rawdb.HashScheme)
	testFastVsFullChains(t, rawdb.PathScheme)
}

func testFastVsFullChains(t *testing.T, scheme string) {
	// Configure and generate a sample block chain
	var (
		key, _  = pqcrypto.HexToMLDSA87("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		address = pqcrypto.MLDSA87ToAddress(key)
		funds   = big.NewInt(1000000000000000)
		gspec   = &Genesis{
			Config:  params.TestChainConfig,
			Alloc:   GenesisAlloc{address: {Balance: funds}},
			BaseFee: big.NewInt(params.InitialBaseFee),
		}
		signer = types.LatestSigner(gspec.Config)
	)
	_, blocks, receipts := GenerateChainWithGenesis(gspec, beacon.NewFaker(), 1024, func(i int, block *BlockGen) {
		block.SetCoinbase(common.Address{0x00})

		// If the block number is multiple of 3, send a few bonus transactions to the miner
		if i%3 == 2 {
			for j := 0; j < i%4+1; j++ {
				tx := types.NewTx(&types.DynamicFeeTx{
					Nonce:     block.TxNonce(address),
					To:        &common.Address{0x00},
					Value:     big.NewInt(1000),
					Gas:       params.TxGas,
					GasFeeCap: block.header.BaseFee,
					Data:      nil,
				})
				signedTx, err := types.SignTx(tx, signer, key)
				if err != nil {
					panic(err)
				}
				block.AddTx(signedTx)
			}
		}
	})
	// Import the chain as an archive node for the comparison baseline
	archiveDb := rawdb.NewMemoryDatabase()
	archive, _ := NewBlockChain(archiveDb, DefaultCacheConfigWithScheme(scheme), gspec, beacon.NewFaker(), vm.Config{}, nil)
	defer archive.Stop()

	if n, err := archive.InsertChain(blocks); err != nil {
		t.Fatalf("failed to process block %d: %v", n, err)
	}
	// Fast import the chain as a non-archive node to test
	fastDb := rawdb.NewMemoryDatabase()
	fast, _ := NewBlockChain(fastDb, DefaultCacheConfigWithScheme(scheme), gspec, beacon.NewFaker(), vm.Config{}, nil)
	defer fast.Stop()

	headers := make([]*types.Header, len(blocks))
	for i, block := range blocks {
		headers[i] = block.Header()
	}
	if n, err := fast.InsertHeaderChain(headers); err != nil {
		t.Fatalf("failed to insert header %d: %v", n, err)
	}
	if n, err := fast.InsertReceiptChain(blocks, receipts, 0); err != nil {
		t.Fatalf("failed to insert receipt %d: %v", n, err)
	}
	// Freezer style fast import the chain.
	ancientDb, err := rawdb.NewDatabaseWithFreezer(rawdb.NewMemoryDatabase(), t.TempDir(), "", false)
	if err != nil {
		t.Fatalf("failed to create temp freezer db: %v", err)
	}
	defer ancientDb.Close()

	ancient, _ := NewBlockChain(ancientDb, DefaultCacheConfigWithScheme(scheme), gspec, beacon.NewFaker(), vm.Config{}, nil)
	defer ancient.Stop()

	if n, err := ancient.InsertHeaderChain(headers); err != nil {
		t.Fatalf("failed to insert header %d: %v", n, err)
	}
	if n, err := ancient.InsertReceiptChain(blocks, receipts, uint64(len(blocks)/2)); err != nil {
		t.Fatalf("failed to insert receipt %d: %v", n, err)
	}

	// Iterate over all chain data components, and cross reference
	for i := 0; i < len(blocks); i++ {
		num, hash, time := blocks[i].NumberU64(), blocks[i].Hash(), blocks[i].Time()

		if fheader, aheader := fast.GetHeaderByHash(hash), archive.GetHeaderByHash(hash); fheader.Hash() != aheader.Hash() {
			t.Errorf("block #%d [%x]: header mismatch: fastdb %v, archivedb %v", num, hash, fheader, aheader)
		}
		if anheader, arheader := ancient.GetHeaderByHash(hash), archive.GetHeaderByHash(hash); anheader.Hash() != arheader.Hash() {
			t.Errorf("block #%d [%x]: header mismatch: ancientdb %v, archivedb %v", num, hash, anheader, arheader)
		}
		if fblock, arblock, anblock := fast.GetBlockByHash(hash), archive.GetBlockByHash(hash), ancient.GetBlockByHash(hash); fblock.Hash() != arblock.Hash() || anblock.Hash() != arblock.Hash() {
			t.Errorf("block #%d [%x]: block mismatch: fastdb %v, ancientdb %v, archivedb %v", num, hash, fblock, anblock, arblock)
		} else if types.DeriveSha(fblock.Transactions(), trie.NewStackTrie(nil)) != types.DeriveSha(arblock.Transactions(), trie.NewStackTrie(nil)) || types.DeriveSha(anblock.Transactions(), trie.NewStackTrie(nil)) != types.DeriveSha(arblock.Transactions(), trie.NewStackTrie(nil)) {
			t.Errorf("block #%d [%x]: transactions mismatch: fastdb %v, ancientdb %v, archivedb %v", num, hash, fblock.Transactions(), anblock.Transactions(), arblock.Transactions())
		}

		// Check receipts.
		freceipts := rawdb.ReadReceipts(fastDb, hash, num, time, fast.Config())
		anreceipts := rawdb.ReadReceipts(ancientDb, hash, num, time, fast.Config())
		areceipts := rawdb.ReadReceipts(archiveDb, hash, num, time, fast.Config())
		if types.DeriveSha(freceipts, trie.NewStackTrie(nil)) != types.DeriveSha(areceipts, trie.NewStackTrie(nil)) {
			t.Errorf("block #%d [%x]: receipts mismatch: fastdb %v, ancientdb %v, archivedb %v", num, hash, freceipts, anreceipts, areceipts)
		}

		// Check that hash-to-number mappings are present in all databases.
		if m := rawdb.ReadHeaderNumber(fastDb, hash); m == nil || *m != num {
			t.Errorf("block #%d [%x]: wrong hash-to-number mapping in fastdb: %v", num, hash, m)
		}
		if m := rawdb.ReadHeaderNumber(ancientDb, hash); m == nil || *m != num {
			t.Errorf("block #%d [%x]: wrong hash-to-number mapping in ancientdb: %v", num, hash, m)
		}
		if m := rawdb.ReadHeaderNumber(archiveDb, hash); m == nil || *m != num {
			t.Errorf("block #%d [%x]: wrong hash-to-number mapping in archivedb: %v", num, hash, m)
		}
	}

	// Check that the canonical chains are the same between the databases
	for i := 0; i < len(blocks)+1; i++ {
		if fhash, ahash := rawdb.ReadCanonicalHash(fastDb, uint64(i)), rawdb.ReadCanonicalHash(archiveDb, uint64(i)); fhash != ahash {
			t.Errorf("block #%d: canonical hash mismatch: fastdb %v, archivedb %v", i, fhash, ahash)
		}
		if anhash, arhash := rawdb.ReadCanonicalHash(ancientDb, uint64(i)), rawdb.ReadCanonicalHash(archiveDb, uint64(i)); anhash != arhash {
			t.Errorf("block #%d: canonical hash mismatch: ancientdb %v, archivedb %v", i, anhash, arhash)
		}
	}
}

// Tests that various import methods move the chain head pointers to the correct
// positions.
func TestLightVsFastVsFullChainHeads(t *testing.T) {
	testLightVsFastVsFullChainHeads(t, rawdb.HashScheme)
	testLightVsFastVsFullChainHeads(t, rawdb.PathScheme)
}

func testLightVsFastVsFullChainHeads(t *testing.T, scheme string) {
	// Configure and generate a sample block chain
	var (
		key, _  = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		address = crypto.PubkeyToAddress(key.PublicKey)
		funds   = big.NewInt(1000000000000000)
		gspec   = &Genesis{
			Config:  params.TestChainConfig,
			Alloc:   GenesisAlloc{address: {Balance: funds}},
			BaseFee: big.NewInt(params.InitialBaseFee),
		}
	)
	height := uint64(1024)
	_, blocks, receipts := GenerateChainWithGenesis(gspec, beacon.NewFaker(), int(height), nil)

	// makeDb creates a db instance for testing.
	makeDb := func() zonddb.Database {
		db, err := rawdb.NewDatabaseWithFreezer(rawdb.NewMemoryDatabase(), t.TempDir(), "", false)
		if err != nil {
			t.Fatalf("failed to create temp freezer db: %v", err)
		}
		return db
	}
	// Configure a subchain to roll back
	remove := blocks[height/2].NumberU64()

	// Create a small assertion method to check the three heads
	assert := func(t *testing.T, kind string, chain *BlockChain, header uint64, fast uint64, block uint64) {
		t.Helper()

		if num := chain.CurrentBlock().Number.Uint64(); num != block {
			t.Errorf("%s head block mismatch: have #%v, want #%v", kind, num, block)
		}
		if num := chain.CurrentSnapBlock().Number.Uint64(); num != fast {
			t.Errorf("%s head snap-block mismatch: have #%v, want #%v", kind, num, fast)
		}
		if num := chain.CurrentHeader().Number.Uint64(); num != header {
			t.Errorf("%s head header mismatch: have #%v, want #%v", kind, num, header)
		}
	}
	// Import the chain as an archive node and ensure all pointers are updated
	archiveDb := makeDb()
	defer archiveDb.Close()

	archiveCaching := *defaultCacheConfig
	archiveCaching.TrieDirtyDisabled = true
	archiveCaching.StateScheme = scheme

	archive, _ := NewBlockChain(archiveDb, &archiveCaching, gspec, beacon.NewFaker(), vm.Config{}, nil)
	if n, err := archive.InsertChain(blocks); err != nil {
		t.Fatalf("failed to process block %d: %v", n, err)
	}
	defer archive.Stop()

	assert(t, "archive", archive, height, height, height)
	archive.SetHead(remove - 1)
	assert(t, "archive", archive, height/2, height/2, height/2)

	// Import the chain as a non-archive node and ensure all pointers are updated
	fastDb := makeDb()
	defer fastDb.Close()
	fast, _ := NewBlockChain(fastDb, DefaultCacheConfigWithScheme(scheme), gspec, beacon.NewFaker(), vm.Config{}, nil)
	defer fast.Stop()

	headers := make([]*types.Header, len(blocks))
	for i, block := range blocks {
		headers[i] = block.Header()
	}
	if n, err := fast.InsertHeaderChain(headers); err != nil {
		t.Fatalf("failed to insert header %d: %v", n, err)
	}
	if n, err := fast.InsertReceiptChain(blocks, receipts, 0); err != nil {
		t.Fatalf("failed to insert receipt %d: %v", n, err)
	}
	assert(t, "fast", fast, height, height, 0)
	fast.SetHead(remove - 1)
	assert(t, "fast", fast, height/2, height/2, 0)

	// Import the chain as a ancient-first node and ensure all pointers are updated
	ancientDb := makeDb()
	defer ancientDb.Close()
	ancient, _ := NewBlockChain(ancientDb, DefaultCacheConfigWithScheme(scheme), gspec, beacon.NewFaker(), vm.Config{}, nil)
	defer ancient.Stop()

	if n, err := ancient.InsertHeaderChain(headers); err != nil {
		t.Fatalf("failed to insert header %d: %v", n, err)
	}
	if n, err := ancient.InsertReceiptChain(blocks, receipts, uint64(3*len(blocks)/4)); err != nil {
		t.Fatalf("failed to insert receipt %d: %v", n, err)
	}
	assert(t, "ancient", ancient, height, height, 0)
	ancient.SetHead(remove - 1)
	assert(t, "ancient", ancient, 0, 0, 0)

	if frozen, err := ancientDb.Ancients(); err != nil || frozen != 1 {
		t.Fatalf("failed to truncate ancient store, want %v, have %v", 1, frozen)
	}
	// Import the chain as a light node and ensure all pointers are updated
	lightDb := makeDb()
	defer lightDb.Close()
	light, _ := NewBlockChain(lightDb, DefaultCacheConfigWithScheme(scheme), gspec, beacon.NewFaker(), vm.Config{}, nil)
	if n, err := light.InsertHeaderChain(headers); err != nil {
		t.Fatalf("failed to insert header %d: %v", n, err)
	}
	defer light.Stop()

	assert(t, "light", light, height, 0, 0)
	light.SetHead(remove - 1)
	assert(t, "light", light, height/2, 0, 0)
}

// Tests that chain reorganisations handle transaction removals and reinsertions.
func TestChainTxReorgs(t *testing.T) {
	testChainTxReorgs(t, rawdb.HashScheme)
	testChainTxReorgs(t, rawdb.PathScheme)
}

func testChainTxReorgs(t *testing.T, scheme string) {
	var (
		key1, _ = pqcrypto.HexToMLDSA87("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		key2, _ = pqcrypto.HexToMLDSA87("8a1f9a8f95be41cd7ccb6168179afb4504aefe388d1e14474d32c45c72ce7b7a")
		key3, _ = pqcrypto.HexToMLDSA87("49a7b37aa6f6645917e7b807e9d1c00d4fa71f18343b0d4122a4d2df64dd6fee")
		addr1   = pqcrypto.MLDSA87ToAddress(key1)
		addr2   = pqcrypto.MLDSA87ToAddress(key2)
		addr3   = pqcrypto.MLDSA87ToAddress(key3)
		gspec   = &Genesis{
			Config:   params.TestChainConfig,
			GasLimit: 3141592,
			Alloc: GenesisAlloc{
				addr1: {Balance: big.NewInt(1000000000000000)},
				addr2: {Balance: big.NewInt(1000000000000000)},
				addr3: {Balance: big.NewInt(1000000000000000)},
			},
		}
		signer = types.LatestSigner(gspec.Config)
	)

	// Create two transactions shared between the chains:
	//  - postponed: transaction included at a later block in the forked chain
	//  - swapped: transaction included at the same block number in the forked chain
	to := common.Address(addr1)
	postponed, _ := types.SignTx(types.NewTx(&types.DynamicFeeTx{Nonce: 0, To: &to, Value: big.NewInt(1000), Gas: params.TxGas, GasFeeCap: big.NewInt(params.InitialBaseFee), Data: nil}), signer, key1)
	swapped, _ := types.SignTx(types.NewTx(&types.DynamicFeeTx{Nonce: 1, To: &to, Value: big.NewInt(1000), Gas: params.TxGas, GasFeeCap: big.NewInt(params.InitialBaseFee), Data: nil}), signer, key1)

	// Create two transactions that will be dropped by the forked chain:
	//  - pastDrop: transaction dropped retroactively from a past block
	//  - freshDrop: transaction dropped exactly at the block where the reorg is detected
	var pastDrop, freshDrop *types.Transaction

	// Create three transactions that will be added in the forked chain:
	//  - pastAdd:   transaction added before the reorganization is detected
	//  - freshAdd:  transaction added at the exact block the reorg is detected
	//  - futureAdd: transaction added after the reorg has already finished
	var pastAdd, freshAdd, futureAdd *types.Transaction

	to2 := common.Address(addr2)
	_, chain, _ := GenerateChainWithGenesis(gspec, beacon.NewFaker(), 3, func(i int, gen *BlockGen) {
		switch i {
		case 0:

			pastDrop, _ = types.SignTx(types.NewTx(&types.DynamicFeeTx{Nonce: gen.TxNonce(addr2), To: &to2, Value: big.NewInt(1000), Gas: params.TxGas, GasFeeCap: gen.header.BaseFee, Data: nil}), signer, key2)

			gen.AddTx(pastDrop)  // This transaction will be dropped in the fork from below the split point
			gen.AddTx(postponed) // This transaction will be postponed till block #3 in the fork

		case 2:
			freshDrop, _ = types.SignTx(types.NewTx(&types.DynamicFeeTx{Nonce: gen.TxNonce(addr2), To: &to2, Value: big.NewInt(1000), Gas: params.TxGas, GasFeeCap: gen.header.BaseFee, Data: nil}), signer, key2)

			gen.AddTx(freshDrop) // This transaction will be dropped in the fork from exactly at the split point
			gen.AddTx(swapped)   // This transaction will be swapped out at the exact height

			gen.OffsetTime(9) // Lower the block difficulty to simulate a weaker chain
		}
	})
	// Import the chain. This runs all block validation rules.
	db := rawdb.NewMemoryDatabase()
	blockchain, _ := NewBlockChain(db, DefaultCacheConfigWithScheme(scheme), gspec, beacon.NewFaker(), vm.Config{}, nil)
	if i, err := blockchain.InsertChain(chain); err != nil {
		t.Fatalf("failed to insert original chain[%d]: %v", i, err)
	}
	defer blockchain.Stop()

	to3 := common.Address(addr3)
	// overwrite the old chain
	_, chain, _ = GenerateChainWithGenesis(gspec, beacon.NewFaker(), 5, func(i int, gen *BlockGen) {
		switch i {
		case 0:
			pastAdd, _ = types.SignTx(types.NewTx(&types.DynamicFeeTx{Nonce: gen.TxNonce(addr3), To: &to3, Value: big.NewInt(1000), Gas: params.TxGas, GasFeeCap: gen.header.BaseFee, Data: nil}), signer, key3)
			gen.AddTx(pastAdd) // This transaction needs to be injected during reorg

		case 2:
			gen.AddTx(postponed) // This transaction was postponed from block #1 in the original chain
			gen.AddTx(swapped)   // This transaction was swapped from the exact current spot in the original chain

			freshAdd, _ = types.SignTx(types.NewTx(&types.DynamicFeeTx{Nonce: gen.TxNonce(addr3), To: &to3, Value: big.NewInt(1000), Gas: params.TxGas, GasFeeCap: gen.header.BaseFee, Data: nil}), signer, key3)
			gen.AddTx(freshAdd) // This transaction will be added exactly at reorg time

		case 3:
			futureAdd, _ = types.SignTx(types.NewTx(&types.DynamicFeeTx{Nonce: gen.TxNonce(addr3), To: &to3, Value: big.NewInt(1000), Gas: params.TxGas, GasFeeCap: gen.header.BaseFee, Data: nil}), signer, key3)
			gen.AddTx(futureAdd) // This transaction will be added after a full reorg
		}
	})
	if _, err := blockchain.InsertChain(chain); err != nil {
		t.Fatalf("failed to insert forked chain: %v", err)
	}

	// removed tx
	for i, tx := range (types.Transactions{pastDrop, freshDrop}) {
		if txn, _, _, _ := rawdb.ReadTransaction(db, tx.Hash()); txn != nil {
			t.Errorf("drop %d: tx %v found while shouldn't have been", i, txn)
		}
		if rcpt, _, _, _ := rawdb.ReadReceipt(db, tx.Hash(), blockchain.Config()); rcpt != nil {
			t.Errorf("drop %d: receipt %v found while shouldn't have been", i, rcpt)
		}
	}
	// added tx
	for i, tx := range (types.Transactions{pastAdd, freshAdd, futureAdd}) {
		if txn, _, _, _ := rawdb.ReadTransaction(db, tx.Hash()); txn == nil {
			t.Errorf("add %d: expected tx to be found", i)
		}
		if rcpt, _, _, _ := rawdb.ReadReceipt(db, tx.Hash(), blockchain.Config()); rcpt == nil {
			t.Errorf("add %d: expected receipt to be found", i)
		}
	}
	// shared tx
	for i, tx := range (types.Transactions{postponed, swapped}) {
		if txn, _, _, _ := rawdb.ReadTransaction(db, tx.Hash()); txn == nil {
			t.Errorf("share %d: expected tx to be found", i)
		}
		if rcpt, _, _, _ := rawdb.ReadReceipt(db, tx.Hash(), blockchain.Config()); rcpt == nil {
			t.Errorf("share %d: expected receipt to be found", i)
		}
	}
}

func TestLogReorgs(t *testing.T) {
	testLogReorgs(t, rawdb.HashScheme)
	testLogReorgs(t, rawdb.PathScheme)
}

func testLogReorgs(t *testing.T, scheme string) {
	var (
		key1, _ = pqcrypto.HexToMLDSA87("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		addr1   = pqcrypto.MLDSA87ToAddress(key1)

		// this code generates a log
		code   = common.Hex2Bytes("60606040525b7f24ec1d3ff24c2f6ff210738839dbc339cd45a5294d85c79361016243157aae7b60405180905060405180910390a15b600a8060416000396000f360606040526008565b00")
		gspec  = &Genesis{Config: params.TestChainConfig, Alloc: GenesisAlloc{addr1: {Balance: big.NewInt(10000000000000000)}}}
		signer = types.LatestSigner(gspec.Config)
	)

	blockchain, _ := NewBlockChain(rawdb.NewMemoryDatabase(), DefaultCacheConfigWithScheme(scheme), gspec, beacon.NewFaker(), vm.Config{}, nil)
	defer blockchain.Stop()

	rmLogsCh := make(chan RemovedLogsEvent)
	blockchain.SubscribeRemovedLogsEvent(rmLogsCh)
	_, chain, _ := GenerateChainWithGenesis(gspec, beacon.NewFaker(), 2, func(i int, gen *BlockGen) {
		if i == 1 {
			tx := types.NewTx(&types.DynamicFeeTx{
				Nonce:     gen.TxNonce(addr1),
				Value:     new(big.Int),
				Gas:       1000000,
				GasFeeCap: gen.header.BaseFee,
				Data:      code,
			})
			tx, err := types.SignTx(tx, signer, key1)
			if err != nil {
				t.Fatalf("failed to create tx: %v", err)
			}
			gen.AddTx(tx)
		}
	})
	if _, err := blockchain.InsertChain(chain); err != nil {
		t.Fatalf("failed to insert chain: %v", err)
	}

	_, chain, _ = GenerateChainWithGenesis(gspec, beacon.NewFaker(), 3, func(i int, gen *BlockGen) {})
	done := make(chan struct{})
	go func() {
		ev := <-rmLogsCh
		if len(ev.Logs) == 0 {
			t.Error("expected logs")
		}
		close(done)
	}()
	if _, err := blockchain.InsertChain(chain); err != nil {
		t.Fatalf("failed to insert forked chain: %v", err)
	}
	timeout := time.NewTimer(1 * time.Second)
	defer timeout.Stop()
	select {
	case <-done:
	case <-timeout.C:
		t.Fatal("Timeout. There is no RemovedLogsEvent has been sent.")
	}
}

// This ZVM code generates a log when the contract is created.
var logCode = common.Hex2Bytes("60606040525b7f24ec1d3ff24c2f6ff210738839dbc339cd45a5294d85c79361016243157aae7b60405180905060405180910390a15b600a8060416000396000f360606040526008565b00")

// This test checks that log events and RemovedLogsEvent are sent
// when the chain reorganizes.
func TestLogRebirth(t *testing.T) {
	testLogRebirth(t, rawdb.HashScheme)
	testLogRebirth(t, rawdb.PathScheme)
}

func testLogRebirth(t *testing.T, scheme string) {
	var (
		key1, _       = pqcrypto.HexToMLDSA87("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		addr1         = pqcrypto.MLDSA87ToAddress(key1)
		gspec         = &Genesis{Config: params.TestChainConfig, Alloc: GenesisAlloc{addr1: {Balance: big.NewInt(10000000000000000)}}}
		signer        = types.LatestSigner(gspec.Config)
		engine        = beacon.NewFaker()
		blockchain, _ = NewBlockChain(rawdb.NewMemoryDatabase(), DefaultCacheConfigWithScheme(scheme), gspec, engine, vm.Config{}, nil)
	)
	defer blockchain.Stop()

	// The event channels.
	newLogCh := make(chan []*types.Log, 10)
	rmLogsCh := make(chan RemovedLogsEvent, 10)
	blockchain.SubscribeLogsEvent(newLogCh)
	blockchain.SubscribeRemovedLogsEvent(rmLogsCh)

	// This chain contains 10 logs.
	genDb, chain, _ := GenerateChainWithGenesis(gspec, engine, 3, func(i int, gen *BlockGen) {
		if i < 2 {
			for ii := 0; ii < 5; ii++ {
				tx, err := types.SignNewTx(key1, signer, &types.DynamicFeeTx{
					Nonce:     gen.TxNonce(addr1),
					GasFeeCap: gen.header.BaseFee,
					Gas:       uint64(1000001),
					Data:      logCode,
				})
				if err != nil {
					t.Fatalf("failed to create tx: %v", err)
				}
				gen.AddTx(tx)
			}
		}
	})
	if _, err := blockchain.InsertChain(chain); err != nil {
		t.Fatalf("failed to insert chain: %v", err)
	}
	checkLogEvents(t, newLogCh, rmLogsCh, 10, 0)

	// Generate long reorg chain containing more logs. Inserting the
	// chain removes one log and adds four.
	_, forkChain, _ := GenerateChainWithGenesis(gspec, engine, 3, func(i int, gen *BlockGen) {
		if i == 2 {
			// The last (head) block is not part of the reorg-chain, we can ignore it
			return
		}
		for ii := 0; ii < 5; ii++ {
			tx, err := types.SignNewTx(key1, signer, &types.DynamicFeeTx{
				Nonce:     gen.TxNonce(addr1),
				GasFeeCap: gen.header.BaseFee,
				Gas:       uint64(1000000),
				Data:      logCode,
			})
			if err != nil {
				t.Fatalf("failed to create tx: %v", err)
			}
			gen.AddTx(tx)
		}
		gen.OffsetTime(-9) // higher block difficulty
	})
	if _, err := blockchain.InsertChain(forkChain); err != nil {
		t.Fatalf("failed to insert forked chain: %v", err)
	}
	checkLogEvents(t, newLogCh, rmLogsCh, 10, 10)

	// This chain segment is rooted in the original chain, but doesn't contain any logs.
	// When inserting it, the canonical chain switches away from forkChain and re-emits
	// the log event for the old chain, as well as a RemovedLogsEvent for forkChain.
	newBlocks, _ := GenerateChain(gspec.Config, chain[len(chain)-1], engine, genDb, 1, func(i int, gen *BlockGen) {})
	if _, err := blockchain.InsertChain(newBlocks); err != nil {
		t.Fatalf("failed to insert forked chain: %v", err)
	}
	checkLogEvents(t, newLogCh, rmLogsCh, 10, 10)
}

func checkLogEvents(t *testing.T, logsCh <-chan []*types.Log, rmLogsCh <-chan RemovedLogsEvent, wantNew, wantRemoved int) {
	t.Helper()
	var (
		countNew int
		countRm  int
		prev     int
	)
	// Drain events.
	for len(logsCh) > 0 {
		x := <-logsCh
		countNew += len(x)
		for _, log := range x {
			// We expect added logs to be in ascending order: 0:0, 0:1, 1:0 ...
			have := 100*int(log.BlockNumber) + int(log.TxIndex)
			if have < prev {
				t.Fatalf("Expected new logs to arrive in ascending order (%d < %d)", have, prev)
			}
			prev = have
		}
	}
	prev = 0
	for len(rmLogsCh) > 0 {
		x := <-rmLogsCh
		countRm += len(x.Logs)
		for _, log := range x.Logs {
			// We expect removed logs to be in ascending order: 0:0, 0:1, 1:0 ...
			have := 100*int(log.BlockNumber) + int(log.TxIndex)
			if have < prev {
				t.Fatalf("Expected removed logs to arrive in ascending order (%d < %d)", have, prev)
			}
			prev = have
		}
	}

	if countNew != wantNew {
		t.Fatalf("wrong number of log events: got %d, want %d", countNew, wantNew)
	}
	if countRm != wantRemoved {
		t.Fatalf("wrong number of removed log events: got %d, want %d", countRm, wantRemoved)
	}
}

func TestReorgSideEvent(t *testing.T) {
	testReorgSideEvent(t, rawdb.HashScheme)
	testReorgSideEvent(t, rawdb.PathScheme)
}

func testReorgSideEvent(t *testing.T, scheme string) {
	var (
		key1, _ = pqcrypto.HexToMLDSA87("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		addr1   = pqcrypto.MLDSA87ToAddress(key1)
		gspec   = &Genesis{
			Config: params.TestChainConfig,
			Alloc:  GenesisAlloc{addr1: {Balance: big.NewInt(10000000000000000)}},
		}
		signer = types.LatestSigner(gspec.Config)
	)
	blockchain, _ := NewBlockChain(rawdb.NewMemoryDatabase(), DefaultCacheConfigWithScheme(scheme), gspec, beacon.NewFaker(), vm.Config{}, nil)
	defer blockchain.Stop()

	_, chain, _ := GenerateChainWithGenesis(gspec, beacon.NewFaker(), 3, func(i int, gen *BlockGen) {})
	if _, err := blockchain.InsertChain(chain); err != nil {
		t.Fatalf("failed to insert chain: %v", err)
	}

	_, replacementBlocks, _ := GenerateChainWithGenesis(gspec, beacon.NewFaker(), 4, func(i int, gen *BlockGen) {
		tx := types.NewTx(&types.DynamicFeeTx{
			Nonce:     gen.TxNonce(addr1),
			Value:     new(big.Int),
			Gas:       1000000,
			GasFeeCap: gen.header.BaseFee,
			Data:      nil,
		})
		tx, err := types.SignTx(tx, signer, key1)
		if i == 2 {
			gen.OffsetTime(-9)
		}
		if err != nil {
			t.Fatalf("failed to create tx: %v", err)
		}
		gen.AddTx(tx)
	})
	chainSideCh := make(chan ChainSideEvent, 64)
	blockchain.SubscribeChainSideEvent(chainSideCh)
	if _, err := blockchain.InsertChain(replacementBlocks); err != nil {
		t.Fatalf("failed to insert chain: %v", err)
	}

	expectedSideHashes := map[common.Hash]bool{
		chain[0].Hash(): true,
		chain[1].Hash(): true,
		chain[2].Hash(): true,
	}

	i := 0

	const timeoutDura = 10 * time.Second
	timeout := time.NewTimer(timeoutDura)
done:
	for {
		select {
		case ev := <-chainSideCh:
			block := ev.Block
			if _, ok := expectedSideHashes[block.Hash()]; !ok {
				t.Errorf("%d: didn't expect %x to be in side chain", i, block.Hash())
			}
			i++

			if i == len(expectedSideHashes) {
				timeout.Stop()

				break done
			}
			timeout.Reset(timeoutDura)

		case <-timeout.C:
			t.Fatal("Timeout. Possibly not all blocks were triggered for sideevent")
		}
	}

	// make sure no more events are fired
	select {
	case e := <-chainSideCh:
		t.Errorf("unexpected event fired: %v", e)
	case <-time.After(250 * time.Millisecond):
	}
}

// Tests if the canonical block can be fetched from the database during chain insertion.
func TestCanonicalBlockRetrieval(t *testing.T) {
	testCanonicalBlockRetrieval(t, rawdb.HashScheme)
	testCanonicalBlockRetrieval(t, rawdb.PathScheme)
}

func testCanonicalBlockRetrieval(t *testing.T, scheme string) {
	_, gspec, blockchain, err := newCanonical(beacon.NewFaker(), 0, true, scheme)
	if err != nil {
		t.Fatalf("failed to create pristine chain: %v", err)
	}
	defer blockchain.Stop()

	_, chain, _ := GenerateChainWithGenesis(gspec, beacon.NewFaker(), 10, func(i int, gen *BlockGen) {})

	var pend sync.WaitGroup
	pend.Add(len(chain))

	for i := range chain {
		go func(block *types.Block) {
			defer pend.Done()

			// try to retrieve a block by its canonical hash and see if the block data can be retrieved.
			for {
				ch := rawdb.ReadCanonicalHash(blockchain.db, block.NumberU64())
				if ch == (common.Hash{}) {
					continue // busy wait for canonical hash to be written
				}
				if ch != block.Hash() {
					t.Errorf("unknown canonical hash, want %s, got %s", block.Hash().Hex(), ch.Hex())
					return
				}
				fb := rawdb.ReadBlock(blockchain.db, ch, block.NumberU64())
				if fb == nil {
					t.Errorf("unable to retrieve block %d for canonical hash: %s", block.NumberU64(), ch.Hex())
					return
				}
				if fb.Hash() != block.Hash() {
					t.Errorf("invalid block hash for block %d, want %s, got %s", block.NumberU64(), block.Hash().Hex(), fb.Hash().Hex())
					return
				}
				return
			}
		}(chain[i])

		if _, err := blockchain.InsertChain(types.Blocks{chain[i]}); err != nil {
			t.Fatalf("failed to insert block %d: %v", i, err)
		}
	}
	pend.Wait()
}

func TestEIP161AccountRemoval(t *testing.T) {
	testEIP161AccountRemoval(t, rawdb.HashScheme)
	testEIP161AccountRemoval(t, rawdb.PathScheme)
}

func testEIP161AccountRemoval(t *testing.T, scheme string) {
	// Configure and generate a sample block chain
	var (
		key, _  = pqcrypto.HexToMLDSA87("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		address = pqcrypto.MLDSA87ToAddress(key)
		funds   = big.NewInt(100000000000000000)
		theAddr = common.Address{1}
		gspec   = &Genesis{
			Config: &params.ChainConfig{
				ChainID: big.NewInt(1),
			},
			Alloc: GenesisAlloc{
				address: {Balance: funds},
			},
		}
	)
	_, blocks, _ := GenerateChainWithGenesis(gspec, beacon.NewFaker(), 2, func(i int, block *BlockGen) {
		var (
			tx     *types.Transaction
			err    error
			signer = types.LatestSigner(gspec.Config)
		)

		switch i {
		case 0:
			tx = types.NewTx(&types.DynamicFeeTx{
				Nonce:     block.TxNonce(address),
				To:        &theAddr,
				Value:     new(big.Int),
				Gas:       21000,
				GasFeeCap: block.header.BaseFee,
				Data:      nil,
			})
			tx, err = types.SignTx(tx, signer, key)
		case 1:
			tx = types.NewTx(&types.DynamicFeeTx{
				Nonce:     block.TxNonce(address),
				To:        &theAddr,
				Value:     new(big.Int),
				Gas:       21000,
				GasFeeCap: block.header.BaseFee,
				Data:      nil,
			})
			tx, err = types.SignTx(tx, signer, key)
		}
		if err != nil {
			t.Fatal(err)
		}
		block.AddTx(tx)
	})

	blockchain, _ := NewBlockChain(rawdb.NewMemoryDatabase(), DefaultCacheConfigWithScheme(scheme), gspec, beacon.NewFaker(), vm.Config{}, nil)
	defer blockchain.Stop()

	// account should not be created
	if _, err := blockchain.InsertChain(types.Blocks{blocks[0]}); err != nil {
		t.Fatal(err)
	}
	if st, _ := blockchain.State(); st.Exist(theAddr) {
		t.Error("account should not exist")
	}

	// account should not be created
	if _, err := blockchain.InsertChain(types.Blocks{blocks[1]}); err != nil {
		t.Fatal(err)
	}
	if st, _ := blockchain.State(); st.Exist(theAddr) {
		t.Error("account should not exist")
	}
}

// This is a regression test (i.e. as weird as it is, don't delete it ever), which
// tests that under weird reorg conditions the blockchain and its internal header-
// chain return the same latest block/header.
//
// https://github.com/theQRL/go-zond/pull/15941
func TestBlockchainHeaderchainReorgConsistency(t *testing.T) {
	testBlockchainHeaderchainReorgConsistency(t, rawdb.HashScheme)
	testBlockchainHeaderchainReorgConsistency(t, rawdb.PathScheme)
}

func testBlockchainHeaderchainReorgConsistency(t *testing.T, scheme string) {
	// Generate a canonical chain to act as the main dataset
	engine := beacon.NewFaker()
	genesis := &Genesis{
		Config:  params.TestChainConfig,
		BaseFee: big.NewInt(params.InitialBaseFee),
	}
	genDb, blocks, _ := GenerateChainWithGenesis(genesis, engine, 64, func(i int, b *BlockGen) { b.SetCoinbase(common.Address{1}) })

	// Generate a bunch of fork blocks, each side forking from the canonical chain
	forks := make([]*types.Block, len(blocks))
	for i := 0; i < len(forks); i++ {
		parent := genesis.ToBlock()
		if i > 0 {
			parent = blocks[i-1]
		}
		fork, _ := GenerateChain(genesis.Config, parent, engine, genDb, 1, func(i int, b *BlockGen) { b.SetCoinbase(common.Address{2}) })
		forks[i] = fork[0]
	}
	// Import the canonical and fork chain side by side, verifying the current block
	// and current header consistency
	chain, err := NewBlockChain(rawdb.NewMemoryDatabase(), DefaultCacheConfigWithScheme(scheme), genesis, engine, vm.Config{}, nil)
	if err != nil {
		t.Fatalf("failed to create tester chain: %v", err)
	}
	defer chain.Stop()

	for i := 0; i < len(blocks); i++ {
		if _, err := chain.InsertChain(blocks[i : i+1]); err != nil {
			t.Fatalf("block %d: failed to insert into chain: %v", i, err)
		}
		if chain.CurrentBlock().Hash() != chain.CurrentHeader().Hash() {
			t.Errorf("block %d: current block/header mismatch: block #%d [%x..], header #%d [%x..]", i, chain.CurrentBlock().Number, chain.CurrentBlock().Hash().Bytes()[:4], chain.CurrentHeader().Number, chain.CurrentHeader().Hash().Bytes()[:4])
		}
		if _, err := chain.InsertChain(forks[i : i+1]); err != nil {
			t.Fatalf(" fork %d: failed to insert into chain: %v", i, err)
		}
		if chain.CurrentBlock().Hash() != chain.CurrentHeader().Hash() {
			t.Errorf(" fork %d: current block/header mismatch: block #%d [%x..], header #%d [%x..]", i, chain.CurrentBlock().Number, chain.CurrentBlock().Hash().Bytes()[:4], chain.CurrentHeader().Number, chain.CurrentHeader().Hash().Bytes()[:4])
		}
	}
}

// Tests that importing small side forks doesn't leave junk in the trie database
// cache (which would eventually cause memory issues).
func TestTrieForkGC(t *testing.T) {
	// Generate a canonical chain to act as the main dataset
	engine := beacon.NewFaker()
	genesis := &Genesis{
		Config:  params.TestChainConfig,
		BaseFee: big.NewInt(params.InitialBaseFee),
	}
	genDb, blocks, _ := GenerateChainWithGenesis(genesis, engine, 2*TriesInMemory, func(i int, b *BlockGen) { b.SetCoinbase(common.Address{1}) })

	// Generate a bunch of fork blocks, each side forking from the canonical chain
	forks := make([]*types.Block, len(blocks))
	for i := 0; i < len(forks); i++ {
		parent := genesis.ToBlock()
		if i > 0 {
			parent = blocks[i-1]
		}
		fork, _ := GenerateChain(genesis.Config, parent, engine, genDb, 1, func(i int, b *BlockGen) { b.SetCoinbase(common.Address{2}) })
		forks[i] = fork[0]
	}
	// Import the canonical and fork chain side by side, forcing the trie cache to cache both
	chain, err := NewBlockChain(rawdb.NewMemoryDatabase(), nil, genesis, engine, vm.Config{}, nil)
	if err != nil {
		t.Fatalf("failed to create tester chain: %v", err)
	}
	defer chain.Stop()

	for i := 0; i < len(blocks); i++ {
		if _, err := chain.InsertChain(blocks[i : i+1]); err != nil {
			t.Fatalf("block %d: failed to insert into chain: %v", i, err)
		}
		if _, err := chain.InsertChain(forks[i : i+1]); err != nil {
			t.Fatalf("fork %d: failed to insert into chain: %v", i, err)
		}
	}
	// Dereference all the recent tries and ensure no past trie is left in
	for i := 0; i < TriesInMemory; i++ {
		chain.TrieDB().Dereference(blocks[len(blocks)-1-i].Root())
		chain.TrieDB().Dereference(forks[len(blocks)-1-i].Root())
	}
	if _, nodes, _ := chain.TrieDB().Size(); nodes > 0 { // all memory is returned in the nodes return for hashdb
		t.Fatalf("stale tries still alive after garbase collection")
	}
}

// Tests that doing large reorgs works even if the state associated with the
// forking point is not available any more.
func TestLargeReorgTrieGC(t *testing.T) {
	t.Skip() // NOTE(rgeraldes24): not valid
	testLargeReorgTrieGC(t, rawdb.HashScheme)
	testLargeReorgTrieGC(t, rawdb.PathScheme)
}

func testLargeReorgTrieGC(t *testing.T, scheme string) {
	// Generate the original common chain segment and the two competing forks
	engine := beacon.NewFaker()
	genesis := &Genesis{
		Config:  params.TestChainConfig,
		BaseFee: big.NewInt(params.InitialBaseFee),
	}
	genDb, shared, _ := GenerateChainWithGenesis(genesis, engine, 64, func(i int, b *BlockGen) { b.SetCoinbase(common.Address{1}) })
	original, _ := GenerateChain(genesis.Config, shared[len(shared)-1], engine, genDb, 2*TriesInMemory, func(i int, b *BlockGen) { b.SetCoinbase(common.Address{2}) })
	competitor, _ := GenerateChain(genesis.Config, shared[len(shared)-1], engine, genDb, 2*TriesInMemory+1, func(i int, b *BlockGen) { b.SetCoinbase(common.Address{3}) })

	// Import the shared chain and the original canonical one
	db, _ := rawdb.NewDatabaseWithFreezer(rawdb.NewMemoryDatabase(), t.TempDir(), "", false)
	defer db.Close()

	chain, err := NewBlockChain(db, DefaultCacheConfigWithScheme(scheme), genesis, engine, vm.Config{}, nil)
	if err != nil {
		t.Fatalf("failed to create tester chain: %v", err)
	}
	defer chain.Stop()

	if _, err := chain.InsertChain(shared); err != nil {
		t.Fatalf("failed to insert shared chain: %v", err)
	}
	if _, err := chain.InsertChain(original); err != nil {
		t.Fatalf("failed to insert original chain: %v", err)
	}
	// Ensure that the state associated with the forking point is pruned away
	if chain.HasState(shared[len(shared)-1].Root()) {
		t.Fatalf("common-but-old ancestor still cache")
	}
	// Import the competitor chain without exceeding the canonical's TD and ensure
	// we have not processed any of the blocks (protection against malicious blocks)
	if _, err := chain.InsertChain(competitor[:len(competitor)-2]); err != nil {
		t.Fatalf("failed to insert competitor chain: %v", err)
	}
	for i, block := range competitor[:len(competitor)-2] {
		if chain.HasState(block.Root()) {
			t.Fatalf("competitor %d: low TD chain became processed", i)
		}
	}
	// Import the head of the competitor chain, triggering the reorg and ensure we
	// successfully reprocess all the stashed away blocks.
	if _, err := chain.InsertChain(competitor[len(competitor)-2:]); err != nil {
		t.Fatalf("failed to finalize competitor chain: %v", err)
	}
	// In path-based trie database implementation, it will keep 128 diff + 1 disk
	// layers, totally 129 latest states available. In hash-based it's 128.
	states := TriesInMemory
	if scheme == rawdb.PathScheme {
		states = states + 1
	}
	for i, block := range competitor[:len(competitor)-states] {
		if chain.HasState(block.Root()) {
			t.Fatalf("competitor %d: unexpected competing chain state", i)
		}
	}
	for i, block := range competitor[len(competitor)-states:] {
		if !chain.HasState(block.Root()) {
			t.Fatalf("competitor %d: competing chain state missing", i)
		}
	}
}

func TestBlockchainRecovery(t *testing.T) {
	testBlockchainRecovery(t, rawdb.HashScheme)
	testBlockchainRecovery(t, rawdb.PathScheme)
}

func testBlockchainRecovery(t *testing.T, scheme string) {
	// Configure and generate a sample block chain
	var (
		key, _  = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		address = crypto.PubkeyToAddress(key.PublicKey)
		funds   = big.NewInt(1000000000)
		gspec   = &Genesis{Config: params.TestChainConfig, Alloc: GenesisAlloc{address: {Balance: funds}}}
	)
	height := uint64(1024)
	_, blocks, receipts := GenerateChainWithGenesis(gspec, beacon.NewFaker(), int(height), nil)

	// Import the chain as a ancient-first node and ensure all pointers are updated
	ancientDb, err := rawdb.NewDatabaseWithFreezer(rawdb.NewMemoryDatabase(), t.TempDir(), "", false)
	if err != nil {
		t.Fatalf("failed to create temp freezer db: %v", err)
	}
	defer ancientDb.Close()
	ancient, _ := NewBlockChain(ancientDb, DefaultCacheConfigWithScheme(scheme), gspec, beacon.NewFaker(), vm.Config{}, nil)

	headers := make([]*types.Header, len(blocks))
	for i, block := range blocks {
		headers[i] = block.Header()
	}
	if n, err := ancient.InsertHeaderChain(headers); err != nil {
		t.Fatalf("failed to insert header %d: %v", n, err)
	}
	if n, err := ancient.InsertReceiptChain(blocks, receipts, uint64(3*len(blocks)/4)); err != nil {
		t.Fatalf("failed to insert receipt %d: %v", n, err)
	}
	rawdb.WriteLastPivotNumber(ancientDb, blocks[len(blocks)-1].NumberU64()) // Force fast sync behavior
	ancient.Stop()

	// Destroy head fast block manually
	midBlock := blocks[len(blocks)/2]
	rawdb.WriteHeadFastBlockHash(ancientDb, midBlock.Hash())

	// Reopen broken blockchain again
	ancient, _ = NewBlockChain(ancientDb, DefaultCacheConfigWithScheme(scheme), gspec, beacon.NewFaker(), vm.Config{}, nil)
	defer ancient.Stop()
	if num := ancient.CurrentBlock().Number.Uint64(); num != 0 {
		t.Errorf("head block mismatch: have #%v, want #%v", num, 0)
	}
	if num := ancient.CurrentSnapBlock().Number.Uint64(); num != midBlock.NumberU64() {
		t.Errorf("head snap-block mismatch: have #%v, want #%v", num, midBlock.NumberU64())
	}
	if num := ancient.CurrentHeader().Number.Uint64(); num != midBlock.NumberU64() {
		t.Errorf("head header mismatch: have #%v, want #%v", num, midBlock.NumberU64())
	}
}

// This test checks that InsertReceiptChain will roll back correctly when attempting to insert a side chain.
func TestInsertReceiptChainRollback(t *testing.T) {
	testInsertReceiptChainRollback(t, rawdb.HashScheme)
	testInsertReceiptChainRollback(t, rawdb.PathScheme)
}

func testInsertReceiptChainRollback(t *testing.T, scheme string) {
	// Generate forked chain. The returned BlockChain object is used to process the side chain blocks.
	tmpChain, sideblocks, canonblocks, gspec, err := getLongAndShortChains(scheme)
	if err != nil {
		t.Fatal(err)
	}
	defer tmpChain.Stop()
	// Get the side chain receipts.
	if _, err := tmpChain.InsertChain(sideblocks); err != nil {
		t.Fatal("processing side chain failed:", err)
	}
	t.Log("sidechain head:", tmpChain.CurrentBlock().Number, tmpChain.CurrentBlock().Hash())
	sidechainReceipts := make([]types.Receipts, len(sideblocks))
	for i, block := range sideblocks {
		sidechainReceipts[i] = tmpChain.GetReceiptsByHash(block.Hash())
	}
	// Get the canon chain receipts.
	if _, err := tmpChain.InsertChain(canonblocks); err != nil {
		t.Fatal("processing canon chain failed:", err)
	}
	t.Log("canon head:", tmpChain.CurrentBlock().Number, tmpChain.CurrentBlock().Hash())
	canonReceipts := make([]types.Receipts, len(canonblocks))
	for i, block := range canonblocks {
		canonReceipts[i] = tmpChain.GetReceiptsByHash(block.Hash())
	}

	// Set up a BlockChain that uses the ancient store.
	ancientDb, err := rawdb.NewDatabaseWithFreezer(rawdb.NewMemoryDatabase(), t.TempDir(), "", false)
	if err != nil {
		t.Fatalf("failed to create temp freezer db: %v", err)
	}
	defer ancientDb.Close()

	ancientChain, _ := NewBlockChain(ancientDb, DefaultCacheConfigWithScheme(scheme), gspec, beacon.NewFaker(), vm.Config{}, nil)
	defer ancientChain.Stop()

	// Import the canonical header chain.
	canonHeaders := make([]*types.Header, len(canonblocks))
	for i, block := range canonblocks {
		canonHeaders[i] = block.Header()
	}
	if _, err = ancientChain.InsertHeaderChain(canonHeaders); err != nil {
		t.Fatal("can't import canon headers:", err)
	}

	// Try to insert blocks/receipts of the side chain.
	_, err = ancientChain.InsertReceiptChain(sideblocks, sidechainReceipts, uint64(len(sideblocks)))
	if err == nil {
		t.Fatal("expected error from InsertReceiptChain.")
	}
	if ancientChain.CurrentSnapBlock().Number.Uint64() != 0 {
		t.Fatalf("failed to rollback ancient data, want %d, have %d", 0, ancientChain.CurrentSnapBlock().Number)
	}
	if frozen, err := ancientChain.db.Ancients(); err != nil || frozen != 1 {
		t.Fatalf("failed to truncate ancient data, frozen index is %d", frozen)
	}

	// Insert blocks/receipts of the canonical chain.
	_, err = ancientChain.InsertReceiptChain(canonblocks, canonReceipts, uint64(len(canonblocks)))
	if err != nil {
		t.Fatalf("can't import canon chain receipts: %v", err)
	}
	if ancientChain.CurrentSnapBlock().Number.Uint64() != canonblocks[len(canonblocks)-1].NumberU64() {
		t.Fatalf("failed to insert ancient recept chain after rollback")
	}
	if frozen, _ := ancientChain.db.Ancients(); frozen != uint64(len(canonblocks))+1 {
		t.Fatalf("wrong ancients count %d", frozen)
	}
}

func TestInsertKnownHeaders(t *testing.T) {
	testInsertKnownChainData(t, "headers")
}
func TestInsertKnownReceiptChain(t *testing.T) {
	testInsertKnownChainData(t, "receipts")
}
func TestInsertKnownBlocks(t *testing.T) {
	testInsertKnownChainData(t, "blocks")
}

func testInsertKnownChainData(t *testing.T, typ string) {
	// Copy the TestChainConfig so we can modify it during tests
	chainConfig := *params.TestChainConfig
	var (
		genesis = &Genesis{
			BaseFee: big.NewInt(params.InitialBaseFee),
			Config:  &chainConfig,
		}
		engine = beacon.New()
	)

	genDb, blocks, receipts := GenerateChainWithGenesis(genesis, engine, 32,
		func(i int, b *BlockGen) {
			b.SetCoinbase(common.Address{1})
		})

	// Longer chain and shorter chain
	blocks2, receipts2 := GenerateChain(genesis.Config, blocks[len(blocks)-1], engine, genDb, 65, func(i int, b *BlockGen) {
		b.SetCoinbase(common.Address{1})
	})
	blocks3, receipts3 := GenerateChain(genesis.Config, blocks[len(blocks)-1], engine, genDb, 64, func(i int, b *BlockGen) {
		b.SetCoinbase(common.Address{1})
		b.OffsetTime(-9) // Time shifted
	})
	// Import the shared chain and the original canonical one
	chaindb, err := rawdb.NewDatabaseWithFreezer(rawdb.NewMemoryDatabase(), t.TempDir(), "", false)
	if err != nil {
		t.Fatalf("failed to create temp freezer db: %v", err)
	}
	defer chaindb.Close()

	chain, err := NewBlockChain(chaindb, nil, genesis, engine, vm.Config{}, nil)
	if err != nil {
		t.Fatalf("failed to create tester chain: %v", err)
	}
	defer chain.Stop()

	var (
		inserter func(blocks []*types.Block, receipts []types.Receipts) error
		asserter func(t *testing.T, block *types.Block)
	)
	if typ == "headers" {
		inserter = func(blocks []*types.Block, receipts []types.Receipts) error {
			headers := make([]*types.Header, 0, len(blocks))
			for _, block := range blocks {
				headers = append(headers, block.Header())
			}
			i, err := chain.InsertHeaderChain(headers)
			if err != nil {
				return fmt.Errorf("index %d, number %d: %w", i, headers[i].Number, err)
			}
			return err
		}
		asserter = func(t *testing.T, block *types.Block) {
			if chain.CurrentHeader().Hash() != block.Hash() {
				t.Fatalf("current head header mismatch, have %v, want %v", chain.CurrentHeader().Hash().Hex(), block.Hash().Hex())
			}
		}
	} else if typ == "receipts" {
		inserter = func(blocks []*types.Block, receipts []types.Receipts) error {
			headers := make([]*types.Header, 0, len(blocks))
			for _, block := range blocks {
				headers = append(headers, block.Header())
			}
			i, err := chain.InsertHeaderChain(headers)
			if err != nil {
				return fmt.Errorf("index %d: %w", i, err)
			}
			_, err = chain.InsertReceiptChain(blocks, receipts, 0)
			return err
		}
		asserter = func(t *testing.T, block *types.Block) {
			if chain.CurrentSnapBlock().Hash() != block.Hash() {
				t.Fatalf("current head fast block mismatch, have %v, want %v", chain.CurrentSnapBlock().Hash().Hex(), block.Hash().Hex())
			}
		}
	} else {
		inserter = func(blocks []*types.Block, _ []types.Receipts) error {
			i, err := chain.InsertChain(blocks)
			if err != nil {
				return fmt.Errorf("index %d: %w", i, err)
			}
			return nil
		}
		asserter = func(t *testing.T, block *types.Block) {
			if chain.CurrentBlock().Hash() != block.Hash() {
				t.Fatalf("current head block mismatch, have %v, want %v", chain.CurrentBlock().Hash().Hex(), block.Hash().Hex())
			}
		}
	}
	if err := inserter(blocks, receipts); err != nil {
		t.Fatalf("failed to insert chain data: %v", err)
	}

	// Reimport the chain data again. All the imported
	// chain data are regarded "known" data.
	if err := inserter(blocks, receipts); err != nil {
		t.Fatalf("failed to insert chain data: %v", err)
	}
	asserter(t, blocks[len(blocks)-1])

	// Import a long canonical chain with some known data as prefix.
	rollback := blocks[len(blocks)/2].NumberU64()
	chain.SetHead(rollback - 1)
	if err := inserter(blocks, receipts); err != nil {
		t.Fatalf("failed to insert chain data: %v", err)
	}
	asserter(t, blocks[len(blocks)-1])

	// Import a longer chain with some known data as prefix.
	if err := inserter(append(blocks, blocks2...), append(receipts, receipts2...)); err != nil {
		t.Fatalf("failed to insert chain data: %v", err)
	}
	asserter(t, blocks2[len(blocks2)-1])

	// Import a shorter chain with some known data as prefix.
	// The reorg is expected since the fork choice rule is
	// already changed.
	if err := inserter(append(blocks, blocks3...), append(receipts, receipts3...)); err != nil {
		t.Fatalf("failed to insert chain data: %v", err)
	}
	// The head shouldn't change.
	asserter(t, blocks3[len(blocks3)-1])

	// Reimport the longer chain again, the reorg is still expected
	chain.SetHead(rollback - 1)
	if err := inserter(append(blocks, blocks2...), append(receipts, receipts2...)); err != nil {
		t.Fatalf("failed to insert chain data: %v", err)
	}
	asserter(t, blocks2[len(blocks2)-1])
}

// getLongAndShortChains returns two chains: A is longer, B is heavier.
func getLongAndShortChains(scheme string) (*BlockChain, []*types.Block, []*types.Block, *Genesis, error) {
	// Generate a canonical chain to act as the main dataset
	engine := beacon.NewFaker()
	genesis := &Genesis{
		Config:  params.TestChainConfig,
		BaseFee: big.NewInt(params.InitialBaseFee),
	}
	// Generate and import the canonical chain,
	// Offset the time, to keep the difficulty low
	genDb, longChain, _ := GenerateChainWithGenesis(genesis, engine, 80, func(i int, b *BlockGen) {
		b.SetCoinbase(common.Address{1})
	})
	chain, err := NewBlockChain(rawdb.NewMemoryDatabase(), DefaultCacheConfigWithScheme(scheme), genesis, engine, vm.Config{}, nil)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to create tester chain: %v", err)
	}
	// Generate fork chain, make it shorter than canon, with common ancestor pretty early
	parentIndex := 3
	parent := longChain[parentIndex]
	heavyChainExt, _ := GenerateChain(genesis.Config, parent, engine, genDb, 75, func(i int, b *BlockGen) {
		b.SetCoinbase(common.Address{2})
		b.OffsetTime(-9)
	})
	var heavyChain []*types.Block
	heavyChain = append(heavyChain, longChain[:parentIndex+1]...)
	heavyChain = append(heavyChain, heavyChainExt...)

	longerNum := longChain[len(longChain)-1].NumberU64()
	shorterNum := heavyChain[len(heavyChain)-1].NumberU64()
	if shorterNum >= longerNum {
		return nil, nil, nil, nil, fmt.Errorf("test is moot, heavyChain num (%v) must be lower than canon num (%v)", shorterNum, longerNum)
	}
	return chain, longChain, heavyChain, genesis, nil
}

// TestReorgToShorterRemovesCanonMapping tests that if we
// 1. Have a chain [0 ... N .. X]
// 2. Reorg to shorter but heavier chain [0 ... N ... Y]
// 3. Then there should be no canon mapping for the block at height X
// 4. The forked block should still be retrievable by hash
func TestReorgToShorterRemovesCanonMapping(t *testing.T) {
	testReorgToShorterRemovesCanonMapping(t, rawdb.HashScheme)
	testReorgToShorterRemovesCanonMapping(t, rawdb.PathScheme)
}

func testReorgToShorterRemovesCanonMapping(t *testing.T, scheme string) {
	chain, canonblocks, sideblocks, _, err := getLongAndShortChains(scheme)
	if err != nil {
		t.Fatal(err)
	}
	defer chain.Stop()

	if n, err := chain.InsertChain(canonblocks); err != nil {
		t.Fatalf("block %d: failed to insert into chain: %v", n, err)
	}
	canonNum := chain.CurrentBlock().Number.Uint64()
	canonHash := chain.CurrentBlock().Hash()
	_, err = chain.InsertChain(sideblocks)
	if err != nil {
		t.Errorf("Got error, %v", err)
	}
	head := chain.CurrentBlock()
	if got := sideblocks[len(sideblocks)-1].Hash(); got != head.Hash() {
		t.Fatalf("head wrong, expected %x got %x", head.Hash(), got)
	}
	// We have now inserted a sidechain.
	if blockByNum := chain.GetBlockByNumber(canonNum); blockByNum != nil {
		t.Errorf("expected block to be gone: %v", blockByNum.NumberU64())
	}
	if headerByNum := chain.GetHeaderByNumber(canonNum); headerByNum != nil {
		t.Errorf("expected header to be gone: %v", headerByNum.Number)
	}
	if blockByHash := chain.GetBlockByHash(canonHash); blockByHash == nil {
		t.Errorf("expected block to be present: %x", blockByHash.Hash())
	}
	if headerByHash := chain.GetHeaderByHash(canonHash); headerByHash == nil {
		t.Errorf("expected header to be present: %x", headerByHash.Hash())
	}
}

// TestReorgToShorterRemovesCanonMappingHeaderChain is the same scenario
// as TestReorgToShorterRemovesCanonMapping, but applied on headerchain
// imports -- that is, for fast sync
func TestReorgToShorterRemovesCanonMappingHeaderChain(t *testing.T) {
	testReorgToShorterRemovesCanonMappingHeaderChain(t, rawdb.HashScheme)
	testReorgToShorterRemovesCanonMappingHeaderChain(t, rawdb.PathScheme)
}

func testReorgToShorterRemovesCanonMappingHeaderChain(t *testing.T, scheme string) {
	chain, canonblocks, sideblocks, _, err := getLongAndShortChains(scheme)
	if err != nil {
		t.Fatal(err)
	}
	defer chain.Stop()

	// Convert into headers
	canonHeaders := make([]*types.Header, len(canonblocks))
	for i, block := range canonblocks {
		canonHeaders[i] = block.Header()
	}
	if n, err := chain.InsertHeaderChain(canonHeaders); err != nil {
		t.Fatalf("header %d: failed to insert into chain: %v", n, err)
	}
	canonNum := chain.CurrentHeader().Number.Uint64()
	canonHash := chain.CurrentBlock().Hash()
	sideHeaders := make([]*types.Header, len(sideblocks))
	for i, block := range sideblocks {
		sideHeaders[i] = block.Header()
	}
	if n, err := chain.InsertHeaderChain(sideHeaders); err != nil {
		t.Fatalf("header %d: failed to insert into chain: %v", n, err)
	}
	head := chain.CurrentHeader()
	if got := sideblocks[len(sideblocks)-1].Hash(); got != head.Hash() {
		t.Fatalf("head wrong, expected %x got %x", head.Hash(), got)
	}
	// We have now inserted a sidechain.
	if blockByNum := chain.GetBlockByNumber(canonNum); blockByNum != nil {
		t.Errorf("expected block to be gone: %v", blockByNum.NumberU64())
	}
	if headerByNum := chain.GetHeaderByNumber(canonNum); headerByNum != nil {
		t.Errorf("expected header to be gone: %v", headerByNum.Number.Uint64())
	}
	if blockByHash := chain.GetBlockByHash(canonHash); blockByHash == nil {
		t.Errorf("expected block to be present: %x", blockByHash.Hash())
	}
	if headerByHash := chain.GetHeaderByHash(canonHash); headerByHash == nil {
		t.Errorf("expected header to be present: %x", headerByHash.Hash())
	}
}

func TestTransactionIndices(t *testing.T) {
	// Configure and generate a sample block chain
	var (
		key, _  = pqcrypto.HexToMLDSA87("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		address = pqcrypto.MLDSA87ToAddress(key)
		funds   = big.NewInt(100000000000000000)
		gspec   = &Genesis{
			Config:  params.TestChainConfig,
			Alloc:   GenesisAlloc{address: {Balance: funds}},
			BaseFee: big.NewInt(params.InitialBaseFee),
		}
		signer = types.LatestSigner(gspec.Config)
	)
	_, blocks, receipts := GenerateChainWithGenesis(gspec, beacon.NewFaker(), 128, func(i int, block *BlockGen) {
		tx := types.NewTx(&types.DynamicFeeTx{
			Nonce:     block.TxNonce(address),
			To:        &common.Address{0x00},
			Value:     big.NewInt(1000),
			Gas:       params.TxGas,
			GasFeeCap: block.header.BaseFee,
			Data:      nil,
		})
		tx, err := types.SignTx(tx, signer, key)
		if err != nil {
			panic(err)
		}
		block.AddTx(tx)
	})

	check := func(tail *uint64, chain *BlockChain) {
		stored := rawdb.ReadTxIndexTail(chain.db)
		if tail == nil && stored != nil {
			t.Fatalf("Oldest indexded block mismatch, want nil, have %d", *stored)
		}
		if tail != nil && *stored != *tail {
			t.Fatalf("Oldest indexded block mismatch, want %d, have %d", *tail, *stored)
		}
		if tail != nil {
			for i := *tail; i <= chain.CurrentBlock().Number.Uint64(); i++ {
				block := rawdb.ReadBlock(chain.db, rawdb.ReadCanonicalHash(chain.db, i), i)
				if block.Transactions().Len() == 0 {
					continue
				}
				for _, tx := range block.Transactions() {
					if index := rawdb.ReadTxLookupEntry(chain.db, tx.Hash()); index == nil {
						t.Fatalf("Miss transaction indice, number %d hash %s", i, tx.Hash().Hex())
					}
				}
			}
			for i := uint64(0); i < *tail; i++ {
				block := rawdb.ReadBlock(chain.db, rawdb.ReadCanonicalHash(chain.db, i), i)
				if block.Transactions().Len() == 0 {
					continue
				}
				for _, tx := range block.Transactions() {
					if index := rawdb.ReadTxLookupEntry(chain.db, tx.Hash()); index != nil {
						t.Fatalf("Transaction indice should be deleted, number %d hash %s", i, tx.Hash().Hex())
					}
				}
			}
		}
	}
	// Init block chain with external ancients, check all needed indices has been indexed.
	limit := []uint64{0, 32, 64, 128}
	for _, l := range limit {
		frdir := t.TempDir()
		ancientDb, _ := rawdb.NewDatabaseWithFreezer(rawdb.NewMemoryDatabase(), frdir, "", false)
		rawdb.WriteAncientBlocks(ancientDb, append([]*types.Block{gspec.ToBlock()}, blocks...), append([]types.Receipts{{}}, receipts...))

		l := l
		chain, err := NewBlockChain(ancientDb, nil, gspec, beacon.NewFaker(), vm.Config{}, &l)
		if err != nil {
			t.Fatalf("failed to create tester chain: %v", err)
		}
		chain.indexBlocks(rawdb.ReadTxIndexTail(ancientDb), 128, make(chan struct{}))

		var tail uint64
		if l != 0 {
			tail = uint64(128) - l + 1
		}
		check(&tail, chain)
		chain.Stop()
		ancientDb.Close()
		os.RemoveAll(frdir)
	}

	// Reconstruct a block chain which only reserves HEAD-64 tx indices
	ancientDb, _ := rawdb.NewDatabaseWithFreezer(rawdb.NewMemoryDatabase(), t.TempDir(), "", false)
	defer ancientDb.Close()

	rawdb.WriteAncientBlocks(ancientDb, append([]*types.Block{gspec.ToBlock()}, blocks...), append([]types.Receipts{{}}, receipts...))
	limit = []uint64{0, 64 /* drop stale */, 32 /* shorten history */, 64 /* extend history */, 0 /* restore all */}
	for _, l := range limit {
		l := l
		chain, err := NewBlockChain(ancientDb, nil, gspec, beacon.NewFaker(), vm.Config{}, &l)
		if err != nil {
			t.Fatalf("failed to create tester chain: %v", err)
		}
		var tail uint64
		if l != 0 {
			tail = uint64(128) - l + 1
		}
		chain.indexBlocks(rawdb.ReadTxIndexTail(ancientDb), 128, make(chan struct{}))
		check(&tail, chain)
		chain.Stop()
	}
}

func TestSkipStaleTxIndicesInSnapSync(t *testing.T) {
	testSkipStaleTxIndicesInSnapSync(t, rawdb.HashScheme)
	testSkipStaleTxIndicesInSnapSync(t, rawdb.PathScheme)
}

func testSkipStaleTxIndicesInSnapSync(t *testing.T, scheme string) {
	// Configure and generate a sample block chain
	var (
		key, _  = pqcrypto.HexToMLDSA87("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		address = pqcrypto.MLDSA87ToAddress(key)
		funds   = big.NewInt(100000000000000000)
		gspec   = &Genesis{Config: params.TestChainConfig, Alloc: GenesisAlloc{address: {Balance: funds}}}
		signer  = types.LatestSigner(gspec.Config)
	)
	_, blocks, receipts := GenerateChainWithGenesis(gspec, beacon.NewFaker(), 128, func(i int, block *BlockGen) {
		tx := types.NewTx(&types.DynamicFeeTx{
			Nonce:     block.TxNonce(address),
			To:        &common.Address{0x00},
			Value:     big.NewInt(1000),
			Gas:       params.TxGas,
			GasFeeCap: block.header.BaseFee,
			Data:      nil,
		})
		tx, err := types.SignTx(tx, signer, key)
		if err != nil {
			panic(err)
		}
		block.AddTx(tx)
	})

	check := func(tail *uint64, chain *BlockChain) {
		stored := rawdb.ReadTxIndexTail(chain.db)
		if tail == nil && stored != nil {
			t.Fatalf("Oldest indexded block mismatch, want nil, have %d", *stored)
		}
		if tail != nil && *stored != *tail {
			t.Fatalf("Oldest indexded block mismatch, want %d, have %d", *tail, *stored)
		}
		if tail != nil {
			for i := *tail; i <= chain.CurrentBlock().Number.Uint64(); i++ {
				block := rawdb.ReadBlock(chain.db, rawdb.ReadCanonicalHash(chain.db, i), i)
				if block.Transactions().Len() == 0 {
					continue
				}
				for _, tx := range block.Transactions() {
					if index := rawdb.ReadTxLookupEntry(chain.db, tx.Hash()); index == nil {
						t.Fatalf("Miss transaction indice, number %d hash %s", i, tx.Hash().Hex())
					}
				}
			}
			for i := uint64(0); i < *tail; i++ {
				block := rawdb.ReadBlock(chain.db, rawdb.ReadCanonicalHash(chain.db, i), i)
				if block.Transactions().Len() == 0 {
					continue
				}
				for _, tx := range block.Transactions() {
					if index := rawdb.ReadTxLookupEntry(chain.db, tx.Hash()); index != nil {
						t.Fatalf("Transaction indice should be deleted, number %d hash %s", i, tx.Hash().Hex())
					}
				}
			}
		}
	}

	ancientDb, err := rawdb.NewDatabaseWithFreezer(rawdb.NewMemoryDatabase(), t.TempDir(), "", false)
	if err != nil {
		t.Fatalf("failed to create temp freezer db: %v", err)
	}
	defer ancientDb.Close()

	// Import all blocks into ancient db, only HEAD-32 indices are kept.
	l := uint64(32)
	chain, err := NewBlockChain(ancientDb, DefaultCacheConfigWithScheme(scheme), gspec, beacon.NewFaker(), vm.Config{}, &l)
	if err != nil {
		t.Fatalf("failed to create tester chain: %v", err)
	}
	defer chain.Stop()

	headers := make([]*types.Header, len(blocks))
	for i, block := range blocks {
		headers[i] = block.Header()
	}
	if n, err := chain.InsertHeaderChain(headers); err != nil {
		t.Fatalf("failed to insert header %d: %v", n, err)
	}
	// The indices before ancient-N(32) should be ignored. After that all blocks should be indexed.
	if n, err := chain.InsertReceiptChain(blocks, receipts, 64); err != nil {
		t.Fatalf("block %d: failed to insert into chain: %v", n, err)
	}
	tail := uint64(32)
	check(&tail, chain)
}

// Benchmarks large blocks with value transfers to non-existing accounts
func benchmarkLargeNumberOfValueToNonexisting(b *testing.B, numTxs, numBlocks int, recipientFn func(uint64) common.Address) {
	var (
		address, _      = common.NewAddressFromString("Z000000000000000000000000000000000000c0de")
		signer          = types.ShanghaiSigner{}
		testBankKey, _  = pqcrypto.HexToMLDSA87("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		testBankAddress = pqcrypto.MLDSA87ToAddress(testBankKey)
		bankFunds       = big.NewInt(100000000000000000)
		gspec           = &Genesis{
			Config: params.TestChainConfig,
			Alloc: GenesisAlloc{
				testBankAddress: {Balance: bankFunds},
				address: {
					Code:    []byte{0x60, 0x01, 0x50},
					Balance: big.NewInt(0),
				}, // push 1, pop
			},
			GasLimit: 100e6, // 100 M
		}
	)
	// Generate the original common chain segment and the two competing forks
	engine := beacon.NewFaker()

	blockGenerator := func(i int, block *BlockGen) {
		block.SetCoinbase(common.Address{1})
		for txi := 0; txi < numTxs; txi++ {
			uniq := uint64(i*numTxs + txi)
			recipient := recipientFn(uniq)
			tx := types.NewTx(&types.DynamicFeeTx{
				Nonce:     uniq,
				To:        &recipient,
				Value:     big.NewInt(1),
				Gas:       params.TxGas,
				GasFeeCap: block.header.BaseFee,
				Data:      nil,
			})
			tx, err := types.SignTx(tx, signer, testBankKey)
			if err != nil {
				b.Error(err)
			}
			block.AddTx(tx)
		}
	}

	_, shared, _ := GenerateChainWithGenesis(gspec, engine, numBlocks, blockGenerator)
	b.StopTimer()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Import the shared chain and the original canonical one
		chain, err := NewBlockChain(rawdb.NewMemoryDatabase(), nil, gspec, engine, vm.Config{}, nil)
		if err != nil {
			b.Fatalf("failed to create tester chain: %v", err)
		}
		b.StartTimer()
		if _, err := chain.InsertChain(shared); err != nil {
			b.Fatalf("failed to insert shared chain: %v", err)
		}
		b.StopTimer()
		block := chain.GetBlockByHash(chain.CurrentBlock().Hash())
		if got := block.Transactions().Len(); got != numTxs*numBlocks {
			b.Fatalf("Transactions were not included, expected %d, got %d", numTxs*numBlocks, got)
		}
	}
}

func BenchmarkBlockChain_1x1000ValueTransferToNonexisting(b *testing.B) {
	var (
		numTxs    = 1000
		numBlocks = 1
	)
	recipientFn := func(nonce uint64) common.Address {
		return common.BigToAddress(new(big.Int).SetUint64(1337 + nonce))
	}
	benchmarkLargeNumberOfValueToNonexisting(b, numTxs, numBlocks, recipientFn)
}

func BenchmarkBlockChain_1x1000ValueTransferToExisting(b *testing.B) {
	var (
		numTxs    = 1000
		numBlocks = 1
	)
	b.StopTimer()
	b.ResetTimer()

	recipientFn := func(nonce uint64) common.Address {
		return common.BigToAddress(new(big.Int).SetUint64(1337))
	}
	benchmarkLargeNumberOfValueToNonexisting(b, numTxs, numBlocks, recipientFn)
}

func BenchmarkBlockChain_1x1000Executions(b *testing.B) {
	var (
		numTxs    = 1000
		numBlocks = 1
	)
	b.StopTimer()
	b.ResetTimer()

	recipientFn := func(nonce uint64) common.Address {
		return common.BigToAddress(new(big.Int).SetUint64(0xc0de))
	}
	benchmarkLargeNumberOfValueToNonexisting(b, numTxs, numBlocks, recipientFn)
}

// TestInitThenFailCreateContract tests a pretty notorious case that happened
// on mainnet over blocks 7338108, 7338110 and 7338115.
//   - Block 7338108: address Ze771789f5cccac282f23bb7add5690e1f6ca467c is initiated
//     with 0.001 zond (thus created but no code)
//   - Block 7338110: a CREATE2 is attempted. The CREATE2 would deploy code on
//     the same address Ze771789f5cccac282f23bb7add5690e1f6ca467c. However, the
//     deployment fails due to OOG during initcode execution
//   - Block 7338115: another tx checks the balance of
//     Ze771789f5cccac282f23bb7add5690e1f6ca467c, and the snapshotter returned it as
//     zero.
//
// The problem being that the snapshotter maintains a destructset, and adds items
// to the destructset in case something is created "onto" an existing item.
// We need to either roll back the snapDestructs, or not place it into snapDestructs
// in the first place.

func TestInitThenFailCreateContract(t *testing.T) {
	testInitThenFailCreateContract(t, rawdb.HashScheme)
	testInitThenFailCreateContract(t, rawdb.PathScheme)
}

func testInitThenFailCreateContract(t *testing.T, scheme string) {
	var (
		engine = beacon.NewFaker()

		// A sender who makes transactions, has some funds
		key, _  = pqcrypto.HexToMLDSA87("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		address = pqcrypto.MLDSA87ToAddress(key)
		funds   = big.NewInt(1000000000000000)
		bb, _   = common.NewAddressFromString("Z000000000000000000000000000000000000bbbb")
	)

	// The bb-code needs to CREATE2 the aa contract. It consists of
	// both initcode and deployment code
	// initcode:
	// 1. If blocknum < 1, error out (e.g invalid opcode)
	// 2. else, return a snippet of code
	initCode := []byte{
		byte(vm.PUSH1), 0x1, // y (2)
		byte(vm.NUMBER), // x (number)
		byte(vm.GT),     // x > y?
		byte(vm.PUSH1), byte(0x8),
		byte(vm.JUMPI), // jump to label if number > 2
		byte(0xFE),     // illegal opcode
		byte(vm.JUMPDEST),
		byte(vm.PUSH1), 0x2, // size
		byte(vm.PUSH1), 0x0, // offset
		byte(vm.RETURN), // return 2 bytes of zero-code
	}
	if l := len(initCode); l > 32 {
		t.Fatalf("init code is too long for a pushx, need a more elaborate deployer")
	}
	bbCode := []byte{
		// Push initcode onto stack
		byte(vm.PUSH1) + byte(len(initCode)-1)}
	bbCode = append(bbCode, initCode...)
	bbCode = append(bbCode, []byte{
		byte(vm.PUSH1), 0x0, // memory start on stack
		byte(vm.MSTORE),
		byte(vm.PUSH1), 0x00, // salt
		byte(vm.PUSH1), byte(len(initCode)), // size
		byte(vm.PUSH1), byte(32 - len(initCode)), // offset
		byte(vm.PUSH1), 0x00, // endowment
		byte(vm.CREATE2),
	}...)

	initHash := crypto.Keccak256Hash(initCode)
	aa := crypto.CreateAddress2(bb, [32]byte{}, initHash[:])
	t.Logf("Destination address: %x\n", aa)

	gspec := &Genesis{
		Config: params.TestChainConfig,
		Alloc: GenesisAlloc{
			address: {Balance: funds},
			// The address aa has some funds
			aa: {Balance: big.NewInt(100000)},
			// The contract BB tries to create code onto AA
			bb: {
				Code:    bbCode,
				Balance: big.NewInt(1),
			},
		},
	}
	nonce := uint64(0)
	_, blocks, _ := GenerateChainWithGenesis(gspec, engine, 4, func(i int, b *BlockGen) {
		b.SetCoinbase(common.Address{1})
		// One transaction to BB
		tx := types.NewTx(&types.DynamicFeeTx{
			Nonce:     nonce,
			To:        &bb,
			Value:     big.NewInt(0),
			Gas:       100000,
			GasFeeCap: b.header.BaseFee,
			Data:      nil,
		})
		tx, _ = types.SignTx(tx, types.ShanghaiSigner{ChainId: big.NewInt(1)}, key)
		b.AddTx(tx)
		nonce++
	})

	// Import the canonical chain
	chain, err := NewBlockChain(rawdb.NewMemoryDatabase(), DefaultCacheConfigWithScheme(scheme), gspec, engine, vm.Config{
		//Debug:  true,
		//Tracer: vm.NewJSONLogger(nil, os.Stdout),
	}, nil)
	if err != nil {
		t.Fatalf("failed to create tester chain: %v", err)
	}
	defer chain.Stop()

	statedb, _ := chain.State()
	if got, exp := statedb.GetBalance(aa), big.NewInt(100000); got.Cmp(exp) != 0 {
		t.Fatalf("Genesis err, got %v exp %v", got, exp)
	}
	// First block tries to create, but fails
	{
		block := blocks[0]
		if _, err := chain.InsertChain([]*types.Block{blocks[0]}); err != nil {
			t.Fatalf("block %d: failed to insert into chain: %v", block.NumberU64(), err)
		}
		statedb, _ = chain.State()
		if got, exp := statedb.GetBalance(aa), big.NewInt(100000); got.Cmp(exp) != 0 {
			t.Fatalf("block %d: got %v exp %v", block.NumberU64(), got, exp)
		}
	}
	// Import the rest of the blocks
	for _, block := range blocks[1:] {
		if _, err := chain.InsertChain([]*types.Block{block}); err != nil {
			t.Fatalf("block %d: failed to insert into chain: %v", block.NumberU64(), err)
		}
	}
}

// TestEIP2718Transition tests that an EIP-2718 transaction will be accepted
// This is verified by sending an EIP-2930 access list transaction , which
// specifies a single slot access, and then checking that the gas usage of a
// hot SLOAD and a cold SLOAD are calculated correctly.
func TestEIP2718Transition(t *testing.T) {
	testEIP2718Transition(t, rawdb.HashScheme)
	testEIP2718Transition(t, rawdb.PathScheme)
}

func testEIP2718Transition(t *testing.T, scheme string) {
	var (
		aa, _  = common.NewAddressFromString("Z000000000000000000000000000000000000aaaa")
		engine = beacon.NewFaker()

		// A sender who makes transactions, has some funds
		key, _  = pqcrypto.HexToMLDSA87("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		address = pqcrypto.MLDSA87ToAddress(key)
		funds   = big.NewInt(1000000000000000)
		gspec   = &Genesis{
			Config: params.TestChainConfig,
			Alloc: GenesisAlloc{
				address: {Balance: funds},
				// The address 0xAAAA sloads 0x00 and 0x01
				aa: {
					Code: []byte{
						byte(vm.PC),
						byte(vm.PC),
						byte(vm.SLOAD),
						byte(vm.SLOAD),
					},
					Nonce:   0,
					Balance: big.NewInt(0),
				},
			},
		}
	)
	// Generate blocks
	_, blocks, _ := GenerateChainWithGenesis(gspec, engine, 1, func(i int, b *BlockGen) {
		b.SetCoinbase(common.Address{1})

		// One transaction to 0xAAAA
		signer := types.LatestSigner(gspec.Config)
		tx, _ := types.SignNewTx(key, signer, &types.DynamicFeeTx{
			ChainID:   gspec.Config.ChainID,
			Nonce:     0,
			To:        &aa,
			Gas:       30000,
			GasFeeCap: b.header.BaseFee,
			GasTipCap: big.NewInt(0),
			AccessList: types.AccessList{{
				Address:     aa,
				StorageKeys: []common.Hash{{0}},
			}},
		})
		b.AddTx(tx)
	})

	// Import the canonical chain
	chain, err := NewBlockChain(rawdb.NewMemoryDatabase(), DefaultCacheConfigWithScheme(scheme), gspec, engine, vm.Config{}, nil)
	if err != nil {
		t.Fatalf("failed to create tester chain: %v", err)
	}
	defer chain.Stop()

	if n, err := chain.InsertChain(blocks); err != nil {
		t.Fatalf("block %d: failed to insert into chain: %v", n, err)
	}

	block := chain.GetBlockByNumber(1)

	// Expected gas is intrinsic + 2 * pc + hot load + cold load, since only one load is in the access list
	expected := params.TxGas + params.TxAccessListAddressGas + params.TxAccessListStorageKeyGas +
		vm.GasQuickStep*2 + params.WarmStorageReadCostEIP2929 + params.ColdSloadCostEIP2929
	if block.GasUsed() != expected {
		t.Fatalf("incorrect amount of gas spent: expected %d, got %d", expected, block.GasUsed())
	}
}

// TestEIP1559Transition tests the following:
//
//  1. A transaction whose gasFeeCap is greater than the baseFee is valid.
//  2. Gas accounting for access lists on EIP-1559 transactions is correct.
//  3. Only the transaction's tip will be received by the coinbase.
//  4. The transaction sender pays for both the tip and baseFee.
//  5. The coinbase receives only the partially realized tip when
//     gasFeeCap - gasTipCap < baseFee.
func TestEIP1559Transition(t *testing.T) {
	testEIP1559Transition(t, rawdb.HashScheme)
	testEIP1559Transition(t, rawdb.PathScheme)
}

func testEIP1559Transition(t *testing.T, scheme string) {
	var (
		aa, _  = common.NewAddressFromString("Z000000000000000000000000000000000000aaaa")
		engine = beacon.NewFaker()

		// A sender who makes transactions, has some funds
		key1, _ = pqcrypto.HexToMLDSA87("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		key2, _ = pqcrypto.HexToMLDSA87("8a1f9a8f95be41cd7ccb6168179afb4504aefe388d1e14474d32c45c72ce7b7a")
		addr1   = pqcrypto.MLDSA87ToAddress(key1)
		addr2   = pqcrypto.MLDSA87ToAddress(key2)
		funds   = new(big.Int).Mul(common.Big1, big.NewInt(params.Zond))
		config  = *params.AllBeaconProtocolChanges
		gspec   = &Genesis{
			Config: &config,
			Alloc: GenesisAlloc{
				addr1: {Balance: funds},
				addr2: {Balance: funds},
				// The address 0xAAAA sloads 0x00 and 0x01
				aa: {
					Code: []byte{
						byte(vm.PC),
						byte(vm.PC),
						byte(vm.SLOAD),
						byte(vm.SLOAD),
					},
					Nonce:   0,
					Balance: big.NewInt(0),
				},
			},
		}
	)

	signer := types.LatestSigner(gspec.Config)

	genDb, blocks, _ := GenerateChainWithGenesis(gspec, engine, 1, func(i int, b *BlockGen) {
		b.SetCoinbase(common.Address{1})

		// One transaction to 0xAAAA
		accesses := types.AccessList{types.AccessTuple{
			Address:     aa,
			StorageKeys: []common.Hash{{0}},
		}}

		txdata := &types.DynamicFeeTx{
			ChainID:    gspec.Config.ChainID,
			Nonce:      0,
			To:         &aa,
			Gas:        30000,
			GasFeeCap:  newGplanck(5),
			GasTipCap:  big.NewInt(2),
			AccessList: accesses,
			Data:       []byte{},
		}
		tx := types.NewTx(txdata)
		tx, _ = types.SignTx(tx, signer, key1)

		b.AddTx(tx)
	})
	chain, err := NewBlockChain(rawdb.NewMemoryDatabase(), DefaultCacheConfigWithScheme(scheme), gspec, engine, vm.Config{}, nil)
	if err != nil {
		t.Fatalf("failed to create tester chain: %v", err)
	}
	defer chain.Stop()

	if n, err := chain.InsertChain(blocks); err != nil {
		t.Fatalf("block %d: failed to insert into chain: %v", n, err)
	}

	block := chain.GetBlockByNumber(1)

	// 1+2: Ensure EIP-1559 access lists are accounted for via gas usage.
	expectedGas := params.TxGas + params.TxAccessListAddressGas + params.TxAccessListStorageKeyGas +
		vm.GasQuickStep*2 + params.WarmStorageReadCostEIP2929 + params.ColdSloadCostEIP2929
	if block.GasUsed() != expectedGas {
		t.Fatalf("incorrect amount of gas spent: expected %d, got %d", expectedGas, block.GasUsed())
	}

	state, _ := chain.State()

	// 3: Ensure that miner received only the tx's tip.
	actual := state.GetBalance(block.Coinbase())
	expected := new(big.Int).SetUint64(block.GasUsed() * block.Transactions()[0].GasTipCap().Uint64())
	if actual.Cmp(expected) != 0 {
		t.Fatalf("miner balance incorrect: expected %d, got %d", expected, actual)
	}

	// 4: Ensure the tx sender paid for the gasUsed * (tip + block baseFee).
	actual = new(big.Int).Sub(funds, state.GetBalance(addr1))
	expected = new(big.Int).SetUint64(block.GasUsed() * (block.Transactions()[0].GasTipCap().Uint64() + block.BaseFee().Uint64()))
	if actual.Cmp(expected) != 0 {
		t.Fatalf("sender balance incorrect: expected %d, got %d", expected, actual)
	}

	blocks, _ = GenerateChain(gspec.Config, block, engine, genDb, 1, func(i int, b *BlockGen) {
		b.SetCoinbase(common.Address{2})

		txdata := &types.DynamicFeeTx{
			Nonce:     0,
			To:        &aa,
			Gas:       30000,
			GasFeeCap: newGplanck(5),
			GasTipCap: newGplanck(5),
		}
		tx := types.NewTx(txdata)
		tx, _ = types.SignTx(tx, signer, key2)

		b.AddTx(tx)
	})

	if n, err := chain.InsertChain(blocks); err != nil {
		t.Fatalf("block %d: failed to insert into chain: %v", n, err)
	}

	block = chain.GetBlockByNumber(2)
	state, _ = chain.State()
	effectiveTip := block.Transactions()[0].GasTipCap().Uint64() - block.BaseFee().Uint64()

	// 6+5: Ensure that miner received only the tx's effective tip.
	actual = state.GetBalance(block.Coinbase())
	expected = new(big.Int).SetUint64(block.GasUsed() * effectiveTip)
	if actual.Cmp(expected) != 0 {
		t.Fatalf("miner balance incorrect: expected %d, got %d", expected, actual)
	}

	// 4: Ensure the tx sender paid for the gasUsed * (effectiveTip + block baseFee).
	actual = new(big.Int).Sub(funds, state.GetBalance(addr2))
	expected = new(big.Int).SetUint64(block.GasUsed() * (effectiveTip + block.BaseFee().Uint64()))
	if actual.Cmp(expected) != 0 {
		t.Fatalf("sender balance incorrect: expected %d, got %d", expected, actual)
	}
}

// Tests the scenario the chain is requested to another point with the missing state.
// It expects the state is recovered and all relevant chain markers are set correctly.
func TestSetCanonical(t *testing.T) {
	testSetCanonical(t, rawdb.HashScheme)
	testSetCanonical(t, rawdb.PathScheme)
}

func testSetCanonical(t *testing.T, scheme string) {
	//log.Root().SetHandler(log.LvlFilterHandler(log.LvlDebug, log.StreamHandler(os.Stderr, log.TerminalFormat(true))))

	var (
		key, _  = pqcrypto.HexToMLDSA87("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		address = pqcrypto.MLDSA87ToAddress(key)
		funds   = big.NewInt(100000000000000000)
		gspec   = &Genesis{
			Config:  params.TestChainConfig,
			Alloc:   GenesisAlloc{address: {Balance: funds}},
			BaseFee: big.NewInt(params.InitialBaseFee),
		}
		signer = types.LatestSigner(gspec.Config)
		engine = beacon.NewFaker()
	)
	// Generate and import the canonical chain
	_, canon, _ := GenerateChainWithGenesis(gspec, engine, 2*TriesInMemory, func(i int, gen *BlockGen) {
		tx := types.NewTx(&types.DynamicFeeTx{
			Nonce:     gen.TxNonce(address),
			To:        &common.Address{0x00},
			Value:     big.NewInt(1000),
			Gas:       params.TxGas,
			GasFeeCap: gen.header.BaseFee,
			Data:      nil,
		})
		tx, err := types.SignTx(tx, signer, key)
		if err != nil {
			panic(err)
		}
		gen.AddTx(tx)
	})
	diskdb, _ := rawdb.NewDatabaseWithFreezer(rawdb.NewMemoryDatabase(), t.TempDir(), "", false)
	defer diskdb.Close()

	chain, err := NewBlockChain(diskdb, DefaultCacheConfigWithScheme(scheme), gspec, engine, vm.Config{}, nil)
	if err != nil {
		t.Fatalf("failed to create tester chain: %v", err)
	}
	defer chain.Stop()

	if n, err := chain.InsertChain(canon); err != nil {
		t.Fatalf("block %d: failed to insert into chain: %v", n, err)
	}

	// Generate the side chain and import them
	_, side, _ := GenerateChainWithGenesis(gspec, engine, 2*TriesInMemory, func(i int, gen *BlockGen) {
		tx := types.NewTx(&types.DynamicFeeTx{
			Nonce:     gen.TxNonce(address),
			To:        &common.Address{0x00},
			Value:     big.NewInt(1),
			Gas:       params.TxGas,
			GasFeeCap: gen.header.BaseFee,
			Data:      nil,
		})
		tx, err := types.SignTx(tx, signer, key)
		if err != nil {
			panic(err)
		}
		gen.AddTx(tx)
	})
	for _, block := range side {
		err := chain.InsertBlockWithoutSetHead(block)
		if err != nil {
			t.Fatalf("Failed to insert into chain: %v", err)
		}
	}
	for _, block := range side {
		got := chain.GetBlockByHash(block.Hash())
		if got == nil {
			t.Fatalf("Lost the inserted block")
		}
	}

	// Set the chain head to the side chain, ensure all the relevant markers are updated.
	verify := func(head *types.Block) {
		if chain.CurrentBlock().Hash() != head.Hash() {
			t.Fatalf("Unexpected block hash, want %x, got %x", head.Hash(), chain.CurrentBlock().Hash())
		}
		if chain.CurrentSnapBlock().Hash() != head.Hash() {
			t.Fatalf("Unexpected fast block hash, want %x, got %x", head.Hash(), chain.CurrentSnapBlock().Hash())
		}
		if chain.CurrentHeader().Hash() != head.Hash() {
			t.Fatalf("Unexpected head header, want %x, got %x", head.Hash(), chain.CurrentHeader().Hash())
		}
		if !chain.HasState(head.Root()) {
			t.Fatalf("Lost block state %v %x", head.Number(), head.Hash())
		}
	}
	chain.SetCanonical(side[len(side)-1])
	verify(side[len(side)-1])

	// Reset the chain head to original chain
	chain.SetCanonical(canon[TriesInMemory-1])
	verify(canon[TriesInMemory-1])
}

// TestCanonicalHashMarker tests all the canonical hash markers are updated/deleted
// correctly in case reorg is called.
func TestCanonicalHashMarker(t *testing.T) {
	testCanonicalHashMarker(t, rawdb.HashScheme)
	testCanonicalHashMarker(t, rawdb.PathScheme)
}

func testCanonicalHashMarker(t *testing.T, scheme string) {
	var cases = []struct {
		forkA int
		forkB int
	}{
		// ForkA: 10 blocks
		// ForkB: 1 blocks
		//
		// reorged:
		//      markers [2, 10] should be deleted
		//      markers [1] should be updated
		{10, 1},

		// ForkA: 10 blocks
		// ForkB: 2 blocks
		//
		// reorged:
		//      markers [3, 10] should be deleted
		//      markers [1, 2] should be updated
		{10, 2},

		// ForkA: 10 blocks
		// ForkB: 10 blocks
		//
		// reorged:
		//      markers [1, 10] should be updated
		{10, 10},

		// ForkA: 10 blocks
		// ForkB: 11 blocks
		//
		// reorged:
		//      markers [1, 11] should be updated
		{10, 11},
	}
	for _, c := range cases {
		var (
			gspec = &Genesis{
				Config:  params.TestChainConfig,
				Alloc:   GenesisAlloc{},
				BaseFee: big.NewInt(params.InitialBaseFee),
			}
			engine = beacon.NewFaker()
		)
		_, forkA, _ := GenerateChainWithGenesis(gspec, engine, c.forkA, func(i int, gen *BlockGen) {})
		_, forkB, _ := GenerateChainWithGenesis(gspec, engine, c.forkB, func(i int, gen *BlockGen) {})

		// Initialize test chain
		chain, err := NewBlockChain(rawdb.NewMemoryDatabase(), DefaultCacheConfigWithScheme(scheme), gspec, engine, vm.Config{}, nil)
		if err != nil {
			t.Fatalf("failed to create tester chain: %v", err)
		}
		// Insert forkA and forkB, the canonical should on forkA still
		if n, err := chain.InsertChain(forkA); err != nil {
			t.Fatalf("block %d: failed to insert into chain: %v", n, err)
		}
		if n, err := chain.InsertChain(forkB); err != nil {
			t.Fatalf("block %d: failed to insert into chain: %v", n, err)
		}

		verify := func(head *types.Block) {
			if chain.CurrentBlock().Hash() != head.Hash() {
				t.Fatalf("Unexpected block hash, want %x, got %x", head.Hash(), chain.CurrentBlock().Hash())
			}
			if chain.CurrentSnapBlock().Hash() != head.Hash() {
				t.Fatalf("Unexpected fast block hash, want %x, got %x", head.Hash(), chain.CurrentSnapBlock().Hash())
			}
			if chain.CurrentHeader().Hash() != head.Hash() {
				t.Fatalf("Unexpected head header, want %x, got %x", head.Hash(), chain.CurrentHeader().Hash())
			}
			if !chain.HasState(head.Root()) {
				t.Fatalf("Lost block state %v %x", head.Number(), head.Hash())
			}
		}

		// Switch canonical chain to forkB if necessary
		if len(forkA) < len(forkB) {
			verify(forkB[len(forkB)-1])
		} else {
			verify(forkA[len(forkA)-1])
			chain.SetCanonical(forkB[len(forkB)-1])
			verify(forkB[len(forkB)-1])
		}

		// Ensure all hash markers are updated correctly
		for i := 0; i < len(forkB); i++ {
			block := forkB[i]
			hash := chain.GetCanonicalHash(block.NumberU64())
			if hash != block.Hash() {
				t.Fatalf("Unexpected canonical hash %d", block.NumberU64())
			}
		}
		if c.forkA > c.forkB {
			for i := uint64(c.forkB) + 1; i <= uint64(c.forkA); i++ {
				hash := chain.GetCanonicalHash(i)
				if hash != (common.Hash{}) {
					t.Fatalf("Unexpected canonical hash %d", i)
				}
			}
		}
		chain.Stop()
	}
}

// TestTxIndexer tests the tx indexes are updated correctly.
func TestTxIndexer(t *testing.T) {
	var (
		testBankKey, _  = pqcrypto.GenerateMLDSA87Key()
		testBankAddress = pqcrypto.MLDSA87ToAddress(testBankKey)
		testBankFunds   = big.NewInt(1000000000000000000)

		gspec = &Genesis{
			Config:  params.TestChainConfig,
			Alloc:   GenesisAlloc{testBankAddress: {Balance: testBankFunds}},
			BaseFee: big.NewInt(params.InitialBaseFee),
		}
		engine = beacon.NewFaker()
		nonce  = uint64(0)
	)
	_, blocks, receipts := GenerateChainWithGenesis(gspec, engine, 128, func(i int, gen *BlockGen) {
		to, _ := common.NewAddressFromString("Z00000000000000000000000000000000deadbeef")
		tx := types.NewTx(&types.DynamicFeeTx{
			Nonce:     nonce,
			To:        &to,
			Value:     big.NewInt(1000),
			Gas:       params.TxGas,
			GasFeeCap: big.NewInt(10 * params.InitialBaseFee),
			Data:      nil,
		})
		tx, _ = types.SignTx(tx, types.ShanghaiSigner{ChainId: big.NewInt(1)}, testBankKey)
		gen.AddTx(tx)
		nonce += 1
	})

	// verifyIndexes checks if the transaction indexes are present or not
	// of the specified block.
	verifyIndexes := func(db zonddb.Database, number uint64, exist bool) {
		if number == 0 {
			return
		}
		block := blocks[number-1]
		for _, tx := range block.Transactions() {
			lookup := rawdb.ReadTxLookupEntry(db, tx.Hash())
			if exist && lookup == nil {
				t.Fatalf("missing %d %x", number, tx.Hash().Hex())
			}
			if !exist && lookup != nil {
				t.Fatalf("unexpected %d %x", number, tx.Hash().Hex())
			}
		}
	}
	// verifyRange runs verifyIndexes for a range of blocks, from and to are included.
	verifyRange := func(db zonddb.Database, from, to uint64, exist bool) {
		for number := from; number <= to; number += 1 {
			verifyIndexes(db, number, exist)
		}
	}
	verify := func(db zonddb.Database, expTail uint64) {
		tail := rawdb.ReadTxIndexTail(db)
		if tail == nil {
			t.Fatal("Failed to write tx index tail")
		}
		if *tail != expTail {
			t.Fatalf("Unexpected tx index tail, want %v, got %d", expTail, *tail)
		}
		if *tail != 0 {
			verifyRange(db, 0, *tail-1, false)
		}
		verifyRange(db, *tail, 128, true)
	}

	var cases = []struct {
		limitA uint64
		tailA  uint64
		limitB uint64
		tailB  uint64
		limitC uint64
		tailC  uint64
	}{
		{
			// LimitA: 0
			// TailA:  0
			//
			// all blocks are indexed
			limitA: 0,
			tailA:  0,

			// LimitB: 1
			// TailB:  128
			//
			// block-128 is indexed
			limitB: 1,
			tailB:  128,

			// LimitB: 64
			// TailB:  65
			//
			// block [65, 128] are indexed
			limitC: 64,
			tailC:  65,
		},
		{
			// LimitA: 64
			// TailA:  65
			//
			// block [65, 128] are indexed
			limitA: 64,
			tailA:  65,

			// LimitB: 1
			// TailB:  128
			//
			// block-128 is indexed
			limitB: 1,
			tailB:  128,

			// LimitB: 64
			// TailB:  65
			//
			// block [65, 128] are indexed
			limitC: 64,
			tailC:  65,
		},
		{
			// LimitA: 127
			// TailA:  2
			//
			// block [2, 128] are indexed
			limitA: 127,
			tailA:  2,

			// LimitB: 1
			// TailB:  128
			//
			// block-128 is indexed
			limitB: 1,
			tailB:  128,

			// LimitB: 64
			// TailB:  65
			//
			// block [65, 128] are indexed
			limitC: 64,
			tailC:  65,
		},
		{
			// LimitA: 128
			// TailA:  1
			//
			// block [2, 128] are indexed
			limitA: 128,
			tailA:  1,

			// LimitB: 1
			// TailB:  128
			//
			// block-128 is indexed
			limitB: 1,
			tailB:  128,

			// LimitB: 64
			// TailB:  65
			//
			// block [65, 128] are indexed
			limitC: 64,
			tailC:  65,
		},
		{
			// LimitA: 129
			// TailA:  0
			//
			// block [0, 128] are indexed
			limitA: 129,
			tailA:  0,

			// LimitB: 1
			// TailB:  128
			//
			// block-128 is indexed
			limitB: 1,
			tailB:  128,

			// LimitB: 64
			// TailB:  65
			//
			// block [65, 128] are indexed
			limitC: 64,
			tailC:  65,
		},
	}
	for _, c := range cases {
		frdir := t.TempDir()
		db, _ := rawdb.NewDatabaseWithFreezer(rawdb.NewMemoryDatabase(), frdir, "", false)
		rawdb.WriteAncientBlocks(db, append([]*types.Block{gspec.ToBlock()}, blocks...), append([]types.Receipts{{}}, receipts...))

		// Index the initial blocks from ancient store
		chain, _ := NewBlockChain(db, nil, gspec, engine, vm.Config{}, &c.limitA)
		chain.indexBlocks(nil, 128, make(chan struct{}))
		verify(db, c.tailA)

		chain.SetTxLookupLimit(c.limitB)
		chain.indexBlocks(rawdb.ReadTxIndexTail(db), 128, make(chan struct{}))
		verify(db, c.tailB)

		chain.SetTxLookupLimit(c.limitC)
		chain.indexBlocks(rawdb.ReadTxIndexTail(db), 128, make(chan struct{}))
		verify(db, c.tailC)

		// Recover all indexes
		chain.SetTxLookupLimit(0)
		chain.indexBlocks(rawdb.ReadTxIndexTail(db), 128, make(chan struct{}))
		verify(db, 0)

		chain.Stop()
		db.Close()
		os.RemoveAll(frdir)
	}
}

func TestEIP3651(t *testing.T) {
	var (
		aa, _  = common.NewAddressFromString("Z000000000000000000000000000000000000aaaa")
		bb, _  = common.NewAddressFromString("Z000000000000000000000000000000000000bbbb")
		engine = beacon.NewFaker()

		// A sender who makes transactions, has some funds
		key1, _ = pqcrypto.HexToMLDSA87("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		key2, _ = pqcrypto.HexToMLDSA87("8a1f9a8f95be41cd7ccb6168179afb4504aefe388d1e14474d32c45c72ce7b7a")
		addr1   = pqcrypto.MLDSA87ToAddress(key1)
		addr2   = pqcrypto.MLDSA87ToAddress(key2)
		funds   = new(big.Int).Mul(common.Big1, big.NewInt(params.Zond))
		config  = *params.AllBeaconProtocolChanges
		gspec   = &Genesis{
			Config: &config,
			Alloc: GenesisAlloc{
				addr1: {Balance: funds},
				addr2: {Balance: funds},
				// The address 0xAAAA sloads 0x00 and 0x01
				aa: {
					Code: []byte{
						byte(vm.PC),
						byte(vm.PC),
						byte(vm.SLOAD),
						byte(vm.SLOAD),
					},
					Nonce:   0,
					Balance: big.NewInt(0),
				},
				// The address 0xBBBB calls 0xAAAA
				bb: {
					Code: []byte{
						byte(vm.PUSH1), 0, // out size
						byte(vm.DUP1),  // out offset
						byte(vm.DUP1),  // out insize
						byte(vm.DUP1),  // in offset
						byte(vm.PUSH2), // address
						byte(0xaa),
						byte(0xaa),
						byte(vm.GAS), // gas
						byte(vm.DELEGATECALL),
					},
					Nonce:   0,
					Balance: big.NewInt(0),
				},
			},
		}
	)

	signer := types.LatestSigner(gspec.Config)

	_, blocks, _ := GenerateChainWithGenesis(gspec, engine, 1, func(i int, b *BlockGen) {
		b.SetCoinbase(aa)
		// One transaction to Coinbase
		txdata := &types.DynamicFeeTx{
			ChainID:    gspec.Config.ChainID,
			Nonce:      0,
			To:         &bb,
			Gas:        500000,
			GasFeeCap:  newGplanck(5),
			GasTipCap:  big.NewInt(2),
			AccessList: nil,
			Data:       []byte{},
		}
		tx := types.NewTx(txdata)
		tx, _ = types.SignTx(tx, signer, key1)

		b.AddTx(tx)
	})
	chain, err := NewBlockChain(rawdb.NewMemoryDatabase(), nil, gspec, engine, vm.Config{Tracer: logger.NewMarkdownLogger(&logger.Config{}, os.Stderr)}, nil)
	if err != nil {
		t.Fatalf("failed to create tester chain: %v", err)
	}
	defer chain.Stop()
	if n, err := chain.InsertChain(blocks); err != nil {
		t.Fatalf("block %d: failed to insert into chain: %v", n, err)
	}

	block := chain.GetBlockByNumber(1)

	// 1+2: Ensure EIP-1559 access lists are accounted for via gas usage.
	innerGas := vm.GasQuickStep*2 + params.ColdSloadCostEIP2929*2
	expectedGas := params.TxGas + 5*vm.GasFastestStep + vm.GasQuickStep + 100 + innerGas // 100 because 0xaaaa is in access list
	if block.GasUsed() != expectedGas {
		t.Fatalf("incorrect amount of gas spent: expected %d, got %d", expectedGas, block.GasUsed())
	}

	state, _ := chain.State()

	// 3: Ensure that miner received only the tx's tip.
	actual := state.GetBalance(block.Coinbase())
	expected := new(big.Int).SetUint64(block.GasUsed() * block.Transactions()[0].GasTipCap().Uint64())
	if actual.Cmp(expected) != 0 {
		t.Fatalf("miner balance incorrect: expected %d, got %d", expected, actual)
	}

	// 4: Ensure the tx sender paid for the gasUsed * (tip + block baseFee).
	actual = new(big.Int).Sub(funds, state.GetBalance(addr1))
	expected = new(big.Int).SetUint64(block.GasUsed() * (block.Transactions()[0].GasTipCap().Uint64() + block.BaseFee().Uint64()))
	if actual.Cmp(expected) != 0 {
		t.Fatalf("sender balance incorrect: expected %d, got %d", expected, actual)
	}
}
