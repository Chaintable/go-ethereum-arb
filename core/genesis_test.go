// Copyright 2017 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package core

import (
	"bytes"
	"encoding/json"
	"math/big"
	"reflect"
	"testing"

	ptypes "github.com/Chaintable/pipeline/types"
	"github.com/davecgh/go-spew/spew"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/triedb"
	"github.com/ethereum/go-ethereum/triedb/pathdb"
)

func TestSetupGenesis(t *testing.T) {
	testSetupGenesis(t, rawdb.HashScheme)
	testSetupGenesis(t, rawdb.PathScheme)
}

func testSetupGenesis(t *testing.T, scheme string) {
	var (
		customghash = common.HexToHash("0x89c99d90b79719238d2645c7642f2c9295246e80775b38cfd162b696817fbd50")
		customg     = Genesis{
			Config: &params.ChainConfig{HomesteadBlock: big.NewInt(3), Ethash: &params.EthashConfig{}},
			Alloc: types.GenesisAlloc{
				{1}: {Balance: big.NewInt(1), Storage: map[common.Hash]common.Hash{{1}: {1}}},
			},
		}
		oldcustomg = customg
	)
	oldcustomg.Config = &params.ChainConfig{HomesteadBlock: big.NewInt(2), Ethash: &params.EthashConfig{}}

	tests := []struct {
		name           string
		fn             func(ethdb.Database) (*params.ChainConfig, common.Hash, *params.ConfigCompatError, error)
		wantConfig     *params.ChainConfig
		wantHash       common.Hash
		wantErr        error
		wantCompactErr *params.ConfigCompatError
	}{
		{
			name: "genesis without ChainConfig",
			fn: func(db ethdb.Database) (*params.ChainConfig, common.Hash, *params.ConfigCompatError, error) {
				return SetupGenesisBlock(db, triedb.NewDatabase(db, newDbConfig(scheme)), new(Genesis))
			},
			wantErr: errGenesisNoConfig,
		},
		{
			name: "no block in DB, genesis == nil",
			fn: func(db ethdb.Database) (*params.ChainConfig, common.Hash, *params.ConfigCompatError, error) {
				return SetupGenesisBlock(db, triedb.NewDatabase(db, newDbConfig(scheme)), nil)
			},
			wantHash:   params.MainnetGenesisHash,
			wantConfig: params.MainnetChainConfig,
		},
		{
			name: "mainnet block in DB, genesis == nil",
			fn: func(db ethdb.Database) (*params.ChainConfig, common.Hash, *params.ConfigCompatError, error) {
				DefaultGenesisBlock().MustCommit(db, triedb.NewDatabase(db, newDbConfig(scheme)))
				return SetupGenesisBlock(db, triedb.NewDatabase(db, newDbConfig(scheme)), nil)
			},
			wantHash:   params.MainnetGenesisHash,
			wantConfig: params.MainnetChainConfig,
		},
		{
			name: "custom block in DB, genesis == nil",
			fn: func(db ethdb.Database) (*params.ChainConfig, common.Hash, *params.ConfigCompatError, error) {
				tdb := triedb.NewDatabase(db, newDbConfig(scheme))
				customg.Commit(db, tdb)
				return SetupGenesisBlock(db, tdb, nil)
			},
			wantHash:   customghash,
			wantConfig: customg.Config,
		},
		{
			name: "custom block in DB, genesis == sepolia",
			fn: func(db ethdb.Database) (*params.ChainConfig, common.Hash, *params.ConfigCompatError, error) {
				tdb := triedb.NewDatabase(db, newDbConfig(scheme))
				customg.Commit(db, tdb)
				return SetupGenesisBlock(db, tdb, DefaultSepoliaGenesisBlock())
			},
			wantErr: &GenesisMismatchError{Stored: customghash, New: params.SepoliaGenesisHash},
		},
		{
			name: "custom block in DB, genesis == hoodi",
			fn: func(db ethdb.Database) (*params.ChainConfig, common.Hash, *params.ConfigCompatError, error) {
				tdb := triedb.NewDatabase(db, newDbConfig(scheme))
				customg.Commit(db, tdb)
				return SetupGenesisBlock(db, tdb, DefaultHoodiGenesisBlock())
			},
			wantErr: &GenesisMismatchError{Stored: customghash, New: params.HoodiGenesisHash},
		},
		{
			name: "compatible config in DB",
			fn: func(db ethdb.Database) (*params.ChainConfig, common.Hash, *params.ConfigCompatError, error) {
				tdb := triedb.NewDatabase(db, newDbConfig(scheme))
				oldcustomg.Commit(db, tdb)
				return SetupGenesisBlock(db, tdb, &customg)
			},
			wantHash:   customghash,
			wantConfig: customg.Config,
		},
		{
			name: "incompatible config in DB",
			fn: func(db ethdb.Database) (*params.ChainConfig, common.Hash, *params.ConfigCompatError, error) {
				// Commit the 'old' genesis block with Homestead transition at #2.
				// Advance to block #4, past the homestead transition block of customg.
				tdb := triedb.NewDatabase(db, newDbConfig(scheme))
				oldcustomg.Commit(db, tdb)

				bc, _ := NewBlockChain(db, nil, &oldcustomg, ethash.NewFullFaker(), DefaultConfig().WithStateScheme(scheme))
				defer bc.Stop()

				_, blocks, _ := GenerateChainWithGenesis(&oldcustomg, ethash.NewFaker(), 4, nil)
				bc.InsertChain(blocks)

				// This should return a compatibility error.
				return SetupGenesisBlock(db, tdb, &customg)
			},
			wantHash:   customghash,
			wantConfig: customg.Config,
			wantCompactErr: &params.ConfigCompatError{
				What:          "Homestead fork block",
				StoredBlock:   big.NewInt(2),
				NewBlock:      big.NewInt(3),
				RewindToBlock: 1,
			},
		},
	}

	for _, test := range tests {
		db := rawdb.NewMemoryDatabase()
		config, hash, compatErr, err := test.fn(db)
		// Check the return values.
		if !reflect.DeepEqual(err, test.wantErr) {
			spew := spew.ConfigState{DisablePointerAddresses: true, DisableCapacities: true}
			t.Errorf("%s: returned error %#v, want %#v", test.name, spew.NewFormatter(err), spew.NewFormatter(test.wantErr))
		}
		if !reflect.DeepEqual(compatErr, test.wantCompactErr) {
			spew := spew.ConfigState{DisablePointerAddresses: true, DisableCapacities: true}
			t.Errorf("%s: returned error %#v, want %#v", test.name, spew.NewFormatter(compatErr), spew.NewFormatter(test.wantCompactErr))
		}
		if !reflect.DeepEqual(config, test.wantConfig) {
			t.Errorf("%s:\nreturned %v\nwant     %v", test.name, config, test.wantConfig)
		}
		if hash != test.wantHash {
			t.Errorf("%s: returned hash %s, want %s", test.name, hash.Hex(), test.wantHash.Hex())
		} else if err == nil {
			// Check database content.
			stored := rawdb.ReadBlock(db, test.wantHash, 0)
			if stored.Hash() != test.wantHash {
				t.Errorf("%s: block in DB has hash %s, want %s", test.name, stored.Hash(), test.wantHash)
			}
		}
	}
}

// TestGenesisHashes checks the congruity of default genesis data to
// corresponding hardcoded genesis hash values.
func TestGenesisHashes(t *testing.T) {
	for i, c := range []struct {
		genesis *Genesis
		want    common.Hash
	}{
		{DefaultGenesisBlock(), params.MainnetGenesisHash},
		{DefaultSepoliaGenesisBlock(), params.SepoliaGenesisHash},
		{DefaultHoleskyGenesisBlock(), params.HoleskyGenesisHash},
		{DefaultHoodiGenesisBlock(), params.HoodiGenesisHash},
	} {
		// Test via MustCommit
		db := rawdb.NewMemoryDatabase()
		if have := c.genesis.MustCommit(db, triedb.NewDatabase(db, triedb.HashDefaults)).Hash(); have != c.want {
			t.Errorf("case: %d a), want: %s, got: %s", i, c.want.Hex(), have.Hex())
		}
		// Test via ToBlock
		if have := c.genesis.ToBlock().Hash(); have != c.want {
			t.Errorf("case: %d a), want: %s, got: %s", i, c.want.Hex(), have.Hex())
		}
	}
}

func TestGenesisCommit(t *testing.T) {
	genesis := &Genesis{
		BaseFee: big.NewInt(params.InitialBaseFee),
		Config:  params.TestChainConfig,
		// difficulty is nil
	}

	db := rawdb.NewMemoryDatabase()
	genesisBlock := genesis.MustCommit(db, triedb.NewDatabase(db, triedb.HashDefaults))

	if genesis.Difficulty != nil {
		t.Fatalf("assumption wrong")
	}

	// This value should have been set as default in the ToBlock method.
	if genesisBlock.Difficulty().Cmp(params.GenesisDifficulty) != 0 {
		t.Errorf("assumption wrong: want: %d, got: %v", params.GenesisDifficulty, genesisBlock.Difficulty())
	}
}

func TestReadWriteGenesisAlloc(t *testing.T) {
	var (
		db    = rawdb.NewMemoryDatabase()
		alloc = &types.GenesisAlloc{
			{1}: {Balance: big.NewInt(1), Storage: map[common.Hash]common.Hash{{1}: {1}}},
			{2}: {Balance: big.NewInt(2), Storage: map[common.Hash]common.Hash{{2}: {2}}},
		}
		hash, _ = hashAlloc(alloc, false)
	)
	blob, _ := json.Marshal(alloc)
	rawdb.WriteGenesisStateSpec(db, hash, blob)

	var reload types.GenesisAlloc
	err := reload.UnmarshalJSON(rawdb.ReadGenesisStateSpec(db, hash))
	if err != nil {
		t.Fatalf("Failed to load genesis state %v", err)
	}
	if len(reload) != len(*alloc) {
		t.Fatal("Unexpected genesis allocation")
	}
	for addr, account := range reload {
		want, ok := (*alloc)[addr]
		if !ok {
			t.Fatal("Account is not found")
		}
		if !reflect.DeepEqual(want, account) {
			t.Fatal("Unexpected account")
		}
	}
}

func TestDumpGenesisAllocStrict(t *testing.T) {
	addr := common.HexToAddress("0x1234")
	code := []byte{1, 2, 3}
	alloc := types.GenesisAlloc{
		addr: {
			Balance: big.NewInt(22),
			Code:    code,
			Nonce:   3,
			Storage: map[common.Hash]common.Hash{{1}: {2}},
		},
	}
	db := rawdb.NewMemoryDatabase()
	config := *triedb.HashDefaults
	config.Preimages = true
	trieDB := triedb.NewDatabase(db, &config)
	root, err := flushAlloc(&alloc, trieDB)
	if err != nil {
		t.Fatal(err)
	}
	stateDB, err := state.New(root, state.NewDatabase(trieDB, nil))
	if err != nil {
		t.Fatal(err)
	}
	dump, finalState, err := dumpGenesisAllocStrict(stateDB)
	if err != nil {
		t.Fatal(err)
	}
	account, ok := finalState[addr]
	if !ok {
		t.Fatalf("account %s missing from final state", addr)
	}
	if account.Balance.Cmp(big.NewInt(22)) != 0 || account.Nonce != 3 || !bytes.Equal(account.Code, code) {
		t.Fatalf("unexpected final state account %+v", account)
	}
	if account.Storage != nil {
		t.Fatalf("builder-only final state contains storage: %v", account.Storage)
	}
	diff := dump.ToStorageDiff(true)
	if diff.Hash != root || len(diff.NewAccounts) != 1 || len(diff.NewCodes) != 1 || len(diff.StorageDiff) != 1 || len(diff.StorageDiff[0].Values) != 1 {
		t.Fatalf("unexpected state diff %+v", diff)
	}
}

func TestEmitArbGenesisBlock(t *testing.T) {
	addr := common.HexToAddress("0x1234")
	alloc := types.GenesisAlloc{addr: {Balance: big.NewInt(22)}}
	db := rawdb.NewMemoryDatabase()
	trieDB := triedb.NewDatabase(db, &triedb.Config{Preimages: false})
	root, err := flushAlloc(&alloc, trieDB)
	if err != nil {
		t.Fatal(err)
	}
	stateDB, err := state.New(root, state.NewDatabase(trieDB, nil))
	if err != nil {
		t.Fatal(err)
	}
	block := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(1)})

	t.Run("v1 uses best effort dump", func(t *testing.T) {
		called := false
		hooks := &tracing.Hooks{
			OnArbGenesisBlock: func(gotBlock *types.Block, stateDiff *ptypes.BlockStorageDiff) {
				called = true
				if gotBlock != block || len(stateDiff.NewAccounts) != 1 {
					t.Fatalf("unexpected hook payload block=%v diff=%+v", gotBlock, stateDiff)
				}
			},
		}
		if err := emitArbGenesisBlock(hooks, block, stateDB, true); err != nil {
			t.Fatal(err)
		}
		if !called {
			t.Fatal("v1 hook was not called")
		}
	})

	t.Run("v2 orbit requires address preimages", func(t *testing.T) {
		called := false
		hooks := &tracing.Hooks{
			OnArbGenesisBlockV2: func(*types.Block, types.GenesisAlloc, *ptypes.BlockStorageDiff) {
				called = true
			},
		}
		if err := emitArbGenesisBlock(hooks, block, stateDB, true); err == nil {
			t.Fatal("expected strict dump error")
		}
		if called {
			t.Fatal("v2 hook called after strict dump error")
		}
	})

	t.Run("v2 arbitrum one remains best effort", func(t *testing.T) {
		called := false
		hooks := &tracing.Hooks{
			OnArbGenesisBlockV2: func(gotBlock *types.Block, finalState types.GenesisAlloc, stateDiff *ptypes.BlockStorageDiff) {
				called = true
				if gotBlock != block || finalState != nil || len(stateDiff.NewAccounts) != 1 {
					t.Fatalf("unexpected hook payload block=%v finalState=%v diff=%+v", gotBlock, finalState, stateDiff)
				}
			},
		}
		if err := emitArbGenesisBlock(hooks, block, stateDB, false); err != nil {
			t.Fatal(err)
		}
		if !called {
			t.Fatal("v2 hook was not called")
		}
	})
}

func newDbConfig(scheme string) *triedb.Config {
	if scheme == rawdb.HashScheme {
		return triedb.HashDefaults
	}
	config := *pathdb.Defaults
	config.NoAsyncFlush = true
	return &triedb.Config{PathDB: &config}
}

func TestVerkleGenesisCommit(t *testing.T) {
	var verkleTime uint64 = 0
	verkleConfig := &params.ChainConfig{
		ChainID:                 big.NewInt(1),
		HomesteadBlock:          big.NewInt(0),
		DAOForkBlock:            nil,
		DAOForkSupport:          false,
		EIP150Block:             big.NewInt(0),
		EIP155Block:             big.NewInt(0),
		EIP158Block:             big.NewInt(0),
		ByzantiumBlock:          big.NewInt(0),
		ConstantinopleBlock:     big.NewInt(0),
		PetersburgBlock:         big.NewInt(0),
		IstanbulBlock:           big.NewInt(0),
		MuirGlacierBlock:        big.NewInt(0),
		BerlinBlock:             big.NewInt(0),
		LondonBlock:             big.NewInt(0),
		ArrowGlacierBlock:       big.NewInt(0),
		GrayGlacierBlock:        big.NewInt(0),
		MergeNetsplitBlock:      nil,
		ShanghaiTime:            &verkleTime,
		CancunTime:              &verkleTime,
		PragueTime:              &verkleTime,
		OsakaTime:               &verkleTime,
		VerkleTime:              &verkleTime,
		TerminalTotalDifficulty: big.NewInt(0),
		EnableVerkleAtGenesis:   true,
		Ethash:                  nil,
		Clique:                  nil,
		BlobScheduleConfig: &params.BlobScheduleConfig{
			Cancun: params.DefaultCancunBlobConfig,
			Prague: params.DefaultPragueBlobConfig,
			Osaka:  params.DefaultOsakaBlobConfig,
			Verkle: params.DefaultPragueBlobConfig,
		},
	}

	genesis := &Genesis{
		BaseFee:    big.NewInt(params.InitialBaseFee),
		Config:     verkleConfig,
		Timestamp:  verkleTime,
		Difficulty: big.NewInt(0),
		Alloc: types.GenesisAlloc{
			{1}: {Balance: big.NewInt(1), Storage: map[common.Hash]common.Hash{{1}: {1}}},
		},
	}

	expected := common.FromHex("018d20eebb130b5e2b796465fe36aafab650650729a92435aec071bf2386f080")
	got := genesis.ToBlock().Root().Bytes()
	if !bytes.Equal(got, expected) {
		t.Fatalf("invalid genesis state root, expected %x, got %x", expected, got)
	}

	db := rawdb.NewMemoryDatabase()

	config := *pathdb.Defaults
	config.NoAsyncFlush = true

	triedb := triedb.NewDatabase(db, &triedb.Config{
		IsVerkle: true,
		PathDB:   &config,
	})
	block := genesis.MustCommit(db, triedb)
	if !bytes.Equal(block.Root().Bytes(), expected) {
		t.Fatalf("invalid genesis state root, expected %x, got %x", expected, block.Root())
	}

	// Test that the trie is verkle
	if !triedb.IsVerkle() {
		t.Fatalf("expected trie to be verkle")
	}
	vdb := rawdb.NewTable(db, string(rawdb.VerklePrefix))
	if !rawdb.HasAccountTrieNode(vdb, nil) {
		t.Fatal("could not find node")
	}
}
