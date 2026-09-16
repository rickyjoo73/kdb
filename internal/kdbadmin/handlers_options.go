package kdbadmin

import "github.com/rickyjoo73/kdb/internal/kdb"

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

// entityTypes — 필터 어휘. **원본은 kdb.EntityTypes 하나다** (2026-09-16).
//
// ★여기 손으로 적힌 목록이 몇 달째 틀려 있었다:
//     person group work place organization brand event term unknown
//   work·place·brand·event 는 enum 에 **없는 값**이라 고르면 화면이 500 이 되고,
//   show·drama·movie·song_album·agency·character 와 새 유형 10종(정당·기업·게임…)은
//   **목록에 아예 없어서** 운영자가 고를 수도, 인박스에서 승격시킬 수도 없었다.
//   문서와 API 는 새 유형을 받는데 사람이 쓰는 화면만 옛 세상에 있었다.
var entityTypes = kdb.EntityTypes

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
