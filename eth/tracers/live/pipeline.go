package live

import (
	"encoding/json"
	"fmt"

	"math/big"
	"strings"
	"time"

	"github.com/Chaintable/pipeline/metrics"
	"github.com/Chaintable/pipeline/processor"
	"github.com/Chaintable/pipeline/tracer"
	ptypes "github.com/Chaintable/pipeline/types"
	"github.com/Chaintable/pipeline/util"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/tracers"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
)

// 需要上传3种data
// 1. block
// 2. state diff
// 3. block file

func init() {
	tracers.LiveDirectory.Register("pipeline", NewPipelineTracer)
}

func NewPipelineTracer(cfg json.RawMessage) (*tracing.Hooks, error) {
	t, err := NewNativePipelineTracer(cfg)
	if err != nil {
		return nil, err
	}
	return &tracing.Hooks{
		OnBlockchainInit:  t.OnBlockchainInit,
		OnClose:           t.OnClose,
		OnBlockStart:      t.OnBlockStart,
		OnTxStart:         t.OnTxStart,
		OnTxEnd:           t.OnTxEnd,
		OnEnter:           t.OnEnter,
		OnExit:            t.OnExit,
		OnLog:             t.OnLog,
		OnOpcode:          t.OnOpcode,
		OnGenesisBlock:    t.OnGenesisBlock,
		OnCommit:          t.OnCommit,
		OnArbGenesisBlock: t.OnArbGenesisBlock,
	}, nil
}

// 需要上传3种data
// 1. block
// 2. state diff
// 3. block file

type PipelineTracer struct {
	pipelineTracer *tracer.PipelineTracer
}

type pipelineTracerConfig struct {
	Region               string   `json:"region"`
	NodeXBucket          string   `json:"node_x_bucket"`
	ChainTableBucket     string   `json:"chain_table_bucket"`
	Brokers              []string `json:"brokers"`
	Topic                string   `json:"topic"`
	S3TempDir            string   `json:"s3_temp_dir"`
	IsBackup             bool     `json:"is_backup"`
	EnablePreStateTracer bool     `json:"enable_prestate_tracer"`
}

func NewNativePipelineTracer(cfg json.RawMessage) (*PipelineTracer, error) {
	pipelineTracer, err := tracer.NewPipelineTracer(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create pipeline tracer: %v", err)
	}
	t := &PipelineTracer{
		pipelineTracer: pipelineTracer,
	}
	return t, nil
}

func (t *PipelineTracer) OnBlockchainInit(chainConfig *params.ChainConfig) {
	t.pipelineTracer.OnBlockchainInit(chainConfig)
}

func (t *PipelineTracer) OnClose() {
	t.pipelineTracer.OnClose()
}

func (t *PipelineTracer) OnBlockStart(event tracing.BlockEvent) {
	t.pipelineTracer.OnBlockStart(event)
}

func (t *PipelineTracer) OnSystemCallStartHookV2(vm *tracing.VMContext) {
	t.pipelineTracer.OnSystemCallStartHookV2(vm)
}

func (t *PipelineTracer) OnBlockEnd(blockErr error) {
	t.pipelineTracer.OnBlockEnd(blockErr)
}

func (t *PipelineTracer) OnTxStart(vm *tracing.VMContext, tx *types.Transaction, from common.Address) {
	t.pipelineTracer.OnTxStart(vm, tx, from)
}

func (t *PipelineTracer) OnTxEnd(receipt *types.Receipt, err error) {
	t.pipelineTracer.OnTxEnd(receipt, err)
}

func (t *PipelineTracer) OnEnter(depth int, typ byte, from common.Address, to common.Address, input []byte, gas uint64, value *big.Int) {
	t.pipelineTracer.OnEnter(depth, typ, from, to, input, gas, value)
}

func (t *PipelineTracer) OnExit(depth int, output []byte, gasUsed uint64, err error, reverted bool) {
	t.pipelineTracer.OnExit(depth, output, gasUsed, err, reverted)
}

func (t *PipelineTracer) OnOpcode(pc uint64, op byte, gas, cost uint64, scope tracing.OpContext, rData []byte, depth int, err error) {
	t.pipelineTracer.OnOpcode(pc, op, gas, cost, scope, rData, depth, err)
}

func (t *PipelineTracer) OnLog(log *types.Log) {
	t.pipelineTracer.OnLog(log)
}

func (t *PipelineTracer) OnGenesisBlock(block *types.Block, alloc types.GenesisAlloc) {
	t.pipelineTracer.OnGenesisBlock(block, alloc)
}

func (t *PipelineTracer) OnBlockDBStart(db tracing.StateDB) {
	t.pipelineTracer.OnBlockDBStart(db)
}

func (t *PipelineTracer) OnCommit(originRoot common.Hash, root common.Hash, destructs map[common.Hash]struct{}, accounts map[common.Hash][]byte, accountsOrigin map[common.Address][]byte, storages map[common.Hash]map[common.Hash][]byte, storagesOrigin map[common.Address]map[common.Hash][]byte, codes map[common.Hash][]byte) {
	t.pipelineTracer.OnCommit(originRoot, root, destructs, accounts, accountsOrigin, storages, storagesOrigin, codes)
}

func (t *PipelineTracer) OnBalanceChange(addr common.Address, prev, new *big.Int, reason tracing.BalanceChangeReason) {
	t.pipelineTracer.OnBalanceChange(addr, prev, new, reason)
}

// OnArbGenesisBlock Copy from pipelineTracer onBlockGenesisBlock hook
func (t *PipelineTracer) OnArbGenesisBlock(block *types.Block, blockDiff *ptypes.BlockStorageDiff) {
	if tracer.NodeXPusher.LastBlockNotice != nil {
		return
	}

	// 内部s3
	header := util.BuildPilelineBlockHeader(block)
	err := uploadBlockHeader(header)
	if err != nil {
		log.Crit("Failed to upload block", "err", err)
	}
	log.Info("[inner s3] 1.upload genesis block", "block hash", block.Hash().Hex(), "block number", block.Number().Uint64())

	blockDiff.Hash = block.Root()
	// genesis block has no parent
	blockDiff.ParentHash = types.EmptyRootHash
	err = uploadBlockDiff(blockDiff)
	if err != nil {
		log.Crit("Failed to upload block diff files to s3", "err", err)
	}
	log.Info("[inner s3] 2.upload genesis state diff", "block", block.Hash().Hex())

	// 业务s3
	blockFile := &ptypes.BlockFile{
		Block:            util.BuildPipelineBlock(block),
		Txs:              make([]ptypes.Transaction, 0),
		Events:           make([]ptypes.Event, 0),
		Traces:           make([]ptypes.Trace, 0),
		ErrorEvents:      make([]ptypes.Event, 0),
		ErrorTraces:      make([]ptypes.Trace, 0),
		StorageContracts: make([]string, 0),
	}
	for _, diff := range blockDiff.StorageDiff {
		blockFile.StorageContracts = append(blockFile.StorageContracts, strings.ToLower(diff.Address.Hex()))
	}
	// upload block file and meta data
	err = uploadBlockFile(blockFile)
	if err != nil {
		log.Crit("Failed to upload block files to s3", "err", err)
	}
	log.Info("3.upload block file", "block hash", header.Hash.Hex(), "block number", header.Number.ToInt().Uint64())

	// upload block file validation
	err = uploadblockFileValidation(blockFile)
	if err != nil {
		log.Crit("Failed to upload file validation to s3", "err", err)
	}
	log.Info("4.upload block file validation", "block hash", header.Hash.Hex(), "block number", header.Number.ToInt().Uint64())

	// push block change notification
	blockChanges := &ptypes.BlockChangeNotification{
		ChangeType: 1,
		NewBlocks: []ptypes.BlockContext{
			{
				Hash:        block.Hash(),
				ParentHash:  block.ParentHash(),
				BlockNumber: block.NumberU64(),
				Timestamp:   block.Time(),
			},
		},
	}

	err = tracer.NodeXPusher.PushBlockChangeNotification(blockChanges)
	if err != nil {
		log.Crit("Failed to push block change notification", "err", err)
	}

	log.Info("push genesis block change notification", "block hash", block.Hash().Hex(), "block number", block.Number().Uint64())
}

func uploadBlockHeader(blockHeader *ptypes.Header) error {
	start := time.Now()
	defer func() {
		metrics.BlockHeaderUploadTimer.UpdateSince(start)
	}()
	s3BlockFile, err := processor.SerializeHeader(tracer.BizChainID, blockHeader)
	if err != nil {
		return fmt.Errorf("failed to serialize block header: %v", err)
	}
	err = tracer.NodeXPusher.UploadFile(s3BlockFile)
	if err != nil {
		return fmt.Errorf("failed to upload block header: %v", err)
	}
	return nil
}

func uploadBlockDiff(blockDiff *ptypes.BlockStorageDiff) error {
	start := time.Now()
	defer func() {
		metrics.StateDiffUploadTimer.UpdateSince(start)
	}()
	s3file, err := processor.SerializeStateDiff(tracer.BizChainID, blockDiff)
	if err != nil {
		return fmt.Errorf("failed to serialize state diff: %v", err)
	}
	err = tracer.NodeXPusher.UploadFile(s3file)
	if err != nil {
		return fmt.Errorf("failed to upload state diff: %v", err)
	}
	return nil
}

func uploadBlockFile(blockFile *ptypes.BlockFile) error {
	s3file, err := processor.SerializeFile(tracer.BizChainID, blockFile)
	if err != nil {
		return fmt.Errorf("failed to serialize block file: %v", err)
	}
	err = tracer.ChainTableBucketPusher.UploadFile(s3file)
	if err != nil {
		return fmt.Errorf("failed to upload block file: %v", err)
	}
	return nil
}

func uploadblockFileValidation(blockFile *ptypes.BlockFile) error {
	start := time.Now()
	defer func() {
		metrics.BlockFileValidationTimer.UpdateSince(start)
	}()
	blockFileValidation, err := processor.SerializeFileValidation(tracer.BizChainID, blockFile)
	if err != nil {
		return fmt.Errorf("failed to serialize block file validation: %v", err)
	}
	err = tracer.ChainTableBucketPusher.UploadFile(blockFileValidation)
	if err != nil {
		return fmt.Errorf("failed to upload block file validation: %v", err)
	}
	return nil
}
