// Copyright 2016 The go-ethereum Authors
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

package types

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/theQRL/go-qrllib/dilithium"
	"github.com/theQRL/go-zond/common"
	"github.com/theQRL/go-zond/crypto/pqcrypto"
	"github.com/theQRL/go-zond/params"
)

var ErrInvalidChainId = errors.New("invalid chain id for signer")

// ErrNoChainID is returned whenever the user failed to specify a chain id.
var ErrNoChainID = errors.New("no chain id specified")

// sigCache is used to cache the derived sender and contains
// the signer used to derive it.
type sigCache struct {
	signer Signer
	from   common.Address
}

// TODO(theQRL/go-zond/issues/39)
// MakeSigner returns a Signer based on the given chain config and block number.
func MakeSigner(config *params.ChainConfig, blockNumber *big.Int, blockTime uint64) Signer {
	return NewShangaiSigner(config.ChainID)
}

// LatestSigner returns the 'most permissive' Signer available for the given chain
// configuration. Specifically, this enables support of all types of transacrions
// when their respective forks are scheduled to occur at any block number (or time)
// in the chain config.
//
// Use this in transaction-handling code where the current block number is unknown. If you
// have the current block number available, use MakeSigner instead.
func LatestSigner(config *params.ChainConfig) Signer {
	return NewShangaiSigner(config.ChainID)
}

// LatestSignerForChainID returns the 'most permissive' Signer available. Specifically,
// this enables support for EIP-155 replay protection and all implemented EIP-2718
// transaction types if chainID is non-nil.
//
// Use this in transaction-handling code where the current block number and fork
// configuration are unknown. If you have a ChainConfig, use LatestSigner instead.
// If you have a ChainConfig and know the current block number, use MakeSigner instead.
func LatestSignerForChainID(chainID *big.Int) (Signer, error) {
	if chainID == nil {
		return nil, ErrNoChainID
	}
	return NewShangaiSigner(chainID), nil
}

// SignTx signs the transaction using the given dilithium signer and private key.
func SignTx(tx *Transaction, s Signer, d *dilithium.Dilithium) (*Transaction, error) {
	h := s.Hash(tx)
	sig, err := pqcrypto.Sign(h[:], d)
	if err != nil {
		return nil, err
	}
	pk := d.GetPK()
	return tx.WithSignatureAndPublicKey(s, sig[:], pk[:])
}

// SignNewTx creates a transaction and signs it.
func SignNewTx(d *dilithium.Dilithium, s Signer, txdata TxData) (*Transaction, error) {
	tx := NewTx(txdata)
	h := s.Hash(tx)
	sig, err := pqcrypto.Sign(h[:], d)
	if err != nil {
		return nil, err
	}
	pk := d.GetPK()
	return tx.WithSignatureAndPublicKey(s, sig, pk[:])
}

// MustSignNewTx creates a transaction and signs it.
// This panics if the transaction cannot be signed.
func MustSignNewTx(d *dilithium.Dilithium, s Signer, txdata TxData) *Transaction {
	tx, err := SignNewTx(d, s, txdata)
	if err != nil {
		panic(err)
	}
	return tx
}

// Sender returns the address derived from the signature (V, R, S) using secp256k1
// elliptic curve and an error if it failed deriving or upon an incorrect
// signature.
//
// Sender may cache the address, allowing it to be used regardless of
// signing method. The cache is invalidated if the cached signer does
// not match the signer used in the current call.
func Sender(signer Signer, tx *Transaction) (common.Address, error) {
	if sc := tx.from.Load(); sc != nil {
		sigCache := sc.(sigCache)
		// If the signer used to derive from in a previous
		// call is not the same as used current, invalidate
		// the cache.
		if sigCache.signer.Equal(signer) {
			return sigCache.from, nil
		}
	}

	addr, err := signer.Sender(tx)
	if err != nil {
		return common.Address{}, err
	}
	tx.from.Store(sigCache{signer: signer, from: addr})
	return addr, nil
}

// Signer encapsulates transaction signature handling. The name of this type is slightly
// misleading because Signers don't actually sign, they're just for validating and
// processing of signatures.
//
// Note that this interface is not a stable API and may change at any time to accommodate
// new protocol rules.
type Signer interface {
	// Sender returns the sender address of the transaction.
	Sender(tx *Transaction) (common.Address, error)

	// SignatureAndPublicKeyValues returns the raw signature, publicKey values corresponding to the
	// given signature.
	SignatureAndPublicKeyValues(tx *Transaction, sig, pk []byte) (signature, publicKey []byte, err error)
	ChainID() *big.Int

	// Hash returns 'signature hash', i.e. the transaction hash that is signed by the
	// private key. This hash does not uniquely identify the transaction.
	Hash(tx *Transaction) common.Hash

	// Equal returns true if the given signer is the same as the receiver.
	Equal(Signer) bool
}

type shangaiSigner struct {
	chainId *big.Int
}

// NewShangaiSigner returns a signer that accepts
// - EIP-1559 dynamic fee transactions,
// - EIP-2930 access list transactions,
// - EIP-155 replay protected transactions
func NewShangaiSigner(chainId *big.Int) Signer {
	return shangaiSigner{chainId: chainId}
}

func (s shangaiSigner) Sender(tx *Transaction) (common.Address, error) {
	if tx.ChainId().Cmp(s.chainId) != 0 {
		return common.Address{}, fmt.Errorf("%w: have %d want %d", ErrInvalidChainId, tx.ChainId(), s.chainId)
	}
	return pqcrypto.DilithiumPKToAddress(tx.RawPublicKeyValue()), nil
}

func (s shangaiSigner) Equal(s2 Signer) bool {
	x, ok := s2.(shangaiSigner)
	return ok && x.chainId.Cmp(s.chainId) == 0
}

func (s shangaiSigner) SignatureAndPublicKeyValues(tx *Transaction, sig, pk []byte) (Signature, PublicKey []byte, err error) {
	// Check that chain ID of tx matches the signer. We also accept ID zero here,
	// because it indicates that the chain ID was not specified in the tx.
	if chainID := tx.ChainId(); chainID.Sign() != 0 && chainID.Cmp(s.chainId) != 0 {
		return nil, nil, fmt.Errorf("%w: have %d want %d", ErrInvalidChainId, chainID, s.chainId)
	}
	Signature = decodeSignature(sig)
	PublicKey = decodePublicKey(pk)
	return Signature, PublicKey, nil
}

// Hash returns the hash to be signed by the sender.
// It does not uniquely identify the transaction.
func (s shangaiSigner) Hash(tx *Transaction) common.Hash {
	switch tx.Type() {
	case DynamicFeeTxType:
		return prefixedRlpHash(
			tx.Type(),
			[]interface{}{
				s.chainId,
				tx.Nonce(),
				tx.GasTipCap(),
				tx.GasFeeCap(),
				tx.Gas(),
				tx.To(),
				tx.Value(),
				tx.Data(),
				tx.AccessList(),
			})
	case LegacyTxType:
		return rlpHash([]interface{}{
			tx.Nonce(),
			tx.GasPrice(),
			tx.Gas(),
			tx.To(),
			tx.Value(),
			tx.Data(),
			s.chainId,
		})
	case AccessListTxType:
		return prefixedRlpHash(
			tx.Type(),
			[]interface{}{
				s.chainId,
				tx.Nonce(),
				tx.GasPrice(),
				tx.Gas(),
				tx.To(),
				tx.Value(),
				tx.Data(),
				tx.AccessList(),
			})
	default:
		// This _should_ not happen, but in case someone sends in a bad
		// json struct via RPC, it's probably more prudent to return an
		// empty hash instead of killing the node with a panic
		//panic("Unsupported transaction type: %d", tx.typ)
		return common.Hash{}
	}
}

func (s shangaiSigner) ChainID() *big.Int {
	return s.chainId
}

func decodeSignature(sig []byte) (signature []byte) {
	if len(sig) != pqcrypto.DilithiumSignatureLength {
		panic(fmt.Sprintf("wrong size for signature: got %d, want %d", len(sig), pqcrypto.DilithiumSignatureLength))
	}
	signature = make([]byte, pqcrypto.DilithiumSignatureLength)
	copy(signature, sig)
	return signature
}

func decodePublicKey(pk []byte) (publicKey []byte) {
	if len(pk) != pqcrypto.DilithiumPublicKeyLength {
		panic(fmt.Sprintf("wrong size for dilithium publickey: got %d, want %d", len(pk), pqcrypto.DilithiumPublicKeyLength))
	}
	publicKey = make([]byte, pqcrypto.DilithiumPublicKeyLength)
	copy(publicKey, pk)
	return publicKey
}
