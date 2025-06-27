package eth

import (
	"context"
	"fmt"
	"strings"

	ptypes "github.com/Chaintable/pipeline/types"
	"github.com/Chaintable/pipeline/util"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/eth/tracers/native"
	"github.com/ethereum/go-ethereum/internal/ethapi"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/rpc"
)

type PublicTraceAPI struct {
	e *Ethereum
}

// NewPublicTraceAPI creates a new trace API.
func NewPublicTraceAPI(e *Ethereum) *PublicTraceAPI {
	return &PublicTraceAPI{e: e}
}

func (api *PublicTraceAPI) DebankBlockRaw(ctx context.Context, blockNrOrHash rpc.BlockNumberOrHash) (*rpc.DebankOutPut, error) {
	block, err := api.e.APIBackend.BlockByNumberOrHash(ctx, blockNrOrHash)
	if err != nil {
		return nil, err
	}
	blockFile := &ptypes.BlockFile{
		Block:            util.BuildPipelineBlock(block),
		Txs:              make([]ptypes.Transaction, 0),
		Events:           make([]ptypes.Event, 0),
		Traces:           make([]ptypes.Trace, 0),
		ErrorEvents:      make([]ptypes.Event, 0),
		ErrorTraces:      make([]ptypes.Trace, 0),
		StorageContracts: make([]string, 0),
	}
	if block.Number().Uint64() == api.e.BlockChain().Config().ArbitrumChainParams.GenesisBlockNum {
		stateDb, err := api.e.BlockChain().StateAt(block.Root())
		if err != nil {
			return nil, fmt.Errorf("failed to load genesis state database: %w", err)
		}
		genesisAlloc := &state.Alloc{
			Accounts: make(map[common.Hash]state.DumpAccount),
		}
		stateDb.DumpToCollector(genesisAlloc, &state.DumpConfig{})
		blockDiff := genesisAlloc.ToStorageDiff()
		for _, diff := range blockDiff.StorageDiff {
			blockFile.StorageContracts = append(blockFile.StorageContracts, strings.ToLower(diff.Address.Hex()))
		}
		return &rpc.DebankOutPut{
			BlockFile:      blockFile,
			Header:         util.BuildPilelineBlockHeader(block),
			StateDiff:      blockDiff,
			ValidationHash: blockFile.Validation().ValidationHash,
		}, nil
	}
	parent, err := api.e.APIBackend.BlockByHash(ctx, block.ParentHash())
	if err != nil {
		return nil, err
	}
	statedb, release, err := api.e.APIBackend.StateAtBlock(ctx, parent, 0, nil, true, false)
	if err != nil {
		return nil, err
	}
	defer release()
	blockCtx := core.NewEVMBlockContext(block.Header(), ethapi.NewChainContext(ctx, api.e.APIBackend), nil)
	evm := vm.NewEVM(blockCtx, statedb, api.e.APIBackend.ChainConfig(), vm.Config{})
	if beaconRoot := block.BeaconRoot(); beaconRoot != nil {
		core.ProcessBeaconBlockRoot(*beaconRoot, evm)
	}
	if !api.e.APIBackend.ChainConfig().IsArbitrum() && api.e.APIBackend.ChainConfig().IsPrague(block.Number(), block.Time(), blockCtx.ArbOSVersion) {
		core.ProcessParentBlockHash(block.ParentHash(), evm)
	}
	var (
		txs          = block.Transactions()
		arbosVersion = types.DeserializeHeaderExtraInformation(block.Header()).ArbOSFormatVersion
		signer       = types.MakeSigner(api.e.APIBackend.ChainConfig(), block.Number(), block.Time(), arbosVersion)
	)

	for i, tx := range txs {
		tracer := native.NewDebankCallTracer(blockFile, tx.Hash().Hex())
		evm = api.e.APIBackend.GetEVM(ctx, statedb, parent.Header(), &vm.Config{NoBaseFee: true, Tracer: tracer}, &blockCtx)
		// Generate the next state snapshot fast without tracing
		_, err = core.TransactionToMessage(tx, signer, block.BaseFee(), core.MessageReplayMode)
		if err != nil {
			return nil, fmt.Errorf("failed to process transaction %d: %w", i, err)
		}
	}
	stateDiff, err := statedb.GetStateDiff()
	if err != nil {
		return nil, fmt.Errorf("failed to load state diff: %w", err)
	}
	return &rpc.DebankOutPut{
		BlockFile:      blockFile,
		Header:         util.BuildPilelineBlockHeader(block),
		StateDiff:      stateDiff,
		ValidationHash: blockFile.Validation().ValidationHash,
	}, nil
}

func (api *PublicTraceAPI) DebankBlock(ctx context.Context, blockNrOrHash rpc.BlockNumberOrHash) (*rpc.DebankOutPutJs, error) {
	output, err := api.DebankBlockRaw(ctx, blockNrOrHash)
	if err != nil {
		return nil, err
	}
	data, err := rlp.EncodeToBytes(output.StateDiff)
	if err != nil {
		return nil, err
	}
	return &rpc.DebankOutPutJs{
		BlockFile:      output.BlockFile,
		Header:         output.Header,
		StateDiff:      data,
		ValidationHash: output.ValidationHash,
	}, nil
}
