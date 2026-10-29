package state

import (
	"testing"

	ptypes "github.com/Chaintable/pipeline/types"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/triedb"
	"github.com/holiman/uint256"
)

func setApeSharePrice(s *StateDB, price uint64) {
	s.SetState(types.ArbosStateAddress, sharePriceSlot, common.BigToHash(new(uint256.Int).SetUint64(price).ToBig()))
}

const apeTestSharePrice = 1_000_000_007

var (
	apeAuto      = common.HexToAddress("0xa0")
	apeDisabled  = common.HexToAddress("0xb0")
	apeDelegator = common.HexToAddress("0xc0")
	apeDelegate  = common.HexToAddress("0xd0")
)

// newApeYieldState holds one account per yield mode at a share price that
// leaves a remainder, committed to a fresh database.
func newApeYieldState(t *testing.T) (*StateDB, *CachingDB) {
	t.Helper()
	db := rawdb.NewMemoryDatabase()
	tdb := NewDatabase(triedb.NewDatabase(db, &triedb.Config{Preimages: true}), nil)
	s, err := New(types.EmptyRootHash, tdb)
	if err != nil {
		t.Fatal(err)
	}
	s.SetNonce(types.ArbosStateAddress, 1, 0)
	setApeSharePrice(s, apeTestSharePrice)
	s.SetBalance(apeAuto, uint256.NewInt(5_000_000_123), 0)
	s.SetFlags(apeDisabled, types.YieldDisabled, nil)
	s.SetBalance(apeDisabled, uint256.NewInt(77), 0)
	s.SetFlags(apeDelegator, types.YieldDelegated, &apeDelegate)
	s.SetBalance(apeDelegator, uint256.NewInt(300), 0)
	s.SetBalance(apeDelegate, uint256.NewInt(1_000), 0)
	root, err := s.Commit(0, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if s, err = New(root, tdb); err != nil {
		t.Fatal(err)
	}
	return s, tdb
}

var apeTestBalances = map[common.Address]uint64{
	apeAuto:      5_000_000_123,
	apeDisabled:  77,
	apeDelegator: 300,
	apeDelegate:  1_000,
}

// resolveApeBalance computes a balance from the yield slots of a state diff,
// as an RPC node does.
func resolveApeBalance(slots map[common.Hash]*uint256.Int, addr common.Address, sharePrice uint64) uint64 {
	addrHash := crypto.Keccak256Hash(addr.Bytes())
	value := func(i int) *uint256.Int {
		if v, ok := slots[ptypes.ApeYieldStorageKey(addrHash, i)]; ok {
			return v
		}
		return new(uint256.Int)
	}
	flags := value(0).Bytes32()[0]
	balance := value(1).Clone()
	if flags == types.YieldAutomatic {
		balance.Add(balance, new(uint256.Int).Mul(value(2), uint256.NewInt(sharePrice)))
	}
	return balance.Sub(balance, value(3)).Uint64()
}

// The pipeline decodes Ape accounts with its own copy of the slim RLP layout.
func TestApeSlimAccountMatchesPipeline(t *testing.T) {
	acct := types.NewEmptyStateAccount()
	acct.Nonce, acct.Flags, acct.Delegate = 7, types.YieldDelegated, common.HexToAddress("0xd0")
	acct.Fixed, acct.Shares, acct.Debt = uint256.NewInt(11), uint256.NewInt(13), uint256.NewInt(17)
	acct.Root, acct.CodeHash = common.HexToHash("0x01"), crypto.Keccak256([]byte{0x60})
	for _, a := range []*types.StateAccount{acct, types.NewEmptyStateAccount()} {
		got, err := ptypes.DecodeApeSlimAccount(types.SlimAccountRLP(*a))
		if err != nil {
			t.Fatal(err)
		}
		y := got.Yield()
		if got.Nonce != a.Nonce || got.CodeHashOrEmpty() != common.BytesToHash(a.CodeHash) || y.Flags != a.Flags ||
			!y.Fixed.Eq(a.Fixed) || !y.Shares.Eq(a.Shares) || !y.Debt.Eq(a.Debt) || y.Delegate != a.Delegate {
			t.Fatalf("pipeline decoded %+v, account %+v", got, a)
		}
	}
}

func TestApeDumpResolvesBalanceAndExportsYieldSlots(t *testing.T) {
	s, _ := newApeYieldState(t)
	if got := s.GetBalance(apeAuto).Uint64(); got != apeTestBalances[apeAuto] {
		t.Fatalf("setup: balance %d", got)
	}
	alloc := &Alloc{Accounts: make(map[common.Hash]DumpAccount)}
	if _, err := s.DumpToCollectorStrict(alloc, &DumpConfig{UseStorageKeyHash: true}); err != nil {
		t.Fatal(err)
	}
	for addr, want := range apeTestBalances {
		if got := alloc.Accounts[crypto.Keccak256Hash(addr.Bytes())].Balance; got != uint256.NewInt(want).String() {
			t.Errorf("%s: dumped balance %s, want %d", addr, got, want)
		}
	}

	diff := alloc.ToStorageDiff(true)
	for _, acc := range diff.NewAccounts {
		if !acc.Balance.IsZero() {
			t.Errorf("account %x: state diff balance %s, want 0", acc.Address, acc.Balance)
		}
	}
	slots := make(map[common.Hash]*uint256.Int)
	for _, storage := range diff.StorageDiff {
		if storage.Address != ptypes.ApeYieldAddressHash {
			continue
		}
		for _, pair := range storage.Values {
			if pair.Value.IsZero() {
				t.Errorf("full export writes a zero slot %x", pair.Index)
			}
			slots[pair.Index] = pair.Value
		}
	}
	if len(slots) == 0 {
		t.Fatal("no yield slots in the state diff")
	}
	for addr, want := range apeTestBalances {
		if got := resolveApeBalance(slots, addr, apeTestSharePrice); got != want {
			t.Errorf("%s: resolved balance %d, want %d", addr, got, want)
		}
	}
}

func TestApeCommitHookYieldDiff(t *testing.T) {
	s, _ := newApeYieldState(t)
	var (
		gotAccounts map[common.Hash][]byte
		gotOrigin   map[common.Address][]byte
		gotDestruct map[common.Hash]struct{}
	)
	s.SetOnCommitLogger(func(_, _ common.Hash, destructs map[common.Hash]struct{}, accounts map[common.Hash][]byte, accountsOrigin map[common.Address][]byte, _ map[common.Hash]map[common.Hash][]byte, _ map[common.Address]map[common.Hash][]byte, _ map[common.Hash][]byte) {
		gotDestruct, gotAccounts, gotOrigin = destructs, accounts, accountsOrigin
	})
	// Moving funds into the delegator raises the delegate's debt. The delegate's
	// state object shares Fixed and Debt pointers with its origin, so this also
	// checks that the origin keeps the pre-block values.
	s.AddBalance(apeDelegator, uint256.NewInt(50), 0)
	if _, err := s.Commit(1, false, false); err != nil {
		t.Fatal(err)
	}
	changes, err := ptypes.ApeYieldChangesFromCommit(gotDestruct, gotAccounts, gotOrigin)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[common.Hash]uint64)
	if diff := ptypes.ApeYieldStorageDiff(changes); diff != nil {
		for _, pair := range diff.Values {
			got[pair.Index] = pair.Value.Uint64()
		}
	}
	key := func(addr common.Address, i int) common.Hash {
		return ptypes.ApeYieldStorageKey(crypto.Keccak256Hash(addr.Bytes()), i)
	}
	if got[key(apeDelegator, 1)] != 350 {
		t.Errorf("delegator fixed: %v", got)
	}
	if got[key(apeDelegate, 3)] != 350 {
		t.Errorf("delegate debt not written; origin lost the pre-block value? %v", got)
	}
	if _, ok := got[key(apeAuto, 1)]; ok {
		t.Errorf("untouched account written: %v", got)
	}
}
