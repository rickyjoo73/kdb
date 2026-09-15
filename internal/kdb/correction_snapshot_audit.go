package kdb

// correction_snapshot_audit — `correction-verified` 로 박힌 값을 **다시 대조한다.**
//
// ★이 값들은 가변 소스의 한 시점 사본이다(2026-09-15 실증).
//   우리 `에반` 의 es 가 `Heeseung love` 였다. 그때 위키데이터의 es 라벨이 그랬고,
//   "Wikidata 교차검증 일치 — 강제 자동 반영"으로 박혔다. 지금 위키데이터는 `Heeseung` 이다.
//   **위키데이터는 고쳐졌는데 우리 사본만 그대로 남았다.**
//
//   correction 경로는 `operator_locked` 도 우회하는 강제 반영이라 가장 세게 쓰인다.
//   가장 센 경로가 가장 늙은 값을 들고 있는 셈이다.
//
// ★읽기만 한다. 운영 실측 964건이 이 출처를 갖고 있어, 무엇이 얼마나 어긋났는지
//   먼저 눈으로 봐야 한다(오판 28 의 교훈 — 세어만 보고 돌리지 않는다).

import (
	"context"
	"log"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
)

type CorrectionDrift struct {
	ID, KO, Locale, Ours, NowLabel, QID string
	// Anchored — 그 대상이 실제로 그 QID 를 갖고 있나. false 면 애초에 다리가 없었다.
	Anchored bool
}

var correctionLocaleCols = [][2]string{
	{"en", "canonical_en"}, {"ja", "canonical_ja"}, {"vi", "canonical_vi"},
	{"zh", "canonical_zh"}, {"zh_hant", "canonical_zh_hant"}, {"es", "canonical_es"},
	{"id", "canonical_id"}, {"pt_br", "canonical_pt_br"},
}

// AuditCorrectionSnapshots — correction-verified 값이 지금의 위키데이터와 같은지 본다.
// 두 번째 반환값은 실제로 조회한 건수(모수). **표본 0을 모집단 0이라 말하지 않기 위해서다.**
func AuditCorrectionSnapshots(ctx context.Context, pool *pgxpool.Pool, cl *wikidata.Client, limit int) ([]CorrectionDrift, int) {
	if pool == nil || cl == nil {
		return nil, 0
	}
	if limit <= 0 {
		limit = 200
	}
	var sel []string
	for _, c := range correctionLocaleCols {
		sel = append(sel, "('"+c[0]+"', COALESCE(e."+c[1]+",''), COALESCE(e."+c[1]+"_source,''))")
	}
	rows, err := pool.Query(ctx, `
SELECT e.id::text, e.canonical_ko, g.loc, g.val,
       COALESCE((SELECT x.external_id FROM kwave_entity_external_refs x
                  WHERE x.entity_id = e.id AND x.provider = 'wikidata' LIMIT 1), '')
  FROM kwave_entities e
 CROSS JOIN LATERAL (VALUES `+strings.Join(sel, ",")+`) g(loc, val, src)
 WHERE e.status = 'active' AND g.src = 'correction-verified' AND g.val <> ''
 ORDER BY e.canonical_ko
 LIMIT $1`, limit)
	if err != nil {
		log.Printf("kdb.correction-audit: select: %v", err)
		return nil, 0
	}
	type row struct{ id, ko, loc, val, qid string }
	var items []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.id, &r.ko, &r.loc, &r.val, &r.qid) == nil {
			items = append(items, r)
		}
	}
	rows.Close()

	var out []CorrectionDrift
	checked := 0
	for _, it := range items {
		// ★앵커가 없으면 **지금도 대조할 다리가 없다.** 이름 검색으로 메우지 않는다 —
		//   그 메움이 애초에 `에반` 사고를 만들었다. 다리 없음 자체를 보고한다.
		if it.qid == "" {
			out = append(out, CorrectionDrift{ID: it.id, KO: it.ko, Locale: it.loc,
				Ours: it.val, NowLabel: "(대조 불가 — 앵커 없음)", Anchored: false})
			continue
		}
		ent, err := cl.Fetch(ctx, it.qid)
		if err != nil || ent == nil {
			continue
		}
		checked++
		now := ent.Labels[it.loc]
		if now == "" || normCorrection(now) == normCorrection(it.val) {
			continue
		}
		out = append(out, CorrectionDrift{ID: it.id, KO: it.ko, Locale: it.loc,
			Ours: it.val, NowLabel: now, QID: it.qid, Anchored: true})
	}
	return out, checked
}

func normCorrection(s string) string {
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
