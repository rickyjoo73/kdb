package hermes

import (
	"testing"

	"github.com/google/uuid"

	"github.com/rickyjoo73/kdb/internal/kdb/agents"
)

func TestIncidentTracker_OpenAfterThreshold(t *testing.T) {
	tr := NewIncidentTracker(3)
	role := agents.RoleEnricher

	for i := 0; i < 2; i++ {
		if opened, _ := tr.Record(role, false); opened {
			t.Fatalf("opened too early at fail %d", i+1)
		}
	}
	opened, _ := tr.Record(role, false) // 3rd consecutive fail
	if !opened {
		t.Fatal("expected incident to open on 3rd fail")
	}
	if !tr.IsOpen(role) {
		t.Fatal("expected IsOpen true")
	}
	// Further fails do not re-open.
	if opened, _ := tr.Record(role, false); opened {
		t.Fatal("should not re-open while already open")
	}
}

func TestIncidentTracker_Recover(t *testing.T) {
	tr := NewIncidentTracker(2)
	role := agents.RoleClassifier
	tr.Record(role, false)
	tr.Record(role, false) // open
	if !tr.IsOpen(role) {
		t.Fatal("expected open")
	}
	_, recovered := tr.Record(role, true)
	if !recovered {
		t.Fatal("expected recovery on first success")
	}
	if tr.IsOpen(role) {
		t.Fatal("expected closed after recovery")
	}
}

func TestIncidentTracker_ConsecutiveResetOnSuccess(t *testing.T) {
	tr := NewIncidentTracker(3)
	role := agents.RolePersonExtractor
	tr.Record(role, false)
	tr.Record(role, false)
	tr.Record(role, true) // reset
	tr.Record(role, false)
	tr.Record(role, false)
	if tr.IsOpen(role) {
		t.Fatal("should not be open: success reset the counter")
	}
}

func TestLeakDetail(t *testing.T) {
	l := agents.Leak{Selected: 5, Accounted: 3, Unaccounted: []uuid.UUID{uuid.New(), uuid.New()}}
	if leakDetail(l) == "" {
		t.Fatal("empty leak detail")
	}
}
