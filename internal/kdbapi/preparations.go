package kdbapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/kdb/readiness"
)

func preparationOwner(r *http.Request) string {
	if id, ok := r.Context().Value(ctxKeyConsumer).(string); ok && id != "" {
		return "consumer:" + id
	}
	if tier, ok := r.Context().Value(ctxKeyTier).(keyTier); ok && tier == tierWrite {
		sum := sha256.Sum256([]byte(requestAPIKey(r)))
		return "operator:" + hex.EncodeToString(sum[:])
	}
	return ""
}

func (h *handler) preparationEnabled() bool {
	return os.Getenv("KDB_READINESS_ENABLED") == "1" && h.store != nil && h.store.Pool != nil
}

func (h *handler) preparationAccess(w http.ResponseWriter, r *http.Request) (*readiness.Store, string, bool) {
	owner := preparationOwner(r)
	if owner == "" {
		writeError(w, http.StatusUnauthorized, "authenticated preparation owner required")
		return nil, "", false
	}
	if !h.preparationEnabled() {
		writeError(w, http.StatusServiceUnavailable, "preparation tracking is not enabled")
		return nil, "", false
	}
	return &readiness.Store{Pool: h.store.Pool, CommonEnabled: commonReadinessEnabled()}, owner, true
}

func commonReadinessEnabled() bool {
	return os.Getenv("KDB_COMMON_READINESS_ENABLED") == "1" && os.Getenv("KDB_COMMON_ENTITY_ENABLED") == "1"
}

func preparationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, readiness.ErrPolicy):
		writeError(w, http.StatusServiceUnavailable, "requested readiness policy is not enabled or requires review")
	case errors.Is(err, readiness.ErrNotFound):
		writeError(w, http.StatusNotFound, "preparation not found")
	case errors.Is(err, readiness.ErrConflict), errors.Is(err, readiness.ErrRevision):
		writeError(w, http.StatusConflict, err.Error())
	default:
		log.Printf("preparation store: %v", err)
		writeError(w, http.StatusServiceUnavailable, "preparation store unavailable; retry with the same Idempotency-Key")
	}
}

func (h *handler) createPreparation(w http.ResponseWriter, r *http.Request) {
	s, owner, ok := h.preparationAccess(w, r)
	if !ok {
		return
	}
	var in readiness.Input
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid preparation JSON")
		return
	}
	in, err := readiness.Normalize(in)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(r.Header.Get("Idempotency-Key")) > 200 {
		writeError(w, http.StatusBadRequest, "Idempotency-Key too long")
		return
	}
	p, err := s.Create(r.Context(), owner, r.Header.Get("Idempotency-Key"), in)
	if err != nil {
		preparationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (h *handler) getPreparation(w http.ResponseWriter, r *http.Request) {
	s, owner, ok := h.preparationAccess(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid preparation ID")
		return
	}
	p, err := s.Get(r.Context(), owner, id)
	if err != nil {
		preparationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (h *handler) cancelPreparation(w http.ResponseWriter, r *http.Request) {
	s, owner, ok := h.preparationAccess(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid preparation ID")
		return
	}
	var req struct {
		Revision int64  `json:"revision"`
		Reason   string `json:"reason"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err = json.NewDecoder(r.Body).Decode(&req); err != nil || req.Revision < 1 || strings.TrimSpace(req.Reason) == "" || len(req.Reason) > 1000 {
		writeError(w, http.StatusBadRequest, "revision and cancellation reason required")
		return
	}
	if err = s.Cancel(r.Context(), owner, id, req.Revision, req.Reason); err != nil {
		preparationError(w, err)
		return
	}
	p, err := s.Get(r.Context(), owner, id)
	if err != nil {
		preparationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (h *handler) trackPreparation(r *http.Request, req PrepareRequest, response *PrepareResponse) {
	if !h.preparationEnabled() {
		return
	}
	owner := preparationOwner(r)
	if owner == "" {
		response.TrackingStatus = "unavailable"
		return
	}
	in := readiness.Input{Locales: req.Locales, SourceURL: req.SourceURL, ArticleID: req.ArticleID, ArticleVersion: req.ArticleVersion}
	for _, raw := range req.Terms {
		pt := parsePrepareTerm(raw)
		if pt.Ko == "" {
			continue
		}
		context := pt.Context
		if context == "" {
			context = req.Context
		}
		in.Terms = append(in.Terms, readiness.Term{KO: pt.Ko, Type: pt.Type, Context: context})
	}
	s := &readiness.Store{Pool: h.store.Pool}
	p, err := s.Create(r.Context(), owner, r.Header.Get("Idempotency-Key"), in)
	if err != nil {
		response.TrackingStatus = "unavailable"
		if errors.Is(err, readiness.ErrConflict) {
			response.TrackingStatus = "idempotency_conflict"
		}
		log.Printf("legacy prepare tracking: %v", err)
		return
	}
	response.PreparationID = p.ID.String()
	response.TrackingStatus = "recorded"
}
