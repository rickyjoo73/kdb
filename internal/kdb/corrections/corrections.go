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
	"net/url"
	"os"
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


// stripInvisible — 제안값에서 **보이지 않는 문자**를 털어내고 공백을 정리한다.
//
// ★왜 필요한가 (2026-09-17 실측). 「오싹한 연애」 의 일본어 제목 정정이 8월 16일부터
// 한 달 넘게 운영자 대기로 묶여 있었다. 제안값은 «恋は命がけ» 로 완벽한 일본어인데,
// 끝에 U+FEFF(BOM/제로폭 공백)가 한 글자 붙어 있었다:
//
//	U+604B U+306F U+547D U+304C U+3051 U+FEFF
//
// IsValidSpellingForLocale("ja", …) 가 그 한 글자 때문에 거절했고, 원장에는
// «suggested 가 ja 문자셋 가드 미통과» 라고만 남았다. 사람이 화면에서 봐서는 절대
// 알 수 없다 — 눈에 안 보이는 문자다.
//
// 소비자는 웹페이지나 문서에서 제목을 복사해 보낸다. BOM·제로폭 조이너·방향 표식은
// 그 과정에서 흔히 딸려 온다. **내용이 맞는데 보이지 않는 문자 하나로 버리는 것은
// 오거부다.** 가드를 느슨하게 하는 게 아니라 입력을 정리하는 것이 맞다.
func stripInvisible(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		switch r {
		case '\uFEFF', // BOM / zero-width no-break space
			'\u200B', // zero-width space
			'\u200C', // zero-width non-joiner
			'\u200D', // zero-width joiner
			'\u200E', // left-to-right mark
			'\u200F', // right-to-left mark
			'\u2060', // word joiner
			'\u00AD': // soft hyphen
			return -1
		}
		return r
	}, s))
}

// Submit — 신고를 해석·판정·적재한다. reporter 는 신고자 식별(키 해시 prefix 등).
func (s *Service) Submit(ctx context.Context, req Request, reporter string) (Result, error) {
	loc := normLocale(req.Locale)
	col, ok := localeCol[loc]
	if !ok {
		return Result{}, fmt.Errorf("unsupported locale: %q", req.Locale)
	}
	suggested := stripInvisible(req.Suggested)
	if suggested == "" {
		return Result{}, errors.New("suggested required")
	}

	// 대상 entity 해석.
	eid, ko, err := s.resolveEntity(ctx, req)
	if err != nil {
		return Result{}, err
	}

	// ① 중복 제출 차단 — 같은 (entity_id, locale, suggested_value) 가 이미 pending/proposed/verifying 이면
	// 새 row 를 만들지 않고 기존 correction ID 를 돌려준다(client 재시도·중복 제출 방지).
	var existID int64
	dupErr := s.Pool.QueryRow(ctx, `
SELECT id FROM kwave_kdb_corrections
 WHERE entity_id=$1 AND locale=$2 AND suggested_value=$3
   AND status IN ('pending','proposed','verifying')
 ORDER BY id DESC LIMIT 1`, eid, loc, suggested).Scan(&existID)
	if dupErr == nil && existID > 0 {
		return Result{Status: "queued", EntityID: eid.String(), ID: existID,
			Resolution: "동일 교정신고가 이미 접수 중입니다."}, nil
	}

	// ② 문자셋 가드 — 실패해도 리젝하지 않는다(방침: 모든 교정신고를 존중·접수하고
	// 우리쪽이 검토). suggested 의 charset 가 의심스러우면 자동 반영만 막고, 신고 자체는
	// 운영자 검토 큐(pending)로 받는다. 클라가 올린 "현재값이 틀렸다"는 신호는 유효하다.
	if !kdb.IsValidSpellingForLocale(loc, suggested) {
		resn := "suggested 가 " + loc + " 문자셋 가드 미통과 — 자동반영 보류, 운영자 검토 접수"
		res := Result{Status: "queued", EntityID: eid.String(), Resolution: resn}
		res.ID, _ = s.record(ctx, eid, loc, req, suggested, reporter, "pending", resn)
		return res, nil
	}

	// ② 교차검증: Wikidata label/sitelink 와 정규화 일치?
	corroborated, evidence := s.corroborate(ctx, eid, ko, loc, suggested)

	// ③ Wikidata 교차검증 일치 → 강제 자동 반영.
	// Wikidata 검증 + charset 통과 = operator 승인에 준하는 외부 권위.
	// source priority(musicbrainz/rss-observation 등) 무관하게 덮어씀.
	// operator_locked 도 locale 값 교정은 허용(entity 삭제 보호와 무관).
	if corroborated {
		old, err := s.applyWikidataVerified(ctx, eid, col, suggested)
		if err != nil {
			return Result{}, err
		}
		resn := "Wikidata 교차검증 일치(" + evidence + ") — 강제 자동 반영"
		res := Result{Status: "auto_applied", EntityID: eid.String(), Resolution: resn, Value: suggested}
		req.Returned = old
		res.ID, _ = s.record(ctx, eid, loc, req, suggested, reporter, res.Status, resn)
		return res, nil
	}

	// ④ 빈칸 + 신뢰출처 직접등록(오너 방침: 출처 명시·입증 시 즉시반영). 현재값이 비어
	// 있어 덮어쓰기 위험이 없고(빈칸>틀린값 유지: 빈칸만 채움), 클라가 신뢰 도메인 출처를
	// 명시했으면 LLM 재판단 없이 즉시 반영. 출처는 correction 행 evidence_url + resolution
	// 에 도메인으로 기록(provenance 투명). Wikidata 미일치라도 권위 도메인이면 수용.
	if dom, okDom := trustedSourceDomain(req.EvidenceURL); okDom && os.Getenv("KDB_CORRECTION_TRUSTED_SOURCE") == "1" {
		if cur := current(s, ctx, eid, col); strings.TrimSpace(cur) == "" {
			applied, old, aerr := s.apply(ctx, eid, col, suggested, "correction-verified")
			if aerr == nil && applied {
				resn := "신뢰출처 직접등록(빈 locale 채움, 출처: " + dom + ") — 즉시 반영"
				req.Returned = old
				res := Result{Status: "auto_applied", EntityID: eid.String(), Resolution: resn, Value: suggested}
				res.ID, _ = s.record(ctx, eid, loc, req, suggested, reporter, res.Status, resn)
				return res, nil
			}
		}
	}

	// ⑤ Wikidata 로 판정 안 됨 → codex 검증을 비동기로(HTTP 타임아웃 회피, codex 가
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

// trustedCorrectionDomains — 클라이언트 정정의 evidence_url 이 권위 출처일 때 빈 locale 을
// LLM 재판단 없이 즉시 채우기 위한 신뢰 도메인 allowlist(오너 방침: 출처 명시·입증 시 즉시
// 반영). 공식 방송사·위키·권위 DB·주요 K-미디어. 소비자 요청 시 확장.
var trustedCorrectionDomains = map[string]bool{
	"kbs.co.kr": true, "imbc.com": true, "mbc.co.kr": true, "sbs.co.kr": true,
	"jtbc.co.kr": true, "tving.com": true, "wavve.com": true, "cjenm.com": true,
	"mnetplus.world": true, "channelmnet.com": true, "kocowa.com": true,
	"wikipedia.org": true, "wikidata.org": true, "themoviedb.org": true,
	"hancinema.net": true, "mydramalist.com": true,
}

// trustedSourceDomain — evidence_url 호스트를 정규화해 allowlist(또는 그 서브도메인)면
// (등록도메인, true). 빈 URL/미신뢰는 ("", false). 예: program.kbs.co.kr → "kbs.co.kr".
func trustedSourceDomain(rawURL string) (string, bool) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", false
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return "", false
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	for dom := range trustedCorrectionDomains {
		if host == dom || strings.HasSuffix(host, "."+dom) {
			return dom, true
		}
	}
	return "", false
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
// 정규화 일치하는지. **대상이 가진 QID 로만** 본다 — 이름 검색은 쓰지 않는다.
// 이름이 같다는 것은 같은 대상이라는 증거가 아니다(아래 실증 주석).
func (s *Service) corroborate(ctx context.Context, eid uuid.UUID, ko, locale, suggested string) (bool, string) {
	// pool 이 없으면 **그 대상이 어느 QID 를 갖는지 볼 수가 없다.** 앵커를 못 보면
	// 교차검증도 없다 — 이름 검색으로 메우지 않는다(그 메움이 에반 사고를 만들었다).
	if s.WD == nil || s.Pool == nil {
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
		// ★QID 미보유 대상은 **교차검증하지 않는다**(2026-09-15 실측으로 막았다).
		//
		//   종전엔 ko 로 위키데이터를 검색해 그 결과를 "외부 권위 교차검증"으로 썼다.
		//   SearchAndFetch 가 ko 정규화 일치일 때만 돌려주니 안전하다고 봤는데, **이름이
		//   같다는 것은 같은 대상이라는 증거가 아니다** — 그것이 동명 함정 그 자체다.
		//
		//   실증: 우리 `에반`(외부ID 0건)에 신고가 들어오자 이름 검색이 Q105717901 을
		//   찾았다. 그 항목은 한국어 라벨이 정확히 `에반` 인데 실제로는 ENHYPEN 희승이다.
		//   그래서 희승의 라벨이 `강제 자동 반영`으로 에반에 박혔다:
		//     en=Heeseung · ja=ヒスン · es=`Heeseung love`
		//   살아 있는 사람에게 다른 사람 이름이 authoritative 로 나가고 있었다.
		//
		//   앵커가 없으면 "그 항목이 이 대상"이라는 다리가 없다. 다리 없이 건너가지 않는다.
		//   신고는 버리지 않는다 — 아래 codex 검증/운영자 큐로 간다(모든 신고를 접수한다).
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

// applyWikidataVerified — Wikidata 교차검증 완료 교정 강제 적용.
// can_replace_canonical 가드 우회 — Wikidata 검증은 외부 권위(musicbrainz·rss·operator_locked
// 무관). operator_locked 는 entity 삭제 보호용이지 locale 값 교정 차단용이 아님.
// charset guard 는 호출 전 Submit() 에서 이미 통과.
func (s *Service) applyWikidataVerified(ctx context.Context, eid uuid.UUID, col, val string) (string, error) {
	var old string
	q := fmt.Sprintf(`
WITH old AS (SELECT COALESCE(%[1]s,'') v FROM kwave_entities WHERE id=$1)
UPDATE kwave_entities
   SET %[1]s=$2, %[1]s_source='correction-verified', updated_at=now()
 WHERE id=$1
 RETURNING (SELECT v FROM old)`, col)
	if err := s.Pool.QueryRow(ctx, q, eid, val).Scan(&old); err != nil {
		return "", err
	}
	return old, nil
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
  (entity_id, locale, returned_value, suggested_value, evidence_url, reporter, reason, status, resolution,
   resolved_at, verifying_since)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,
        CASE WHEN $8 IN ('pending','verifying','proposed') THEN NULL ELSE now() END,
        -- 검증에 들어간 시각. ReapStale 이 «갇힌 행》을 이 값으로 가린다(mig 0149).
        CASE WHEN $8 = 'verifying' THEN now() ELSE NULL END)
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
	// 보호되어 미적용이면 status='pending'(미해결) → resolved_at 은 NULL 유지(불변식:
	// pending/verifying/proposed 는 resolved_at IS NULL). 적용되면 approved(종결).
	dbStatus := map[bool]string{true: "approved", false: "pending"}[applied]
	_, _ = s.Pool.Exec(ctx, `UPDATE kwave_kdb_corrections
		SET status=$2, returned_value=$3, resolution=$4,
		    resolved_at = CASE WHEN $2 IN ('pending','verifying','proposed') THEN NULL ELSE now() END
		 WHERE id=$1`, id, dbStatus, old, resn)
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
