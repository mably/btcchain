package btcchain_test

import (
	"testing"

	"compress/bzip2"
	"encoding/binary"
	"github.com/conformal/btclog"
	"github.com/mably/btcchain"
	"github.com/mably/btcdb"
	_ "github.com/mably/btcdb/memdb"
	"github.com/mably/btcnet"
	"github.com/mably/btcutil"
	"github.com/mably/btcwire"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
)

func TestPPCProcessBlocks(t *testing.T) {
	activeNetParams := btcnet.MainNetParams
	dbbc, err := btcdb.CreateDB("memdb")
	genesis := btcutil.NewBlockWithMetas(
		activeNetParams.GenesisBlock, activeNetParams.GenesisMeta)
	_, err = dbbc.InsertBlock(genesis)
	if err != nil {
		t.Errorf("createdb: %v", err)
		return
	}
	defer dbbc.Close()
	btcchain.SetLogWriter(os.Stdout, btclog.InfoLvl.String())
	bc := btcchain.New(dbbc, &activeNetParams, nil)
	//blocks, _ := _loadBlocks(t, "blocks1-1536.bz2")
	blocks, _ := _loadBlocksMax(t, "blk0001.dat", 10000, 1)
	for h, block := range blocks {
		sha, _ := block.Sha()
		isOrphan, err := bc.ProcessBlock(block, btcchain.BFNone)
		if err != nil {
			t.Errorf("processBlock: %v", err)
			return
		}
		if isOrphan {
			t.Errorf("unexpected orphan %d %v", h, sha)
			return
		}
	}
}

func _loadBlocks(t *testing.T, file string) (blocks []*btcutil.Block, err error) {
	return _loadBlocksMax(t, file, math.MaxInt64, 0)
}

func _loadBlocksMax(t *testing.T, file string, maxHeight int64, skip int64) (blocks []*btcutil.Block, err error) {
	testdatafile := filepath.Join("testdata", file)
	var dr io.Reader
	var fi io.ReadCloser
	fi, err = os.Open(testdatafile)
	if err != nil {
		t.Errorf("failed to open file %v, err %v", testdatafile, err)
		return
	}
	if strings.HasSuffix(testdatafile, ".bz2") {
		z := bzip2.NewReader(fi)
		dr = z
	} else {
		dr = fi
	}

	defer func() {
		if err := fi.Close(); err != nil {
			t.Errorf("failed to close file %v %v", testdatafile, err)
		}
	}()

	// Set the first block as the genesis block.
	//genesis := btcutil.NewBlock(btcnet.MainNetParams.GenesisBlock)
	//blocks = append(blocks, genesis)

	var count int64 = 0
	var block *btcutil.Block
	err = nil
	for height := int64(1); height <= maxHeight && err == nil; height++ {
		var rintbuf uint32
		err = binary.Read(dr, binary.LittleEndian, &rintbuf)
		if err == io.EOF {
			// hit end of file at expected offset: no warning
			height--
			err = nil
			break
		}
		if err != nil {
			t.Errorf("failed to load network type, err %v", err)
			break
		}
		if rintbuf != uint32(btcwire.MainNet) {
			t.Errorf("Block doesn't match network: %v expects %v(%v)",
				rintbuf, btcwire.MainNet, uint32(btcwire.MainNet))
			break
		}
		err = binary.Read(dr, binary.LittleEndian, &rintbuf)
		blocklen := rintbuf

		rbytes := make([]byte, blocklen)

		// read block
		dr.Read(rbytes)

		block, err = btcutil.NewBlockFromBytes(rbytes)
		if err != nil {
			t.Errorf("failed to parse block %v", height)
			return
		}

		if count >= skip {
			blocks = append(blocks, block)
		}

		count++
	}
	return
}
