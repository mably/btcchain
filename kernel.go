// Copyright (c) 2014-2014 PPCD developers.
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package btcchain

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"
	"sort"

	"github.com/mably/btcscript"
	"github.com/mably/btcutil"
	"github.com/mably/btcwire"
)

const (

	// Modifier interval: time to elapse before new modifier is computed
	// Set to 6-hour for production network and 20-minute for test network
	nModifierInterval      int64 = 6 * 60 * 60
	nModifierIntervalRatio int64 = 3
	StakeTargetSpacing     int64 = 10 * 60           // 10 minutes
	StakeMinAge            int64 = 60 * 60 * 24 * 30 // minimum age for coin age
	StakeMaxAge            int64 = 60 * 60 * 24 * 90 // stake age of full weight
	COIN                   int64 = 1000000           // util.h
	MaxClockDrift          int64 = 2 * 60 * 60       // two hours (main.h)
)

var (
	// Hard checkpoints of stake modifiers to ensure they are deterministic
	mapStakeModifierCheckpoints map[int64]uint32 = map[int64]uint32{
		0:     uint32(0x0e00670b),
		19080: uint32(0xad4e4d29),
		30583: uint32(0xdc7bf136),
		99999: uint32(0xf555cfd2),
	}
)

type blockTimeHash struct {
	time int64
	hash *btcwire.ShaHash
}

type blockTimeHashSorter []blockTimeHash

// Len returns the number of timestamps in the slice.  It is part of the
// sort.Interface implementation.
func (s blockTimeHashSorter) Len() int {
	return len(s)
}

// Swap swaps the timestamps at the passed indices.  It is part of the
// sort.Interface implementation.
func (s blockTimeHashSorter) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}

// Less returns whether the timstamp with index i should sort before the
// timestamp with index j.  It is part of the sort.Interface implementation.
func (s blockTimeHashSorter) Less(i, j int) bool {
	return s[i].time < s[j].time
}

// Get the last stake modifier and its generation time from a given block
func (b *BlockChain) GetLastStakeModifier(pindex *blockNode) (
	nStakeModifier uint64, nModifierTime int64, err error) {

	if pindex == nil {
		err = errors.New("GetLastStakeModifier: nil pindex")
		return
	}

	for pindex.parentHash != nil && !pindex.meta.GeneratedStakeModifier {
		pindex, err = b.getPrevNodeFromNode(pindex)
		if err != nil {
			return
		}
	}
	if !pindex.meta.GeneratedStakeModifier {
		err = errors.New("GetLastStakeModifier: no generation at genesis block")
		return
	}

	nStakeModifier = pindex.meta.StakeModifier
	nModifierTime = pindex.timestamp.Unix()
	return
}

// Get selection interval section (in seconds)
func getStakeModifierSelectionIntervalSection(b *BlockChain, nSection int) int64 {
	//assert (nSection >= 0 && nSection < 64)
	return (nModifierInterval * 63 / (63 + ((63 - int64(nSection)) * (nModifierIntervalRatio - 1))))
}

// Get stake modifier selection interval (in seconds)
func getStakeModifierSelectionInterval(b *BlockChain) int64 {
	var nSelectionInterval int64 = 0
	for nSection := 0; nSection < 64; nSection++ {
		nSelectionInterval += getStakeModifierSelectionIntervalSection(b, nSection)
	}
	return nSelectionInterval
}

// select a block from the candidate blocks in vSortedByTimestamp, excluding
// already selected blocks in vSelectedBlocks, and with timestamp up to
// nSelectionIntervalStop.
func selectBlockFromCandidates(
	b *BlockChain, vSortedByTimestamp []blockTimeHash,
	mapSelectedBlocks map[*btcwire.ShaHash]*blockNode,
	nSelectionIntervalStop int64,
	nStakeModifierPrev uint64) (pindexSelected *blockNode, err error) {

	var hashBest *btcwire.ShaHash = new(btcwire.ShaHash)
	fSelected := false

	for _, item := range vSortedByTimestamp {
		pindex, errLoad := b.loadBlockNode(item.hash)
		if errLoad != nil {
			err = fmt.Errorf("SelectBlockFromCandidates: failed to find block index for candidate block %s", item.hash.String())
			return
		}
		if fSelected && pindex.timestamp.Unix() > nSelectionIntervalStop {
			break
		}
		if _, ok := mapSelectedBlocks[pindex.hash]; ok {
			continue
		}

		// compute the selection hash by hashing its proof-hash and the
		// previous proof-of-stake modifier
		var hashProof *btcwire.ShaHash
		if !pindex.meta.HashProofOfStake.IsEqual(&ZeroSha) { // TODO test null pointer in original code
			hashProof = &pindex.meta.HashProofOfStake
		} else {
			hashProof = pindex.hash
		}

		/* ss << hashProof << nStakeModifierPrev */
		buf := bytes.NewBuffer(make([]byte, 0,
			btcwire.HashSize+btcwire.VarIntSerializeSize(nStakeModifierPrev)))
		_, err = buf.Write(hashProof.Bytes())
		if err != nil {
			return
		}
		err = writeElement(buf, nStakeModifierPrev)
		if err != nil {
			return
		}

		var hashSelection *btcwire.ShaHash = new(btcwire.ShaHash)
		_ = hashSelection.SetBytes(btcwire.DoubleSha256(buf.Bytes()))

		// the selection hash is divided by 2**32 so that proof-of-stake block
		// is always favored over proof-of-work block. this is to preserve
		// the energy efficiency property
		if !pindex.meta.HashProofOfStake.IsEqual(&ZeroSha) { // TODO test null pointer in original code
			tmp := ShaHashToBig(hashSelection)
			//hashSelection >>= 32
			tmp = tmp.Rsh(tmp, 32)
			hashSelection.SetBytes(tmp.Bytes()) // TODO BigEndian conversion?
		}

		var hashSelectionInt = ShaHashToBig(hashSelection)
		var hashBestInt = ShaHashToBig(hashBest)

		if fSelected && hashSelectionInt.Cmp(hashBestInt) == -1 {
			hashBest = hashSelection
			pindexSelected = pindex
		} else if !fSelected {
			fSelected = true
			hashBest = hashSelection
			pindexSelected = pindex
		}
	}
	//if fDebug && GetBoolArg("-printstakemodifier") {
	log.Debugf("SelectBlockFromCandidates: selection hash=%s\n", hashBest.String())
	//}
	return
}

// Stake Modifier (hash modifier of proof-of-stake):
// The purpose of stake modifier is to prevent a txout (coin) owner from
// computing future proof-of-stake generated by this txout at the time
// of transaction confirmation. To meet kernel protocol, the txout
// must hash with a future stake modifier to generate the proof.
// Stake modifier consists of bits each of which is contributed from a
// selected block of a given block group in the past.
// The selection of a block is based on a hash of the block's proof-hash and
// the previous stake modifier.
// Stake modifier is recomputed at a fixed time interval instead of every
// block. This is to make it difficult for an attacker to gain control of
// additional bits in the stake modifier, even after generating a chain of
// blocks.
func (b *BlockChain) ComputeNextStakeModifier(pindexCurrent *btcutil.Block) (
	nStakeModifier uint64, fGeneratedStakeModifier bool, err error) {

	nStakeModifier = 0
	fGeneratedStakeModifier = false

	// Get a block node for the block previous to this one.  Will be nil
	// if this is the genesis block.
	pindexPrev, errPrevNode := b.getPrevNodeFromBlock(pindexCurrent)
	if errPrevNode != nil {
		err = fmt.Errorf("fetching prev node: %v", errPrevNode)
	}
	if pindexPrev == nil {
		fGeneratedStakeModifier = true
		return // genesis block's modifier is 0
	}

	// First find current stake modifier and its generation block time
	// if it's not old enough, return the same stake modifier
	var nModifierTime int64 = 0
	nStakeModifier, nModifierTime, stakeErr := b.GetLastStakeModifier(pindexPrev)
	if stakeErr != nil {
		err = fmt.Errorf("ComputeNextStakeModifier: unable to get last modifier: %v", stakeErr)
		return
	}

	log.Debugf("ComputeNextStakeModifier: prev modifier=%u time=%s epoch=%u\n", nStakeModifier, dateTimeStrFormat(nModifierTime), uint(nModifierTime))

	if (nModifierTime / nModifierInterval) >= (pindexPrev.timestamp.Unix() / nModifierInterval) {
		log.Debugf("ComputeNextStakeModifier: no new interval keep current modifier: pindexPrev nHeight=%d nTime=%u\n", pindexPrev.height, pindexPrev.timestamp.Unix)
		return
	}

	pindexCurrentHeader := pindexCurrent.MsgBlock().Header
	if (nModifierTime / nModifierInterval) >= (pindexCurrentHeader.Timestamp.Unix() / nModifierInterval) {
		// v0.4+ requires current block timestamp also be in a different modifier interval
		if isProtocolV04(b, pindexCurrentHeader.Timestamp.Unix()) {
			log.Debugf("ComputeNextStakeModifier: (v0.4+) no new interval keep current modifier: pindexCurrent nHeight=%d nTime=%u\n", pindexCurrent.Height(), pindexCurrentHeader.Timestamp.Unix())
			return
		} else {
			currentSha, errSha := pindexCurrent.Sha()
			if errSha != nil {
				err = errSha
				return
			}
			log.Debugf("ComputeNextStakeModifier: v0.3 modifier at block %s not meeting v0.4+ protocol: pindexCurrent nHeight=%d nTime=%u\n", currentSha.String(), pindexCurrent.Height(), pindexCurrentHeader.Timestamp.Unix())
		}
	}

	// Sort candidate blocks by timestamp
	// TODO(kac-) ouch
	//var vSortedByTimestamp []blockTimeHash = make([]blockTimeHash, 64*nModifierInterval/StakeTargetSpacing)
	var vSortedByTimestamp []blockTimeHash = make([]blockTimeHash, 0)
	//vSortedByTimestamp.reserve(64 * nModifierInterval / STAKE_TARGET_SPACING)
	var nSelectionInterval int64 = getStakeModifierSelectionInterval(b)
	var nSelectionIntervalStart int64 = (pindexPrev.timestamp.Unix()/nModifierInterval)*nModifierInterval - nSelectionInterval
	var pindex *blockNode = pindexPrev
	for pindex != nil && (pindex.timestamp.Unix() >= nSelectionIntervalStart) {
		vSortedByTimestamp = append(vSortedByTimestamp,
			blockTimeHash{pindex.timestamp.Unix(), pindex.hash})
		pindex, err = b.getPrevNodeFromNode(pindex)
	}

	// TODO needs verification
	//reverse(vSortedByTimestamp.begin(), vSortedByTimestamp.end());
	//sort(vSortedByTimestamp.begin(), vSortedByTimestamp.end());
	sort.Reverse(blockTimeHashSorter(vSortedByTimestamp))
	sort.Sort(blockTimeHashSorter(vSortedByTimestamp))

	// Select 64 blocks from candidate blocks to generate stake modifier
	var nStakeModifierNew uint64 = 0
	var nSelectionIntervalStop int64 = nSelectionIntervalStart
	var mapSelectedBlocks map[*btcwire.ShaHash]*blockNode = make(map[*btcwire.ShaHash]*blockNode)
	for nRound := 0; nRound < minInt(64, len(vSortedByTimestamp)); nRound++ {
		// add an interval section to the current selection round
		nSelectionIntervalStop += getStakeModifierSelectionIntervalSection(b, nRound)
		// select a block from the candidates of current round
		pindex, errSelBlk := selectBlockFromCandidates(b, vSortedByTimestamp,
			mapSelectedBlocks, nSelectionIntervalStop, nStakeModifier)
		if errSelBlk != nil {
			err = fmt.Errorf("ComputeNextStakeModifier: unable to select block at round %d : %v", nRound, errSelBlk)
			return
		}
		// write the entropy bit of the selected block
		nStakeModifierNew |= (uint64(pindex.meta.StakeEntropyBit) << uint64(nRound))
		// add the selected block from candidates to selected list
		mapSelectedBlocks[pindex.hash] = pindex
		//if (fDebug && GetBoolArg("-printstakemodifier")) {
		log.Debugf("ComputeNextStakeModifier: selected round %d stop=%s height=%d bit=%d\n",
			nRound, dateTimeStrFormat(nSelectionIntervalStop),
			pindex.height, pindex.meta.StakeEntropyBit)
		//}
	}

	// Print selection map for visualization of the selected blocks
	/*
		if (fDebug && GetBoolArg("-printstakemodifier")) {
			var nHeightFirstCandidate int64
			if pindex == nil {
				nHeightFirstCandidate = 0
			} else {
				nHeightFirstCandidate = pindex.height + 1
			}
			strSelectionMap := ""
			// '-' indicates proof-of-work blocks not selected
			strSelectionMap.insert(0, pindexPrev.height - nHeightFirstCandidate + 1, '-')
			pindex = pindexPrev
			for pindex != nil && (pindex.height >= nHeightFirstCandidate) {
				// '=' indicates proof-of-stake blocks not selected
				if pindex.hashProofOfStake != nil {
					strSelectionMap.replace(pindex.Height() - nHeightFirstCandidate, 1, "=")
				}
				pindex = pindex.pprev
			}
			for _, item := range mapSelectedBlocks {
				// 'S' indicates selected proof-of-stake blocks
				// 'W' indicates selected proof-of-work blocks
				if IsBlockProofOfStake(item) {
					blockType := "S"
				} else {
					blockType := "W"
				}
				strSelectionMap.replace(item.Height() - nHeightFirstCandidate, 1,  blockType);
			}
			log.Debugf("ComputeNextStakeModifier: selection height [%d, %d] map %s\n", nHeightFirstCandidate, pindexPrev.Height(), strSelectionMap)
		}*/

	log.Debugf("ComputeNextStakeModifier: new modifier=%u time=%s\n",
		nStakeModifierNew, dateTimeStrFormat(pindexPrev.timestamp.Unix()))

	nStakeModifier = nStakeModifierNew
	fGeneratedStakeModifier = true

	return
}

// The stake modifier used to hash for a stake kernel is chosen as the stake
// modifier about a selection interval later than the coin generating the kernel
func (b *BlockChain) GetKernelStakeModifier(
	hashBlockFrom *btcwire.ShaHash, fPrintProofOfStake bool) (
	nStakeModifier uint64, nStakeModifierHeight int32, nStakeModifierTime int64,
	err error) {

	nStakeModifier = 0
	pindexFrom, loadErr := b.loadBlockNode(hashBlockFrom)
	if loadErr != nil {
		err = errors.New("GetKernelStakeModifier() : block not indexed")
		return
	}
	nStakeModifierHeight = int32(pindexFrom.height)
	nStakeModifierTime = pindexFrom.timestamp.Unix()
	var nStakeModifierSelectionInterval int64 = getStakeModifierSelectionInterval(b)
	var pindex *blockNode = pindexFrom
	// loop to find the stake modifier later by a selection interval
	for nStakeModifierTime < (pindexFrom.timestamp.Unix() + nStakeModifierSelectionInterval) {
		if len(pindex.children) == 0 { // reached best block; may happen if node is behind on block chain
			if fPrintProofOfStake || (pindex.timestamp.Unix()+StakeMinAge-nStakeModifierSelectionInterval > getAdjustedTime()) {
				err = fmt.Errorf("GetKernelStakeModifier() : reached best block %s at height %d from block %s",
					pindex.hash.String(), pindex.height, hashBlockFrom.String())
				return
			} else {
				return
			}
		}
		pindex = pindex.children[0]
		if pindex.meta.GeneratedStakeModifier {
			nStakeModifierHeight = int32(pindex.height)
			nStakeModifierTime = pindex.timestamp.Unix()
		}
	}
	nStakeModifier = pindex.meta.StakeModifier
	return
}

// ppcoin kernel protocol
// coinstake must meet hash target according to the protocol:
// kernel (input 0) must meet the formula
//     hash(nStakeModifier + txPrev.block.nTime + txPrev.offset + txPrev.nTime + txPrev.vout.n + nTime) < bnTarget * nCoinDayWeight
// this ensures that the chance of getting a coinstake is proportional to the
// amount of coin age one owns.
// The reason this hash is chosen is the following:
//   nStakeModifier:
//       (v0.3) scrambles computation to make it very difficult to precompute
//              future proof-of-stake at the time of the coin's confirmation
//       (v0.2) nBits (deprecated): encodes all past block timestamps
//   txPrev.block.nTime: prevent nodes from guessing a good timestamp to
//                       generate transaction for future advantage
//   txPrev.offset: offset of txPrev inside block, to reduce the chance of
//                  nodes generating coinstake at the same time
//   txPrev.nTime: reduce the chance of nodes generating coinstake at the same
//                 time
//   txPrev.vout.n: output number of txPrev, to reduce the chance of nodes
//                  generating coinstake at the same time
//   block/tx hash should not be used here as they can be generated in vast
//   quantities so as to generate blocks faster, degrading the system back into
//   a proof-of-work situation.
//
func (b *BlockChain) CheckStakeKernelHash(
	nBits uint32, blockFrom *btcutil.Block, nTxPrevOffset uint32,
	txPrev *btcutil.Tx, prevout *btcwire.OutPoint, nTimeTx int64,
	fPrintProofOfStake bool) (
	hashProofOfStake *btcwire.ShaHash, success bool, err error) {

	success = false

	txMsgPrev := txPrev.MsgTx()
	if nTimeTx < txMsgPrev.Time.Unix() { // Transaction timestamp violation
		err = errors.New("CheckStakeKernelHash() : nTime violation")
		return
	}

	var nTimeBlockFrom int64 = blockFrom.MsgBlock().Header.Timestamp.Unix()
	if nTimeBlockFrom+StakeMinAge > nTimeTx { // Min age requirement
		err = errors.New("CheckStakeKernelHash() : min age violation")
		return
	}

	bnTargetPerCoinDay := CompactToBig(uint32(nBits))

	var nValueIn int64 = txMsgPrev.TxOut[prevout.Index].Value
	// v0.3 protocol kernel hash weight starts from 0 at the 30-day min age
	// this change increases active coins participating the hash and helps
	// to secure the network when proof-of-stake difficulty is low
	var timeDelta int64
	if isProtocolV03(b, int64(nTimeTx)) {
		timeDelta = StakeMinAge
	} else {
		timeDelta = 0
	}
	var nTimeWeight int64 = minInt64(int64(nTimeTx)-txMsgPrev.Time.Unix(), int64(StakeMaxAge)) - timeDelta
	//CBigNum bnCoinDayWeight = CBigNum(nValueIn) * nTimeWeight / COIN / (24 * 60 * 60)
	var bnCoinDayWeight *big.Int = new(big.Int).Mul(
		big.NewInt(nValueIn), big.NewInt(nTimeWeight/COIN/24*60*60))

	// Calculate hash
	buf := bytes.NewBuffer(make([]byte, 0, 1000)) // TODO calculate size

	var nStakeModifier uint64
	var nStakeModifierHeight int32
	var nStakeModifierTime int64
	if isProtocolV03(b, int64(nTimeTx)) { // v0.3 protocol
		var blockSha *btcwire.ShaHash
		blockSha, err = blockFrom.Sha()
		if err != nil {
			return
		}
		nStakeModifier, nStakeModifierHeight, nStakeModifierTime, err =
			b.GetKernelStakeModifier(blockSha, fPrintProofOfStake)
		if err != nil {
			return
		}
		//ss << nStakeModifier;
		err = writeElement(buf, nStakeModifier)
		if err != nil {
			return
		}
	} else { // v0.2 protocol
		//ss << nBits;
		err = writeElement(buf, nBits)
		if err != nil {
			return
		}
	}

	err = writeElement(buf, nTimeBlockFrom)
	if err != nil {
		return
	}
	err = writeElement(buf, nTxPrevOffset)
	if err != nil {
		return
	}
	err = writeElement(buf, txMsgPrev.Time.Unix())
	if err != nil {
		return
	}
	err = writeElement(buf, prevout.Index)
	if err != nil {
		return
	}
	err = writeElement(buf, nTimeTx)
	if err != nil {
		return
	}

	//ss << nTimeBlockFrom << nTxPrevOffset << txPrev.nTime << prevout.n << nTimeTx;

	//hashProofOfStake = Hash(ss.begin(), ss.end());
	_ = hashProofOfStake.SetBytes(btcwire.DoubleSha256(buf.Bytes()))

	if fPrintProofOfStake {
		if isProtocolV03(b, nTimeTx) {
			log.Debugf("CheckStakeKernelHash() : using modifier %u at height=%d timestamp=%s for block from height=%d timestamp=%s\n",
				nStakeModifier, nStakeModifierHeight,
				dateTimeStrFormat(nStakeModifierTime), blockFrom.Height(),
				dateTimeStrFormat(nTimeBlockFrom))
		}
		var ver string
		var modifier uint64
		if isProtocolV03(b, nTimeTx) {
			ver = "0.3"
			modifier = nStakeModifier
		} else {
			ver = "0.2"
			modifier = uint64(nBits)
		}
		log.Debugf("CheckStakeKernelHash() : check protocol=%s modifier=%u nTimeBlockFrom=%u nTxPrevOffset=%u nTimeTxPrev=%u nPrevout=%u nTimeTx=%u hashProof=%s\n",
			ver, modifier, nTimeBlockFrom, nTxPrevOffset, txMsgPrev.Time.Unix(),
			prevout.Index, nTimeTx, hashProofOfStake.String())
	}

	// Now check if proof-of-stake hash meets target protocol
	hashProofOfStakeInt := ShaHashToBig(hashProofOfStake)
	if hashProofOfStakeInt.Cmp(new(big.Int).Mul(bnCoinDayWeight, bnTargetPerCoinDay)) > 0 {
		return
	}

	//if (fDebug && !fPrintProofOfStake) {
	if !fPrintProofOfStake {
		if isProtocolV03(b, nTimeTx) {
			log.Debugf("CheckStakeKernelHash() : using modifier %u at height=%d timestamp=%s for block from height=%d timestamp=%s\n",
				nStakeModifier, nStakeModifierHeight,
				dateTimeStrFormat(nStakeModifierTime), blockFrom.Height(),
				dateTimeStrFormat(nTimeBlockFrom))
		}
		var ver string
		var modifier uint64
		if isProtocolV03(b, nTimeTx) {
			ver = "0.3"
			modifier = nStakeModifier
		} else {
			ver = "0.2"
			modifier = uint64(nBits)
		}
		log.Debugf("CheckStakeKernelHash() : pass protocol=%s modifier=%u nTimeBlockFrom=%u nTxPrevOffset=%u nTimeTxPrev=%u nPrevout=%u nTimeTx=%u hashProof=%s\n",
			ver, modifier, nTimeBlockFrom, nTxPrevOffset, txMsgPrev.Time.Unix(),
			prevout.Index, nTimeTx, hashProofOfStake.String())
	}

	success = true
	return
}

// Check kernel hash target and coinstake signature
func (b *BlockChain) CheckProofOfStake(tx *btcutil.Tx, nBits uint32) (
	hashProofOfStake *btcwire.ShaHash, success bool, err error) {

	success = false

	msgTx := tx.MsgTx()

	if !msgTx.IsCoinStake() {
		err = fmt.Errorf("CheckProofOfStake() : called on non-coinstake %s", tx.Sha().String())
		return
	}

	// Kernel (input 0) must match the stake hash target per coin age (nBits)
	var txin *btcwire.TxIn = msgTx.TxIn[0]

	// First try finding the previous transaction in database
	txStore, err := b.FetchTransactionStore(tx)
	if err != nil {
		return
	}
	var txPrev *btcutil.Tx
	var prevBlockHeight int64
	if txPrevData, ok := txStore[txin.PreviousOutpoint.Hash]; ok {
		txPrev = txPrevData.Tx
		prevBlockHeight = txPrevData.BlockHeight
	} else {
		//return tx.DoS(1, error("CheckProofOfStake() : INFO: read txPrev failed"))  // previous transaction not in main chain, may occur during initial download
		err = fmt.Errorf("CheckProofOfStake() : INFO: read txPrev failed")
		return
	}

	// Verify signature
	errVerif := verifySignature(txStore, txin, tx, 0, true, 0)
	if errVerif != nil {
		//return tx.DoS(100, error("CheckProofOfStake() : VerifySignature failed on coinstake %s", tx.Sha().String()))
		err = fmt.Errorf("CheckProofOfStake() : VerifySignature failed on coinstake %s", tx.Sha().String())
		return
	}

	// Read block header
	// The desired block height is in the main chain, so look it up
	// from the main chain database.
	prevBlockHash, err := b.db.FetchBlockShaByHeight(prevBlockHeight)
	if err != nil {
		err = errors.New("CheckProofOfStake() : read block failed") // unable to read block of previous transaction
		return
	}
	prevBlock, err := b.db.FetchBlockBySha(prevBlockHash)
	if err != nil {
		err = errors.New("CheckProofOfStake() : read block failed") // unable to read block of previous transaction
		return
	}

	fDebug := true
	//nTxPrevOffset uint := txindex.pos.nTxPos - txindex.pos.nBlockPos
	prevBlockTxLoc, _ := prevBlock.TxLoc() // TODO not optimal way
	var nTxPrevOffset uint32 = uint32(prevBlockTxLoc[txPrev.Index()].TxStart)
	hashProofOfStake, success, err = b.CheckStakeKernelHash(
		nBits, prevBlock, nTxPrevOffset, txPrev, &txin.PreviousOutpoint,
		msgTx.Time.Unix(), fDebug)
	if !success {
		//return tx.DoS(1, error("CheckProofOfStake() : INFO: check kernel failed on coinstake %s, hashProof=%s",
		//		tx.Sha().String(), hashProofOfStake.String())) // may occur during initial download or if behind on block chain sync
		err = fmt.Errorf("CheckProofOfStake() : INFO: check kernel failed on coinstake %s, hashProof=%s",
			tx.Sha().String(), hashProofOfStake.String())
		return
	}

	return
}

// Check whether the coinstake timestamp meets protocol
// called from main.cpp
func (b *BlockChain) CheckCoinStakeTimestamp(
	nTimeBlock int64, nTimeTx int64) bool {

	if isProtocolV03(b, nTimeTx) { // v0.3 protocol
		return (nTimeBlock == nTimeTx)
	} else { // v0.2 protocol
		return ((nTimeTx <= nTimeBlock) && (nTimeBlock <= nTimeTx+MaxClockDrift))
	}
}

// Get stake modifier checksum
// called from main.cpp
func (b *BlockChain) GetStakeModifierChecksum(
	pindex *btcutil.Block) (checkSum uint32, err error) {

	//assert (pindex.pprev || pindex.Sha().IsEqual(hashGenesisBlock))
	// Hash previous checksum with flags, hashProofOfStake and nStakeModifier
	buf := bytes.NewBuffer(make([]byte, 0, 1000)) // TODO calculate size
	//CDataStream ss(SER_GETHASH, 0)
	var parent *blockNode
	parent, err = b.getPrevNodeFromBlock(pindex)
	if parent != nil {
		//ss << pindex.pprev.nStakeModifierChecksum
		err = writeElement(
			buf, parent.meta.StakeModifierChecksum)
		if err != nil {
			return
		}
	} else if err != nil {
		return
	}
	meta := pindex.Meta()
	//ss << pindex.nFlags << pindex.hashProofOfStake << pindex.nStakeModifier
	err = writeElement(buf, meta.Flags)
	if err != nil {
		return
	}
	_, err = buf.Write(meta.HashProofOfStake.Bytes())
	if err != nil {
		return
	}
	err = writeElement(buf, meta.StakeModifier)
	if err != nil {
		return
	}

	//uint256 hashChecksum = Hash(ss.begin(), ss.end())
	var hashChecksum *btcwire.ShaHash
	hashChecksum, err = btcwire.NewShaHash(btcwire.DoubleSha256(buf.Bytes()))
	if err != nil {
		return
	}

	//hashChecksum >>= (256 - 32)
	var hashCheckSumInt = ShaHashToBig(hashChecksum)
	//return hashChecksum.Get64()
	checkSum = uint32(hashCheckSumInt.Rsh(hashCheckSumInt, 256-32).Uint64())

	return
}

// Check stake modifier hard checkpoints
// called from (main.cpp)
func (b *BlockChain) CheckStakeModifierCheckpoints(
	nHeight int64, nStakeModifierChecksum uint32) bool {
	if b.netParams.Name == "testnet3" {
		return true // Testnet has no checkpoints
	}
	if checkpoint, ok := mapStakeModifierCheckpoints[nHeight]; ok {
		return nStakeModifierChecksum == checkpoint
	}
	return true
}

func verifySignature(txStore TxStore, txIn *btcwire.TxIn, tx *btcutil.Tx,
	nIn uint32, fValidatePayToScriptHash bool, nHashType int) error {

	// Setup the script validation flags.  Blocks created after the BIP0016
	// activation time need to have the pay-to-script-hash checks enabled.
	var flags btcscript.ScriptFlags
	if fValidatePayToScriptHash {
		flags |= btcscript.ScriptBip16
	}

	txValItems := make([]*txValidateItem, 1, 1)
	txVI := &txValidateItem{
		txInIndex: int(nIn),
		txIn:      txIn,
		tx:        tx,
	}
	txValItems = append(txValItems, txVI)

	validator := newTxValidator(txStore, flags)
	if err := validator.Validate(txValItems); err != nil {
		return err
	}

	return nil
}

// writeElement writes the little endian representation of element to w.
// original method in btcwire/common.go
func writeElement(w io.Writer, element interface{}) error {
	var scratch [8]byte

	// Attempt to write the element based on the concrete type via fast
	// type assertions first.
	switch e := element.(type) {
	case int32:
		b := scratch[0:4]
		binary.LittleEndian.PutUint32(b, uint32(e))
		_, err := w.Write(b)
		if err != nil {
			return err
		}
		return nil

	case uint32:
		b := scratch[0:4]
		binary.LittleEndian.PutUint32(b, e)
		_, err := w.Write(b)
		if err != nil {
			return err
		}
		return nil

	case int64:
		b := scratch[0:8]
		binary.LittleEndian.PutUint64(b, uint64(e))
		_, err := w.Write(b)
		if err != nil {
			return err
		}
		return nil

	case uint64:
		b := scratch[0:8]
		binary.LittleEndian.PutUint64(b, e)
		_, err := w.Write(b)
		if err != nil {
			return err
		}
		return nil

	case bool:
		b := scratch[0:1]
		if e == true {
			b[0] = 0x01
		} else {
			b[0] = 0x00
		}
		_, err := w.Write(b)
		if err != nil {
			return err
		}
		return nil

	// Message header checksum.
	case [4]byte:
		_, err := w.Write(e[:])
		if err != nil {
			return err
		}
		return nil

	// IP address.
	case [16]byte:
		_, err := w.Write(e[:])
		if err != nil {
			return err
		}
		return nil

	case *btcwire.ShaHash:
		_, err := w.Write(e[:])
		if err != nil {
			return err
		}
		return nil
	}

	// Fall back to the slower binary.Write if a fast path was not available
	// above.
	return binary.Write(w, binary.LittleEndian, element)
}
