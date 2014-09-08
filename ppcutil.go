// Copyright (c) 2014-2014 PPCD developers.
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package btcchain

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/mably/btcutil"
)

func (b *BlockChain) AddToBlockIndex(block *btcutil.Block) (err error) {

    /*hash := block.Sha()

    // Check for duplicate
    if mapBlockIndex.count(hash) {
        err = errors.New("AddToBlockIndex() : %s already exists", hash.ToString().substr(0,20).c_str())
        return
    }

    // Construct new block index object
    CBlockIndex* block = new CBlockIndex(nFile, nBlockPos, *this)
    if !block {
        err = errors.New("AddToBlockIndex() : new CBlockIndex failed")
        return
    }

    block.phashBlock = &hash
    map<uint256, CBlockIndex*>::iterator miPrev = mapBlockIndex.find(hashPrevBlock)
    if miPrev != mapBlockIndex.end() {
        block.pprev = (*miPrev).second
        block.nHeight = block.pprev.nHeight + 1
    }*/

    // ppcoin: compute chain trust score
    var bnChainTrust *big.Int
   	blockTrust := getBlockTrust(block);
    prevNode, err := b.getPrevNodeFromBlock(block)
    if err != nil {
    	bnChainTrust = blockTrust
    } else {
    	bnChainTrust = new(big.Int).Add(prevNode.chainTrust, blockTrust)
    }
	//block.SetChainTrust(bnChainTrust)

    // ppcoin: compute stake entropy bit for stake modifier
    if !block.SetStakeEntropyBit(GetStakeEntropyBit()) {
        err = errors.New("AddToBlockIndex() : SetStakeEntropyBit() failed")
        return
    }

    // Not needed: done in checkConnectBlock (validate.go)
    // ppcoin: record proof-of-stake hash value
    /*if block.IsProofOfStake() {
        if !mapProofOfStake.count(hash) {
            err = errors.New("AddToBlockIndex() : hashProofOfStake not found in map")
            return
        }
        block.hashProofOfStake = mapProofOfStake[hash]
    }*/

    // ppcoin: compute stake modifier
    var nStakeModifier uint64 = 0
    var fGeneratedStakeModifier bool = false
    nStakeModifier, fGeneratedStakeModifier, err =
    	b.ComputeNextStakeModifier(block)
    if err != nil {
        err = errors.New("AddToBlockIndex() : ComputeNextStakeModifier() failed")
        return
    }
    block.SetStakeModifier(nStakeModifier, fGeneratedStakeModifier)
    block.nStakeModifierChecksum = GetStakeModifierChecksum(block)
    if (!CheckStakeModifierCheckpoints(block.nHeight, block.nStakeModifierChecksum)) {
        err = fmt.Errorf("AddToBlockIndex() : Rejected by stake modifier checkpoint height=%d, modifier=%d", block.Height(), nStakeModifier)
        return
    }

    // Add to mapBlockIndex
    /*map<uint256, CBlockIndex*>::iterator mi =
    	mapBlockIndex.insert(make_pair(hash, block)).first
    */
    if block.IsProofOfStake() {
        setStakeSeen.insert(make_pair(block.prevoutStake, block.nStakeTime))
    }
    /*block.phashBlock = &((*mi).first)

    // Write to disk block index
    CTxDB txdb
    if (!txdb.TxnBegin())
        return false
    txdb.WriteBlockIndex(CDiskBlockIndex(block))
    if (!txdb.TxnCommit())
        return false
    */

    // New best
    if block.bnChainTrust > bnBestChainTrust {
        if !SetBestChain(txdb, block) {
            return false
        }
    }

    //txdb.Close()

    return nil
}

func getBlockTrust(block *btcutil.Block) *big.Int {
	nBits := block.MsgBlock().Header.Bits
	bnTarget := CompactToBit(nBits)
    if bnTarget <= 0 {
		return 0
	}
    if block.MsgBlock().IsProofOfStake() {
    	return (CBigNum(1)<<256) / (bnTarget+1)
    } else {
    	return 1;
    }
}

// ppcoin: entropy bit for stake modifier if chosen by modifier
func getStakeEntropyBit(b *BlockChain, block *btcutil.Block) uint32 {

    var nEntropyBit uint32 = 0

    if IsProtocolV04(b, nTime) {

        nEntropyBit = ((GetHash().Get64()) & 1llu) // last bit of block hash

        //if (fDebug && GetBoolArg("-printstakemodifier"))
        //    printf("GetStakeEntropyBit(v0.4+): nTime=%u hashBlock=%s entropybit=%d\n", nTime, GetHash().ToString().c_str(), nEntropyBit);

    } else {

        // old protocol for entropy bit pre v0.4
        uint160 hashSig = Hash160(vchBlockSig)
        //if (fDebug && GetBoolArg("-printstakemodifier"))
        //    printf("GetStakeEntropyBit(v0.3): nTime=%u hashSig=%s", nTime, hashSig.ToString().c_str());
        hashSig >>= 159 // take the first bit of the hash
        nEntropyBit = hashSig.Get64()

        //if (fDebug && GetBoolArg("-printstakemodifier"))
        //    printf(" entropybit=%d\n", nEntropyBit)
    }

    return nEntropyBit
}

