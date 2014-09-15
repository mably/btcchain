// Copyright (c) 2013-2014 Conformal Systems LLC.
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package btcchain_test

import (
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/mably/btcchain"
	"github.com/mably/btcnet"
	"github.com/mably/btcutil"
	"github.com/mably/btcwire"
)

// TestCheckConnectBlock tests the CheckConnectBlock function to ensure it
// fails
func TestCheckConnectBlock(t *testing.T) {
	// Create a new database and chain instance to run tests against.
	chain, teardownFunc, err := chainSetup("checkconnectblock")
	if err != nil {
		t.Errorf("Failed to setup chain instance: %v", err)
		return
	}
	defer teardownFunc()

	err = chain.GenerateInitialIndex()
	if err != nil {
		t.Errorf("GenerateInitialIndex: %v", err)
	}

	// The genesis block should fail to connect since it's already
	// inserted.
	genesisBlock := btcnet.MainNetParams.GenesisBlock
	err = chain.CheckConnectBlock(btcutil.NewBlock(genesisBlock))
	if err == nil {
		t.Errorf("CheckConnectBlock: Did not received expected error")
	}
}

func TestCheckBlockSanity(t *testing.T) {
	powLimit := btcnet.MainNetParams.PowLimit
	block := btcutil.NewBlock(&Block100000)
	err := btcchain.CheckBlockSanity(block, powLimit)
	if err != nil {
		t.Errorf("CheckBlockSanity: %v", err)
	}

	// Ensure a block that has a timestamp with a precision higher than one
	// second fails.
	timestamp := block.MsgBlock().Header.Timestamp
	block.MsgBlock().Header.Timestamp = timestamp.Add(time.Nanosecond)
	err = btcchain.CheckBlockSanity(block, powLimit)
	if err == nil {
		t.Errorf("CheckBlockSanity: error is nil when it shouldn't be")
	}
}

// TestCheckSerializedHeight tests the checkSerializedHeight function with
// various serialized heights and also does negative tests to ensure errors
// and handled properly.
func TestCheckSerializedHeight(t *testing.T) {
	// Create an empty coinbase template to be used in the tests below.
	coinbaseOutpoint := btcwire.NewOutPoint(&btcwire.ShaHash{}, math.MaxUint32)
	coinbaseTx := btcwire.NewMsgTx()
	coinbaseTx.Version = 2
	coinbaseTx.AddTxIn(btcwire.NewTxIn(coinbaseOutpoint, nil))

	// Expected rule errors.
	missingHeightError := btcchain.RuleError{
		ErrorCode: btcchain.ErrMissingCoinbaseHeight,
	}
	badHeightError := btcchain.RuleError{
		ErrorCode: btcchain.ErrBadCoinbaseHeight,
	}

	tests := []struct {
		sigScript  []byte // Serialized data
		wantHeight int64  // Expected height
		err        error  // Expected error type
	}{
		// No serialized height length.
		{[]byte{}, 0, missingHeightError},
		// Serialized height length with no height bytes.
		{[]byte{0x02}, 0, missingHeightError},
		// Serialized height length with too few height bytes.
		{[]byte{0x02, 0x4a}, 0, missingHeightError},
		// Serialized height that needs 2 bytes to encode.
		{[]byte{0x02, 0x4a, 0x52}, 21066, nil},
		// Serialized height that needs 2 bytes to encode, but backwards
		// endianness.
		{[]byte{0x02, 0x4a, 0x52}, 19026, badHeightError},
		// Serialized height that needs 3 bytes to encode.
		{[]byte{0x03, 0x40, 0x0d, 0x03}, 200000, nil},
		// Serialized height that needs 3 bytes to encode, but backwards
		// endianness.
		{[]byte{0x03, 0x40, 0x0d, 0x03}, 1074594560, badHeightError},
	}

	t.Logf("Running %d tests", len(tests))
	for i, test := range tests {
		msgTx := coinbaseTx.Copy()
		msgTx.TxIn[0].SignatureScript = test.sigScript
		tx := btcutil.NewTx(msgTx)

		err := btcchain.TstCheckSerializedHeight(tx, test.wantHeight)
		if reflect.TypeOf(err) != reflect.TypeOf(test.err) {
			t.Errorf("checkSerializedHeight #%d wrong error type "+
				"got: %v <%T>, want: %T", i, err, err, test.err)
			continue
		}

		if rerr, ok := err.(btcchain.RuleError); ok {
			trerr := test.err.(btcchain.RuleError)
			if rerr.ErrorCode != trerr.ErrorCode {
				t.Errorf("checkSerializedHeight #%d wrong "+
					"error code got: %v, want: %v", i,
					rerr.ErrorCode, trerr.ErrorCode)
				continue
			}
		}

	}
}

// Block100000 defines block 100,000 of the block chain.  It is used to
// test Block operations.
// TODO(kac-) actually it's block #99998(PoW) because block 100k is PoS
var Block100000 = btcwire.MsgBlock{
	Header: btcwire.BlockHeader{
		Version: 1,
		PrevBlock: btcwire.ShaHash([32]byte{ // Make go vet happy.
			0x19, 0xa7, 0xd8, 0x3e, 0x44, 0x13, 0x25, 0x10,
			0xb8, 0x91, 0x79, 0xda, 0xe5, 0x28, 0x89, 0x16,
			0x8b, 0x73, 0x03, 0x78, 0xc7, 0xb4, 0xbc, 0xf2,
			0x5c, 0xa8, 0x71, 0xa7, 0x9b, 0x78, 0xd8, 0x2e,
		}), // 2ed8789ba771a85cf2bcb4c77803738b168928e5da7991b8102513443ed8a719
		MerkleRoot: btcwire.ShaHash([32]byte{ // Make go vet happy.
			0xf3, 0x90, 0x05, 0x32, 0xa4, 0xfa, 0x02, 0x70,
			0x9c, 0xf9, 0x35, 0x11, 0x26, 0xf6, 0xf9, 0x07,
			0x2d, 0x9f, 0x97, 0xaf, 0x19, 0xb3, 0xa2, 0x55,
			0xc3, 0xc1, 0x4f, 0x1b, 0xfe, 0x23, 0xb5, 0xa7,
		}), // a7b523fe1b4fc1c355a2b319af979f2d07f9f6261135f99c7002faa4320590f3
		Timestamp: time.Unix(1394105276, 0), // 2014-03-06 12:27:56 +0100 CET
		Bits:      422443218,                // 453281356
		Nonce:     3272548896,               // 274148111
	},
	Transactions: []*btcwire.MsgTx{
		{
			Version: 1,
			Time:    time.Unix(1394105276, 0), // 2014-03-06 12:27:56 +0100 CET
			TxIn: []*btcwire.TxIn{
				{
					PreviousOutpoint: btcwire.OutPoint{
						Hash:  btcwire.ShaHash{},
						Index: 0xffffffff,
					},
					SignatureScript: []byte{
						0x03, 0x9e, 0x86, 0x01, 0x06, 0x2f, 0x50, 0x32,
						0x53, 0x48, 0x2f, 0x04, 0xbd, 0x5b, 0x18, 0x53,
						0x08, 0xf8, 0x02, 0xa4, 0xa5, 0xaf, 0x0a, 0x00,
						0x00, 0x0d, 0x2f, 0x73, 0x74, 0x72, 0x61, 0x74,
						0x75, 0x6d, 0x50, 0x6f, 0x6f, 0x6c, 0x2f,
					},
					Sequence: 0,
				},
			},
			TxOut: []*btcwire.TxOut{
				{
					Value: 101700000,
					PkScript: []byte{
						0x21, 0x02, 0xac, 0xf8, 0x2e, 0xcd, 0xa9, 0xec,
						0xab, 0x97, 0x5f, 0x75, 0x91, 0x7a, 0xb6, 0xb9,
						0x44, 0x8f, 0x63, 0xbc, 0x28, 0x6a, 0x2d, 0x89,
						0xfa, 0xa5, 0xae, 0xf2, 0x11, 0x82, 0xb0, 0x34,
						0xbd, 0x48, 0xac,
					},
				},
			},
			LockTime: 0,
		},
	},
	Signature: []byte{
		0x30, 0x45, 0x02, 0x21, 0x00, 0xf2, 0xac, 0x13,
		0xf1, 0x42, 0xdd, 0x82, 0x5c, 0x27, 0x70, 0x5b,
		0x09, 0xfe, 0x75, 0x6a, 0x68, 0x4b, 0x1c, 0xec,
		0x91, 0x6b, 0x24, 0x90, 0x59, 0x87, 0x7d, 0xcc,
		0xa0, 0x43, 0xe2, 0x6b, 0x53, 0x02, 0x20, 0x76,
		0x44, 0xac, 0x74, 0x92, 0xfe, 0xfb, 0x28, 0x0c,
		0x0c, 0xc0, 0x77, 0x61, 0x91, 0xaa, 0x9f, 0xc2,
		0x69, 0xb6, 0xe4, 0x9b, 0x6b, 0xbd, 0x32, 0xfe,
		0xaf, 0x59, 0xa1, 0xd9, 0x65, 0x71, 0x90,
	},
}
