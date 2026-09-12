package wikidata

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type labelsRT struct{}

func (labelsRT) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"entities":{"Q123":{"labels":{"en":{"language":"en","value":"Example (politician)"},"zh":{"language":"zh","value":"範例"},"pt":{"language":"pt","value":"Exemplo"}}}}}`))}, nil
}
func TestFetchPreservesOriginalLocaleAndUnmodifiedLabels(t *testing.T) {
	c := New()
	c.HTTPClient = &http.Client{Transport: labelsRT{}}
	e, err := c.Fetch(context.Background(), "Q123")
	if err != nil {
		t.Fatal(err)
	}
	if e.SourceLabels["en"] != "Example (politician)" || e.SourceLabels["zh"] != "範例" || e.SourceLabels["pt"] != "Exemplo" || e.SourceLabels["pt-br"] != "" {
		t.Fatal(e.SourceLabels)
	}
	if e.Labels["pt_br"] != "Exemplo" {
		t.Fatal("legacy API mapping changed", e.Labels)
	}
}
