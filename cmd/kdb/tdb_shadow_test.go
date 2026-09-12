package main

import (
	"context"
	"github.com/rickyjoo73/kdb/internal/kentity"
	"io"
	"strings"
	"testing"
)

func TestShadowDecoderRefusesNamesSecretsAndExtraJSON(t *testing.T) {
	for _, s := range []string{`{"bindings":[{"tdb_id":"11111111-1111-4111-8111-111111111111","name":"must not import"}]}`, `{"api_key":"synthetic-secret"}`, `{} {}`, `[]`, `{} trailing`} {
		var b kentity.TDBBindingBatch
		if err := decodeTDBBinding([]byte(s), &b); err == nil {
			t.Fatal("accepted non-ID schema", s)
		}
	}
	var b kentity.TDBBindingBatch
	if err := decodeTDBBinding([]byte(`{"source":"wikidata","bindings":[]}`), &b); err != nil {
		t.Fatal(err)
	}
}
func TestShadowCommandBoundsAndGateBeforeDatabase(t *testing.T) {
	t.Setenv("KDB_TDB_SHADOW_ENABLED", "")
	for _, args := range [][]string{{"--apply"}, {"--unknown"}, {"positional"}} {
		if err := tdbShadowCommand(context.Background(), nil, args, strings.NewReader(`{}`), io.Discard); err == nil {
			t.Fatal(args)
		}
	}
	if err := tdbShadowCommand(context.Background(), nil, nil, strings.NewReader(strings.Repeat("x", 65537)), io.Discard); err == nil {
		t.Fatal("oversized input accepted")
	}
}
