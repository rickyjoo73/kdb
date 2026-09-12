package kdbadmin

import (
	"github.com/rickyjoo73/kdb/internal/kentity"
	"net/http"
	"os"
)

func (s *Server) tdbShadowList(w http.ResponseWriter, r *http.Request) {
	if os.Getenv("KDB_COMMON_ENTITY_ENABLED") != "1" || os.Getenv("KDB_TDB_SHADOW_ENABLED") != "1" {
		http.Error(w, "TDB 비교 적재 활성화 전입니다.", 503)
		return
	}
	items, err := (&kentity.Store{Pool: s.pool}).TDBShadows(r.Context(), r.URL.Query().Get("state"), 50)
	data := map[string]any{"title": "TDB 연결 비교", "state": r.URL.Query().Get("state"), "items": items, "loadError": err != nil, "mappingEnabled": tdbMappingEnabled()}
	s.render(w, r, "tdb_shadow.html", data)
}
