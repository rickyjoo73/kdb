package agents

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/rickyjoo73/kdb/internal/kdb/codexcli"
)

// runner is the subset of codexcli.Runner the agents base depends on. Defining
// it as an interface keeps the base unit-testable (a fake runner can return
// canned schema-valid JSON per role, per the E2E plan §F.2) and avoids a hard
// dependency on the exec transport in tests.
type runner interface {
	Run(ctx context.Context, prompt string, schema []byte) (json.RawMessage, error)
}

// LLMRole bundles the three things a role needs to make a gpt-5.5 call through
// codexcli: a Role tag, the strict output JSON schema, and a prompt builder.
// Every concrete agent that calls the model embeds one of these so all model
// I/O goes through one validated path (design §B: "Every gpt-5.5 call uses
// codexcli.Runner.Run with a strict embedded schema; output is JSON-validated
// before any DB write").
type LLMRole struct {
	Role   Role
	Schema []byte
	// BuildPrompt renders the role prompt for one input item. The input is
	// opaque (each role defines its own shape); the builder type-asserts.
	BuildPrompt func(input any) (string, error)
}

// Base wraps a codexcli runner with a per-role schema + prompt. Concrete
// agents embed a *Base and call CallJSON to get schema-validated output. The
// runner is an interface so tests can substitute a fake.
type Base struct {
	Runner runner
	LLM    LLMRole
}

// NewBase constructs a Base from a concrete *codexcli.Runner (nil → a fresh
// one). Pass an explicit runner via the struct literal in tests.
func NewBase(r *codexcli.Runner, role LLMRole) *Base {
	var rn runner
	if r != nil {
		rn = r
	} else {
		rn = codexcli.NewRunner()
	}
	return &Base{Runner: rn, LLM: role}
}

// CallJSON builds the prompt for one input, invokes the model with the role's
// strict schema, and unmarshals the validated JSON into out. A non-nil error
// means the item should be QUARANTINED (schema-invalid / call failure) —
// never silently dropped (design §B).
func (b *Base) CallJSON(ctx context.Context, input any, out any) error {
	if b == nil || b.Runner == nil {
		return fmt.Errorf("agents: nil base/runner for role %q", b.role())
	}
	if b.LLM.BuildPrompt == nil {
		return fmt.Errorf("agents: nil prompt builder for role %q", b.role())
	}
	prompt, err := b.LLM.BuildPrompt(input)
	if err != nil {
		return fmt.Errorf("agents(%s): build prompt: %w", b.role(), err)
	}
	raw, err := b.Runner.Run(ctx, prompt, b.LLM.Schema)
	if err != nil {
		return fmt.Errorf("agents(%s): runner: %w", b.role(), err)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("agents(%s): decode: %w", b.role(), err)
	}
	return nil
}

func (b *Base) role() Role {
	if b == nil {
		return ""
	}
	return b.LLM.Role
}
