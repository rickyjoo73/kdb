package main

import (
	"context"
	"github.com/rickyjoo73/kdb/internal/kentity"
	"io"
	"strings"
	"testing"
)

func TestTDBObservationDecoderAndGate(t *testing.T) {
	for _, raw := range []string{`{"records":[{"binding":{"name":"not allowed"}}]}`, `{"api_key":"synthetic"}`, `{} {}`, `[]`, `{} trailing`} {
		var in kentity.TDBObservationBatch
		if decodeTDBObservations([]byte(raw), &in) == nil {
			t.Fatal(raw)
		}
	}
	t.Setenv("KDB_TDB_OBSERVER_ENABLED", "")
	for _, args := range [][]string{{"--apply"}, {"--list", "--apply"}, {"--unknown"}, {"positional"}} {
		if tdbObservationCommand(context.Background(), nil, args, strings.NewReader(`{}`), io.Discard) == nil {
			t.Fatal(args)
		}
	}
	if tdbObservationCommand(context.Background(), nil, nil, strings.NewReader(strings.Repeat("x", 65537)), io.Discard) == nil {
		t.Fatal("unbounded input")
	}
}
