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

// personRoles is the primary_role filter vocabulary — MUST match the person_role
// enum exactly (DB SELECT casts the value to person_role; an out-of-enum value
// like 'host'/'writer' caused HTTP 500). Keep in sync with the enum definition.
var personRoles = []string{
	"idol", "singer", "rapper", "actor", "broadcaster", "comedian",
	"director", "producer", "model", "creator", "athlete", "politician",
	"businessperson", "journalist", "fictional", "other",
}
