// Package corrections — 외부 소비자의 locale 표기 정정 신고를 받아 근거기반으로
// 자동 반영하거나 운영자 심사 큐에 적재한다.
//
// 신뢰 모델(운영자 선택): 근거기반 자동 + 미달은 큐.
//   - 자동 반영 조건(ALL): ① suggested 가 locale 문자셋 가드 통과, ② 현재 값이
//     교체 가능(can_replace_canonical 으로 codex-fallback/wikipedia/빈칸 등 저신뢰만,
//     operator-locked/consensus/external/rss 는 보호), ③ Wikidata 가 독립 확인
//     (label/sitelink 정규화 일치). → status=auto_applied, source=wikidata-label.
//   - 그 외 → status=pending(운영자 심사). suggested 가 가드 실패면 rejected.
//
// 단일 클라이언트 주장만으로는 절대 자동 반영 안 됨(권위 외부소스 교차검증 필수).
package corrections

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb"
	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
)

// supportedLocales — 정정 가능한 locale 과 canonical 컬럼 매핑.
var localeCol = map[string]string{
	"en": "canonical_en", "ja": "canonical_ja", "vi": "canonical_vi",
	"es": "canonical_es", "id": "canonical_id", "pt_br": "canonical_pt_br",
	"zh": "canonical_zh", "zh_hant": "canonical_zh_hant",
}

// Request — 정정 신고 1건.
type Request struct {
	EntityID    string `json:"entity_id,omitempty"` // 둘 중 하나 필수
	Ko          string `json:"ko,omitempty"`        // entity_id 없으면 ko + locale 로 해석
	Disambig    string `json:"disambig,omitempty"`  // 동명이인 구분(선택)
	Locale      string `json:"locale"`
	Returned    string `json:"returned,omitempty"`  // 클라이언트가 받은(틀린) 값(감사용)
	Suggested   string `json:"suggested"`
	EvidenceURL string `json:"evidence_url,omitempty"`
	Reason      string `json:"reason,omitempty"`
	// 양방향 확인: KDB 수정안(proposed)에 대한 클라이언트 응답.
	ConfirmID int64 `json:"confirm_id,omitempty"`
	Accept    bool  `json:"accept,omitempty"`
}

// Result — 처리 결과.
type Result struct {
	Status     string `json:"status"` // auto_applied | proposed | queued | rejected
	Resolution string `json:"resolution"`
	EntityID   string `json:"entity_id,omitempty"`
	ID         int64  `json:"correction_id,omitempty"`
	// 양방향: KDB 의 판단·수정안. status=proposed 면 client 가 이 값을 확인(confirm)한다.
	Verdict string `json:"verdict,omitempty"`
	Value   string `json:"value,omitempty"` // 반영됐거나(applied) 회신하는(proposed) 값
}

// wikidataLookup — 교차검증에 필요한 Wikidata 메서드만 추상화(테스트 fake 주입).
type wikidataLookup interface {
	Fetch(ctx context.Context, qid string) (*wikidata.Entity, error)
	SearchAndFetch(ctx context.Context, query string) (*wikidata.Entity, *wikidata.Candidate, error)
}

// Service — 정정 처리기.
type Service struct {
	Pool  *pgxpool.Pool
	WD    wikidataLookup // nil 이면 Wikidata 교차검증 생략
	Judge judge          // codex 검증(양방향). nil 이면 Wikidata 미달분은 곧장 큐
}

// Submit — 신고를 해석·판정·적재한다. reporter 는 신고자 식별(키 해시 prefix 등).
func (s *Service) Submit(ctx context.Context, req Request, reporter string) (Result, error) {
	loc := normLocale(req.Locale)
	col, ok := localeCol[loc]
	if !ok {
		return Result{}, fmt.Errorf("unsupported locale: %q", req.Locale)
	}
	suggested := strings.TrimSpace(req.Suggested)
	if suggested == "" {
		return Result{}, errors.New("suggested required")
	}

	// 대상 entity 해석.
	eid, ko, err := s.resolveEntity(ctx, req)
	if err != nil {
		return Result{}, err
	}

	// ① 문자셋 가드 — 실패하면 즉시 reject(잘못된 입력, 큐도 안 탐).
	if !kdb.IsValidSpellingForLocale(loc, suggested) {
		res := Result{Status: "rejected", EntityID: eid.String(),
			Resolution: "suggested 가 " + loc + " 문자셋 가드를 통과하지 못함"}
		res.ID, _ = s.record(ctx, eid, loc, req, suggested, reporter, res.Status, res.Resolution)
		return res, nil
	}

	// ② 교차검증: Wikidata label/sitelink 와 정규화 일치?
	corroborated, evidence := s.corroborate(ctx, eid, ko, loc, suggested)

	// ③ Wikidata 교차검증 일치 → 자동 반영(최고 신뢰).
	if corroborated {
		applied, old, err := s.apply(ctx, eid, col, suggested, "wikidata-label")
		if err != nil {
			return Result{}, err
		}
		if applied {
			resn := "Wikidata 교차검증 일치(" + evidence + ") + 저신뢰 값 교체"
			res := Result{Status: "auto_applied", EntityID: eid.String(), Resolution: resn, Value: suggested}
			req.Returned = old
			res.ID, _ = s.record(ctx, eid, loc, req, suggested, reporter, res.Status, resn)
			return res, nil
		}
		resn := "Wikidata 일치하나 현재 값이 보호됨(operator-locked/검증 source) — 운영자 확인 필요"
		res := Result{Status: "queued", EntityID: eid.String(), Resolution: resn}
		res.ID, _ = s.record(ctx, eid, loc, req, suggested, reporter, "pending", resn)
		return res, nil
	}

	// ④ Wikidata 로 판정 안 됨 → codex 검증을 비동기로(HTTP 타임아웃 회피, codex 가
	// 끝까지 일하게). 즉시 verifying 응답 + correction_id; 결과는 GET 으로 확인.
	if s.Judge != nil {
		cur := current(s, ctx, eid, col)
		etype := ""
		_ = s.Pool.QueryRow(ctx, `SELECT entity_type::text FROM kwave_entities WHERE id=$1`, eid).Scan(&etype)
		id, err := s.record(ctx, eid, loc, req, suggested, reporter, "verifying",
			"KDB codex 검증 중 — GET /v1/corrections/{id} 로 결과 확인")
		if err != nil {
			return Result{}, err
		}
		go s.verifyAsync(id, eid, ko, etype, loc, col, cur, suggested)
		return Result{Status: "verifying", EntityID: eid.String(), ID: id,
			Resolution: "KDB 가 검증 중입니다. correction_id 로 결과를 확인하세요."}, nil
	}

	// Judge 없음 → 운영자 큐.
	resn := "권위 외부소스 교차검증 미달 — 운영자 심사 대기"
	res := Result{Status: "queued", EntityID: eid.String(), Resolution: resn}
	res.ID, _ = s.record(ctx, eid, loc, req, suggested, reporter, "pending", resn)
	return res, nil
}

// current — entity 의 현재 locale 값(검증 프롬프트 입력용).
func current(s *Service, ctx context.Context, eid uuid.UUID, col string) string {
	var v string
	_ = s.Pool.QueryRow(ctx, "SELECT COALESCE("+col+",'') FROM kwave_entities WHERE id=$1", eid).Scan(&v)
	return v
}

// resolveEntity — entity_id 우선, 없으면 ko(+disambig) 로 active 1건 해석.
func (s *Service) resolveEntity(ctx context.Context, req Request) (uuid.UUID, string, error) {
	if id := strings.TrimSpace(req.EntityID); id != "" {
		eid, err := uuid.Parse(id)
		if err != nil {
			return uuid.Nil, "", fmt.Errorf("invalid entity_id")
		}
		var ko string
		if err := s.Pool.QueryRow(ctx,
			`SELECT canonical_ko FROM kwave_entities WHERE id=$1`, eid).Scan(&ko); err != nil {
			return uuid.Nil, "", fmt.Errorf("entity not found")
		}
		return eid, ko, nil
	}
	ko := strings.TrimSpace(req.Ko)
	if ko == "" {
		return uuid.Nil, "", errors.New("entity_id or ko required")
	}
	// disambig 미지정이면 동명이인이 여러 건일 때 모호 → 에러(클라이언트가 좁히게).
	rows, err := s.Pool.Query(ctx, `
SELECT id FROM kwave_entities
 WHERE canonical_ko=$1 AND status='active'
   AND ($2='' OR COALESCE(disambig,'')=$2)`, ko, strings.TrimSpace(req.Disambig))
	if err != nil {
		return uuid.Nil, "", err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	switch len(ids) {
	case 0:
		return uuid.Nil, "", fmt.Errorf("no active entity for ko=%q", ko)
	case 1:
		return ids[0], ko, nil
	default:
		return uuid.Nil, "", fmt.Errorf("ambiguous: %d entities share ko=%q — pass entity_id or disambig", len(ids), ko)
	}
}

// corroborate — suggested 가 그 entity 의 Wikidata label/sitelink(해당 locale)와
// 정규화 일치하는지. QID 는 external_refs 우선, 없으면 ko 로 SearchAndFetch.
func (s *Service) corroborate(ctx context.Context, eid uuid.UUID, ko, locale, suggested string) (bool, string) {
	if s.WD == nil {
		return false, ""
	}
	var ent *wikidata.Entity
	// external_refs 의 QID 우선(이미 확정 매칭).
	var qid string
	_ = s.Pool.QueryRow(ctx,
		`SELECT external_id FROM kwave_entity_external_refs
		  WHERE entity_id=$1 AND provider='wikidata' LIMIT 1`, eid).Scan(&qid)
	if strings.TrimSpace(qid) != "" {
		if e, err := s.WD.Fetch(ctx, qid); err == nil {
			ent = e
		}
	}
	if ent == nil {
		// QID 미보유 → ko 로 검색(SearchAndFetch 가 ko 정규화 일치 시에만 반환 — 오매칭 가드).
		if e, _, err := s.WD.SearchAndFetch(ctx, ko); err == nil {
			ent = e
		}
	}
	if ent == nil {
		return false, ""
	}
	want := normName(suggested)
	if v := ent.Labels[locale]; v != "" && normName(v) == want {
		return true, "label:" + ent.QID
	}
	// sitelink 문서 제목(각 언어판 통용 표기)도 비교.
	if wiki := localeWiki[locale]; wiki != "" {
		if t := ent.SiteTitles[wiki]; t != "" && normName(stripParen(t)) == want {
			return true, "sitelink:" + ent.QID
		}
	}
	return false, ""
}

// apply — value 를 canonical 컬럼에 쓴다(source 명시). can_replace_canonical 가드로
// operator/고우선순위 source 는 보존. 반환: (적용됨, 원값).
func (s *Service) apply(ctx context.Context, eid uuid.UUID, col, value, source string) (bool, string, error) {
	var old string
	q := fmt.Sprintf(`
WITH old AS (SELECT COALESCE(%[1]s,'') v FROM kwave_entities WHERE id=$1)
UPDATE kwave_entities
   SET %[1]s=$2, %[1]s_source=$3, updated_at=now()
 WHERE id=$1
   AND (%[1]s IS NULL OR %[1]s=''
        OR can_replace_canonical(operator_locked, COALESCE(%[1]s_source,''), $3))
 RETURNING (SELECT v FROM old)`, col)
	err := s.Pool.QueryRow(ctx, q, eid, value, source).Scan(&old)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, "", nil // 보호되어 미적용
	}
	if err != nil {
		return false, "", err
	}
	return true, old, nil
}

// record — corrections 큐에 1건 적재. 반환 id.
func (s *Service) record(ctx context.Context, eid uuid.UUID, locale string, req Request, suggested, reporter, status, resolution string) (int64, error) {
	var id int64
	err := s.Pool.QueryRow(ctx, `
INSERT INTO kwave_kdb_corrections
  (entity_id, locale, returned_value, suggested_value, evidence_url, reporter, reason, status, resolution, resolved_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9, CASE WHEN $8 IN ('pending','verifying','proposed') THEN NULL ELSE now() END)
RETURNING id`,
		eid, locale, strings.TrimSpace(req.Returned), suggested,
		strings.TrimSpace(req.EvidenceURL), reporter, strings.TrimSpace(req.Reason),
		status, resolution).Scan(&id)
	return id, err
}

// recordProposed — KDB 수정안(proposed)을 큐에 적재. status='proposed' + proposed_value.
func (s *Service) recordProposed(ctx context.Context, eid uuid.UUID, locale string, req Request, suggested, proposed, reporter, resolution string) (int64, error) {
	var id int64
	err := s.Pool.QueryRow(ctx, `
INSERT INTO kwave_kdb_corrections
  (entity_id, locale, returned_value, suggested_value, proposed_value, evidence_url, reporter, reason, status, resolution)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'proposed',$9)
RETURNING id`,
		eid, locale, strings.TrimSpace(req.Returned), suggested, proposed,
		strings.TrimSpace(req.EvidenceURL), reporter, strings.TrimSpace(req.Reason), resolution).Scan(&id)
	return id, err
}

// Confirm — 클라이언트가 KDB 수정안(proposed)을 확인한다. accept=true 면 proposed_value
// 를 반영(source=correction-verified), false 면 기각. 양방향 루프의 마지막 단계.
func (s *Service) Confirm(ctx context.Context, id int64, accept bool, reporter string) (Result, error) {
	var eid uuid.UUID
	var locale, proposed string
	err := s.Pool.QueryRow(ctx,
		`SELECT entity_id, locale, proposed_value FROM kwave_kdb_corrections
		  WHERE id=$1 AND status='proposed'`, id).Scan(&eid, &locale, &proposed)
	if errors.Is(err, pgx.ErrNoRows) {
		return Result{}, fmt.Errorf("correction %d not awaiting confirmation", id)
	}
	if err != nil {
		return Result{}, err
	}
	if !accept {
		_, _ = s.Pool.Exec(ctx, `UPDATE kwave_kdb_corrections SET status='rejected',
			resolution='클라이언트가 수정안 거부', resolved_at=now() WHERE id=$1`, id)
		return Result{Status: "rejected", ID: id, EntityID: eid.String(), Resolution: "클라이언트가 수정안 거부"}, nil
	}
	col, ok := localeCol[normLocale(locale)]
	if !ok || !kdb.IsValidSpellingForLocale(normLocale(locale), proposed) {
		return Result{}, fmt.Errorf("invalid proposed value/locale")
	}
	applied, old, err := s.apply(ctx, eid, col, proposed, "correction-verified")
	if err != nil {
		return Result{}, err
	}
	status, resn := "auto_applied", "클라이언트 확인 후 KDB 수정안 반영"
	if !applied {
		status, resn = "queued", "확인됐으나 현재 값이 보호됨 — 운영자 심사"
	}
	_, _ = s.Pool.Exec(ctx, `UPDATE kwave_kdb_corrections
		SET status=$2, returned_value=$3, resolution=$4, resolved_at=now() WHERE id=$1`,
		id, map[bool]string{true: "approved", false: "pending"}[applied], old, resn)
	return Result{Status: status, ID: id, EntityID: eid.String(), Resolution: resn, Value: proposed}, nil
}

func normLocale(s string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), "-", "_")
}

// normName — wikidata.normalizeName 과 동일 규칙(비교 일관성).
func normName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch r {
		case ' ', '\t', '·', '・', '-', '.', '_', '\'', '"', ',':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// stripParen — 위키 문서 제목의 disambiguation 괄호 제거("박보검 (배우)" → "박보검").
func stripParen(s string) string {
	if i := strings.IndexAny(s, "(（"); i > 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

// localeWiki — KDB locale → sitelink wiki code.
var localeWiki = map[string]string{
	"en": "enwiki", "ja": "jawiki", "vi": "viwiki", "es": "eswiki",
	"id": "idwiki", "pt_br": "ptwiki", "zh": "zhwiki", "zh_hant": "zhwiki",
}
