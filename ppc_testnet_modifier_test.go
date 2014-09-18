package btcchain_test

import (
	"encoding/hex"
	"github.com/conformal/btclog"
	"github.com/mably/btcchain"
	"github.com/mably/btcdb"
	"github.com/mably/btcnet"
	"math/big"
	"os"
	"path/filepath"
	"testing"
)

func TestBuggyModifierGeneration(t *testing.T) {
	btcchain.SetLogWriter(os.Stdout, btclog.DebugLvl.String())

	db, err := btcdb.OpenDB("leveldb", filepath.Join("testdata", "ldb_testnet_48973"))
	if err != nil {
		t.Error(err)
	}
	defer db.Close()

	c := btcchain.New(db, &btcnet.TestNet3Params, nil)
	sha, err := db.FetchBlockShaByHeight(int64(47940))
	if err != nil {
		t.Error(err)
		return
	}
	block, err := db.FetchBlockBySha(sha)
	if err != nil {
		t.Error(err)
		return
	}
	modifier, generated, err := c.ComputeNextStakeModifier(block)
	if err != nil {
		t.Error(err)
		return
	}
	if !generated {
		t.Error("expected generation")
		return
	}
	bytes, _ := hex.DecodeString("2265c450debfb4dd")
	correctModifier := new(big.Int).SetBytes(bytes).Uint64()
	if modifier != correctModifier {
		t.Errorf("bad modifier, have %v want %v",
			hex.EncodeToString(new(big.Int).SetUint64(modifier).Bytes()),
			hex.EncodeToString(new(big.Int).SetUint64(correctModifier).Bytes()))
	}
}
