// Copyright (c) 2013-2014 Conformal Systems LLC.
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package btcchain

import (
	_ "fmt"
	"math/big"
	"time"

	"github.com/mably/btcwire"
)

const (
	// targetTimespan is the desired amount of time that should elapse
	// before block difficulty requirement is examined to determine how
	// it should be changed in order to maintain the desired block
	// generation rate.
	targetTimespan = time.Hour * 24 * 14

	// targetSpacing is the desired amount of time to generate each block.
	targetSpacing = time.Minute * 10

	// BlocksPerRetarget is the number of blocks between each difficulty
	// retarget.  It is calculated based on the desired block generation
	// rate.
	BlocksPerRetarget = int64(targetTimespan / targetSpacing)

	// retargetAdjustmentFactor is the adjustment factor used to limit
	// the minimum and maximum amount of adjustment that can occur between
	// difficulty retargets.
	retargetAdjustmentFactor = 4

	// minRetargetTimespan is the minimum amount of adjustment that can
	// occur between difficulty retargets.  It equates to 25% of the
	// previous difficulty.
	minRetargetTimespan = int64(targetTimespan / retargetAdjustmentFactor)

	// maxRetargetTimespan is the maximum amount of adjustment that can
	// occur between difficulty retargets.  It equates to 400% of the
	// previous difficulty.
	maxRetargetTimespan = int64(targetTimespan * retargetAdjustmentFactor)
)

var (
	// bigOne is 1 represented as a big.Int.  It is defined here to avoid
	// the overhead of creating it multiple times.
	bigOne = big.NewInt(1)

	// oneLsh256 is 1 shifted left 256 bits.  It is defined here to avoid
	// the overhead of creating it multiple times.
	oneLsh256 = new(big.Int).Lsh(bigOne, 256)
)

// ShaHashToBig converts a btcwire.ShaHash into a big.Int that can be used to
// perform math comparisons.
func ShaHashToBig(hash *btcwire.ShaHash) *big.Int {
	// A ShaHash is in little-endian, but the big package wants the bytes
	// in big-endian.  Reverse them.  ShaHash.Bytes makes a copy, so it
	// is safe to modify the returned buffer.
	buf := hash.Bytes()
	blen := len(buf)
	for i := 0; i < blen/2; i++ {
		buf[i], buf[blen-1-i] = buf[blen-1-i], buf[i]
	}

	return new(big.Int).SetBytes(buf)
}

// CompactToBig converts a compact representation of a whole number N to an
// unsigned 32-bit number.  The representation is similar to IEEE754 floating
// point numbers.
//
// Like IEEE754 floating point, there are three basic components: the sign,
// the exponent, and the mantissa.  They are broken out as follows:
//
//	* the most significant 8 bits represent the unsigned base 256 exponent
// 	* bit 23 (the 24th bit) represents the sign bit
//	* the least significant 23 bits represent the mantissa
//
//	-------------------------------------------------
//	|   Exponent     |    Sign    |    Mantissa     |
//	-------------------------------------------------
//	| 8 bits [31-24] | 1 bit [23] | 23 bits [22-00] |
//	-------------------------------------------------
//
// The formula to calculate N is:
// 	N = (-1^sign) * mantissa * 256^(exponent-3)
//
// This compact form is only used in bitcoin to encode unsigned 256-bit numbers
// which represent difficulty targets, thus there really is not a need for a
// sign bit, but it is implemented here to stay consistent with bitcoind.
func CompactToBig(compact uint32) *big.Int {
	// Extract the mantissa, sign bit, and exponent.
	mantissa := compact & 0x007fffff
	isNegative := compact&0x00800000 != 0
	exponent := uint(compact >> 24)

	// Since the base for the exponent is 256, the exponent can be treated
	// as the number of bytes to represent the full 256-bit number.  So,
	// treat the exponent as the number of bytes and shift the mantissa
	// right or left accordingly.  This is equivalent to:
	// N = mantissa * 256^(exponent-3)
	var bn *big.Int
	if exponent <= 3 {
		mantissa >>= 8 * (3 - exponent)
		bn = big.NewInt(int64(mantissa))
	} else {
		bn = big.NewInt(int64(mantissa))
		bn.Lsh(bn, 8*(exponent-3))
	}

	// Make it negative if the sign bit is set.
	if isNegative {
		bn = bn.Neg(bn)
	}

	return bn
}

// BigToCompact converts a whole number N to a compact representation using
// an unsigned 32-bit number.  The compact representation only provides 23 bits
// of precision, so values larger than (2^23 - 1) only encode the most
// significant digits of the number.  See CompactToBig for details.
func BigToCompact(n *big.Int) uint32 {
	// No need to do any work if it's zero.
	if n.Sign() == 0 {
		return 0
	}

	// Since the base for the exponent is 256, the exponent can be treated
	// as the number of bytes.  So, shift the number right or left
	// accordingly.  This is equivalent to:
	// mantissa = mantissa / 256^(exponent-3)
	var mantissa uint32
	exponent := uint(len(n.Bytes()))
	if exponent <= 3 {
		mantissa = uint32(n.Bits()[0])
		mantissa <<= 8 * (3 - exponent)
	} else {
		// Use a copy to avoid modifying the caller's original number.
		tn := new(big.Int).Set(n)
		mantissa = uint32(tn.Rsh(tn, 8*(exponent-3)).Bits()[0])
	}

	// When the mantissa already has the sign bit set, the number is too
	// large to fit into the available 23-bits, so divide the number by 256
	// and increment the exponent accordingly.
	if mantissa&0x00800000 != 0 {
		mantissa >>= 8
		exponent++
	}

	// Pack the exponent, sign bit, and mantissa into an unsigned 32-bit
	// int and return it.
	compact := uint32(exponent<<24) | mantissa
	if n.Sign() < 0 {
		compact |= 0x00800000
	}
	return compact
}

// CalcWork calculates a work value from difficulty bits.  Bitcoin increases
// the difficulty for generating a block by decreasing the value which the
// generated hash must be less than.  This difficulty target is stored in each
// block header using a compact representation as described in the documenation
// for CompactToBig.  The main chain is selected by choosing the chain that has
// the most proof of work (highest difficulty).  Since a lower target difficulty
// value equates to higher actual difficulty, the work value which will be
// accumulated must be the inverse of the difficulty.  Also, in order to avoid
// potential division by zero and really small floating point numbers, the
// result adds 1 to the denominator and multiplies the numerator by 2^256.
func CalcWork(bits uint32) *big.Int {
	// Return a work value of zero if the passed difficulty bits represent
	// a negative number. Note this should not happen in practice with valid
	// blocks, but an invalid block could trigger it.
	difficultyNum := CompactToBig(bits)
	if difficultyNum.Sign() <= 0 {
		return big.NewInt(0)
	}

	// (1 << 256) / (difficultyNum + 1)
	denominator := new(big.Int).Add(difficultyNum, bigOne)
	return new(big.Int).Div(oneLsh256, denominator)
}

// calcEasiestDifficulty calculates the easiest possible difficulty that a block
// can have given starting difficulty bits and a duration.  It is mainly used to
// verify that claimed proof of work by a block is sane as compared to a
// known good checkpoint.
func (b *BlockChain) calcEasiestDifficulty(bits uint32, duration time.Duration) uint32 {
	// Convert types used in the calculations below.
	durationVal := int64(duration)
	adjustmentFactor := big.NewInt(retargetAdjustmentFactor)

	// The test network rules allow minimum difficulty blocks after more
	// than twice the desired amount of time needed to generate a block has
	// elapsed.
	if b.netParams.ResetMinDifficulty {
		if durationVal > int64(targetSpacing)*2 {
			return b.netParams.PowLimitBits
		}
	}

	// Since easier difficulty equates to higher numbers, the easiest
	// difficulty for a given duration is the largest value possible given
	// the number of retargets for the duration and starting difficulty
	// multiplied by the max adjustment factor.
	newTarget := CompactToBig(bits)
	for durationVal > 0 && newTarget.Cmp(b.netParams.PowLimit) < 0 {
		newTarget.Mul(newTarget, adjustmentFactor)
		durationVal -= maxRetargetTimespan
	}

	// Limit new value to the proof of work limit.
	if newTarget.Cmp(b.netParams.PowLimit) > 0 {
		newTarget.Set(b.netParams.PowLimit)
	}

	return BigToCompact(newTarget)
}

// findPrevTestNetDifficulty returns the difficulty of the previous block which
// did not have the special testnet minimum difficulty rule applied.
func (b *BlockChain) findPrevTestNetDifficulty(startNode *blockNode) (uint32, error) {
	// Search backwards through the chain for the last block without
	// the special rule applied.
	iterNode := startNode
	for iterNode != nil && iterNode.height%BlocksPerRetarget != 0 &&
		iterNode.bits == b.netParams.PowLimitBits {

		// Get the previous block node.  This function is used over
		// simply accessing iterNode.parent directly as it will
		// dynamically create previous block nodes as needed.  This
		// helps allow only the pieces of the chain that are needed
		// to remain in memory.
		var err error
		iterNode, err = b.getPrevNodeFromNode(iterNode)
		if err != nil {
			log.Errorf("getPrevNodeFromNode: %v", err)
			return 0, err
		}
	}

	// Return the found difficulty or the minimum difficulty if no
	// appropriate block was found.
	lastBits := b.netParams.PowLimitBits
	if iterNode != nil {
		lastBits = iterNode.bits
	}
	return lastBits, nil
}

// Peercoin
var (
	ZeroSha                       = btcwire.ShaHash{}
	InitialHashTargetBits  uint32 = 0x1c00ffff
	privStakeTargetSpacing int64  = 10 * 60 // 10 minutes
	TargetSpacingWorkMax   int64  = StakeTargetSpacing * 12
	TargetTimespan         int64  = 7 * 24 * 60 * 60
)

func MinInt(a int64, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// https://github.com/ppcoin/ppcoin/blob/v0.4.0ppc/src/main.cpp#L894
// ppcoin: find last block index up to pindex
func (b *BlockChain) GetLastBlockIndex(last *blockNode, proofOfStake bool) (block *blockNode) {
	block = last
	for true {
		if block == nil {
			break
		}
		//TODO dirty workaround, ppcoin doesn't point to genesis block
		if block.height == 0 {
			return nil
		}
		if block.parent == nil {
			break
		}
		if (block.flags & FBlockProofOfStake) > 0 == proofOfStake {
			break
		}
		block = block.parent
	}
	return block
}

// calcNextRequiredDifficulty calculates the required difficulty for the block
// after the passed previous block node based on the difficulty retarget rules.
// This function differs from the exported CalcNextRequiredDifficulty in that
// the exported version uses the current best chain as the previous block node
// while this function accepts any block node.

// Peercoin https://github.com/ppcoin/ppcoin/blob/v0.4.0ppc/src/main.cpp#L902
func (b *BlockChain) calcNextRequiredDifficulty(lastNode *blockNode, proofOfStake bool) (uint32, error) {
	if lastNode == nil {
		return b.netParams.PowLimitBits, nil // genesis block
	}
	prev := b.GetLastBlockIndex(lastNode, proofOfStake)
	if prev == nil {
		return InitialHashTargetBits, nil // first block
	}
	prevPrev := b.GetLastBlockIndex(prev.parent, proofOfStake)
	if prevPrev == nil {
		return InitialHashTargetBits, nil // second block
	}
	actualSpacing := prev.timestamp.Unix() - prevPrev.timestamp.Unix()
	newTarget := CompactToBig(prev.bits)
	var targetSpacing int64
	if proofOfStake {
		targetSpacing = privStakeTargetSpacing
	} else {
		targetSpacing = MinInt(TargetSpacingWorkMax, privStakeTargetSpacing*(1+lastNode.height-prev.height))
	}
	interval := TargetTimespan / targetSpacing
	tmp := new(big.Int)
	newTarget.Mul(newTarget,
		tmp.SetInt64(interval-1).Mul(tmp, big.NewInt(targetSpacing)).Add(tmp, big.NewInt(actualSpacing+actualSpacing)))
	newTarget.Div(newTarget, tmp.SetInt64(interval+1).Mul(tmp, big.NewInt(targetSpacing)))
	if newTarget.Cmp(b.netParams.PowLimit) > 0 {
		newTarget = b.netParams.PowLimit
	}
	return BigToCompact(newTarget), nil
}

// CalcNextRequiredDifficulty calculates the required difficulty for the block
// after the end of the current best chain based on the difficulty retarget
// rules.
//
// This function is NOT safe for concurrent access.
func (b *BlockChain) CalcNextRequiredDifficulty(proofOfStake bool) (uint32, error) {
	return b.calcNextRequiredDifficulty(b.bestChain, proofOfStake)
}
