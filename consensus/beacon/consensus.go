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

package beacon

import (
	"errors"
	"fmt"
	"io"
	"math/big"

	"github.com/theQRL/go-zond/common"
	"github.com/theQRL/go-zond/consensus"
	"github.com/theQRL/go-zond/consensus/misc/eip1559"
	"github.com/theQRL/go-zond/core/state"
	"github.com/theQRL/go-zond/core/types"
	"github.com/theQRL/go-zond/crypto"
	"github.com/theQRL/go-zond/params"
	"github.com/theQRL/go-zond/rlp"
	"github.com/theQRL/go-zond/rpc"
	"github.com/theQRL/go-zond/trie"
	"golang.org/x/crypto/sha3"
)

// Various error messages to mark blocks invalid. These should be private to
// prevent engine specific errors from being referenced in the remainder of the
// codebase, inherently breaking if the engine is swapped out. Please put common
// error types into the consensus package.
var (
	errInvalidNonce     = errors.New("invalid nonce")
	errInvalidTimestamp = errors.New("invalid timestamp")
)

// Beacon is a consensus engine that uses the proof-of-stake algorithm.
// The beacon here is a half-functional consensus engine with partial functions which
// is only used for necessary consensus checks.
type Beacon struct{}

// New creates a consensus engine with the given embedded eth1 engine.
func New() *Beacon {
	return &Beacon{}
}

// Author implements consensus.Engine, returning the verified author of the block.
func (beacon *Beacon) Author(header *types.Header) (common.Address, error) {
	return header.Coinbase, nil
}

// VerifyHeader checks whether a header conforms to the consensus rules of the
// stock Ethereum consensus engine.
func (beacon *Beacon) VerifyHeader(chain consensus.ChainHeaderReader, header *types.Header) error {
	// Short circuit if the parent is not known
	parent := chain.GetHeader(header.ParentHash, header.Number.Uint64()-1)
	if parent == nil {
		return consensus.ErrUnknownAncestor
	}
	// Sanity checks passed, do a proper verification
	return beacon.verifyHeader(chain, header, parent)
}

// VerifyHeaders is similar to VerifyHeader, but verifies a batch of headers
// concurrently. The method returns a quit channel to abort the operations and
// a results channel to retrieve the async verifications.
// VerifyHeaders expect the headers to be ordered and continuous.
func (beacon *Beacon) VerifyHeaders(chain consensus.ChainHeaderReader, headers []*types.Header) (chan<- struct{}, <-chan error) {
	return beacon.verifyHeaders(chain, headers, nil)
}

// verifyHeader checks whether a header conforms to the consensus rules of the
// stock Ethereum consensus engine. The difference between the beacon and classic is
// (a) The following fields are expected to be constants:
//   - nonce is expected to be 0
//     to be the desired constants
//
// (b) we don't verify if a block is in the future anymore
// (c) the extradata is limited to 32 bytes
func (beacon *Beacon) verifyHeader(chain consensus.ChainHeaderReader, header, parent *types.Header) error {
	// Ensure that the header's extra-data section is of a reasonable size
	if len(header.Extra) > 32 {
		return fmt.Errorf("extra-data longer than 32 bytes (%d)", len(header.Extra))
	}
	// Verify the timestamp
	if header.Time <= parent.Time {
		return errInvalidTimestamp
	}
	// Verify that the gas limit is <= 2^63-1
	if header.GasLimit > params.MaxGasLimit {
		return fmt.Errorf("invalid gasLimit: have %v, max %v", header.GasLimit, params.MaxGasLimit)
	}
	// Verify that the gasUsed is <= gasLimit
	if header.GasUsed > header.GasLimit {
		return fmt.Errorf("invalid gasUsed: have %d, gasLimit %d", header.GasUsed, header.GasLimit)
	}
	// Verify that the block number is parent's +1
	if diff := new(big.Int).Sub(header.Number, parent.Number); diff.Cmp(common.Big1) != 0 {
		return consensus.ErrInvalidNumber
	}
	// Verify the header's EIP-1559 attributes.
	if err := eip1559.VerifyEIP1559Header(chain.Config(), parent, header); err != nil {
		return err
	}
	// Verify existence / non-existence of withdrawalsHash.
	shanghai := chain.Config().IsShanghai(header.Time)
	if shanghai && header.WithdrawalsHash == nil {
		return errors.New("missing withdrawalsHash")
	}
	if !shanghai && header.WithdrawalsHash != nil {
		return fmt.Errorf("invalid withdrawalsHash: have %x, expected nil", header.WithdrawalsHash)
	}

	// TODO(rgeraldes24)
	// Verify the existence / non-existence of cancun-specific header fields
	// cancun := chain.Config().IsCancun(header.Number, header.Time)
	// if !cancun {
	// 	switch {
	// 	case header.ExcessBlobGas != nil:
	// 		return fmt.Errorf("invalid excessBlobGas: have %d, expected nil", header.ExcessBlobGas)
	// 	case header.BlobGasUsed != nil:
	// 		return fmt.Errorf("invalid blobGasUsed: have %d, expected nil", header.BlobGasUsed)
	// 	case header.ParentBeaconRoot != nil:
	// 		return fmt.Errorf("invalid parentBeaconRoot, have %#x, expected nil", header.ParentBeaconRoot)
	// 	}
	// } else {
	// 	if header.ParentBeaconRoot == nil {
	// 		return errors.New("header is missing beaconRoot")
	// 	}
	// 	if err := eip4844.VerifyEIP4844Header(parent, header); err != nil {
	// 		return err
	// 	}
	// }

	return nil
}

// verifyHeaders is similar to verifyHeader, but verifies a batch of headers
// concurrently. The method returns a quit channel to abort the operations and
// a results channel to retrieve the async verifications. An additional parent
// header will be passed if the relevant header is not in the database yet.
func (beacon *Beacon) verifyHeaders(chain consensus.ChainHeaderReader, headers []*types.Header, ancestor *types.Header) (chan<- struct{}, <-chan error) {
	var (
		abort   = make(chan struct{})
		results = make(chan error, len(headers))
	)
	go func() {
		for i, header := range headers {
			var parent *types.Header
			if i == 0 {
				if ancestor != nil {
					parent = ancestor
				} else {
					parent = chain.GetHeader(headers[0].ParentHash, headers[0].Number.Uint64()-1)
				}
			} else if headers[i-1].Hash() == headers[i].ParentHash {
				parent = headers[i-1]
			}
			if parent == nil {
				select {
				case <-abort:
					return
				case results <- consensus.ErrUnknownAncestor:
				}
				continue
			}
			err := beacon.verifyHeader(chain, header, parent)
			select {
			case <-abort:
				return
			case results <- err:
			}
		}
	}()
	return abort, results
}

// Prepare implements consensus.Engine.
func (beacon *Beacon) Prepare(chain consensus.ChainHeaderReader, header *types.Header) error {
	return nil
}

// Finalize implements consensus.Engine and processes withdrawals on top.
func (beacon *Beacon) Finalize(chain consensus.ChainHeaderReader, header *types.Header, state *state.StateDB, txs []*types.Transaction, withdrawals []*types.Withdrawal) {
	// Withdrawals processing.
	for _, w := range withdrawals {
		// Convert amount from gwei to wei.
		amount := new(big.Int).SetUint64(w.Amount)
		amount = amount.Mul(amount, big.NewInt(params.GWei))
		state.AddBalance(w.Address, amount)
	}
	// No block reward which is issued by consensus layer instead.
}

// FinalizeAndAssemble implements consensus.Engine, setting the final state and
// assembling the block.
func (beacon *Beacon) FinalizeAndAssemble(chain consensus.ChainHeaderReader, header *types.Header, state *state.StateDB, txs []*types.Transaction, receipts []*types.Receipt, withdrawals []*types.Withdrawal) (*types.Block, error) {
	shanghai := chain.Config().IsShanghai(header.Time)
	if shanghai {
		// All blocks after Shanghai must include a withdrawals root.
		if withdrawals == nil {
			withdrawals = make([]*types.Withdrawal, 0)
		}
	} else {
		if len(withdrawals) > 0 {
			return nil, errors.New("withdrawals set before Shanghai activation")
		}
	}
	// Finalize and assemble the block.
	beacon.Finalize(chain, header, state, txs, withdrawals)

	// Assign the final state root to header.
	header.Root = state.IntermediateRoot(true)

	// Assemble and return the final block.
	return types.NewBlockWithWithdrawals(header, txs, receipts, withdrawals, trie.NewStackTrie(nil)), nil
}

// SealHash returns the hash of a block prior to it being sealed.
func (beacon *Beacon) SealHash(header *types.Header) common.Hash {
	return SealHash(header)
}

// APIs implements consensus.Engine, returning the user facing RPC APIs.
func (beacon *Beacon) APIs(chain consensus.ChainHeaderReader) []rpc.API {
	return []rpc.API{}
}

// Close shutdowns the consensus engine
func (beacon *Beacon) Close() error {
	return nil
}

// SealHash returns the hash of a block prior to it being sealed.
func SealHash(header *types.Header) (hash common.Hash) {
	hasher := sha3.NewLegacyKeccak256()
	encodeSigHeader(hasher, header)
	hasher.(crypto.KeccakState).Read(hash[:])
	return hash
}

func encodeSigHeader(w io.Writer, header *types.Header) {
	enc := []interface{}{
		header.ParentHash,
		header.Coinbase,
		header.Root,
		header.TxHash,
		header.ReceiptHash,
		header.Bloom,
		header.Number,
		header.GasLimit,
		header.GasUsed,
		header.Time,
		// TODO(rgeraldes24)
		header.Extra[:len(header.Extra)-crypto.SignatureLength], // Yes, this will panic if extra is too short
		header.MixDigest,
	}
	if header.BaseFee != nil {
		enc = append(enc, header.BaseFee)
	}
	if header.WithdrawalsHash != nil {
		panic("unexpected withdrawal hash value in clique")
	}
	if err := rlp.Encode(w, enc); err != nil {
		panic("can't encode: " + err.Error())
	}
}
