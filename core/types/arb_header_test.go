// Copyright 2026, Offchain Labs, Inc.
// For license information, see https://github.com/OffchainLabs/nitro/blob/master/LICENSE.md

package types

import (
	"encoding/binary"
	"math"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/params"
)

// arbHeader builds a minimally-valid arbitrum header for
// DeserializeHeaderExtraInformation: Difficulty == 1, len(Extra) == 32,
// MixDigest encoding the supplied ArbOS version. baseFee may be a zero,
// positive, or nil *big.Int (the last to test the initial guard).
func arbHeader(t *testing.T, arbosVersion uint64, baseFee *big.Int, time uint64) *Header {
	t.Helper()
	mix := common.Hash{}
	binary.BigEndian.PutUint64(mix[16:24], arbosVersion)
	return &Header{
		Difficulty: new(big.Int).Set(common.Big1),
		Extra:      make([]byte, 32),
		MixDigest:  mix,
		BaseFee:    baseFee,
		Time:       time,
	}
}

func withLegacyZeroBaseFeeUntil(t *testing.T, ts uint64) {
	t.Helper()
	prev := legacyZeroBaseFeeUntil.Load()
	legacyZeroBaseFeeUntil.Store(ts)
	t.Cleanup(func() { legacyZeroBaseFeeUntil.Store(prev) })
}

func TestDeserialize_DefaultZeroBaseFeeArbos40(t *testing.T) {
	withLegacyZeroBaseFeeUntil(t, 0)
	h := arbHeader(t, params.ArbosVersion_40, big.NewInt(0), 1_000)
	got := DeserializeHeaderExtraInformation(h)
	if got.ArbOSFormatVersion != params.ArbosVersion_40 {
		t.Fatalf("default behavior should parse zero-basefee headers; got %+v", got)
	}
}

func TestDeserialize_LegacyModeZeroBaseFeeArbos40(t *testing.T) {
	withLegacyZeroBaseFeeUntil(t, 1_000_000)
	h := arbHeader(t, params.ArbosVersion_40, big.NewInt(0), 500_000)
	got := DeserializeHeaderExtraInformation(h)
	if got != (HeaderInfo{}) {
		t.Fatalf("legacy mode should report zero-basefee header as non-arbitrum; got %+v", got)
	}
}

func TestDeserialize_LegacyModeBoundaryStrict(t *testing.T) {
	withLegacyZeroBaseFeeUntil(t, 500)
	// header.Time == flag is NOT in legacy range (strict <).
	h := arbHeader(t, params.ArbosVersion_40, big.NewInt(0), 500)
	got := DeserializeHeaderExtraInformation(h)
	if got.ArbOSFormatVersion != params.ArbosVersion_40 {
		t.Fatalf("boundary timestamp should parse normally; got %+v", got)
	}
}

func TestDeserialize_LegacyModeAboveArbos40(t *testing.T) {
	withLegacyZeroBaseFeeUntil(t, math.MaxUint64)
	h := arbHeader(t, params.ArbosVersion_41, big.NewInt(0), 500)
	got := DeserializeHeaderExtraInformation(h)
	if got.ArbOSFormatVersion != params.ArbosVersion_41 {
		t.Fatalf("ArbOS>40 should bypass the legacy gate; got %+v", got)
	}
}

func TestDeserialize_LegacyModeNonZeroBaseFee(t *testing.T) {
	withLegacyZeroBaseFeeUntil(t, math.MaxUint64)
	h := arbHeader(t, params.ArbosVersion_40, big.NewInt(1), 500)
	got := DeserializeHeaderExtraInformation(h)
	if got.ArbOSFormatVersion != params.ArbosVersion_40 {
		t.Fatalf("non-zero basefee should bypass the legacy gate; got %+v", got)
	}
}

func TestDeserialize_LegacyModeAfterUpgrade(t *testing.T) {
	withLegacyZeroBaseFeeUntil(t, 1_000_000)
	h := arbHeader(t, params.ArbosVersion_40, big.NewInt(0), 1_500_000)
	got := DeserializeHeaderExtraInformation(h)
	if got.ArbOSFormatVersion != params.ArbosVersion_40 {
		t.Fatalf("post-upgrade timestamp should parse normally; got %+v", got)
	}
}

func TestDeserialize_NilBaseFeeUnchanged(t *testing.T) {
	withLegacyZeroBaseFeeUntil(t, math.MaxUint64)
	h := arbHeader(t, params.ArbosVersion_40, nil, 500)
	got := DeserializeHeaderExtraInformation(h)
	if got != (HeaderInfo{}) {
		t.Fatalf("nil basefee should always return empty HeaderInfo; got %+v", got)
	}
}
