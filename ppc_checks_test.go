package btcchain_test

import (
	"bytes"
	"github.com/mably/btcchain"
	"github.com/mably/btcnet"
	"github.com/mably/btcwire"
	"testing"
)

func TestCheckBlockSignature(t *testing.T) {
	if !btcchain.CheckBlockSignature(&Block100000, &btcnet.MainNetParams) {
		t.Error("bad block signature, valid expected")
	}
	var buf bytes.Buffer
	err := Block100000.Serialize(&buf)
	if err != nil {
		t.Error(err)
		return
	}
	rbuf := bytes.NewReader(buf.Bytes())
	block := new(btcwire.MsgBlock)
	err = block.Deserialize(rbuf)
	if err != nil {
		t.Error(err)
		return
	}
	block.Signature[5] ^= 0xff
	if btcchain.CheckBlockSignature(block, &btcnet.MainNetParams) {
		t.Error("good block signature, invalid expected")
	}
}
