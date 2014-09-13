// Copyright (c) 2014-2014 PPCD developers.
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package btcchain

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/mably/btcutil"
)

const (
	// Protocol switch time of v0.3 kernel protocol
	nProtocolV03SwitchTime     int64 = 1363800000
	nProtocolV03TestSwitchTime int64 = 1359781000
	// Protocol switch time of v0.4 kernel protocol
	nProtocolV04SwitchTime     int64 = 1399300000
	nProtocolV04TestSwitchTime int64 = 1395700000
	// TxDB upgrade time for v0.4 protocol
	// Note: v0.4 upgrade does not require block chain re-download. However,
	//       user must upgrade before the protocol switch deadline, otherwise
	//       re-download of blockchain is required. The timestamp of upgrade
	//       is recorded in transaction database to alert user of the requirement.
	nProtocolV04UpgradeTime int64 = 0
)

// AddToBlockIndex processes all ppcoin specific block meta data
func (b *BlockChain) AddToBlockIndex(block *btcutil.Block) (err error) {

	defer timeTrack(now(), fmt.Sprintf("AddToBlockIndex(%v)", slice(block.Sha())[0]))

	meta := block.Meta()

	// ppcoin: compute chain trust score TODO needed here?
	blockTrust := getBlockTrust(block)
	prevNode, err := b.getPrevNodeFromBlock(block)
	if err != nil || prevNode == nil {
		meta.ChainTrust.Set(blockTrust)
	} else {
		meta.ChainTrust.Add(&prevNode.meta.ChainTrust, blockTrust)
	}

	// ppcoin: compute stake entropy bit for stake modifier
	stakeEntropyBit, err := getStakeEntropyBit(b, block)
	if err != nil {
		err = errors.New("AddToBlockIndex() : GetStakeEntropyBit() failed")
		return
	}
	SetStakeEntropyBit(meta, stakeEntropyBit)

	// ppcoin: compute stake modifier
	var nStakeModifier uint64 = 0
	var fGeneratedStakeModifier bool = false
	nStakeModifier, fGeneratedStakeModifier, err =
		b.ComputeNextStakeModifier(block)
	if err != nil {
		err = fmt.Errorf("AddToBlockIndex() : ComputeNextStakeModifier() failed %v", err)
		return
	}

	meta.StakeModifier = nStakeModifier
	SetGeneratedStakeModifier(meta, fGeneratedStakeModifier)

	meta.StakeModifierChecksum, err = b.GetStakeModifierChecksum(block)

	/*bytes := make([]byte, 8)
	binary.BigEndian.PutUint64(bytes, uint64(meta.StakeModifier))
	metaModifHex := hex.EncodeToString(bytes)
	bytesCS := make([]byte, 4)
	binary.BigEndian.PutUint32(bytesCS, uint32(meta.StakeModifierChecksum))
	metaModifHexCS := hex.EncodeToString(bytesCS)
	log.Tracef("Height = %v, Modifier = %v, CheckSum = %v", block.Height(), metaModifHex, metaModifHexCS)*/

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

	log.Debugf("AddToBlockIndex() : height=%d, modifier=%v, checksum=%v",
		block.Height(), getStakeModifierHexString(meta.StakeModifier),
		int32(meta.StakeModifierChecksum))

	return nil
}

func getBlockTrust(block *btcutil.Block) *big.Int {
	return CalcTrust(block.MsgBlock().Header.Bits, block.MsgBlock().IsProofOfStake())
}

// ppcoin: entropy bit for stake modifier if chosen by modifier
func getStakeEntropyBit(b *BlockChain, block *btcutil.Block) (uint32, error) {

	defer timeTrack(now(), fmt.Sprintf("getStakeEntropyBit(%v)", slice(block.Sha())[0]))

	var nEntropyBit uint32 = 0
	hash, _ := block.Sha()

	if isProtocolV04(b, int64(block.MsgBlock().Header.Timestamp.Unix())) {

		nEntropyBit = uint32((ShaHashToBig(hash).Int64()) & 1) // last bit of block hash

		//if (fDebug && GetBoolArg("-printstakemodifier"))
		//    printf("GetStakeEntropyBit(v0.4+): nTime=%d hashBlock=%s entropybit=%d\n", nTime, GetHash().ToString().c_str(), nEntropyBit);

	} else {

		// old protocol for entropy bit pre v0.4
		hashSigBytes := btcutil.Hash160(block.MsgBlock().Signature)
		// to big-endian
		blen := len(hashSigBytes)
		for i := 0; i < blen/2; i++ {
			hashSigBytes[i], hashSigBytes[blen-1-i] = hashSigBytes[blen-1-i], hashSigBytes[i]
		}
		//if (fDebug && GetBoolArg("-printstakemodifier"))
		//    printf("GetStakeEntropyBit(v0.3): nTime=%d hashSig=%s", nTime, hashSig.ToString().c_str());
		hashSig := new(big.Int).SetBytes(hashSigBytes)
		hashSig.Rsh(hashSig, 159) // take the first bit of the hash
		nEntropyBit = uint32(hashSig.Int64())

		//if (fDebug && GetBoolArg("-printstakemodifier"))
		//    printf(" entropybit=%d\n", nEntropyBit)
	}

	log.Tracef("Entropy bit = %d for block %v", nEntropyBit, hash)

	return nEntropyBit, nil
}

func getStakeModifierHexString(stakeModifier uint64) string {
	bytes := make([]byte, 8)
	binary.BigEndian.PutUint64(bytes, stakeModifier)
	return hex.EncodeToString(bytes)
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
	return time.Now().Unix() // TODO differs from ppcoin, probably already exists in btcd
}

func now() time.Time {
    return btcutil.Now()
}

func timeTrack(start time.Time, name string) {
    btcutil.TimeTrack(log, start, name)
}

func slice(args ...interface{}) []interface{} {
    return btcutil.Slice(args)
}