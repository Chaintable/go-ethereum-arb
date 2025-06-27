package native

// Copyright 2021 The go-ethereum Authors
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

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync/atomic"

	ptypes "github.com/Chaintable/pipeline/types"
	"github.com/Chaintable/pipeline/util"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
)

type debankCallFrame struct {
	Type         vm.OpCode         `json:"-"`
	From         common.Address    `json:"from"`
	Gas          uint64            `json:"gas"`
	GasUsed      uint64            `json:"gasUsed"`
	To           *common.Address   `json:"to,omitempty" rlp:"optional"`
	Input        []byte            `json:"input" rlp:"optional"`
	Output       []byte            `json:"output,omitempty" rlp:"optional"`
	Error        string            `json:"error,omitempty" rlp:"optional"`
	RevertReason string            `json:"revertReason,omitempty"`
	ParentFailed bool              `json:"-"` // Indicates if the parent call failed
	Calls        []debankCallFrame `json:"calls,omitempty" rlp:"optional"`
	Logs         []ptypes.Event    `json:"logs,omitempty" rlp:"optional"`

	PosInParentTrace  int    `json:"pos_in_parent_trace"`
	ParentTraceID     string `json:"parent_trace_id"`
	TraceID           string `json:"trace_id"`
	StorageChange     bool   `json:"storageChange"`
	SelfStorageChange bool   `json:"self_storage_change"`

	// Placed at end on purpose. The RLP will be decoded to 0 instead of
	// nil if there are non-empty elements after in the struct.
	Value *big.Int `json:"value,omitempty" rlp:"optional"`
}

func (f debankCallFrame) TypeString() string {
	return f.Type.String()
}

func (f debankCallFrame) failed() bool {
	return len(f.Error) > 0
}

func (f *debankCallFrame) processOutput(output []byte, err error, reverted bool) {
	output = common.CopyBytes(output)
	// Clear error if tx wasn't reverted. This happened
	// for pre-homestead contract storage OOG.
	if err != nil && !reverted {
		err = nil
	}
	if err == nil {
		f.Output = output
		return
	}
	f.Error = err.Error()
	if f.Type == vm.CREATE || f.Type == vm.CREATE2 {
		f.To = nil
	}
	if !errors.Is(err, vm.ErrExecutionReverted) || len(output) == 0 {
		return
	}
	f.Output = output
	if len(output) < 4 {
		return
	}
	if unpacked, err := abi.UnpackRevert(output); err == nil {
		f.RevertReason = unpacked
	}
}

type debankCallTracer struct {
	callstack []debankCallFrame
	config    callTracerConfig
	gasLimit  uint64
	depth     int
	interrupt atomic.Bool // Atomic flag to signal execution interruption
	reason    error       // Textual reason for the interruption

	txID string

	BlockFile       *ptypes.BlockFile
	ChangeContracts map[common.Address]struct{}
}

func NewDebankCallTracer(BlockFile *ptypes.BlockFile, txID string) *tracing.Hooks {
	t := &debankCallTracer{
		callstack: make([]debankCallFrame, 0, 1),
		config: callTracerConfig{
			OnlyTopCall: false,
			WithLog:     true,
		},
		BlockFile: BlockFile,
		txID:      txID,
	}
	return &tracing.Hooks{
		OnTxStart: t.OnTxStart,
		OnTxEnd:   t.OnTxEnd,
		OnEnter:   t.OnEnter,
		OnExit:    t.OnExit,
		OnOpcode:  t.OnOpcode,
		OnLog:     t.OnLog,
	}
}

func (t *debankCallTracer) ToTrace(f *debankCallFrame, traceAddress []int) ptypes.Trace {
	CallCreateType := ""
	CallType := ""
	switch f.Type {
	case vm.CREATE, vm.CREATE2:
		CallCreateType = strings.ToLower(vm.CREATE.String())
	case vm.SELFDESTRUCT:
		CallCreateType = "suicide"
	case vm.CALL, vm.STATICCALL, vm.CALLCODE, vm.DELEGATECALL:
		CallCreateType = strings.ToLower(vm.CALL.String())
		CallType = strings.ToLower(f.Type.String())
	default:
		CallCreateType = "empty"
	}
	to := common.Address{}
	if f.To != nil {
		to = *f.To
	}
	value := big.NewInt(0)
	if f.Value != nil {
		value = f.Value
	}
	err := ""
	if f.failed() {
		err = f.Error
		if f.RevertReason != "" {
			err = fmt.Sprintf("%s: %s", f.Error, f.RevertReason)
		}
	}
	return ptypes.Trace{
		ID:                f.TraceID,
		From:              strings.ToLower(f.From.Hex()),
		Gas:               big.NewInt(int64(f.Gas)),
		Input:             (hexutil.Bytes)(f.Input),
		To:                strings.ToLower(to.Hex()),
		Value:             (*hexutil.Big)(value),
		GasUsed:           big.NewInt(int64(f.GasUsed)),
		Output:            (hexutil.Bytes)(f.Output),
		CallCreateType:    CallCreateType,
		CallType:          CallType,
		TxID:              t.txID,
		ParentTraceID:     f.ParentTraceID,
		PosInParentTrace:  int64(f.PosInParentTrace),
		SelfStorageChange: f.SelfStorageChange,
		StorageChange:     f.StorageChange,
		Subtraces:         int64(len(f.Calls)),
		TraceAddress: func(addresses []int) []int64 {
			output := make([]int64, 0, len(addresses))
			for _, address := range addresses {
				output = append(output, int64(address))
			}
			return output
		}(traceAddress),
		Error: err,
	}
}

func (t *debankCallTracer) OnOpcode(pc uint64, opcode byte, gas, cost uint64, scope tracing.OpContext, rData []byte, depth int, err error) {
	if vm.OpCode(opcode) == vm.SSTORE {
		t.callstack[len(t.callstack)-1].SelfStorageChange = true
		t.callstack[len(t.callstack)-1].StorageChange = true
	}
}

// OnEnter is called when EVM enters a new scope (via call, create or selfdestruct).
func (t *debankCallTracer) OnEnter(depth int, typ byte, from common.Address, to common.Address, input []byte, gas uint64, value *big.Int) {
	t.depth = depth
	if t.config.OnlyTopCall && depth > 0 {
		return
	}
	// Skip if tracing was interrupted
	if t.interrupt.Load() {
		return
	}

	toCopy := to
	call := debankCallFrame{
		Type:  vm.OpCode(typ),
		From:  from,
		To:    &toCopy,
		Input: common.CopyBytes(input),
		Gas:   gas,
		Value: value,
	}
	t.callstack = append(t.callstack, call)
}

// OnExit is called when EVM exits a scope, even if the scope didn't
// execute any code.
func (t *debankCallTracer) OnExit(depth int, output []byte, gasUsed uint64, err error, reverted bool) {
	if depth == 0 {
		t.captureEnd(output, gasUsed, err, reverted)
		return
	}

	t.depth = depth - 1
	if t.config.OnlyTopCall {
		return
	}

	size := len(t.callstack)
	if size <= 1 {
		return
	}
	// Pop call.
	call := t.callstack[size-1]
	t.callstack = t.callstack[:size-1]
	size -= 1

	call.GasUsed = gasUsed
	call.processOutput(output, err, reverted)
	// Nest call into parent.
	// 忽略失败的调用
	call.PosInParentTrace = len(t.callstack[size-1].Calls) + len(t.callstack[size-1].Logs)
	t.callstack[size-1].Calls = append(t.callstack[size-1].Calls, call)
}

func (t *debankCallTracer) captureEnd(output []byte, gasUsed uint64, err error, reverted bool) {
	if len(t.callstack) != 1 {
		return
	}
	t.callstack[0].GasUsed = gasUsed
	t.callstack[0].processOutput(output, err, reverted)
}

func (t *debankCallTracer) OnTxStart(env *tracing.VMContext, tx *types.Transaction, from common.Address) {
	t.gasLimit = tx.Gas()
	t.txID = tx.Hash().Hex()
}

func (t *debankCallTracer) OnTxEnd(receipt *types.Receipt, err error) {
	// Error happened during tx validation.
	if err != nil {
		return
	}
	setParentFailed(&t.callstack[0], false)
	t.setStorageChange(&t.callstack[0])
	if len(t.callstack) == 1 {
		topCall := &t.callstack[0]
		topCall.TraceID = util.ToHash([]string{t.txID, "", "0"})
		if topCall.failed() {
			t.BlockFile.ErrorTraces = append(t.BlockFile.ErrorTraces, t.ToTrace(topCall, []int{}))
		} else {
			t.BlockFile.Traces = append(t.BlockFile.Traces, t.ToTrace(topCall, []int{}))
		}
		t.addTraceAndLog(topCall, []int{})
	}
}

func (t *debankCallTracer) OnLog(log *types.Log) {
	// Only logs need to be captured via opcode processing
	if !t.config.WithLog {
		return
	}
	// Avoid processing nested calls when only caring about top call
	if t.config.OnlyTopCall && t.depth > 0 {
		return
	}
	// Skip if tracing was interrupted
	if t.interrupt.Load() {
		return
	}
	topics := make([]string, len(log.Topics))
	for i, topic := range log.Topics {
		topics[i] = topic.Hex()
	}
	var selector string
	var remainingTopics []string

	if len(topics) > 0 {
		selector = topics[0]
		remainingTopics = topics[1:]
	}
	l := ptypes.Event{
		Address:  strings.ToLower(log.Address.Hex()),
		Selector: selector,
		Topics:   remainingTopics,
		Data:     log.Data,
		Position: int64(len(t.callstack[len(t.callstack)-1].Calls) + len(t.callstack[len(t.callstack)-1].Logs)),
		LogIndex: int64(log.Index),
	}
	t.callstack[len(t.callstack)-1].Logs = append(t.callstack[len(t.callstack)-1].Logs, l)
}

func (t *debankCallTracer) GetResult() (json.RawMessage, error) {
	return nil, nil
}

// Stop terminates execution of the tracer at the first opportune moment.
func (t *debankCallTracer) Stop(err error) {
	t.reason = err
	t.interrupt.Store(true)
}

// setParentFailed recursively sets the ParentFailed flag for the call frame and all its subcalls.
func setParentFailed(cf *debankCallFrame, parentFailed bool) {
	failed := cf.failed() || parentFailed
	for i := range cf.Calls {
		cf.Calls[i].ParentFailed = failed
		setParentFailed(&cf.Calls[i], failed)
	}
}

func (t *debankCallTracer) setStorageChange(cf *debankCallFrame) {
	if cf.To != nil && cf.SelfStorageChange {
		t.ChangeContracts[*cf.To] = struct{}{}
	}
	subCallStorageChange := false
	for i := range cf.Calls {
		t.setStorageChange(&cf.Calls[i])
		if cf.Calls[i].StorageChange && !cf.Calls[i].failed() {
			subCallStorageChange = true
		}
	}
	if subCallStorageChange {
		cf.StorageChange = true
	}
}

func (t *debankCallTracer) addTraceAndLog(cf *debankCallFrame, traceAddress []int) {
	for i := range cf.Calls {
		cf.Calls[i].ParentTraceID = cf.TraceID
		cf.Calls[i].TraceID = util.ToHash([]string{t.txID, cf.TraceID, fmt.Sprintf("%d", cf.Calls[i].PosInParentTrace)})
		t.addTraceAndLog(&cf.Calls[i], childTraceAddress(traceAddress, i))
	}
	for i := range cf.Logs {
		cf.Logs[i].ParentTraceID = cf.TraceID
		cf.Logs[i].ID = util.ToHash([]string{cf.Logs[i].ParentTraceID, fmt.Sprintf("%d", cf.Logs[i].Position)})
		if cf.failed() || cf.ParentFailed {
			cf.Logs[i].LogIndex = 0
			t.BlockFile.ErrorEvents = append(t.BlockFile.ErrorEvents, cf.Logs[i])
		} else {
			t.BlockFile.Events = append(t.BlockFile.Events, cf.Logs[i])
		}
	}
	for i := range cf.Calls {
		if cf.failed() || cf.ParentFailed {
			t.BlockFile.ErrorTraces = append(t.BlockFile.Traces, t.ToTrace(&cf.Calls[i], childTraceAddress(traceAddress, i)))
		} else {
			t.BlockFile.Traces = append(t.BlockFile.Traces, t.ToTrace(&cf.Calls[i], childTraceAddress(traceAddress, i)))
		}
	}
}
