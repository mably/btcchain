// Copyright (c) 2014-2014 PPCD developers.
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package btcchain

import (
	"github.com/mably/btcutil"
	"github.com/mably/btcwire"
	"math/big"
)

// Peercoin
const (
	InitialHashTargetBits  uint32 = 0x1c00ffff
	privStakeTargetSpacing int64  = 10 * 60 // 10 minutes
	TargetSpacingWorkMax   int64  = StakeTargetSpacing * 12
	TargetTimespan         int64  = 7 * 24 * 60 * 60
)

var ZeroSha = btcwire.ShaHash{}

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
		if (block.meta.Flags & FBlockProofOfStake) > 0 == proofOfStake {
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
func (b *BlockChain) ppcCalcNextRequiredDifficulty(lastNode *blockNode, proofOfStake bool) (uint32, error) {
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
		targetSpacing = minInt64(TargetSpacingWorkMax, privStakeTargetSpacing*(1+lastNode.height-prev.height))
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
func (b *BlockChain) PPCCalcNextRequiredDifficulty(proofOfStake bool) (uint32, error) {
	return b.ppcCalcNextRequiredDifficulty(b.bestChain, proofOfStake)
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
func CalcTrust(bits uint32, proofOfStake bool) *big.Int {
	// Return a work value of zero if the passed difficulty bits represent
	// a negative number. Note this should not happen in practice with valid
	// blocks, but an invalid block could trigger it.
	difficultyNum := CompactToBig(bits)
	if difficultyNum.Sign() <= 0 {
		return big.NewInt(0)
	}
	if !proofOfStake {
		return new(big.Int).SetInt64(1)
	}
	// (1 << 256) / (difficultyNum + 1)
	denominator := new(big.Int).Add(difficultyNum, bigOne)
	return new(big.Int).Div(oneLsh256, denominator)
}

// newBlockNode returns a new block node for the given block header.  It is
// completely disconnected from the chain and the workSum value is just the work
// for the passed block.  The work sum is updated accordingly when the node is
// inserted into a chain.
func ppcNewBlockNode(
	blockHeader *btcwire.BlockHeader, blockSha *btcwire.ShaHash, height int64,
	blockMeta *btcutil.Meta) *blockNode {
	// Make a copy of the hash so the node doesn't keep a reference to part
	// of the full block/block header preventing it from being garbage
	// collected.
	prevHash := blockHeader.PrevBlock
	node := blockNode{
		hash:       blockSha,
		parentHash: &prevHash,
		workSum:    CalcTrust(blockHeader.Bits, (blockMeta.Flags&FBlockProofOfStake) > 0),
		height:     height,
		version:    blockHeader.Version,
		bits:       blockHeader.Bits,
		timestamp:  blockHeader.Timestamp,
		meta:       blockMeta,
	}
	return &node
}

func IsGeneratedStakeModifier(meta *btcutil.Meta) bool {
	if meta.Flags&FBlockStakeModifier > 0 {
		return true
	}
	return false
}

func SetGeneratedStakeModifier(meta *btcutil.Meta, generated bool) {
	if generated {
		meta.Flags |= FBlockStakeModifier
	} else {
		meta.Flags &^= FBlockStakeModifier
	}
}

func GetStakeEntropyBit(meta *btcutil.Meta) uint32 {
	if meta.Flags&FBlockStakeEntropy > 0 {
		return 1
	}
	return 0
}

func SetStakeEntropyBit(meta *btcutil.Meta, entropyBit uint32) {
	if entropyBit == 0 {
		meta.Flags &^= FBlockStakeEntropy
	} else {
		meta.Flags |= FBlockStakeEntropy
	}
}
