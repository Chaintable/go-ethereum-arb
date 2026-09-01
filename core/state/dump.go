// Copyright 2014 The go-ethereum Authors
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

package state

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"

	ptypes "github.com/Chaintable/pipeline/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
)

// DumpConfig is a set of options to control what portions of the state will be
// iterated and collected.
type DumpConfig struct {
	SkipCode          bool
	SkipStorage       bool
	OnlyWithAddresses bool
	Start             []byte
	Max               uint64
	UseStorageKeyHash bool
}

// DumpCollector interface which the state trie calls during iteration
type DumpCollector interface {
	// OnRoot is called with the state root
	OnRoot(common.Hash)
	// OnAccount is called once for each account in the trie
	OnAccount(*common.Address, DumpAccount)
}

// DumpAccount represents an account in the state.
type DumpAccount struct {
	Balance     string                 `json:"balance"`
	Nonce       uint64                 `json:"nonce"`
	Root        hexutil.Bytes          `json:"root"`
	CodeHash    hexutil.Bytes          `json:"codeHash"`
	Code        hexutil.Bytes          `json:"code,omitempty"`
	Storage     map[common.Hash]string `json:"storage,omitempty"`
	Address     *common.Address        `json:"address,omitempty"` // Address only present in iterative (line-by-line) mode
	AddressHash hexutil.Bytes          `json:"key,omitempty"`     // If we don't have address, we can output the key
}

// Dump represents the full dump in a collected format, as one large map.
type Dump struct {
	Root     string                 `json:"root"`
	Accounts map[string]DumpAccount `json:"accounts"`
	// Next can be set to represent that this dump is only partial, and Next
	// is where an iterator should be positioned in order to continue the dump.
	Next []byte `json:"next,omitempty"` // nil if no more accounts
}

// OnRoot implements DumpCollector interface
func (d *Dump) OnRoot(root common.Hash) {
	d.Root = fmt.Sprintf("%x", root)
}

// OnAccount implements DumpCollector interface
func (d *Dump) OnAccount(addr *common.Address, account DumpAccount) {
	if addr == nil {
		d.Accounts[fmt.Sprintf("pre(%s)", account.AddressHash)] = account
	}
	if addr != nil {
		d.Accounts[(*addr).String()] = account
	}
}

// iterativeDump is a DumpCollector-implementation which dumps output line-by-line iteratively.
type iterativeDump struct {
	*json.Encoder
}

// OnAccount implements DumpCollector interface
func (d iterativeDump) OnAccount(addr *common.Address, account DumpAccount) {
	dumpAccount := &DumpAccount{
		Balance:     account.Balance,
		Nonce:       account.Nonce,
		Root:        account.Root,
		CodeHash:    account.CodeHash,
		Code:        account.Code,
		Storage:     account.Storage,
		AddressHash: account.AddressHash,
		Address:     addr,
	}
	d.Encode(dumpAccount)
}

// OnRoot implements DumpCollector interface
func (d iterativeDump) OnRoot(root common.Hash) {
	d.Encode(struct {
		Root common.Hash `json:"root"`
	}{root})
}

// DumpToCollector iterates the state according to the given options and inserts
// the items into a collector for aggregation or serialization.
//
// The state iterator is still trie-based and can be converted to snapshot-based
// once the state snapshot is fully integrated into database. TODO(rjl493456442).
func (s *StateDB) DumpToCollector(c DumpCollector, conf *DumpConfig) (nextKey []byte) {
	nextKey, _ = s.dumpToCollector(c, conf, false)
	return nextKey
}

// DumpToCollectorStrict is like DumpToCollector, but returns an error instead
// of producing a partial dump when state data or key preimages are unavailable.
func (s *StateDB) DumpToCollectorStrict(c DumpCollector, conf *DumpConfig) (nextKey []byte, err error) {
	return s.dumpToCollector(c, conf, true)
}

func (s *StateDB) dumpToCollector(c DumpCollector, conf *DumpConfig, strict bool) (nextKey []byte, err error) {
	// Sanitize the input to allow nil configs
	if conf == nil {
		conf = new(DumpConfig)
	}
	var (
		missingPreimages int
		accounts         uint64
		start            = time.Now()
		logged           = time.Now()
	)
	log.Info("Trie dumping started", "root", s.originalRoot)
	c.OnRoot(s.originalRoot)

	tr, err := s.db.OpenTrie(s.originalRoot)
	if err != nil {
		if strict {
			return nil, fmt.Errorf("failed to open state trie: %w", err)
		}
		return nil, nil
	}
	trieIt, err := tr.NodeIterator(conf.Start)
	if err != nil {
		if strict {
			return nil, fmt.Errorf("failed to create state trie iterator: %w", err)
		}
		log.Error("Trie dumping error", "err", err)
		return nil, nil
	}
	it := trie.NewIterator(trieIt)

	for it.Next() {
		var data types.StateAccount
		if err := rlp.DecodeBytes(it.Value, &data); err != nil {
			if strict {
				return nil, fmt.Errorf("failed to decode account %x: %w", it.Key, err)
			}
			panic(err)
		}
		if strict && data.Balance == nil {
			return nil, fmt.Errorf("account %x has nil balance", it.Key)
		}
		var (
			account = DumpAccount{
				Balance:     data.Balance.String(),
				Nonce:       data.Nonce,
				Root:        data.Root[:],
				CodeHash:    data.CodeHash,
				AddressHash: it.Key,
			}
			address   *common.Address
			addr      common.Address
			addrBytes = tr.GetKey(it.Key)
		)
		if addrBytes == nil {
			missingPreimages++
			if strict {
				return nil, fmt.Errorf("missing address preimage for account %x", it.Key)
			}
			if conf.OnlyWithAddresses {
				continue
			}
		} else {
			if strict {
				if len(addrBytes) != common.AddressLength {
					return nil, fmt.Errorf("invalid address preimage length %d for account %x", len(addrBytes), it.Key)
				}
				if hash := crypto.Keccak256Hash(addrBytes); hash != common.BytesToHash(it.Key) {
					return nil, fmt.Errorf("address preimage hash mismatch for account %x", it.Key)
				}
			}
			addr = common.BytesToAddress(addrBytes)
			address = &addr
			account.Address = address
		}
		obj := newObject(s, addr, &data)
		if !conf.SkipCode {
			account.Code = obj.Code()
			if strict {
				if err := s.Error(); err != nil {
					return nil, fmt.Errorf("failed to load code for account %s: %w", addr, err)
				}
				if hash := crypto.Keccak256Hash(account.Code); hash != common.BytesToHash(data.CodeHash) {
					return nil, fmt.Errorf("code hash mismatch for account %s: got %s, want %x", addr, hash, data.CodeHash)
				}
			}
		}
		if !conf.SkipStorage {
			account.Storage = make(map[common.Hash]string)

			storageTr, err := s.db.OpenStorageTrie(s.originalRoot, addr, obj.Root(), tr)
			if err != nil {
				if strict {
					return nil, fmt.Errorf("failed to load storage trie for account %s: %w", addr, err)
				}
				log.Error("Failed to load storage trie", "err", err)
				continue
			}
			trieIt, err := storageTr.NodeIterator(nil)
			if err != nil {
				if strict {
					return nil, fmt.Errorf("failed to create storage trie iterator for account %s: %w", addr, err)
				}
				log.Error("Failed to create trie iterator", "err", err)
				continue
			}
			storageIt := trie.NewIterator(trieIt)
			for storageIt.Next() {
				_, content, _, err := rlp.Split(storageIt.Value)
				if err != nil {
					if strict {
						return nil, fmt.Errorf("failed to decode storage value for account %s: %w", addr, err)
					}
					log.Error("Failed to decode the value returned by iterator", "error", err)
					continue
				}
				if conf.UseStorageKeyHash {
					account.Storage[common.BytesToHash(storageIt.Key)] = common.Bytes2Hex(content)
				} else {
					key := storageTr.GetKey(storageIt.Key)
					if key == nil {
						if strict {
							return nil, fmt.Errorf("missing storage key preimage for account %s", addr)
						}
						continue
					}
					account.Storage[common.BytesToHash(key)] = common.Bytes2Hex(content)
				}
			}
			if strict && storageIt.Err != nil {
				return nil, fmt.Errorf("failed to iterate storage trie for account %s: %w", addr, storageIt.Err)
			}
		}
		c.OnAccount(address, account)
		accounts++
		if time.Since(logged) > 8*time.Second {
			log.Info("Trie dumping in progress", "at", common.Bytes2Hex(it.Key), "accounts", accounts,
				"elapsed", common.PrettyDuration(time.Since(start)))
			logged = time.Now()
		}
		if conf.Max > 0 && accounts >= conf.Max {
			if it.Next() {
				nextKey = it.Key
			}
			break
		}
	}
	if strict && it.Err != nil {
		return nil, fmt.Errorf("failed to iterate state trie: %w", it.Err)
	}
	if missingPreimages > 0 {
		log.Warn("Dump incomplete due to missing preimages", "missing", missingPreimages)
	}
	log.Info("Trie dumping complete", "accounts", accounts,
		"elapsed", common.PrettyDuration(time.Since(start)))

	return nextKey, nil
}

// RawDump returns the state. If the processing is aborted e.g. due to options
// reaching Max, the `Next` key is set on the returned Dump.
func (s *StateDB) RawDump(opts *DumpConfig) Dump {
	dump := &Dump{
		Accounts: make(map[string]DumpAccount),
	}
	dump.Next = s.DumpToCollector(dump, opts)
	return *dump
}

// Dump returns a JSON string representing the entire state as a single json-object
func (s *StateDB) Dump(opts *DumpConfig) []byte {
	dump := s.RawDump(opts)
	json, err := json.MarshalIndent(dump, "", "    ")
	if err != nil {
		log.Error("Error dumping state", "err", err)
	}
	return json
}

// IterativeDump dumps out accounts as json-objects, delimited by linebreaks on stdout
func (s *StateDB) IterativeDump(opts *DumpConfig, output *json.Encoder) {
	s.DumpToCollector(iterativeDump{output}, opts)
}

type Alloc struct {
	Root     common.Hash
	Accounts map[common.Hash]DumpAccount
}

func (a *Alloc) OnRoot(root common.Hash) {
	a.Root = root
}
func (a *Alloc) OnAccount(addr *common.Address, account DumpAccount) {
	if addr == nil {
		a.Accounts[common.BytesToHash(account.AddressHash)] = account
	} else {
		a.Accounts[crypto.Keccak256Hash(addr.Bytes())] = account
	}
}

func (a *Alloc) ToStorageDiff(UseStorageKeyHash bool) *ptypes.BlockStorageDiff {
	diff := &ptypes.BlockStorageDiff{
		Hash:            a.Root,
		ParentHash:      types.EmptyRootHash,
		NewAccounts:     make([]ptypes.NewAccount, 0),
		DeletedAccounts: make([]common.Hash, 0),
		StorageDiff:     make([]ptypes.AccountStorageDiff, 0),
		NewCodes:        make([]ptypes.NewCode, 0),
	}
	for addrHash, acc := range a.Accounts {
		diff.NewAccounts = append(diff.NewAccounts, ptypes.NewAccount{
			Address:  addrHash,
			Balance:  uint256.MustFromDecimal(acc.Balance),
			Nonce:    acc.Nonce,
			CodeHash: crypto.HashData(crypto.NewKeccakState(), acc.Code),
		})
		if len(acc.Code) > 0 {
			diff.NewCodes = append(diff.NewCodes, ptypes.NewCode{
				CodeHash: crypto.HashData(crypto.NewKeccakState(), acc.Code),
				Code:     acc.Code,
			})
		}
		values := make([]ptypes.IndexValuePair, 0)
		for index, storageValue := range acc.Storage {
			v := common.HexToHash(storageValue)
			value := uint256.NewInt(0).SetBytes(v.Bytes())
			var hashedIndex common.Hash
			if UseStorageKeyHash {
				hashedIndex = common.BytesToHash(index[:])
			} else {
				hashedIndex = crypto.Keccak256Hash(index[:])
			}
			values = append(values, ptypes.IndexValuePair{
				Index: hashedIndex,
				Value: value,
			})
		}
		diff.StorageDiff = append(diff.StorageDiff, ptypes.AccountStorageDiff{
			Address: addrHash,
			Values:  values,
		})
	}
	return diff
}
