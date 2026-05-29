package kdbadmin

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/rickyjoo73/kdb/internal/kdb"
	"github.com/rickyjoo73/kdb/internal/kdb/enrich"
)

// --- inbox list -----------------------------------------------------------

// candidateThreshold — 자동 promote 가능 매체 합의 임계 (kdb.CandidateThreshold 와 동일).
const candidateThreshold = 2

// inboxItem — kwave_entities 의 status='candidate' row 1건 + 표시용 가공.
type inboxItem struct {
	ID             uuid.UUID
	Ko             string
	Type           string
	SuggestedType  string // 휴리스틱 추정 (운영자 hint).
	Confidence     float64
	SourceDomains  []string
	Spellings      []inboxSpelling
	Notes          string
	UpdatedAt      time.Time
	FirstSeenAt    time.Time
	Doppelgangers  []doppelganger
}

type inboxSpelling struct {
	Locale  string
	Value   string
	Source  string // canonical_X_source — 어디서 왔는지
	Mark    string // L/l/O/W/w/🔒
	Class   string // 뱃지 색 class
	Aliases []string
}

type doppelganger struct {
	ID       uuid.UUID
	Ko       string
	Type     string
	Status   string
	Distance string // "exact" | "similar"
}

type inboxStats struct {
	PendingTotal   int64
	AutoPromotable int64 // source_domains ≥ threshold
	SingleSource   int64
	OldestPending  *time.Time
}

func (s *Server) kdbInbox(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// 상단 통계.
	var stats inboxStats
	_ = s.pool.QueryRow(ctx, `
SELECT COUNT(*),
       COUNT(*) FILTER (WHERE COALESCE(array_length(source_domains,1),0) >= $1),
       COUNT(*) FILTER (WHERE COALESCE(array_length(source_domains,1),0) < $1),
       MIN(updated_at)
FROM kwave_entities WHERE status='candidate'`, candidateThreshold).Scan(
		&stats.PendingTotal, &stats.AutoPromotable, &stats.SingleSource, &stats.OldestPending)

	// 후보 목록 — 매체 많은 순 → 최신 순.
	rows, err := s.pool.Query(ctx, `
SELECT id, canonical_ko, entity_type::text, confidence,
       source_domains, COALESCE(notes,''), updated_at, created_at,
       COALESCE(canonical_en,''),    COALESCE(canonical_en_source,''),
       COALESCE(canonical_ja,''),    COALESCE(canonical_ja_source,''),
       COALESCE(canonical_vi,''),    COALESCE(canonical_vi_source,''),
       COALESCE(canonical_id,''),    COALESCE(canonical_id_source,''),
       COALESCE(canonical_es,''),    COALESCE(canonical_es_source,''),
       COALESCE(canonical_pt_br,''), COALESCE(canonical_pt_br_source,''),
       COALESCE(canonical_zh_hant,''), COALESCE(canonical_zh_hant_source,''),
       COALESCE(canonical_zh,''),    COALESCE(canonical_zh_source,''),
       aliases_en, aliases_ja, aliases_vi, aliases_id, aliases_es, aliases_pt_br, aliases_zh_hant, aliases_zh
FROM kwave_entities WHERE status='candidate'
ORDER BY COALESCE(array_length(source_domains,1),0) DESC, updated_at DESC
LIMIT 200`)
	if err != nil {
		s.renderError(w, r, "inbox list", err)
		return
	}
	defer rows.Close()

	items := []inboxItem{}
	kos := []string{}
	for rows.Next() {
		var it inboxItem
		// 9 locale value + source + aliases
		var en, enS, ja, jaS, vi, viS, id_, idS, es, esS, ptBr, ptBrS, zhHant, zhHantS, zh, zhS string
		var aliasesEn, aliasesJa, aliasesVi, aliasesID, aliasesEs, aliasesPtBr, aliasesZhHant, aliasesZh []string
		if err := rows.Scan(
			&it.ID, &it.Ko, &it.Type, &it.Confidence,
			&it.SourceDomains, &it.Notes, &it.UpdatedAt, &it.FirstSeenAt,
			&en, &enS, &ja, &jaS, &vi, &viS, &id_, &idS,
			&es, &esS, &ptBr, &ptBrS, &zhHant, &zhHantS, &zh, &zhS,
			&aliasesEn, &aliasesJa, &aliasesVi, &aliasesID, &aliasesEs, &aliasesPtBr, &aliasesZhHant, &aliasesZh,
		); err != nil {
			continue
		}
		add := func(loc, val, src string, aliases []string) {
			if val == "" {
				return
			}
			source := kdb.Source(src)
			it.Spellings = append(it.Spellings, inboxSpelling{
				Locale:  loc,
				Value:   val,
				Source:  src,
				Mark:    kdb.Mark(source),
				Class:   kdb.MarkClass(source),
				Aliases: aliases,
			})
		}
		add("en", en, enS, aliasesEn)
		add("ja", ja, jaS, aliasesJa)
		add("vi", vi, viS, aliasesVi)
		add("id", id_, idS, aliasesID)
		add("es", es, esS, aliasesEs)
		add("pt-br", ptBr, ptBrS, aliasesPtBr)
		add("zh-hant", zhHant, zhHantS, aliasesZhHant)
		add("zh", zh, zhS, aliasesZh)
		it.SuggestedType = suggestEntityType(it.Ko)
		items = append(items, it)
		kos = append(kos, it.Ko)
	}

	// 동명이인 가능성 — 같은 canonical_ko 가 status!='candidate' 로 이미 존재?
	// (UNIQUE constraint 때문에 정확히 같은 ko 는 사실상 발생 X 하지만 확인용)
	// 그리고 aliases_ko 에 같은 값이 있는 active entity 찾기.
	if len(kos) > 0 {
		doppelByKo := map[string][]doppelganger{}
		dRows, _ := s.pool.Query(ctx, `
SELECT e.canonical_ko AS hint, e.id, e.canonical_ko AS active_ko, e.entity_type::text, e.status
FROM kwave_entities e
WHERE e.status <> 'candidate'
  AND (e.canonical_ko = ANY($1::text[])
       OR EXISTS (SELECT 1 FROM unnest($1::text[]) k WHERE k = ANY(e.aliases_ko)))`, kos)
		if dRows != nil {
			defer dRows.Close()
			for dRows.Next() {
				var hint, activeKo string
				var d doppelganger
				if err := dRows.Scan(&hint, &d.ID, &activeKo, &d.Type, &d.Status); err == nil {
					d.Ko = activeKo
					if activeKo == hint {
						d.Distance = "exact"
					} else {
						d.Distance = "alias"
					}
					doppelByKo[hint] = append(doppelByKo[hint], d)
				}
			}
		}
		for i := range items {
			items[i].Doppelgangers = doppelByKo[items[i].Ko]
		}
	}

	// flash 메시지 (액션 결과).
	flash := r.URL.Query().Get("flash")

	s.render(w, r, "kdb_inbox.html", map[string]any{
		"title": "신규 후보 (Inbox)",
		"items": items,
		"stats": stats,
		"threshold":   candidateThreshold,
		"flash":       flash,
		"entityTypes": entityTypes,
		"page":        "/admin/kdb/inbox",
	})
}

// suggestEntityType — ko_hint 길이 휴리스틱.
// 2-4자 한글 = person 우선, 5+ 자 또는 영문 mixed = unknown 우선.
// 운영자가 항상 직접 확인하므로 hint 일 뿐.
func suggestEntityType(ko string) string {
	runes := []rune(strings.TrimSpace(ko))
	if len(runes) >= 2 && len(runes) <= 4 {
		return "person"
	}
	return ""
}

// --- inbox actions --------------------------------------------------------

func (s *Server) inboxPromote(w http.ResponseWriter, r *http.Request) {
	id, ok := parseInboxID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	// 운영자가 선택한 entity_type 검증 (운영자 결정 필수 — unknown 거부).
	if err := r.ParseForm(); err != nil {
		s.redirectInbox(w, r, "promote: bad form")
		return
	}
	chosenType := strings.TrimSpace(r.FormValue("entity_type"))
	if chosenType == "" || chosenType == "unknown" {
		s.redirectInbox(w, r, "promote 차단: entity_type 을 먼저 선택하라 (unknown 금지)")
		return
	}
	if !isValidEntityType(chosenType) {
		s.redirectInbox(w, r, "promote: 알 수 없는 entity_type — "+chosenType)
		return
	}

	// 현재 ko + 매체 수 가져옴.
	var ko string
	var sourceDomains []string
	if err := s.pool.QueryRow(ctx,
		`SELECT canonical_ko, source_domains FROM kwave_entities WHERE id=$1 AND status='candidate'`,
		id).Scan(&ko, &sourceDomains); err != nil {
		s.redirectInbox(w, r, "promote: 후보 없음 또는 이미 처리됨")
		return
	}

	conf := 0.60
	if len(sourceDomains) >= candidateThreshold {
		conf = 0.75
	}
	if _, err := s.pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='active',
       entity_type = $2::kwave_entity_type,
       confidence = GREATEST(confidence, $3::numeric),
       updated_at = now()
 WHERE id = $1 AND status='candidate'`, id, chosenType, conf); err != nil {
		s.redirectInbox(w, r, "promote 실패: "+err.Error())
		return
	}

	// 2) 인물이면 kwave_persons + kwave_entity_person_details 자동 sync.
	personNote := ""
	if chosenType == "person" {
		_, _ = s.pool.Exec(ctx, `
INSERT INTO kwave_persons (name_ko, primary_role, confidence, last_verified_at, created_at)
VALUES ($1, 'other'::person_role, 0.500, now(), now())
ON CONFLICT (name_ko) DO NOTHING`, ko)
		_, _ = s.pool.Exec(ctx, `
INSERT INTO kwave_entity_person_details (entity_id, primary_role)
VALUES ($1, 'other'::person_role)
ON CONFLICT (entity_id) DO NOTHING`, id)
		personNote = " · persons sync ✓"
	}

	// 3) enrich cascade — L2 MusicBrainz (해당 시) → L3 Wikidata → L4 Codex LLM fallback.
	// 빈 locale 만 채움. 우선순위 룰 적용 (L > l > O > W > w > codex).
	orch := enrich.New(s.pool)
	rep, _ := orch.Enrich(ctx, id)
	enrichNote := ""
	if rep != nil {
		filledCount := len(rep.Filled)
		stillEmpty := len(rep.StillEmpty)
		enrichNote = fmt.Sprintf(" · enrich [%s] %d 채움, %d 빈채로 남음", strings.Join(rep.LayersRun, "→"), filledCount, stillEmpty)
	}

	s.redirectInbox(w, r, "promote → "+chosenType+": "+ko+personNote+enrichNote)
}

// isValidEntityType — kwave_entity_type enum whitelist.
func isValidEntityType(t string) bool {
	for _, v := range entityTypes {
		if v == t {
			return true
		}
	}
	return false
}

func (s *Server) inboxReject(w http.ResponseWriter, r *http.Request) {
	id, ok := parseInboxID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var ko string
	if err := s.pool.QueryRow(ctx,
		`UPDATE kwave_entities SET status='rejected', confidence=0.000, updated_at=now()
		 WHERE id=$1 AND status='candidate' RETURNING canonical_ko`, id).Scan(&ko); err != nil {
		s.redirectInbox(w, r, "reject 실패: 후보 없음")
		return
	}
	s.redirectInbox(w, r, "reject: "+ko)
}

func (s *Server) inboxDefer(w http.ResponseWriter, r *http.Request) {
	id, ok := parseInboxID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	// updated_at 만 미래로 살짝 미뤄서 정렬 뒤로 보냄 — 사실상 no-op.
	_, _ = s.pool.Exec(ctx, `UPDATE kwave_entities SET updated_at = updated_at WHERE id=$1`, id)
	s.redirectInbox(w, r, "defer (no-op)")
}

func parseInboxID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return uuid.UUID{}, false
	}
	return id, true
}

func (s *Server) redirectInbox(w http.ResponseWriter, r *http.Request, flash string) {
	q := "/admin/kdb/inbox"
	if flash != "" {
		q += "?flash=" + urlEscape(flash)
	}
	http.Redirect(w, r, q, http.StatusSeeOther)
}

func urlEscape(s string) string {
	// 간단한 query escape — chi 가 들어왔으니 net/url 도 OK.
	return strings.ReplaceAll(strings.ReplaceAll(s, " ", "+"), "&", "%26")
}
