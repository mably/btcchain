// Copyright (c) 2014-2014 PPCD developers.
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package btcchain

import (
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/mably/btcutil"
)

const(
	// Protocol switch time of v0.3 kernel protocol
	nProtocolV03SwitchTime      int64   = 1363800000
	nProtocolV03TestSwitchTime  int64   = 1359781000
	// Protocol switch time of v0.4 kernel protocol
	nProtocolV04SwitchTime      int64   = 1399300000
	nProtocolV04TestSwitchTime  int64   = 1395700000
	// TxDB upgrade time for v0.4 protocol
	// Note: v0.4 upgrade does not require block chain re-download. However,
	//       user must upgrade before the protocol switch deadline, otherwise
	//       re-download of blockchain is required. The timestamp of upgrade
	//       is recorded in transaction database to alert user of the requirement.
	nProtocolV04UpgradeTime     int64   = 0
)

// AddToBlockIndex processes all ppcoin specific block meta data
func (b *BlockChain) AddToBlockIndex(block *btcutil.Block) (err error) {

	meta := block.Meta()

	// ppcoin: compute chain trust score
	blockTrust := getBlockTrust(block)
	if err != nil {
		meta.ChainTrust = *blockTrust
	} else {
		prevNode, _ := b.getPrevNodeFromBlock(block)
		meta.ChainTrust =
			*new(big.Int).Add(&prevNode.meta.ChainTrust, blockTrust)
	}

	// ppcoin: compute stake entropy bit for stake modifier
	meta.StakeEntropyBit, err = getStakeEntropyBit(b, block)
	if err != nil {
		err = errors.New("AddToBlockIndex() : GetStakeEntropyBit() failed")
		return
	}

	// ppcoin: compute stake modifier
	var nStakeModifier uint64 = 0
	var fGeneratedStakeModifier bool = false
	nStakeModifier, fGeneratedStakeModifier, err =
		b.ComputeNextStakeModifier(block)
	if err != nil {
		err = errors.New("AddToBlockIndex() : ComputeNextStakeModifier() failed")
		return
	}

	meta.StakeModifier = nStakeModifier
	meta.GeneratedStakeModifier = fGeneratedStakeModifier
	meta.StakeModifierChecksum, err = b.GetStakeModifierChecksum(block)
	if err != nil {
		err = errors.New("AddToBlockIndex() : GetStakeModifierChecksum() failed")
		return
	}
	if !b.CheckStakeModifierCheckpoints(block.Height(), meta.StakeModifierChecksum) {
		err = fmt.Errorf("AddToBlockIndex() : Rejected by stake modifier checkpoint height=%d, modifier=%d", block.Height(), meta.StakeModifier)
		return
	}

	/* kac-temp-off
	if block.IsProofOfStake() {
		setStakeSeen.insert(make_pair(block.prevoutStake, block.nStakeTime)) // TODO later to prevent block flood
	}
	*/

	// New best
	if meta.ChainTrust.Cmp(&b.bestChain.meta.ChainTrust) > 0 {
		/*if !SetBestChain(b, block) {
			return false
		}*/
	}

	return nil
}

func getBlockTrust(block *btcutil.Block) *big.Int {
	nBits := block.MsgBlock().Header.Bits
	bnTarget := CompactToBig(nBits)
	tmp := new(big.Int)
	if bnTarget.Cmp(big.NewInt(0)) <= 0 {
		return tmp.SetInt64(0)
	}
	if block.MsgBlock().IsProofOfStake() {
		return tmp.SetInt64(1).Lsh(tmp, 256).Div(tmp, bnTarget.Add(bnTarget, big.NewInt(1)))
	} else {
		return tmp.SetInt64(1)
	}
}

// ppcoin: entropy bit for stake modifier if chosen by modifier
func getStakeEntropyBit(b *BlockChain, block *btcutil.Block) (uint32, error) {

	var nEntropyBit uint32 = 0

	if isProtocolV04(b, int64(block.MsgBlock().Header.Timestamp.Unix())) {
		hash, _ := block.Sha()
		nEntropyBit = uint32((ShaHashToBig(hash).Int64()) & 1) // last bit of block hash

		//if (fDebug && GetBoolArg("-printstakemodifier"))
		//    printf("GetStakeEntropyBit(v0.4+): nTime=%u hashBlock=%s entropybit=%d\n", nTime, GetHash().ToString().c_str(), nEntropyBit);

	} else {

		// old protocol for entropy bit pre v0.4
		hashSigBytes := btcutil.Hash160(block.MsgBlock().Signature)
		// to big-endian
		blen := len(hashSigBytes)
		for i := 0; i < blen/2; i++ {
			hashSigBytes[i], hashSigBytes[blen-1-i] = hashSigBytes[blen-1-i], hashSigBytes[i]
		}
		//if (fDebug && GetBoolArg("-printstakemodifier"))
		//    printf("GetStakeEntropyBit(v0.3): nTime=%u hashSig=%s", nTime, hashSig.ToString().c_str());
		hashSig := new(big.Int).SetBytes(hashSigBytes)
		hashSig.Rsh(hashSig, 159) // take the first bit of the hash
		nEntropyBit = uint32(hashSig.Int64())

		//if (fDebug && GetBoolArg("-printstakemodifier"))
		//    printf(" entropybit=%d\n", nEntropyBit)
	}

	return nEntropyBit, nil
}

// Whether the given coinstake is subject to new v0.3 protocol
func isProtocolV03(b *BlockChain, nTimeCoinStake int64) bool {
	var switchTime int64
	if b.netParams.Name == "testnet3" {
		switchTime = nProtocolV03TestSwitchTime
	} else {
		switchTime = nProtocolV03SwitchTime
	}
	return nTimeCoinStake >= switchTime
}

// Whether the given block is subject to new v0.4 protocol
func isProtocolV04(b *BlockChain, nTimeBlock int64) bool {
	var v04SwitchTime int64
	if b.netParams.Name == "testnet3" {
		v04SwitchTime = nProtocolV04TestSwitchTime
	} else {
		v04SwitchTime = nProtocolV04SwitchTime
	}
	return nTimeBlock >= v04SwitchTime
}

// dateTimeStrFormat displays time in RFC3339 format
func dateTimeStrFormat(t int64) string {
	return time.Unix(t, 0).UTC().Format(time.RFC3339)
}

func minInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

func minInt64(a int64, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func getAdjustedTime() int64 {
	return time.Now().Unix() // TODO differs from peercoin core, probably exists in btcd
}
