// Copyright (c) 2014-2014 PPCD developers.
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package btcchain

import (
	"fmt"
	"github.com/mably/btcutil"
	"github.com/mably/btcwire"
	"math/big"
)

// Peercoin
const (
	InitialHashTargetBits  uint32 = 0x1c00ffff
	TargetSpacingWorkMax   int64  = StakeTargetSpacing * 12
	TargetTimespan         int64  = 7 * 24 * 60 * 60
)

var ZeroSha = btcwire.ShaHash{}

// getBlockNode try to obtain a node form the memory block chain and loads it
// form the database in not found in memory.
func (b *BlockChain) getBlockNode(hash *btcwire.ShaHash) (*blockNode, error) {

	// Return the existing previous block node if it's already there.
	if bn, ok := b.index[*hash]; ok {
		return bn, nil
	}

	// Dynamically load the previous block from the block database, create
	// a new block node for it, and update the memory chain accordingly.
	prevBlockNode, err := b.loadBlockNode(hash)
	if err != nil {
		return nil, err
	}
	return prevBlockNode, nil
}

// https://github.com/ppcoin/ppcoin/blob/v0.4.0ppc/src/main.cpp#L894
// ppcoin: find last block index up to pindex
func (b *BlockChain) GetLastBlockIndex(last *blockNode, proofOfStake bool) (block *blockNode) {

	if last == nil {
		defer timeTrack(now(), fmt.Sprintf("GetLastBlockIndex"))
	} else {
		defer timeTrack(now(), fmt.Sprintf("GetLastBlockIndex(%v)", last.hash))
	}

	block = last
	for true {
		if block == nil {
			break
		}
		// TODO dirty workaround, ppcoin doesn't point to genesis block
		if block.height == 0 {
			break
		}
		if block.parentHash == nil {
			break
		}
		if block.IsProofOfStake() == proofOfStake {
			break
		}
		block, _ = b.getPrevNodeFromNode(block)
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

	defer timeTrack(now(), fmt.Sprintf("ppcCalcNextRequiredDifficulty(%v)", lastNode.hash))

	prev := b.GetLastBlockIndex(lastNode, proofOfStake)
	if prev.hash.IsEqual(b.netParams.GenesisHash) {
		return InitialHashTargetBits, nil // first block
	}
	prevParent, _ := b.getPrevNodeFromNode(prev)
	prevPrev := b.GetLastBlockIndex(prevParent, proofOfStake)
	if prevPrev.hash.IsEqual(b.netParams.GenesisHash) {
		return InitialHashTargetBits, nil // second block
	}

	actualSpacing := prev.timestamp.Unix() - prevPrev.timestamp.Unix()

	newTarget := CompactToBig(prev.bits)
	var targetSpacing int64
	if proofOfStake {
		targetSpacing = StakeTargetSpacing
	} else {
		targetSpacing = minInt64(TargetSpacingWorkMax, StakeTargetSpacing * ( 1 + lastNode.height - prev.height))
	}
	interval := TargetTimespan / targetSpacing
	targetSpacingBig := big.NewInt(targetSpacing)
	intervalMinusOne := big.NewInt(interval - 1)
	intervalPlusOne := big.NewInt(interval + 1)
	tmp := new(big.Int).Mul(intervalMinusOne, targetSpacingBig)
	tmp.Add(tmp, big.NewInt(actualSpacing + actualSpacing))
	newTarget.Mul(newTarget, tmp)
	newTarget.Div(newTarget, new(big.Int).Mul(intervalPlusOne, targetSpacingBig))

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

// IsCoinStake determines whether or not a transaction is a coinstake.  A coinstake
// is a special transaction created by peercoin minters.
func IsCoinStake(tx *btcutil.Tx) bool {
	return tx.MsgTx().IsCoinStake()
}

// newBlockNode returns a new block node for the given block header.  It is
// completely disconnected from the chain and the workSum value is just the work
// for the passed block.  The work sum is updated accordingly when the node is
// inserted into a chain.
func ppcNewBlockNode(
	blockHeader *btcwire.BlockHeader, blockSha *btcwire.ShaHash, height int64,
	blockMeta *btcwire.Meta) *blockNode {
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

// https://github.com/ppcoin/ppcoin/blob/v0.4.0ppc/src/main.h#L962
// ppcoin: two types of block: proof-of-work or proof-of-stake
func (block *blockNode) IsProofOfStake() bool {
	return block.meta.Flags&FBlockProofOfStake != 0
}

// SetProofOfStake
func SetProofOfStake(meta *btcwire.Meta, proofOfStake bool) {
	if proofOfStake {
		meta.Flags |= FBlockProofOfStake
	} else {
		meta.Flags &^= FBlockProofOfStake
	}
}

// IsGeneratedStakeModifier
func IsGeneratedStakeModifier(meta *btcwire.Meta) bool {
	return meta.Flags&FBlockStakeModifier != 0
}

// SetGeneratedStakeModifier
func SetGeneratedStakeModifier(meta *btcwire.Meta, generated bool) {
	if generated {
		meta.Flags |= FBlockStakeModifier
	} else {
		meta.Flags &^= FBlockStakeModifier
	}
}

// GetStakeEntropyBit
func GetStakeEntropyBit(meta *btcwire.Meta) uint32 {
	if meta.Flags&FBlockStakeEntropy != 0 {
		return 1
	}
	return 0
}

// SetStakeEntropyBit
func SetStakeEntropyBit(meta *btcwire.Meta, entropyBit uint32) {
	if entropyBit == 0 {
		meta.Flags &^= FBlockStakeEntropy
	} else {
		meta.Flags |= FBlockStakeEntropy
	}
}

// checkProofOfStake
func (b *BlockChain) checkBlockProofOfStake(block *btcutil.Block) error {
	if block.MsgBlock().IsProofOfStake() {
		blockHash, err := block.Sha()
		if err != nil { return err }
		log.Tracef("Block %v is PoS", blockHash)
		tx, err := block.Tx(1)
		if err != nil { return err }
		hashProofOfStake, err :=
			b.checkTxProofOfStake(tx, block.MsgBlock().Header.Bits)
		if err != nil {
			str := fmt.Sprintf("Proof of stake check failed for block %v : %v", blockHash, err)
			return ruleError(ErrProofOfStakeCheck, str)
		} else {
			SetProofOfStake(block.Meta(), true) // Important: flags
			block.Meta().HashProofOfStake = *hashProofOfStake
			log.Debugf("Proof of stake for block %v = %v", blockHash, hashProofOfStake)
		}
	}
	return nil
}


// BigToShaHash converts a big.Int into a btcwire.ShaHash.
func BigToShaHash(value *big.Int) (*btcwire.ShaHash, error) {

	buf := value.Bytes()

	blen := len(buf)
	for i := 0; i < blen/2; i++ {
		buf[i], buf[blen-1-i] = buf[blen-1-i], buf[i]
	}

	// Make sure the byte slice is the right length by appending zeros to
	// pad it out.
	pbuf := buf
	if btcwire.HashSize-blen > 0 {
		pbuf = make([]byte, btcwire.HashSize)
		copy(pbuf, buf)
	}

	return btcwire.NewShaHash(pbuf)
}