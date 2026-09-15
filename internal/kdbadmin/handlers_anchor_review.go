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
	"net/http"
	"time"
)

type anchorReviewRow struct {
	ID, KO, EntityType, QID, Verdict, Class, Desc string
	EN, JA, JASource, Tier                        string
	CheckedAt                                     time.Time
}

type anchorReviewCount struct {
	Verdict string
	N       int
}

// verdictLabel — 저장된 판정 코드를 화면 말로 옮긴다. 코드는 kdb 쪽 상수와 같아야 한다.
var verdictLabel = map[string]string{
	"name-element":       "앵커가 '이름' 항목 — 어떤 대상의 근거도 아니다",
	"not-human":          "앵커가 사람이 아님 — 유형이 틀렸을 수도, 앵커가 틀렸을 수도",
	"fictional":          "앵커가 배역·가상 인물인데 우리는 person 이라 함",
	"human-on-character": "배역 자리에 실존 인물 앵커",
}

func (s *Server) anchorReview(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	want := r.URL.Query().Get("verdict")

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
       COALESCE(e.canonical_en,''), COALESCE(e.canonical_ja,''),
       COALESCE(e.canonical_ja_source,''), COALESCE(e.verification_tier,'')
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
				&x.Desc, &x.CheckedAt, &x.EN, &x.JA, &x.JASource, &x.Tier) == nil {
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
