// Copyright 2020 The go-ethereum Authors
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

package zond

import (
	"fmt"
	"math/big"

	"github.com/theQRL/go-zond/common"
	"github.com/theQRL/go-zond/core"
	"github.com/theQRL/go-zond/core/types"
	"github.com/theQRL/go-zond/p2p/enode"
	"github.com/theQRL/go-zond/zond/protocols/zond"
)

// ethHandler implements the zond.Backend interface to handle the various network
// packets that are sent as replies or broadcasts.
type ethHandler handler

func (h *ethHandler) Chain() *core.BlockChain { return h.chain }
func (h *ethHandler) TxPool() zond.TxPool     { return h.txpool }

// RunPeer is invoked when a peer joins on the `zond` protocol.
func (h *ethHandler) RunPeer(peer *zond.Peer, hand zond.Handler) error {
	return (*handler)(h).runEthPeer(peer, hand)
}

// PeerInfo retrieves all known `eth` information about a peer.
func (h *ethHandler) PeerInfo(id enode.ID) interface{} {
	if p := h.peers.peer(id.String()); p != nil {
		return p.info()
	}
	return nil
}

// AcceptTxs retrieves whether transaction processing is enabled on the node
// or if inbound transactions should simply be dropped.
func (h *ethHandler) AcceptTxs() bool {
	return h.acceptTxs.Load()
}

// Handle is invoked from a peer's message handler when it receives a new remote
// message that the handler couldn't consume and serve itself.
func (h *ethHandler) Handle(peer *zond.Peer, packet zond.Packet) error {
	// Consume any broadcasts and announces, forwarding the rest to the downloader
	switch packet := packet.(type) {
	case *zond.NewBlockHashesPacket:
		hashes, numbers := packet.Unpack()
		return h.handleBlockAnnounces(peer, hashes, numbers)

	case *zond.NewBlockPacket:
		return h.handleBlockBroadcast(peer, packet.Block, packet.TD)

	case *zond.NewPooledTransactionHashesPacket66:
		return h.txFetcher.Notify(peer.ID(), *packet)

	case *zond.NewPooledTransactionHashesPacket68:
		return h.txFetcher.Notify(peer.ID(), packet.Hashes)

	case *zond.TransactionsPacket:
		return h.txFetcher.Enqueue(peer.ID(), *packet, false)

	case *zond.PooledTransactionsPacket:
		return h.txFetcher.Enqueue(peer.ID(), *packet, true)

	default:
		return fmt.Errorf("unexpected eth packet type: %T", packet)
	}
}

// handleBlockAnnounces is invoked from a peer's message handler when it transmits a
// batch of block announcements for the local node to process.
func (h *ethHandler) handleBlockAnnounces(peer *zond.Peer, hashes []common.Hash, numbers []uint64) error {
	// TODO (MariusVanDerWijden) drop non-updated peers after the merge
	return nil
	// return errors.New("unexpected block announces")
}

// handleBlockBroadcast is invoked from a peer's message handler when it transmits a
// block broadcast for the local node to process.
func (h *ethHandler) handleBlockBroadcast(peer *zond.Peer, block *types.Block, td *big.Int) error {
	// TODO (MariusVanDerWijden) drop non-updated peers after the merge
	return nil
	// return errors.New("unexpected block announces")
}
