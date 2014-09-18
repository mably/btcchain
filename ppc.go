// Copyright (c) 2014-2014 PPCD developers.
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package btcchain

import (
	"fmt"
	"github.com/mably/btcnet"
	"github.com/mably/btcutil"
	"github.com/mably/btcwire"
	"math/big"
)

// Peercoin
const (
	InitialHashTargetBits uint32 = 0x1c00ffff
	TargetSpacingWorkMax  int64  = StakeTargetSpacing * 12
	TargetTimespan        int64  = 7 * 24 * 60 * 60

	Cent               int64 = 10000
	Coin               int64 = 100 * Cent
	MinTxFee           int64 = Cent
	MinRelayTxFee      int64 = Cent
	MaxMoney           int64 = 2000000000 * Coin
	MaxMintProofOfWork int64 = 9999 * Coin
	MinTxOutAmount     int64 = MinTxFee
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
		return b.netParams.InitialHashTargetBits, nil // first block
	}
	prevParent, _ := b.getPrevNodeFromNode(prev)
	prevPrev := b.GetLastBlockIndex(prevParent, proofOfStake)
	if prevPrev.hash.IsEqual(b.netParams.GenesisHash) {
		return b.netParams.InitialHashTargetBits, nil // second block
	}

	actualSpacing := prev.timestamp.Unix() - prevPrev.timestamp.Unix()

	newTarget := CompactToBig(prev.bits)
	var targetSpacing int64
	if proofOfStake {
		targetSpacing = StakeTargetSpacing
	} else {
		targetSpacing = minInt64(TargetSpacingWorkMax, StakeTargetSpacing*(1+lastNode.height-prev.height))
	}
	interval := TargetTimespan / targetSpacing
	targetSpacingBig := big.NewInt(targetSpacing)
	intervalMinusOne := big.NewInt(interval - 1)
	intervalPlusOne := big.NewInt(interval + 1)
	tmp := new(big.Int).Mul(intervalMinusOne, targetSpacingBig)
	tmp.Add(tmp, big.NewInt(actualSpacing+actualSpacing))
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

// SetCoinbaseMaturity sets required coinbase maturity and return old one
// Required for tests
func (b *BlockChain) SetCoinbaseMaturity(coinbaseMaturity int64) (old int64) {
	old = b.netParams.CoinbaseMaturity
	b.netParams.CoinbaseMaturity = coinbaseMaturity
	return
}

// CalcTrust calculates a work value from difficulty bits.  Bitcoin increases
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

func (b *BlockChain) CalcMintAndMoneySupply(node *blockNode, block *btcutil.Block) error {

    var nFees int64 = 0
    var nValueIn int64 = 0
    var nValueOut int64 = 0

    txStore, err := b.fetchInputTransactions(node, block)
	if err != nil {
		return err
	}

	transactions := block.Transactions()
	for _, tx := range transactions {

		var nTxValueOut int64 = 0
		for _, txOut := range tx.MsgTx().TxOut {
			nTxValueOut += txOut.Value
		}

        if IsCoinBase(tx) {
            nValueOut += nTxValueOut
        } else {
			var nTxValueIn int64 = 0
			for _, txIn := range tx.MsgTx().TxIn {
				txInHash := &txIn.PreviousOutpoint.Hash
				originTx, _ := txStore[*txInHash]
				originTxIndex := txIn.PreviousOutpoint.Index
				originTxSatoshi := originTx.Tx.MsgTx().TxOut[originTxIndex].Value
				nTxValueIn += originTxSatoshi
			}
            nValueIn += nTxValueIn
            nValueOut += nTxValueOut
            if !IsCoinStake(tx) {
                nFees += nTxValueIn - nTxValueOut
			}
        }
    }

	log.Debugf("height = %v, nValueIn = %v, nValueOut = %v, nFees = %v", block.Height(), nValueIn, nValueOut, nFees)

    // ppcoin: track money supply and mint amount info
    block.Meta().Mint = nValueOut - nValueIn + nFees
	var prevNode *blockNode
    prevNode, err = b.getPrevNodeFromNode(node)
    if err != nil {
		return err
	}
    if prevNode == nil {
    	block.Meta().MoneySupply = nValueOut - nValueIn
    } else {
    	block.Meta().MoneySupply = prevNode.meta.MoneySupply + nValueOut - nValueIn
    }

	log.Debugf("height = %v, mint = %v, moneySupply = %v", block.Height(), block.Meta().Mint, block.Meta().MoneySupply)

    return nil
}

// IsCoinStake determines whether or not a transaction is a coinstake.  A coinstake
// is a special transaction created by peercoin minters.
func IsCoinStake(tx *btcutil.Tx) bool {
	return tx.MsgTx().IsCoinStake()
}

// ppcNewBlockNode returns a new block node for the given block header.  It is
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
	workSum := CalcTrust(blockHeader.Bits, (blockMeta.Flags&FBlockProofOfStake) > 0)
	//log.Debugf("Height = %v, WorkSum = %v", height, workSum)
	node := blockNode{
		hash:       blockSha,
		parentHash: &prevHash,
		workSum:    workSum,
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

// https://github.com/ppcoin/ppcoin/blob/v0.4.0ppc/src/main.cpp#L829
func PPCGetProofOfWorkReward(nBits uint32, netParams *btcnet.Params) (subsidy int64) {
	bigTwo := new(big.Int).SetInt64(2)
	bnSubsidyLimit := new(big.Int).SetInt64(MaxMintProofOfWork)
	bnTarget := CompactToBig(nBits)
	bnTargetLimit := netParams.PowLimit
	// TODO(kac-) wat? bnTargetLimit.SetCompact(bnTargetLimit.GetCompact());
	bnTargetLimit = CompactToBig(BigToCompact(bnTargetLimit))
	// ppcoin: subsidy is cut in half every 16x multiply of difficulty
	// A reasonably continuous curve is used to avoid shock to market
	// (nSubsidyLimit / nSubsidy) ** 4 == bnProofOfWorkLimit / bnTarget
	bnLowerBound := new(big.Int).SetInt64(Cent)
	bnUpperBound := new(big.Int).Set(bnSubsidyLimit)
	for new(big.Int).Add(bnLowerBound, new(big.Int).SetInt64(Cent)).Cmp(bnUpperBound) <= 0 {
		bnMidValue := new(big.Int).Div(new(big.Int).Add(bnLowerBound, bnUpperBound), bigTwo)
		/*
			if (fDebug && GetBoolArg("-printcreation"))
			printf("GetProofOfWorkReward() : lower=%"PRI64d" upper=%"PRI64d" mid=%"PRI64d"\n", bnLowerBound.getuint64(), bnUpperBound.getuint64(), bnMidValue.getuint64());
		*/
		mid := new(big.Int).Set(bnMidValue)
		sub := new(big.Int).Set(bnSubsidyLimit)
		//if (bnMidValue * bnMidValue * bnMidValue * bnMidValue * bnTargetLimit > bnSubsidyLimit * bnSubsidyLimit * bnSubsidyLimit * bnSubsidyLimit * bnTarget)
		if mid.Mul(mid, mid).Mul(mid, mid).Mul(mid, bnTargetLimit).Cmp(sub.Mul(sub, sub).Mul(sub, sub).Mul(sub, bnTarget)) > 0 {
			bnUpperBound = bnMidValue
		} else {
			bnLowerBound = bnMidValue
		}
	}
	subsidy = bnUpperBound.Int64()
	subsidy = (subsidy / Cent) * Cent
	if subsidy > MaxMintProofOfWork {
		subsidy = MaxMintProofOfWork
	}
	return
}
