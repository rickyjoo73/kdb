package kdbadmin

// handlers_anchor_review — 앵커 판정 검수 화면.
//
// ★왜 화면이 필요한가 (2026-09-15).
//   활성 원장의 wikidata 앵커 590건이 유형과 어긋났다. 그중 자동으로 고친 것은
//   근거가 한 방향인 것뿐이다 — 앵커 철회 89(이름 항목) · 유형 교정 104.
//   나머지는 **근거만으로 못 가른다.** 실측으로 정확히 갈린 자리:
//
//     황해     en `The Yellow Sea`       QID = 배우      → 유형 맞고 앵커 틀림
//     덕혜옹주  en `The Last Princess`    QID = 실제 옹주  → 유형 맞고 앵커 틀림
//     씨스타19  en `Sistar19`             QID = 걸그룹    → 앵커 맞고 유형 틀림
//     강지환    character                 QID = 배우      → 배역에 실존 인물 앵커
//
//   P31 은 **그 QID 가 무엇인지**만 말한다. **우리 행이 무엇인지**는 말하지 않는다.
//   그러니 사람이 한 줄씩 봐야 한다. 428건이면 못 할 양이 아니다.
//
// ★이 화면은 읽기만 한다. 고치는 것은 대상 상세 화면의 기존 경로를 쓴다 —
//   여기에 일괄 버튼을 달면 오늘 하루 피한 실수를 화면으로 옮겨놓는 것이 된다.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

type anchorReviewRow struct {
	ID, KO, EntityType, QID, Verdict, Class, Desc string
	EN, ENSource, LabelEN, JA, JASource, Tier     string
	CheckedAt                                     time.Time
	// Reading — 근거를 겹쳐 읽은 결과. 결정이 아니라 **읽은 것**이다.
	Reading string
}

// circularENSources — 우리 en 이 이 출처에서 왔으면 QID 라벨과 같은 것은 당연하다.
// 같은 대상의 증거가 못 된다(순환).
var circularENSources = map[string]bool{
	"wikidata-label": true, "wikipedia-langlinks": true,
	"wikipedia-sitelink": true, "wikipedia-zh-variant": true, "": true,
}

func normLabel(s string) string {
	var b []rune
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b = append(b, r)
		}
	}
	return string(b)
}

// readAnchor — 우리 en 과 QID 영문 라벨을 겹쳐 **무엇이 틀렸는지**를 읽는다.
//
// ★한국어 라벨은 안 본다. 동명 함정이다 — 무용가 `가비` 와 2012년 영화 `가비` 가 같은 한글이다.
//   한 번 그렇게 읽었다가 무용가를 영화로 만들 뻔했다(2026-09-15).
// ★우리 en 이 위키데이터에서 왔으면 일치는 순환이다. 증거로 안 친다.
func readAnchor(verdict, en, enSource, labelEN string) string {
	switch verdict {
	case "name-element":
		return "앵커를 뗀다 — '이름' 항목은 어떤 대상의 근거도 아니다"
	case "fictional":
		return "우리는 실존 인물이라는데 앵커는 배역이다 — 개별 확인"
	}
	if circularENSources[strings.TrimSpace(enSource)] {
		return "판정 보류 — 우리 영문이 이 QID 에서 왔다(일치해도 증거가 아니다)"
	}
	if en == "" || labelEN == "" {
		return "판정 보류 — 견줄 영문이 없다"
	}
	if normLabel(en) == normLabel(labelEN) {
		return "같은 대상 — **유형**을 QID 쪽으로 고친다"
	}
	return "다른 대상 — **앵커**를 뗀다"
}

type anchorReviewCount struct {
	Verdict string
	N       int
}

// verdictLabel — 저장된 판정 코드를 화면 말로 옮긴다. 코드는 kdb 쪽 상수와 같아야 한다.
var verdictLabel = map[string]string{
	"name-element":       "앵커가 '이름' 항목 — 어떤 대상의 근거도 아니다",
	"type-mismatch":      "QID 가 말하는 유형과 우리 유형이 다르다 — 영문 라벨이 어느 쪽이 틀렸는지 가른다",
	"not-human":          "앵커가 사람이 아님 — 유형이 틀렸을 수도, 앵커가 틀렸을 수도",
	"fictional":          "앵커가 배역·가상 인물인데 우리는 person 이라 함",
	"human-on-character": "배역 자리에 실존 인물 앵커",
}

func (s *Server) anchorReview(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	want := r.URL.Query().Get("verdict")

	// ★판정 표가 아직 없으면 **500 이 아니라 "안 봤음"** 이다 (2026-09-15 회귀가 잡았다).
	//   0138 이전 DB·새 환경·복원 직후가 그렇다. 표 하나 없다고 화면이 죽으면,
	//   "아직 안 봤다"를 보여주려고 만든 화면이 그 상태에서만 안 열린다.
	if !s.anchorAuditTableExists(ctx) {
		s.render(w, r, "anchor_review.html", map[string]any{
			"title": "앵커 검수", "counts": []anchorReviewCount{}, "total": 0,
			"rows": []anchorReviewRow{}, "verdict": "", "labels": verdictLabel,
			"everChecked": int64(0), "page": "/admin/entities/anchors",
		})
		return
	}

	counts := []anchorReviewCount{}
	total := 0
	if rr, err := s.pool.Query(ctx, `
SELECT verdict, count(*) FROM kwave_kdb_anchor_audit a
 WHERE a.verdict <> ''
   AND EXISTS (SELECT 1 FROM kwave_entities e
                WHERE e.id = a.entity_id AND e.status = 'active'
                  AND e.entity_type::text = a.entity_type)
 GROUP BY 1 ORDER BY 2 DESC`); err == nil {
		defer rr.Close()
		for rr.Next() {
			var c anchorReviewCount
			if rr.Scan(&c.Verdict, &c.N) == nil {
				counts = append(counts, c)
				total += c.N
			}
		}
	} else {
		s.renderError(w, r, "anchor review counts", err)
		return
	}

	// 판정이 아직 하나도 없으면 "문제 0"이 아니라 "아직 안 봤다"이다. 화면이 그렇게 말한다.
	var everChecked int64
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM kwave_kdb_anchor_audit`).Scan(&everChecked)

	rows := []anchorReviewRow{}
	if rr, err := s.pool.Query(ctx, `
SELECT a.entity_id::text, e.canonical_ko, a.entity_type, a.external_id,
       a.verdict, a.class, a.description, a.checked_at,
       COALESCE(e.canonical_en,''), COALESCE(e.canonical_en_source,''), a.label_en,
       COALESCE(e.canonical_ja,''), COALESCE(e.canonical_ja_source,''), COALESCE(e.verification_tier,'')
  FROM kwave_kdb_anchor_audit a
  JOIN kwave_entities e ON e.id = a.entity_id
 WHERE a.verdict <> '' AND e.status = 'active' AND e.entity_type::text = a.entity_type
   AND ($1 = '' OR a.verdict = $1)
 ORDER BY a.verdict, e.canonical_ko
 LIMIT 500`, want); err == nil {
		defer rr.Close()
		for rr.Next() {
			var x anchorReviewRow
			if rr.Scan(&x.ID, &x.KO, &x.EntityType, &x.QID, &x.Verdict, &x.Class,
				&x.Desc, &x.CheckedAt, &x.EN, &x.ENSource, &x.LabelEN, &x.JA, &x.JASource, &x.Tier) == nil {
				x.Reading = readAnchor(x.Verdict, x.EN, x.ENSource, x.LabelEN)
				rows = append(rows, x)
			}
		}
	} else {
		s.renderError(w, r, "anchor review rows", err)
		return
	}

	s.render(w, r, "anchor_review.html", map[string]any{
		"title":        "앵커 검수",
		"counts":       counts,
		"total":        total,
		"rows":         rows,
		"verdict":      want,
		"labels":       verdictLabel,
		"everChecked":  everChecked,
		"page":         "/admin/entities/anchors",
	})
}

// anchorAuditTableExists — 판정 표가 있는지. 없으면 화면은 "아직 안 봤음"으로 그린다.
func (s *Server) anchorAuditTableExists(ctx context.Context) bool {
	var ok bool
	if err := s.pool.QueryRow(ctx,
		`SELECT to_regclass('public.kwave_kdb_anchor_audit') IS NOT NULL`).Scan(&ok); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			return false
		}
		return false
	}
	return ok
}
