package codexcli

import _ "embed"

// Embedded output schemas (copies of scripts/codex_schemas/*). Kept local to
// the package so go:embed compiles cleanly regardless of build context.

//go:embed schemas/kdb_extract.schema.json
var ExtractSchema []byte

//go:embed schemas/kdb_classify.schema.json
var ClassifySchema []byte

//go:embed schemas/kdb_fill_locale.schema.json
var FillLocaleSchema []byte

//go:embed schemas/kdb_gatekeeper.schema.json
var GatekeeperSchema []byte

//go:embed schemas/kdb_person_reconcile.schema.json
var PersonReconcileSchema []byte

//go:embed schemas/kdb_fill_person.schema.json
var FillPersonSchema []byte

//go:embed schemas/kdb_disambiguate.schema.json
var DisambiguateSchema []byte
