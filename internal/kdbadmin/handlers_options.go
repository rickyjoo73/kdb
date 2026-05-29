package kdbadmin

// Shared filter-option vocabularies for the admin entity/person list & inbox
// pages. Referenced by handlers_entities.go / handlers_persons.go /
// handlers_inbox.go / handlers_locale_gaps.go / handlers_unclassified.go (which
// pass them into the page data as "types" / "entityTypes" / "allRoles") and
// iterated by their templates as the <select> filter dropdowns, e.g.
// {{range $.entityTypes}}...{{end}}.
//
// They are static enums sourced from the kwave_entities.entity_type and
// kwave_entity_person_details.primary_role domains, so a pure in-code
// vocabulary keeps the filter UI working without a per-request DB round-trip.

// entityTypes is the entity_type filter vocabulary (kwave_entity_type enum).
var entityTypes = []string{
	"person", "group", "work", "place", "organization", "brand", "event",
	"term", "unknown",
}

// entityStatuses is the status filter vocabulary (kwave_entities.status).
var entityStatuses = []string{"candidate", "active", "rejected"}

// personRoles is the primary_role filter vocabulary (person_role enum).
var personRoles = []string{
	"actor", "singer", "idol", "host", "comedian", "model", "athlete",
	"director", "producer", "writer", "other",
}
