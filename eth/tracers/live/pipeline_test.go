package live

import (
	"encoding/json"
	"testing"
)

func TestNewPipelineTracerArbGenesisHookVersion(t *testing.T) {
	tests := []struct {
		name       string
		config     string
		wantV2Hook bool
	}{
		{name: "default v1", config: `{"is_backup":true}`},
		{name: "orbit genesis transactions v2", config: `{"is_backup":true,"enable_orbit_genesis_transactions":true}`, wantV2Hook: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			hooks, err := NewPipelineTracer(json.RawMessage(test.config))
			if err != nil {
				t.Fatal(err)
			}
			if got := hooks.OnArbGenesisBlockV2 != nil; got != test.wantV2Hook {
				t.Fatalf("v2 hook registered = %t, want %t", got, test.wantV2Hook)
			}
			if got, want := hooks.OnArbGenesisBlock != nil, !test.wantV2Hook; got != want {
				t.Fatalf("v1 hook registered = %t, want %t", got, want)
			}
		})
	}
}
