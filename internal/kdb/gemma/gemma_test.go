package gemma

import "testing"

func TestGemmaEndpointsUnifiedGateway(t *testing.T) {
	t.Setenv("KDB_GEMMA_BASE_URL", "https://ai.aiinplanet.com/")
	t.Setenv("KDB_GEMMA_MODEL", "gemma4")

	eps := gemmaEndpoints()
	if len(eps) != 1 {
		t.Fatalf("endpoint count = %d, want 1", len(eps))
	}
	if eps[0].base != "https://ai.aiinplanet.com" {
		t.Fatalf("base = %q", eps[0].base)
	}
	if eps[0].model != "gemma4" {
		t.Fatalf("model = %q", eps[0].model)
	}
}

func TestGemmaEndpointsUnifiedGatewayDefaultModel(t *testing.T) {
	t.Setenv("KDB_GEMMA_BASE_URL", "https://ai.aiinplanet.com")
	t.Setenv("KDB_GEMMA_MODEL", "")

	eps := gemmaEndpoints()
	if len(eps) != 1 || eps[0].model != defaultGemmaModel {
		t.Fatalf("endpoints = %#v, want default model %q", eps, defaultGemmaModel)
	}
}

func TestGemmaEndpointsKeepsCSVRollbackCompatibility(t *testing.T) {
	t.Setenv("KDB_GEMMA_BASE_URL", "https://one.invalid, https://two.invalid/")
	t.Setenv("KDB_GEMMA_MODEL", "model-one")

	eps := gemmaEndpoints()
	if len(eps) != 2 {
		t.Fatalf("endpoint count = %d, want 2", len(eps))
	}
	if eps[0].model != "model-one" || eps[1].model != "model-one" {
		t.Fatalf("models = %q, %q; want last model reused", eps[0].model, eps[1].model)
	}
	if eps[1].base != "https://two.invalid" {
		t.Fatalf("trimmed base = %q", eps[1].base)
	}
}
