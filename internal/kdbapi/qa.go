package kdbapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// qa.go — 엔티티 QA 파이프라인(정화+보강) 의 KDB ↔ 서버22 워커 통신 엔드포인트.
// 설계: docs/KDB_ENTITY_QA_DESIGN.md. 워커는 /v1/qa/work 로 작업을 받아 Gemma
// 다회투표·검색·gpt5.5 판정 후 /v1/qa/result 로 결과를 보낸다. write scope 필요.

// QAEntity — 워커가 한 엔티티를 판정/보강하는 데 필요한 정보.
type QAEntity struct {
	ID           string            `json:"id"`
	Ko           string            `json:"ko"`
	EntityType   string            `json:"entity_type"`
	PrimaryRole  string            `json:"primary_role,omitempty"`
	NotableWorks []string          `json:"notable_works,omitempty"`
	Groups       []string          `json:"groups,omitempty"`
	Locales      map[string]string `json:"locales"` // loc → canonical 값('' = 빈칸)
	Sources      map[string]string `json:"sources"` // loc → source
	Priority     string            `json:"priority"` // research_queue|unfilled|rest
}

var qaLocales = []string{"en", "ja", "vi", "id", "es", "pt_br", "zh", "zh_hant"}

// QAWork — QA 대상 active 엔티티를 우선순위(소비자요청>빈칸>나머지) 순으로 batch 건 반환.
// 전수 처리 — updated_at 오래된 순으로 돌면 반복 호출 시 전 엔티티를 커버.
// priFilter="unfilled" 면 외국어 빈칸 있는 것만(gap-fill 워커용).
func (s *Store) QAWork(ctx context.Context, limit int, priFilter string) ([]QAEntity, error) {
	where := "e.status='active' AND e.operator_locked = false"
	if priFilter == "unfilled" {
		where += ` AND (e.canonical_ja='' OR e.canonical_vi='' OR e.canonical_zh_hant='' OR e.canonical_es='' OR e.canonical_pt_br='')`
	}
	rows, err := s.Pool.Query(ctx, `
SELECT e.id::text, e.canonical_ko, e.entity_type::text,
       COALESCE(d.primary_role::text,''), COALESCE(d.notable_works,'{}'), COALESCE(d.groups,'{}'),
       COALESCE(e.canonical_en,''),  COALESCE(e.canonical_en_source,''),
       COALESCE(e.canonical_ja,''),  COALESCE(e.canonical_ja_source,''),
       COALESCE(e.canonical_vi,''),  COALESCE(e.canonical_vi_source,''),
       COALESCE(e.canonical_id,''),  COALESCE(e.canonical_id_source,''),
       COALESCE(e.canonical_es,''),  COALESCE(e.canonical_es_source,''),
       COALESCE(e.canonical_pt_br,''),  COALESCE(e.canonical_pt_br_source,''),
       COALESCE(e.canonical_zh,''),  COALESCE(e.canonical_zh_source,''),
       COALESCE(e.canonical_zh_hant,''),  COALESCE(e.canonical_zh_hant_source,''),
       CASE WHEN rq.entity_ko IS NOT NULL THEN 'research_queue'
            WHEN e.canonical_ja='' OR e.canonical_vi='' OR e.canonical_zh_hant='' OR e.canonical_es=''
                 OR e.canonical_en='' OR e.canonical_id='' OR e.canonical_pt_br='' OR e.canonical_zh=''
              THEN 'unfilled'
            ELSE 'rest' END AS pri
FROM kwave_entities e
LEFT JOIN kwave_entity_person_details d ON d.entity_id = e.id
LEFT JOIN (SELECT DISTINCT entity_ko FROM kwave_entity_research_queue) rq ON rq.entity_ko = e.canonical_ko
WHERE `+where+`
ORDER BY (CASE WHEN rq.entity_ko IS NOT NULL THEN 0
               WHEN e.canonical_ja='' OR e.canonical_vi='' OR e.canonical_zh_hant='' OR e.canonical_es=''
                    OR e.canonical_en='' OR e.canonical_id='' OR e.canonical_pt_br='' OR e.canonical_zh=''
                 THEN 1 ELSE 2 END),
         e.updated_at ASC
LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []QAEntity{}
	for rows.Next() {
		var e QAEntity
		e.Locales = map[string]string{}
		e.Sources = map[string]string{}
		var en, enS, ja, jaS, vi, viS, id, idS, es, esS, pt, ptS, zh, zhS, zhh, zhhS string
		if err := rows.Scan(&e.ID, &e.Ko, &e.EntityType, &e.PrimaryRole, &e.NotableWorks, &e.Groups,
			&en, &enS, &ja, &jaS, &vi, &viS, &id, &idS, &es, &esS, &pt, &ptS, &zh, &zhS, &zhh, &zhhS,
			&e.Priority); err != nil {
			return nil, err
		}
		vals := map[string]string{"en": en, "ja": ja, "vi": vi, "id": id, "es": es, "pt_br": pt, "zh": zh, "zh_hant": zhh}
		srcs := map[string]string{"en": enS, "ja": jaS, "vi": viS, "id": idS, "es": esS, "pt_br": ptS, "zh": zhS, "zh_hant": zhhS}
		for _, loc := range qaLocales {
			e.Locales[loc] = vals[loc]
			e.Sources[loc] = srcs[loc]
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// qaWork — GET /v1/qa/work?limit=N (write scope). 서버22 워커가 작업을 받는다.
func (h *handler) qaWork(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	items, err := h.store.QAWork(r.Context(), limit, r.URL.Query().Get("pri"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "qa work query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

// QAResultRequest — 서버22 워커가 한 엔티티 판정+보강 결과를 보낸다.
type QAResultRequest struct {
	EntityID  string   `json:"entity_id"`
	Ko        string   `json:"ko"`
	KoVerdict string   `json:"ko_verdict"` // real_k|contaminated|out_of_scope|uncertain
	Agree     int      `json:"agree"`      // 다회투표 동의 수
	Total     int      `json:"total"`      // 총 투표 수
	Evidence  string   `json:"evidence"`
	Fills     []QAFill `json:"fills,omitempty"` // 보강: 빈 locale 검색추출값
}

// QAFill — 워커가 검색으로 찾은 현지표기. native=현지문자형(canonical 후보), latin=라틴형(alias).
type QAFill struct {
	Locale string `json:"locale"`
	Value  string `json:"value"`
	Kind   string `json:"kind"` // native|latin
}

// qaColumns — locale → (canonical, aliases, source) 컬럼명.
func qaColumns(loc string) (string, string, string) {
	switch loc {
	case "en", "ja", "vi", "id", "es", "zh":
		return "canonical_" + loc, "aliases_" + loc, "canonical_" + loc + "_source"
	case "pt_br":
		return "canonical_pt_br", "aliases_pt_br", "canonical_pt_br_source"
	case "zh_hant":
		return "canonical_zh_hant", "aliases_zh_hant", "canonical_zh_hant_source"
	}
	return "", "", ""
}

func hasRange(s string, lo, hi rune) bool {
	for _, r := range s {
		if r >= lo && r <= hi {
			return true
		}
	}
	return false
}

// qaCharsetOK — locale 문자셋 가드(오염 차단). 한글은 어느 외국어칸에도 금지.
func qaCharsetOK(loc, v string) bool {
	if v == "" || hasRange(v, '가', '힣') {
		return false
	}
	han := hasRange(v, '一', '鿿')
	kana := hasRange(v, 'ぁ', 'ヿ')
	latin := false
	for _, r := range v {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			latin = true
			break
		}
	}
	switch loc {
	case "ja":
		return kana || han
	case "zh", "zh_hant":
		return han && !kana
	case "en", "vi", "id", "es", "pt_br":
		return latin && !han && !kana
	}
	return false
}

// applyQAFills — 워커가 검색으로 찾은 fills 를 빈 locale 에만 영속화한다(기존값 보존).
// native(현지문자·char-set 적합) → canonical(빈칸만, source=local-search·최저우선이라
// TMDb/Wikidata 가 나중에 업그레이드 가능). latin → aliases append. 반환=적용 수.
func (s *Store) applyQAFills(ctx context.Context, id string, fills []QAFill) int {
	n := 0
	for _, f := range fills {
		canonCol, aliasCol, srcCol := qaColumns(f.Locale)
		v := strings.TrimSpace(f.Value)
		if canonCol == "" || v == "" || !qaCharsetOK(f.Locale, v) {
			continue
		}
		// 설명형 오염 backstop: 제목/이름은 짧다. 너무 길거나(>40자) 다른 제목을 감싼
		// 괄호(《》【】)·설명형 마커가 있으면 거부(예: "《精武门》电影插曲"=정무문 삽입곡 설명).
		if len([]rune(v)) > 40 || strings.ContainsAny(v, "《》【】") {
			continue
		}
		// canonical 은 빈칸일 때만(기존값·권위소스 보존).
		isNativeScript := f.Kind == "native" || hasRange(v, '一', '鿿') || hasRange(v, 'ぁ', 'ヿ')
		if isNativeScript || f.Locale == "en" || f.Locale == "vi" || f.Locale == "es" || f.Locale == "id" || f.Locale == "pt_br" {
			tag, err := s.Pool.Exec(ctx,
				`UPDATE kwave_entities SET `+canonCol+`=$2, `+srcCol+`='local-search', updated_at=now()
				  WHERE id=$1 AND (`+canonCol+` IS NULL OR `+canonCol+`='')`, id, v)
			if err == nil && tag.RowsAffected() > 0 {
				n++
				continue
			}
		}
		// canonical 못 채우면(이미 값 있음·라틴이 한자칸 등) alias 로 보존.
		tag, err := s.Pool.Exec(ctx,
			`UPDATE kwave_entities SET `+aliasCol+`=array_append(COALESCE(`+aliasCol+`,'{}'),$2), updated_at=now()
			  WHERE id=$1 AND $2 <> ALL(COALESCE(`+aliasCol+`,'{}'))`, id, v)
		if err == nil && tag.RowsAffected() > 0 {
			n++
		}
	}
	return n
}

// QAApplyResult — 판정 적용. 자동화 정책(확정됨): 만장일치 오염만 자동 reject,
// 경계(표 갈림)·범위밖은 운영자 검토 플래그(자동삭제 X). 모든 변경은 notes 마커로
// 되돌리기 가능. 반환=수행 action.
func (s *Store) QAApplyResult(ctx context.Context, req QAResultRequest) (string, error) {
	strong := req.Total >= 3 && req.Agree == req.Total // 만장일치
	ev := req.Evidence
	if len(ev) > 300 {
		ev = ev[:300]
	}
	switch req.KoVerdict {
	case "contaminated":
		// ★기본 안전정책: 자동삭제 안 함 — 확신 오염도 운영자 플래그(notes, 비파괴).
		// 자동 reject 는 운영자가 KDB_QA_AUTOREJECT=1 로 명시 허용 + 만장일치일 때만.
		if strong && os.Getenv("KDB_QA_AUTOREJECT") == "1" {
			_, err := s.Pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='rejected', confidence=0.000,
       notes = left(COALESCE(NULLIF(notes,'')||' · ','')||'[kdb:qa-reject] 오염 만장일치 — '||$2, 1500),
       updated_at = now()
 WHERE id=$1 AND status='active' AND operator_locked=false`, req.EntityID, ev)
			return "rejected", err
		}
		return s.flag(ctx, req.EntityID, "[kdb:qa-review] 오염 의심 — "+ev)
	case "out_of_scope":
		return s.flag(ctx, req.EntityID, "[kdb:scope-review] K-범위밖 의심 — "+ev)
	case "uncertain":
		return s.flag(ctx, req.EntityID, "[kdb:qa-review] 불확실 — "+ev)
	default: // real_k 등 — 유지 + 보강 fills 적용(빈칸만)
		if nf := s.applyQAFills(ctx, req.EntityID, req.Fills); nf > 0 {
			return fmt.Sprintf("kept+filled(%d)", nf), nil
		}
		return "kept", nil
	}
}

// flag — notes 에 검토 마커 append(멱등). 자동삭제·상태변경 없이 운영자 검토큐에만 노출.
func (s *Store) flag(ctx context.Context, id, marker string) (string, error) {
	tag, err := s.Pool.Exec(ctx, `
UPDATE kwave_entities
   SET notes = CASE WHEN POSITION($2 IN COALESCE(notes,''))>0 THEN notes
                    ELSE left(COALESCE(NULLIF(notes,'')||' · ','')||$2, 1500) END,
       updated_at = now()
 WHERE id=$1 AND status='active'`, id, marker[:min(len(marker), 60)])
	if err != nil || tag.RowsAffected() == 0 {
		return "noop", err
	}
	return "flagged", nil
}

// qaResult — POST /v1/qa/result (write scope). 워커가 판정 결과를 보낸다.
func (h *handler) qaResult(w http.ResponseWriter, r *http.Request) {
	var req QAResultRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad json")
		return
	}
	if req.EntityID == "" || req.KoVerdict == "" {
		writeError(w, http.StatusBadRequest, "entity_id and ko_verdict required")
		return
	}
	action, err := h.store.QAApplyResult(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "apply failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"action": action})
}
