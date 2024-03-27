// Copyright 2023 The go-ethereum Authors
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
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/bits"

	"github.com/theQRL/go-zond/beacon/params"
	"github.com/theQRL/go-zond/common"
	"github.com/theQRL/go-zond/common/hexutil"
)

// SerializedSyncCommitteeSize is the size of the sync committee plus the
// aggregate public key.
const SerializedSyncCommitteeSize = params.SyncCommitteeSize * params.DilithiumPubkeySize

// SerializedSyncCommittee is the serialized version of a sync committee
// plus the aggregate public key.
type SerializedSyncCommittee [SerializedSyncCommitteeSize]byte

// jsonSyncCommittee is the JSON representation of a sync committee.
//
// See data structure definition here:
// https://github.com/ethereum/consensus-specs/blob/dev/specs/altair/beacon-chain.md#syncaggregate
type jsonSyncCommittee struct {
	Pubkeys []hexutil.Bytes `json:"pubkeys"`
}

// MarshalJSON implements json.Marshaler.
func (s *SerializedSyncCommittee) MarshalJSON() ([]byte, error) {
	sc := jsonSyncCommittee{Pubkeys: make([]hexutil.Bytes, params.SyncCommitteeSize)}
	for i := range sc.Pubkeys {
		sc.Pubkeys[i] = make(hexutil.Bytes, params.DilithiumPubkeySize)
		copy(sc.Pubkeys[i][:], s[i*params.DilithiumPubkeySize:(i+1)*params.DilithiumPubkeySize])
	}
	return json.Marshal(&sc)
}

// UnmarshalJSON implements json.Marshaler.
func (s *SerializedSyncCommittee) UnmarshalJSON(input []byte) error {
	var sc jsonSyncCommittee
	if err := json.Unmarshal(input, &sc); err != nil {
		return err
	}
	if len(sc.Pubkeys) != params.SyncCommitteeSize {
		return fmt.Errorf("invalid number of pubkeys %d", len(sc.Pubkeys))
	}
	for i, key := range sc.Pubkeys {
		if len(key) != params.DilithiumPubkeySize {
			return fmt.Errorf("pubkey %d has invalid size %d", i, len(key))
		}
		copy(s[i*params.DilithiumPubkeySize:], key[:])
	}

	return nil
}

// Root calculates the root hash of the binary tree representation of a sync
// committee provided in serialized format.
//
// TODO(zsfelfoldi): Get rid of this when SSZ encoding lands.
func (s *SerializedSyncCommittee) Root() common.Hash {
	var (
		hasher = sha256.New()
		// TODO(rgeraldes24)
		// padding [64 - params.DilithiumPubkeySize]byte
		padding [16]byte
		data    [params.SyncCommitteeSize]common.Hash
		l       = params.SyncCommitteeSize
	)
	for i := range data {
		hasher.Reset()
		hasher.Write(s[i*params.DilithiumPubkeySize : (i+1)*params.DilithiumPubkeySize])
		hasher.Write(padding[:])
		hasher.Sum(data[i][:0])
	}
	for l > 1 {
		for i := 0; i < l/2; i++ {
			hasher.Reset()
			hasher.Write(data[i*2][:])
			hasher.Write(data[i*2+1][:])
			hasher.Sum(data[i][:0])
		}
		l /= 2
	}
	hasher.Reset()
	hasher.Write(s[SerializedSyncCommitteeSize-params.DilithiumPubkeySize : SerializedSyncCommitteeSize])
	hasher.Write(padding[:])
	hasher.Sum(data[1][:0])
	hasher.Reset()
	hasher.Write(data[0][:])
	hasher.Write(data[1][:])
	hasher.Sum(data[0][:0])
	return data[0]
}

// Deserialize splits open the pubkeys into proper key types.
func (s *SerializedSyncCommittee) Deserialize() (*SyncCommittee, error) {
	sc := new(SyncCommittee)
	for i := 0; i < params.SyncCommitteeSize; i++ {
		var pk []byte
		copy(pk[:], s[i*params.DilithiumPubkeySize:(i+1)*params.DilithiumPubkeySize])
		sc.keys[i] = pk
	}
	return sc, nil
}

// SyncCommittee is a set of sync committee signer pubkeys and the aggregate key.
//
// See data structure definition here:
// https://github.com/ethereum/consensus-specs/blob/dev/specs/altair/beacon-chain.md#syncaggregate
type SyncCommittee struct {
	keys [params.SyncCommitteeSize][]byte
}

// VerifySignature returns true if the given sync aggregate is a valid signature
// or the given hash.
func (sc *SyncCommittee) VerifySignatures(signingRoot common.Hash, signature *SyncAggregate) bool {
	var (
		keys = make([][]byte, 0, params.SyncCommitteeSize)
	)
	for i, key := range sc.keys {
		if signature.Signers[i/8]&(byte(1)<<(i%8)) != 0 {
			keys = append(keys, key)
		}
	}
	// TODO(rgeraldes24)
	// for i, key := range keys {
	// 	if success := dilithium.Verify(signingRoot[:], signature.Signatures[i], key); !success {
	// 		return false
	// 	}
	// }

	return true
}

//go:generate go run github.com/fjl/gencodec -type SyncAggregate -field-override syncAggregateMarshaling -out gen_syncaggregate_json.go

// SyncAggregate represents signatures with Signers referring
// to a subset of the corresponding sync committee.
//
// See data structure definition here:
// https://github.com/ethereum/consensus-specs/blob/dev/specs/altair/beacon-chain.md#syncaggregate
type SyncAggregate struct {
	Signers    [params.SyncCommitteeBitmaskSize]byte `gencodec:"required" json:"sync_committee_bits"`
	Signatures [][params.DilithiumSignatureSize]byte `gencodec:"required" json:"sync_committee_signatures"`
}

// TODO(rgeraldes24): new artifact generation
type syncAggregateMarshaling struct {
	Signers    hexutil.Bytes
	Signatures []hexutil.Bytes
}

// SignerCount returns the number of signers in the aggregate signature.
func (s *SyncAggregate) SignerCount() int {
	var count int
	for _, v := range s.Signers {
		count += bits.OnesCount8(v)
	}
	return count
}
