package kdbadmin

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// --- 검증 tier · 정체성 (/admin/quality/verification) -----------------------
// 엔티티-레벨 "정체성 검증 tier"(internal/kdb/verify, 증분2)를 보여준다. 이 tier 는
// /v1/lookup·match 가 실제로 서빙하는 캐시값이라, 이 화면이 곧 "외부에 어떤 신뢰도로
// 답하고 있나"의 SSOT 다.
//   authoritative : Wikidata/TMDb 등 권위앵커 보유(결정론).
//   evidenced     : wiki langlink·강한 source·conf≥0.75, 또는 검색+gemma 확인.
//   unverified    : 독립 확증 없음 = 리스크 표면(evidence 업그레이드/운영자 검토 대상).
// 기본 필터는 unverified(가장 손봐야 할 것)로, 여기서 실재 K-엔티티를 골라 evidence
// 패스(`kdb-app verify-entities evidence`)로 올리거나 운영자가 검토한다.

type verifRow struct {
	ID         string
	Ko         string
	Type       string
	Tier       string
	Evidence   string
	Confidence float64
	UpdatedAt  time.Time
}

type verifTypeRow struct {
	Type          string
	Total         int64
	Authoritative int64
	Evidenced     int64
	Unverified    int64
}

func (s *Server) qualityVerification(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	p := newPage(r, "/admin/quality/verification")
	tierFilter := strings.TrimSpace(r.URL.Query().Get("tier"))
	if tierFilter == "" {
		tierFilter = "unverified" // 기본 = 리스크 표면
	}
	p.Extras["tier"] = tierFilter

	// tier 요약 (active 전체)
	var cAuth, cEvid, cUnver, cNone int64
	_ = s.pool.QueryRow(ctx, `
SELECT count(*) FILTER (WHERE verification_tier='authoritative'),
       count(*) FILTER (WHERE verification_tier='evidenced'),
       count(*) FILTER (WHERE verification_tier='unverified'),
       count(*) FILTER (WHERE verification_tier IS NULL OR verification_tier='')
FROM kwave_entities WHERE status='active'`).Scan(&cAuth, &cEvid, &cUnver, &cNone)

	// 유형별 tier 분해
	typeRows := []verifTypeRow{}
	if rr, err := s.pool.Query(ctx, `
SELECT entity_type::text, count(*),
       count(*) FILTER (WHERE verification_tier='authoritative'),
       count(*) FILTER (WHERE verification_tier='evidenced'),
       count(*) FILTER (WHERE verification_tier='unverified')
FROM kwave_entities WHERE status='active'
GROUP BY entity_type ORDER BY count(*) DESC`); err == nil {
		defer rr.Close()
		for rr.Next() {
			var x verifTypeRow
			if err := rr.Scan(&x.Type, &x.Total, &x.Authoritative, &x.Evidenced, &x.Unverified); err == nil {
				typeRows = append(typeRows, x)
			}
		}
	}

	// 마지막 검증 시각(스윕/증거 패스가 돌았나)
	var lastVerified *time.Time
	_ = s.pool.QueryRow(ctx, `SELECT max(verified_tier_at) FROM kwave_entities WHERE status='active'`).Scan(&lastVerified)

	// 목록 (tier 필터)
	var total int64
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM kwave_entities WHERE status='active' AND verification_tier=$1`, tierFilter).Scan(&total)
	p.finalize(total)

	items := []verifRow{}
	rows, err := s.pool.Query(ctx, `
SELECT id::text, canonical_ko, entity_type::text,
       COALESCE(verification_tier,''), COALESCE(verification_evidence,''),
       confidence::float8, updated_at
FROM kwave_entities
WHERE status='active' AND verification_tier=$1
ORDER BY confidence DESC, updated_at DESC
LIMIT $2 OFFSET $3`, tierFilter, p.Limit, p.Offset)
	if err != nil {
		s.renderError(w, r, "quality-verification", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var x verifRow
		if err := rows.Scan(&x.ID, &x.Ko, &x.Type, &x.Tier, &x.Evidence, &x.Confidence, &x.UpdatedAt); err != nil {
			continue
		}
		items = append(items, x)
	}

	verifiedPct := 0
	if tot := cAuth + cEvid + cUnver; tot > 0 {
		verifiedPct = int((cAuth + cEvid) * 100 / tot)
	}

	s.render(w, r, "verification.html", map[string]any{
		"title":         "검증 tier · 정체성",
		"items":         items,
		"typeRows":      typeRows,
		"p":             p,
		"tierFilter":    tierFilter,
		"cAuth":         cAuth,
		"cEvid":         cEvid,
		"cUnver":        cUnver,
		"cNone":         cNone,
		"verifiedPct":   verifiedPct,
		"lastVerified":  lastVerified,
		"page":          "/admin/quality/verification",
	})
}
