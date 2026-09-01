package live

import (
	"encoding/json"

	"github.com/Chaintable/pipeline/tracer"
	ptypes "github.com/Chaintable/pipeline/types"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/tracers"
)

// 需要上传3种data
// 1. block
// 2. state diff
// 3. block file

func init() {
	tracers.LiveDirectory.Register("pipeline", NewPipelineTracer)
}

func NewPipelineTracer(cfg json.RawMessage) (*tracing.Hooks, error) {
	t, err := tracer.NewPipelineTracer(cfg)
	if err != nil {
		return nil, err
	}
	hooks := &tracing.Hooks{
		OnBlockchainInit: t.OnBlockchainInit,
		OnClose:          t.OnClose,
		OnBlockStart:     t.OnBlockStart,
		OnTxStart:        t.OnTxStart,
		OnTxEnd:          t.OnTxEnd,
		OnEnter:          t.OnEnter,
		OnExit:           t.OnExit,
		OnLog:            t.OnLog,
		OnOpcode:         t.OnOpcode,
		OnGenesisBlock:   t.OnGenesisBlock,
		OnCommit:         t.OnCommit,
	}
	if t.EnableOrbitGenesisTransactions() {
		hooks.OnArbGenesisBlockV2 = t.OnArbGenesisBlock
	} else {
		hooks.OnArbGenesisBlock = func(block *types.Block, blockDiff *ptypes.BlockStorageDiff) {
			t.OnArbGenesisBlock(block, nil, blockDiff)
		}
	}
	return hooks, nil
}
