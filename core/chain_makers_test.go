// Copyright 2015 The go-ethereum Authors
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
	"testing"

	"github.com/theQRL/go-zond/common"
	"github.com/theQRL/go-zond/consensus/beacon"
	"github.com/theQRL/go-zond/core/rawdb"
	"github.com/theQRL/go-zond/core/types"
	"github.com/theQRL/go-zond/core/vm"
	"github.com/theQRL/go-zond/crypto/pqcrypto"
	"github.com/theQRL/go-zond/params"
	"github.com/theQRL/go-zond/trie"
)

func TestGenerateWithdrawalChain(t *testing.T) {
	var (
		keyHex  = "9c647b8b7c4e7c3490668fb6c11473619db80c93704c70893d3813af4090c39c"
		key, _  = pqcrypto.HexToMLDSA87(keyHex)
		address = pqcrypto.MLDSA87ToAddress(key)
		aa      = common.Address{0xaa}
		bb      = common.Address{0xbb}
		funds   = big.NewInt(0).Mul(big.NewInt(1337), big.NewInt(params.Zond))
		config  = *params.AllBeaconProtocolChanges
		gspec   = &Genesis{
			Config:   &config,
			Alloc:    GenesisAlloc{address: {Balance: funds}},
			BaseFee:  big.NewInt(params.InitialBaseFee),
			GasLimit: 5_000_000,
		}
		gendb  = rawdb.NewMemoryDatabase()
		signer = types.LatestSigner(gspec.Config)
		db     = rawdb.NewMemoryDatabase()
	)

	// init 0xaa with some storage elements
	storage := make(map[common.Hash]common.Hash)
	storage[common.Hash{0x00}] = common.Hash{0x00}
	storage[common.Hash{0x01}] = common.Hash{0x01}
	storage[common.Hash{0x02}] = common.Hash{0x02}
	storage[common.Hash{0x03}] = common.HexToHash("0303")
	gspec.Alloc[aa] = GenesisAccount{
		Balance: common.Big1,
		Nonce:   1,
		Storage: storage,
		Code:    common.Hex2Bytes("6042"),
	}
	gspec.Alloc[bb] = GenesisAccount{
		Balance: common.Big2,
		Nonce:   1,
		Storage: storage,
		Code:    common.Hex2Bytes("600154600354"),
	}
	genesis := gspec.MustCommit(gendb, trie.NewDatabase(gendb, trie.HashDefaults))

	chain, _ := GenerateChain(gspec.Config, genesis, beacon.NewFaker(), gendb, 4, func(i int, gen *BlockGen) {
		to := common.Address(address)
		tx := types.NewTx(&types.DynamicFeeTx{
			Nonce:     gen.TxNonce(address),
			To:        &to,
			Value:     big.NewInt(1000),
			Gas:       params.TxGas,
			GasFeeCap: new(big.Int).Add(gen.BaseFee(), common.Big1),
			Data:      nil,
		})
		signedTx, _ := types.SignTx(tx, signer, key)
		gen.AddTx(signedTx)
		if i == 1 {
			gen.AddWithdrawal(&types.Withdrawal{
				Validator: 42,
				Address:   common.Address{0xee},
				Amount:    1337,
			})
			gen.AddWithdrawal(&types.Withdrawal{
				Validator: 13,
				Address:   common.Address{0xee},
				Amount:    1,
			})
		}
		if i == 3 {
			gen.AddWithdrawal(&types.Withdrawal{
				Validator: 42,
				Address:   common.Address{0xee},
				Amount:    1337,
			})
			gen.AddWithdrawal(&types.Withdrawal{
				Validator: 13,
				Address:   common.Address{0xee},
				Amount:    1,
			})
		}
	})

	// Import the chain. This runs all block validation rules.
	blockchain, _ := NewBlockChain(db, nil, gspec, beacon.NewFaker(), vm.Config{}, nil)
	defer blockchain.Stop()

	if i, err := blockchain.InsertChain(chain); err != nil {
		fmt.Printf("insert error (block %d): %v\n", chain[i].NumberU64(), err)
		return
	}

	// enforce that withdrawal indexes are monotonically increasing from 0
	var (
		withdrawalIndex uint64
		head            = blockchain.CurrentBlock().Number.Uint64()
	)
	for i := 0; i < int(head); i++ {
		block := blockchain.GetBlockByNumber(uint64(i))
		if block == nil {
			t.Fatalf("block %d not found", i)
		}
		if len(block.Withdrawals()) == 0 {
			continue
		}
		for j := 0; j < len(block.Withdrawals()); j++ {
			if block.Withdrawals()[j].Index != withdrawalIndex {
				t.Fatalf("withdrawal index %d does not equal expected index %d", block.Withdrawals()[j].Index, withdrawalIndex)
			}
			withdrawalIndex += 1
		}
	}
}

func ExampleGenerateChain() {
	var (
		key1, _ = pqcrypto.HexToMLDSA87("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		key2, _ = pqcrypto.HexToMLDSA87("8a1f9a8f95be41cd7ccb6168179afb4504aefe388d1e14474d32c45c72ce7b7a")
		key3, _ = pqcrypto.HexToMLDSA87("49a7b37aa6f6645917e7b807e9d1c00d4fa71f18343b0d4122a4d2df64dd6fee")
		addr1   = pqcrypto.MLDSA87ToAddress(key1)
		addr2   = pqcrypto.MLDSA87ToAddress(key2)
		addr3   = pqcrypto.MLDSA87ToAddress(key3)
		db      = rawdb.NewMemoryDatabase()
		genDb   = rawdb.NewMemoryDatabase()
	)

	// Ensure that key1 has some funds in the genesis block.
	gspec := &Genesis{
		Config: &params.ChainConfig{
			ChainID: big.NewInt(1),
		},
		Alloc: GenesisAlloc{addr1: {Balance: big.NewInt(2000000000000000)}},
	}
	genesis := gspec.MustCommit(genDb, trie.NewDatabase(genDb, trie.HashDefaults))

	// This call generates a chain of 5 blocks. The function runs for
	// each block and adds different features to gen based on the
	// block index.
	signer := types.ShanghaiSigner{ChainId: big.NewInt(1)}
	chain, _ := GenerateChain(gspec.Config, genesis, beacon.NewFaker(), genDb, 5, func(i int, gen *BlockGen) {
		switch i {
		case 0:
			// In block 1, addr1 sends addr2 some zond.
			to := common.Address(addr2)
			tx := types.NewTx(&types.DynamicFeeTx{
				Nonce:     gen.TxNonce(addr1),
				To:        &to,
				Value:     big.NewInt(10000000000000),
				Gas:       params.TxGas,
				GasFeeCap: gen.header.BaseFee,
				Data:      nil,
			})
			signedTx, _ := types.SignTx(tx, signer, key1)
			gen.AddTx(signedTx)

		case 1:
			// In block 2, addr1 sends some more zond to addr2.
			// addr2 passes it on to addr3.
			to2 := common.Address(addr2)
			to3 := common.Address(addr3)
			tx1 := types.NewTx(&types.DynamicFeeTx{
				Nonce:     gen.TxNonce(addr1),
				To:        &to2,
				Value:     big.NewInt(10000000000000),
				Gas:       params.TxGas,
				GasFeeCap: gen.header.BaseFee,
				Data:      nil,
			})
			tx2 := types.NewTx(&types.DynamicFeeTx{
				Nonce:     gen.TxNonce(addr2),
				To:        &to3,
				Value:     big.NewInt(10000000),
				Gas:       params.TxGas,
				GasFeeCap: gen.header.BaseFee,
				Data:      nil,
			})
			signedTx1, _ := types.SignTx(tx1, signer, key1)
			signedTx2, _ := types.SignTx(tx2, signer, key2)
			gen.AddTx(signedTx1)
			gen.AddTx(signedTx2)
		case 2:
			// Block 3 is empty but was mined by addr3.
			gen.SetCoinbase(addr3)
			gen.SetExtra([]byte("yeehaw"))
		case 3:
			// Block 4 includes blocks 2 and 3 as uncle headers (with modified extra data).
			b2 := gen.PrevBlock(1).Header()
			b2.Extra = []byte("foo")
			b3 := gen.PrevBlock(2).Header()
			b3.Extra = []byte("foo")
		}
	})

	// Import the chain. This runs all block validation rules.
	blockchain, _ := NewBlockChain(db, DefaultCacheConfigWithScheme(rawdb.HashScheme), gspec, beacon.NewFaker(), vm.Config{}, nil)
	defer blockchain.Stop()

	if i, err := blockchain.InsertChain(chain); err != nil {
		fmt.Printf("insert error (block %d): %v\n", chain[i].NumberU64(), err)
		return
	}

	state, _ := blockchain.State()
	fmt.Printf("last block: #%d\n", blockchain.CurrentBlock().Number)
	fmt.Println("balance of addr1:", state.GetBalance(addr1))
	fmt.Println("balance of addr2:", state.GetBalance(addr2))
	fmt.Println("balance of addr3:", state.GetBalance(addr3))
	// Output:
	// last block: #5
	// balance of addr1: 1945526403675000
	// balance of addr2: 3901393675000
	// balance of addr3: 10000000
}
