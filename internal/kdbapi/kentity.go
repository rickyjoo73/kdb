package kdbapi

import (
	"errors"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/kentity"
	"net/http"
	"os"
)

func (h *handler) commonEntities(w http.ResponseWriter, r *http.Request) {
	if h.store == nil || h.store.Pool == nil {
		writeError(w, 503, "common entity catalog unavailable")
		return
	}
	if os.Getenv("KDB_COMMON_ENTITY_ENABLED") != "1" {
		writeError(w, 503, "common entity catalog not enabled")
		return
	}
	items, err := (&kentity.Store{Pool: h.store.Pool}).Search(r.Context(), r.URL.Query().Get("q"), r.URL.Query().Get("type"), r.URL.Query().Get("domain"), 50)
	if errors.Is(err, kentity.ErrInvalid) {
		writeError(w, 400, "invalid query")
		return
	}
	if err != nil {
		writeError(w, 503, "common entity catalog unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "limit": 50, "catalog_only": true})
}
func (h *handler) commonEntity(w http.ResponseWriter, r *http.Request) {
	if h.store == nil || h.store.Pool == nil {
		writeError(w, 503, "common entity catalog unavailable")
		return
	}
	if os.Getenv("KDB_COMMON_ENTITY_ENABLED") != "1" {
		writeError(w, 503, "common entity catalog not enabled")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, 400, "invalid entity ID")
		return
	}
	e, err := (&kentity.Store{Pool: h.store.Pool}).Get(r.Context(), id)
	if errors.Is(err, kentity.ErrNotFound) {
		writeError(w, 404, "entity not found")
		return
	}
	if err != nil {
		writeError(w, 503, "common entity catalog unavailable")
		return
	}
	writeJSON(w, 200, e)
}
