package rpc

import (
	"context"
	"net/http/httptest"
	"testing"
)

type debankBlockTestService struct{}

func (debankBlockTestService) GetBlockByHeight(height string) string { return height }
func (debankBlockTestService) GetBlockById(id string) string         { return id }
func (debankBlockTestService) BlockIsValid(id string) bool           { return id == "canonical" }

func TestDebankHistoricalBlockMethods(t *testing.T) {
	server := NewServer()
	defer server.Stop()
	if err := server.RegisterName("debank", debankBlockTestService{}); err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	client, err := Dial(httpServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	for _, prefix := range []string{"", "debank_"} {
		var height, id string
		var valid bool
		calls := []BatchElem{
			{Method: prefix + "getBlockByHeight", Args: []interface{}{"0x2a"}, Result: &height},
			{Method: prefix + "getBlockById", Args: []interface{}{"block-hash"}, Result: &id},
			{Method: prefix + "blockIsValid", Args: []interface{}{"canonical"}, Result: &valid},
		}
		for _, call := range calls {
			if err := client.CallContext(context.Background(), call.Result, call.Method, call.Args...); err != nil {
				t.Fatalf("%s: %v", call.Method, err)
			}
		}
		if height != "0x2a" || id != "block-hash" || !valid {
			t.Fatalf("single call results: height=%q id=%q valid=%v", height, id, valid)
		}
		height, id, valid = "", "", false
		if err := client.BatchCallContext(context.Background(), calls); err != nil {
			t.Fatal(err)
		}
		for _, call := range calls {
			if call.Error != nil {
				t.Fatalf("batch %s: %v", call.Method, call.Error)
			}
		}
		if height != "0x2a" || id != "block-hash" || !valid {
			t.Fatalf("batch results: height=%q id=%q valid=%v", height, id, valid)
		}
	}
}
