package state

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
)

const recentWasmsRetain = uint16(8)

func newTestState(t *testing.T) *StateDB {
	t.Helper()
	state, err := New(types.EmptyRootHash, NewDatabaseForTesting())
	if err != nil {
		t.Fatalf("failed to create state: %v", err)
	}
	return state
}

// Copy only carries the block-level cache, so entries promoted by Finalise must
// survive a copy while the copy stays independent of the original.
func TestRecentWasmsInsertAndCopy(t *testing.T) {
	state := newTestState(t)

	hash1 := common.HexToHash("0x01")
	hash2 := common.HexToHash("0x02")
	hash3 := common.HexToHash("0x03")

	if hit := state.GetRecentWasms().Insert(hash1, recentWasmsRetain); hit {
		t.Fatalf("first insert of hash1 should be a miss")
	}
	if hit := state.GetRecentWasms().Insert(hash1, recentWasmsRetain); !hit {
		t.Fatalf("second insert of hash1 should be a hit (txCache)")
	}
	if hit := state.GetRecentWasms().Insert(hash2, recentWasmsRetain); hit {
		t.Fatalf("first insert of hash2 should be a miss")
	}

	// Finalise promotes hash1/hash2 into the block cache, without it the above hashes are dropped
	state.Finalise(true)

	cp := state.Copy()
	if hit := cp.GetRecentWasms().Insert(hash1, recentWasmsRetain); !hit {
		t.Fatalf("copy: expected hit for hash1 present before copy")
	}
	if hit := cp.GetRecentWasms().Insert(hash2, recentWasmsRetain); !hit {
		t.Fatalf("copy: expected hit for hash2 present before copy")
	}
	if hit := cp.GetRecentWasms().Insert(hash3, recentWasmsRetain); hit {
		t.Fatalf("copy: first insert of hash3 should be a miss")
	}
	if hit := state.GetRecentWasms().Insert(hash3, recentWasmsRetain); hit {
		t.Fatalf("original: hash3 inserted on the copy must not leak back")
	}
}

func TestRecentWasmsTransfer(t *testing.T) {
	hashA := common.HexToHash("0x0a")
	hashB := common.HexToHash("0x0b")

	t.Run("promotes to block cache and survives next tx", func(t *testing.T) {
		state := newTestState(t)
		if hit := state.GetRecentWasms().Insert(hashA, recentWasmsRetain); hit {
			t.Fatalf("first insert should be a miss")
		}
		state.Finalise(true) // commit the tx: txCache -> blockCache

		// Even after a new tx clears txCache, the block cache keeps hashA warm.
		state.SetTxContext(common.HexToHash("0xaa"), 0)
		if hit := state.GetRecentWasms().Insert(hashA, recentWasmsRetain); !hit {
			t.Fatalf("expected block-level hit after promote")
		}
	})

	t.Run("dropped tx does not leak", func(t *testing.T) {
		state := newTestState(t)
		// Simulate a filtered tx: insert, but never Finalise (it's dropped).
		state.SetTxContext(common.HexToHash("0x01"), 0)
		if hit := state.GetRecentWasms().Insert(hashB, recentWasmsRetain); hit {
			t.Fatalf("first insert should be a miss")
		}
		// Next tx starts: SetTxContext must drop the dropped tx's txCache.
		state.SetTxContext(common.HexToHash("0x02"), 0)
		if hit := state.GetRecentWasms().Insert(hashB, recentWasmsRetain); hit {
			t.Fatalf("dropped tx leaked a warm-start into the next tx")
		}
	})

	t.Run("finalise without stylus calls does not panic", func(t *testing.T) {
		state := newTestState(t)
		state.SetTxContext(common.HexToHash("0x01"), 0)
		state.Finalise(true) // must tolerate nil txCache/blockCache
	})

	// A copy taken mid-tx (before Finalise) (as group-rollback checkpoints do)
	// must not carry the in-flight txCache, or a rolled-back group would leave
	// programs warm
	t.Run("uncommitted txCache does not survive Copy", func(t *testing.T) {
		state := newTestState(t)
		state.SetTxContext(common.HexToHash("0x01"), 0)
		if hit := state.GetRecentWasms().Insert(hashA, recentWasmsRetain); hit {
			t.Fatalf("first insert should be a miss")
		}
		// Copy before Finalise: hashA is still only in txCache.
		cp := state.Copy()
		if hit := cp.GetRecentWasms().Insert(hashA, recentWasmsRetain); hit {
			t.Fatalf("copy must not see the original's uncommitted txCache warming")
		}
	})
}

func TestActivateWasmRevert(t *testing.T) {
	db := NewDatabaseForTesting()
	state, err := New(types.EmptyRootHash, db)
	if err != nil {
		t.Fatalf("failed to create state: %v", err)
	}

	module1 := common.HexToHash("0x01")
	module2 := common.HexToHash("0x02")

	asmMap := map[rawdb.WasmTarget][]byte{
		rawdb.TargetArm64:          []byte("sp-arm"),
		rawdb.TargetArm64Cranelift: []byte("cl-arm"),
		rawdb.TargetWavm:           []byte("wavm"),
	}

	// Activate first module before snapshot.
	if err := state.ActivateWasm(module1, asmMap); err != nil {
		t.Fatalf("ActivateWasm(module1): %v", err)
	}

	snap := state.Snapshot()

	// Activate second module after snapshot.
	asmMap2 := map[rawdb.WasmTarget][]byte{
		rawdb.TargetArm64:          []byte("sp-arm-2"),
		rawdb.TargetArm64Cranelift: []byte("cl-arm-2"),
		rawdb.TargetWavm:           []byte("wavm-2"),
	}
	if err := state.ActivateWasm(module2, asmMap2); err != nil {
		t.Fatalf("ActivateWasm(module2): %v", err)
	}

	// Verify both modules are accessible — both singlepass and cranelift.
	if asm := state.ActivatedAsm(rawdb.TargetArm64, module1); string(asm) != "sp-arm" {
		t.Fatalf("module1 singlepass: got %q, want %q", asm, "sp-arm")
	}
	if asm := state.ActivatedAsm(rawdb.TargetArm64Cranelift, module1); string(asm) != "cl-arm" {
		t.Fatalf("module1 cranelift: got %q, want %q", asm, "cl-arm")
	}
	if asm := state.ActivatedAsm(rawdb.TargetArm64, module2); string(asm) != "sp-arm-2" {
		t.Fatalf("module2 singlepass: got %q, want %q", asm, "sp-arm-2")
	}

	// Revert to snapshot — should undo module2 activation.
	state.RevertToSnapshot(snap)

	// module1 should still be accessible.
	if asm := state.ActivatedAsm(rawdb.TargetArm64, module1); string(asm) != "sp-arm" {
		t.Fatalf("module1 singlepass lost after revert: got %q", asm)
	}
	if asm := state.ActivatedAsm(rawdb.TargetArm64Cranelift, module1); string(asm) != "cl-arm" {
		t.Fatalf("module1 cranelift lost after revert: got %q", asm)
	}

	// module2 should be fully reverted.
	if asm := state.ActivatedAsm(rawdb.TargetArm64, module2); len(asm) > 0 {
		t.Fatal("module2 singlepass should be reverted")
	}
	if asm := state.ActivatedAsm(rawdb.TargetArm64Cranelift, module2); len(asm) > 0 {
		t.Fatal("module2 cranelift should be reverted")
	}
}
