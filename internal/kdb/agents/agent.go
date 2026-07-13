// Package agents — role-scoped agent abstraction for the KDB Hermes
// multi-agent system (see docs/KDB_HERMES_AGENTS_DESIGN.md §B).
//
// Each functional responsibility (Classifier, CandidateGatekeeper,
// PersonExtractor, Enricher, Disambiguator, …) is owned by a role-scoped
// agent: a precise role prompt + a strict I/O contract (JSON schema enforced
// by internal/kdb/codexcli) + a self-check. Agents are supervised by the
// Hermes supervisor (internal/kdb/hermes) which records a run row per
// invocation, enforces an item-conservation (leak) invariant, and opens an
// incident on repeated failure or a failed self-check.
//
// PR1 (this file) defines ONLY the types + a registry. There is no behaviour
// change to autopilot: existing callers keep working untouched. Concrete
// agents (and the wrapping of the existing 8 sweep steps) arrive in later PRs.
package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Role identifies an agent's single responsibility. Stored verbatim in
// kwave_kdb_hermes_runs.role.
type Role string

const (
	RoleClassifier          Role = "Classifier"
	RoleCandidateGatekeeper Role = "CandidateGatekeeper"
	RolePersonExtractor     Role = "PersonExtractor"
	RoleEnricher            Role = "Enricher"
	RoleFillVerifier        Role = "FillVerifier" // re-verify codex-fallback values vs authoritative sources
	RoleLocalFill           Role = "LocalFill"    // on-demand 빈 locale 검색보강(websearch+gemma 다회투표)
	RoleDisambiguator       Role = "Disambiguator"
	RoleBridge              Role = "Bridge" // bridge_health generalized onto Hermes

	// Roles for the wrapped legacy autopilot steps (PR2). Behaviour is
	// unchanged; these exist so each step gets an audited run row + leak
	// detection. See internal/kdb/autopilot/agents.go for the wrapping.
	RoleStepRepairBrokenJamo      Role = "step:RepairBrokenJamo"
	RoleStepRejectQualifiedMember Role = "step:RejectQualifiedMember"
	RoleStepSyncPersons           Role = "step:SyncPersons"
	RoleStepReviewCandidates      Role = "step:ReviewCandidates"
	RoleStepClassifyUnknown       Role = "step:ClassifyUnknown"
	RoleStepPromoteConsensus      Role = "step:PromoteConsensus"
	RoleStepEnrichEmpty           Role = "step:EnrichEmpty"
	RoleStepQualityReview         Role = "step:QualityReview"
	RoleStepResolveAliasConflicts Role = "step:ResolveAliasConflicts"
	RoleStepResolveUnknowns       Role = "step:ResolveUnknowns"
)

// Action is the per-item outcome recorded for leak detection. Every selected
// item MUST end in exactly one Action — anything else is a silent leak that
// Hermes will flag (see ItemConservation).
type Action string

const (
	ActionFilled      Action = "filled"
	ActionMerged      Action = "merged"
	ActionRejected    Action = "rejected"
	ActionKept        Action = "kept"
	ActionSplit       Action = "split"
	ActionQuarantined Action = "quarantined" // schema-invalid / uncertain → accounted, not dropped
	ActionSkipped     Action = "skipped"     // explicitly skipped WITH a reason
	ActionNoop        Action = "noop"        // examined, no change warranted (loose criteria)
	ActionErrored     Action = "errored"     // transient/logical error on this item
)

// accountedActions are the Actions that count an item as handled for the
// item-conservation invariant. ActionNoop counts as handled because the agent
// explicitly examined and dispositioned the item.
func accountedActions() map[Action]bool {
	return map[Action]bool{
		ActionFilled:      true,
		ActionMerged:      true,
		ActionRejected:    true,
		ActionKept:        true,
		ActionSplit:       true,
		ActionQuarantined: true,
		ActionSkipped:     true,
		ActionNoop:        true,
		ActionErrored:     true,
	}
}

// RunInput — what an agent is asked to process this cycle.
type RunInput struct {
	RunID  uuid.UUID   // cycle/run correlation id (Hermes-assigned)
	IDs    []uuid.UUID // ids selected this cycle (from Agent.Select)
	Budget int         // max items / LLM calls for this run
}

// ItemResult — per-item disposition with before/after snapshots for audit and
// leak detection. Before/After are opaque JSON so any role can snapshot its
// own shape.
type ItemResult struct {
	ID     uuid.UUID       `json:"id"`
	Action Action          `json:"action"`
	Before json.RawMessage `json:"before,omitempty"`
	After  json.RawMessage `json:"after,omitempty"`
	Source string          `json:"source,omitempty"` // musicbrainz|wikidata|gpt-5.5|heuristic
	Conf   float64         `json:"conf,omitempty"`
	Reason string          `json:"reason,omitempty"`
}

// Check — a single named self-check result.
type Check struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail,omitempty"`
}

// SelfCheck — the agent's own assessment that it did its job correctly.
type SelfCheck struct {
	Pass   bool    `json:"pass"`
	Checks []Check `json:"checks,omitempty"`
}

// RunReport — what ran: counts in/out, per-item dispositions, dropped items,
// errors, self-check result, duration. This is the unit Hermes records and
// runs the leak detector over.
type RunReport struct {
	Role        Role          `json:"role"`
	RunID       uuid.UUID     `json:"run_id"`
	Selected    int           `json:"selected"`
	Acted       int           `json:"acted"` // items with a state-changing Action
	Quarantined int           `json:"quarantined"`
	Skipped     int           `json:"skipped"`
	Errored     int           `json:"errored"`
	Results     []ItemResult  `json:"results,omitempty"`
	SelfCheck   SelfCheck     `json:"self_check"`
	Errors      []string      `json:"errors,omitempty"` // run-level (non per-item) errors
	StartedAt   time.Time     `json:"started_at"`
	Duration    time.Duration `json:"duration_ns"`
}

// isStateChanging reports whether an Action means the item's state actually
// changed (used to populate RunReport.Acted).
func isStateChanging(a Action) bool {
	switch a {
	case ActionFilled, ActionMerged, ActionRejected, ActionSplit:
		return true
	default:
		return false
	}
}

// Summarize fills the aggregate counters (Acted/Quarantined/Skipped/Errored)
// from Results. Idempotent; safe to call before recording.
func (r *RunReport) Summarize() {
	var acted, quar, skip, errd int
	for _, it := range r.Results {
		switch {
		case isStateChanging(it.Action):
			acted++
		case it.Action == ActionQuarantined:
			quar++
		case it.Action == ActionSkipped:
			skip++
		case it.Action == ActionErrored:
			errd++
		}
	}
	r.Acted = acted
	r.Quarantined = quar
	r.Skipped = skip
	r.Errored = errd
}

// Leak — result of the item-conservation check. Conserved is true when every
// selected id is accounted for (has exactly one valid Action). Unaccounted
// lists selected ids that were silently dropped.
type Leak struct {
	Conserved   bool        `json:"conserved"`
	Selected    int         `json:"selected"`
	Accounted   int         `json:"accounted"`
	Duplicated  []uuid.UUID `json:"duplicated,omitempty"`  // ids appearing more than once in Results
	Unaccounted []uuid.UUID `json:"unaccounted,omitempty"` // selected but never dispositioned
	Unexpected  []uuid.UUID `json:"unexpected,omitempty"`  // in Results but not selected
}

// ItemConservation enforces the leak invariant from the design (§C.2):
//
//	selected == acted + quarantined + skipped(with reason) + noop + errored
//
// Operationally: each selected id must appear in Results exactly once with an
// accounted Action. Any selected id that is neither changed, quarantined,
// skipped-with-reason, noop, nor errored is a leak. The check is purely over
// the RunReport (before/after snapshots) — no DB diffing needed.
func ItemConservation(selected []uuid.UUID, results []ItemResult) Leak {
	selSet := make(map[uuid.UUID]bool, len(selected))
	for _, id := range selected {
		selSet[id] = true
	}
	accounted := accountedActions()
	seen := make(map[uuid.UUID]int, len(results))
	var unexpected []uuid.UUID
	for _, it := range results {
		if !accounted[it.Action] {
			// An unknown/empty Action does not count as accounting.
			continue
		}
		// Skipped must carry a reason to count as accounted.
		if it.Action == ActionSkipped && it.Reason == "" {
			continue
		}
		if !selSet[it.ID] {
			unexpected = append(unexpected, it.ID)
			continue
		}
		seen[it.ID]++
	}
	var unaccounted, duplicated []uuid.UUID
	for _, id := range selected {
		switch seen[id] {
		case 0:
			unaccounted = append(unaccounted, id)
		case 1:
			// ok
		default:
			duplicated = append(duplicated, id)
		}
	}
	sortUUIDs(unaccounted)
	sortUUIDs(duplicated)
	sortUUIDs(unexpected)
	return Leak{
		Conserved:   len(unaccounted) == 0 && len(duplicated) == 0 && len(unexpected) == 0,
		Selected:    len(selected),
		Accounted:   len(selected) - len(unaccounted),
		Duplicated:  duplicated,
		Unaccounted: unaccounted,
		Unexpected:  unexpected,
	}
}

func sortUUIDs(s []uuid.UUID) {
	sort.Slice(s, func(i, j int) bool { return s[i].String() < s[j].String() })
}

// Criterion is Hermes's success-criteria hook for a role. MetricBefore is
// snapshot before Run; MetricAfter after; Met decides whether the run met its
// goal. A loose/no-op criterion (used when wrapping the legacy steps in PR2)
// returns Met=true unconditionally.
type Criterion interface {
	// Metric measures the role's success scalar over the selected set (e.g.
	// Enricher: fillable_gaps; Gatekeeper: junk ratio; Disambiguator:
	// needs_disambig count). Lower-is-better or higher-is-better is encoded
	// in Met, not here.
	Metric(ctx context.Context, pool *pgxpool.Pool, ids []uuid.UUID) (float64, error)
	// Met evaluates the criterion given the before/after metric and the run
	// report. detail is a human-readable explanation for the audit row.
	Met(before, after float64, rep RunReport) (ok bool, detail string)
}

// Agent — a role-scoped unit of work. Select plans the batch; Run executes it
// and returns an auditable RunReport; Criterion gives Hermes the success test.
type Agent interface {
	Role() Role
	// Select returns up to budget ids to process this cycle.
	Select(ctx context.Context, pool *pgxpool.Pool, budget int) ([]uuid.UUID, error)
	// Run processes the selected ids and returns a RunReport. Run MUST NOT
	// silently drop a selected id: every id in in.IDs must appear in
	// RunReport.Results with an accounted Action (see ItemConservation).
	Run(ctx context.Context, pool *pgxpool.Pool, in RunInput) (RunReport, error)
	// Criterion is the success test Hermes evaluates after Run.
	Criterion() Criterion
}

// LooseCriterion — a no-op success criterion: always met, metric always 0.
// Used to wrap the existing autopilot steps in PR2 so we get audited run rows
// + leak detection WITHOUT changing their pass/fail behaviour.
type LooseCriterion struct{}

func (LooseCriterion) Metric(context.Context, *pgxpool.Pool, []uuid.UUID) (float64, error) {
	return 0, nil
}

func (LooseCriterion) Met(_, _ float64, _ RunReport) (bool, string) {
	return true, "loose criterion (no-op): always met"
}

// --- Registry -------------------------------------------------------------

// Registry — a process-wide map of Role → Agent. Concurrency-safe. Hermes
// (PR2) iterates the registry to drive a cycle.
type Registry struct {
	mu     sync.RWMutex
	agents map[Role]Agent
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{agents: make(map[Role]Agent)}
}

// Register adds (or replaces) an agent for its role. Returns an error if the
// agent or its role is empty.
func (r *Registry) Register(a Agent) error {
	if a == nil {
		return fmt.Errorf("agents: nil agent")
	}
	if a.Role() == "" {
		return fmt.Errorf("agents: empty role")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents[a.Role()] = a
	return nil
}

// Get returns the agent for a role, or (nil, false).
func (r *Registry) Get(role Role) (Agent, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.agents[role]
	return a, ok
}

// roleStage is the deterministic data-safety order for a Hermes cycle.
// Normalization/rejection must precede classification and promotion; otherwise
// a contextual mention such as "group member" can be promoted before the
// canonicalization guard sees it. Dedicated agents run in place of their
// superseded legacy wrappers.
func roleStage(role Role) int {
	switch role {
	case RoleStepRepairBrokenJamo:
		return 10
	case RoleStepRejectQualifiedMember:
		return 20
	case RoleCandidateGatekeeper:
		return 30
	case RoleStepClassifyUnknown:
		return 40
	case RoleStepResolveUnknowns:
		return 50
	case RoleStepPromoteConsensus:
		return 60
	case RoleStepSyncPersons:
		return 70
	case RolePersonExtractor:
		return 80
	case RoleEnricher:
		return 90
	case RoleFillVerifier:
		return 100
	case RoleStepQualityReview:
		return 110
	case RoleDisambiguator:
		return 120
	case RoleStepResolveAliasConflicts:
		return 130
	default:
		return 1000
	}
}

// Roles returns registered roles in deterministic pipeline stage order.
func (r *Registry) Roles() []Role {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Role, 0, len(r.agents))
	for role := range r.agents {
		out = append(out, role)
	}
	sort.Slice(out, func(i, j int) bool {
		si, sj := roleStage(out[i]), roleStage(out[j])
		if si != sj {
			return si < sj
		}
		return out[i] < out[j]
	})
	return out
}

// All returns registered agents in pipeline stage order.
func (r *Registry) All() []Agent {
	roles := r.Roles()
	out := make([]Agent, 0, len(roles))
	for _, role := range roles {
		if a, ok := r.Get(role); ok {
			out = append(out, a)
		}
	}
	return out
}

// Len returns the number of registered agents.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.agents)
}
