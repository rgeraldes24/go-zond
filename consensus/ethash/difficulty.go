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

package ethash

import (
	"math/big"

	"github.com/holiman/uint256"
	"github.com/theQRL/go-zond/core/types"
)

const (
	// minimumDifficulty The minimum that the difficulty may ever be.
	minimumDifficulty = 131072
	// expDiffPeriod is the exponential difficulty period
	expDiffPeriodUint = 100000
	// difficultyBoundDivisorBitShift is the bound divisor of the difficulty (2048),
	// This constant is the right-shifts to use for the division.
	difficultyBoundDivisor = 11
)

// MakeDifficultyCalculatorU256 creates a difficultyCalculator with the given bomb-delay.
// the difficulty is calculated with Byzantium rules, which differs from Homestead in
// how uncles affect the calculation
func MakeDifficultyCalculatorU256(bombDelay *big.Int) func(time uint64, parent *types.Header) *big.Int {
	// Note, the calculations below looks at the parent number, which is 1 below
	// the block number. Thus we remove one from the delay given
	bombDelayFromParent := bombDelay.Uint64() - 1
	return func(time uint64, parent *types.Header) *big.Int {
		/*
			https://github.com/ethereum/EIPs/issues/100
			pDiff = parent.difficulty
			BLOCK_DIFF_FACTOR = 9
			a = pDiff + (pDiff // BLOCK_DIFF_FACTOR) * adj_factor
			b = min(parent.difficulty, MIN_DIFF)
			child_diff = max(a,b )
		*/
		x := (time - parent.Time) / 9 // (block_timestamp - parent_timestamp) // 9
		c := uint64(1)                // if parent.unclehash == emptyUncleHashHash
		if parent.UncleHash != types.EmptyUncleHash {
			c = 2
		}
		xNeg := x >= c
		if xNeg {
			// x is now _negative_ adjustment factor
			x = x - c // - ( (t-p)/p -( 2 or 1) )
		} else {
			x = c - x // (2 or 1) - (t-p)/9
		}
		if x > 99 {
			x = 99 // max(x, 99)
		}
		// parent_diff + (parent_diff / 2048 * max((2 if len(parent.uncles) else 1) - ((timestamp - parent.timestamp) // 9), -99))
		y := new(uint256.Int)
		y.SetFromBig(parent.Difficulty)    // y: p_diff
		pDiff := y.Clone()                 // pdiff: p_diff
		z := new(uint256.Int).SetUint64(x) //z : +-adj_factor (either pos or negative)
		y.Rsh(y, difficultyBoundDivisor)   // y: p__diff / 2048
		z.Mul(y, z)                        // z: (p_diff / 2048 ) * (+- adj_factor)

		if xNeg {
			y.Sub(pDiff, z) // y: parent_diff + parent_diff/2048 * adjustment_factor
		} else {
			y.Add(pDiff, z) // y: parent_diff + parent_diff/2048 * adjustment_factor
		}
		// minimum difficulty can ever be (before exponential factor)
		if y.LtUint64(minimumDifficulty) {
			y.SetUint64(minimumDifficulty)
		}
		// calculate a fake block number for the ice-age delay
		// Specification: https://eips.ethereum.org/EIPS/eip-1234
		var pNum = parent.Number.Uint64()
		if pNum >= bombDelayFromParent {
			if fakeBlockNumber := pNum - bombDelayFromParent; fakeBlockNumber >= 2*expDiffPeriodUint {
				z.SetOne()
				z.Lsh(z, uint(fakeBlockNumber/expDiffPeriodUint-2))
				y.Add(z, y)
			}
		}
		return y.ToBig()
	}
}
