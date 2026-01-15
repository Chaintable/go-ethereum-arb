package arbitrum

import (
	"context"
	"fmt"
	"math/big"
	"strings"

	ptracer "github.com/Chaintable/pipeline/tracer"
	ptypes "github.com/Chaintable/pipeline/types"
	"github.com/Chaintable/pipeline/util"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/tracers"
	"github.com/ethereum/go-ethereum/internal/ethapi"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/holiman/uint256"
)

type DebankAPI struct {
	backend *APIBackend
}

func NewDebankAPI(backend *APIBackend) *DebankAPI {
	return &DebankAPI{
		backend: backend,
	}
}

func stateDumpToStateDiff(dump state.Dump, stateRoot common.Hash) *ptypes.BlockStorageDiff {
	diff := &ptypes.BlockStorageDiff{}
	diff.NewAccounts = make([]ptypes.NewAccount, 0)
	diff.NewCodes = make([]ptypes.NewCode, 0)
	diff.StorageDiff = make([]ptypes.AccountStorageDiff, 0)
	diff.DeletedAccounts = make([]common.Hash, 0)

	for _, account := range dump.Accounts {
		var addrHash common.Hash
		if len(account.AddressHash) > 0 {
			addrHash = common.BytesToHash(account.AddressHash)
		} else if account.Address != nil {
			addr := *account.Address
			addrHash = common.BytesToHash(crypto.Keccak256(addr[:]))
		} else {
			continue
		}

		balance := new(big.Int)
		balance.SetString(account.Balance, 10)

		var codeHash common.Hash
		if len(account.Code) > 0 {
			codeHash = common.BytesToHash(crypto.Keccak256(account.Code))
		} else {
			codeHash = common.BytesToHash(account.CodeHash)
		}

		diff.NewAccounts = append(diff.NewAccounts, ptypes.NewAccount{
			Address:  addrHash,
			Balance:  uint256.MustFromBig(balance),
			Nonce:    account.Nonce,
			CodeHash: codeHash,
		})

		if len(account.Code) > 0 {
			diff.NewCodes = append(diff.NewCodes, ptypes.NewCode{
				CodeHash: codeHash,
				Code:     account.Code,
			})
		}

		if len(account.Storage) > 0 {
			values := make([]ptypes.IndexValuePair, 0, len(account.Storage))
			for indexHash, valueHex := range account.Storage {
				valueBytes := common.Hex2Bytes(valueHex)
				value := uint256.NewInt(0).SetBytes(valueBytes)
				values = append(values, ptypes.IndexValuePair{
					Index: indexHash,
					Value: value,
				})
			}
			diff.StorageDiff = append(diff.StorageDiff, ptypes.AccountStorageDiff{
				Address: addrHash,
				Values:  values,
			})
		}
	}

	diff.Hash = stateRoot
	diff.ParentHash = types.EmptyRootHash

	return diff
}

func (api *DebankAPI) DebankBlock(ctx context.Context, blockNrOrHash rpc.BlockNumberOrHash) (*ptypes.DebankOutPut, error) {
	block, err := api.backend.BlockByNumberOrHash(ctx, blockNrOrHash)
	if err != nil {
		return nil, err
	}
	if block.NumberU64() == 0 {
		statedb, release, err := api.backend.StateAtBlock(ctx, block, 128, nil, true, false)
		if err != nil {
			return nil, fmt.Errorf("could not get genesis state: %w", err)
		}
		defer release()

		dump := statedb.RawDump(&state.DumpConfig{
			SkipCode:          false,
			SkipStorage:       false,
			OnlyWithAddresses: false,
			UseStorageKeyHash: true,
		})

		header := util.BuildPilelineBlockHeader(block)
		blockDiff := stateDumpToStateDiff(dump, block.Root())

		// 构建 BlockFile
		blockFile := &ptypes.BlockFile{
			Block:            util.BuildPipelineBlock(block),
			Txs:              make([]ptypes.Transaction, 0),
			Events:           make([]ptypes.Event, 0),
			Traces:           make([]ptypes.Trace, 0),
			ErrorEvents:      make([]ptypes.Event, 0),
			ErrorTraces:      make([]ptypes.Trace, 0),
			StorageContracts: make([]string, 0),
		}

		for _, account := range dump.Accounts {
			if account.Address != nil && len(account.Storage) > 0 {
				blockFile.StorageContracts = append(blockFile.StorageContracts,
					strings.ToLower(account.Address.Hex()))
			}
		}

		var stateDiffBytes []byte
		if blockDiff != nil {
			stateDiffBytes, err = util.EncodeToRlp(blockDiff)
			if err != nil {
				log.Error("Failed to encode state diff", "err", err)
				stateDiffBytes = []byte{}
			}
		} else {
			stateDiffBytes = []byte{}
		}

		return &ptypes.DebankOutPut{
			BlockFile:      blockFile,
			Header:         header,
			StateDiff:      hexutil.Bytes(stateDiffBytes),
			ValidationHash: blockFile.Validation().ValidationHash,
		}, nil
	}
	// Prepare base state
	parent, err := api.backend.BlockByHash(ctx, block.ParentHash())
	if err != nil {
		return nil, err
	}
	statedb, release, err := api.backend.StateAtBlock(ctx, parent, 128, nil, true, false)
	if err != nil {
		return nil, err
	}
	defer release()

	rpcTracer := ptracer.RPCTracer{}
	tracer := &tracers.Tracer{
		Hooks: &tracing.Hooks{
			OnTxStart: rpcTracer.OnTxStart,
			OnTxEnd:   rpcTracer.OnTxEnd,
			OnEnter:   rpcTracer.OnEnter,
			OnExit:    rpcTracer.OnExit,
			OnOpcode:  rpcTracer.OnOpcode,
			OnLog:     rpcTracer.OnLog,
		},
		Stop:      rpcTracer.Stop,
		GetResult: rpcTracer.GetResult,
	}
	blockCtx := core.NewEVMBlockContext(block.Header(), ethapi.NewChainContext(ctx, api.backend), nil)
	evm := vm.NewEVM(blockCtx, vm.TxContext{}, statedb, api.backend.ChainConfig(), vm.Config{Tracer: tracer.Hooks})

	rpcTracer.OnBlockStart(block)

	if beaconRoot := block.BeaconRoot(); beaconRoot != nil {
		core.ProcessBeaconBlockRoot(*beaconRoot, evm, statedb)
	}
	// if api.backend.ChainConfig().IsPrague(block.Number(), block.Time()) || api.backend.ChainConfig().IsVerkle(block.Number(), block.Time()) {
	// 	core.ProcessParentBlockHash(block.ParentHash(), evm)
	// }
	var (
		txs     = block.Transactions()
		signer  = types.MakeSigner(api.backend.ChainConfig(), block.Number(), block.Time())
		gp      = new(core.GasPool).AddGas(block.GasLimit())
		usedGas = new(uint64)
	)

	for i, tx := range txs {
		msg, err := core.TransactionToMessage(tx, signer, blockCtx.BaseFee, core.MessageReplayMode)
		if err != nil {
			return nil, fmt.Errorf("could not apply tx %d [%v]: %w", i, tx.Hash().Hex(), err)
		}
		statedb.SetTxContext(tx.Hash(), i)

		receipt, _, err := core.ApplyTransactionWithEVM(msg, api.backend.ChainConfig(), gp, statedb, block.Number(), block.Hash(), tx, usedGas, evm, nil)
		if err != nil {
			return nil, fmt.Errorf("could not apply tx %d [%v]: %w", i, tx.Hash().Hex(), err)
		}

		receipt.SetEffectiveGasPrice(tx, blockCtx.BaseFee, blockCtx.BlockNumber, api.backend.ChainConfig())
	}

	root, destructs, accounts, storages, codes, err := statedb.StateDiff(api.backend.ChainConfig().IsEIP158(block.Number()))
	if err != nil {
		return nil, fmt.Errorf("could not get state diff: %w", err)
	}

	if root != block.Header().Root {
		return nil, fmt.Errorf("state root mismatch: expected %x, got %x", block.Header().Root, root)
	}

	parentRoot := parent.Root()

	res := rpcTracer.GetOutPut(parentRoot, root, destructs, accounts, storages, codes)

	return res, nil
}
