package kdb

// demand_evidence — **여러 매체가 거듭 묻는다는 사실을 근거로 센다.**
//
// ★운영자 지적 (2026-09-15): "키야, 효리수 같은 경우는 요즘 바로 유행되는 것이라."
//
//   지금 막 뜨는 것은 위키데이터에 아직 없다. 그래서 근거 기반 게이트가 **정확히
//   그것들을** 떨어뜨린다 — 기사가 가장 많이 다루는, 가장 값진 낱말들이다.
//
// ★실측이 그대로 보여준다.
//
//     효리수  2026-06-17  게이트 기각 "gpt:common_noun — 수학적 개념인 '효율수'로 판단"
//                         그 뒤 existing_rejected_entity 로 자동 종결 3회
//             08-20~09-15  **2개 매체가 11회 요청** — 전부 out_of_scope
//     키야    2026-07-11  no_evidence_expired (TTL 21일 만료)
//             09-15        3회 요청 — 전부 out_of_scope
//
//   LLM 이 이름을 모르니 일반어로 판정했고, 그 판정이 한 달 내내 재생됐다.
//
// ★그런데 **요청 자체가 근거다.** 서로 다른 언론사가 같은 시기에 같은 고유명사를
//   유형까지 붙여 거듭 쓴다면, 그것은 실재한다. 이것은 매체 합의(media-consensus)와
//   같은 논리이고, 다만 합의 대상이 "표기"가 아니라 "존재"다.
//
//   실측: 출처 2곳 이상 × 3일 이상 반복인데 못 주는 낱말이 66개였다.
//     틈만나면, 29회/8출처 · 열린음악회 14회/2출처 · 쇼! 음악코어 10회/2출처
//     2026 가요대전 SUMMER 8회/3출처 · 황재균 8회/2출처 · 효리수 11회/2출처
//   8개 언론사가 29번 물은 것을 "근거 없음"이라 부를 수는 없다.
//
// ★그래도 active 로 바로 올리지 않는다. 수요는 **존재의 근거**이지 표기의 근거가
//   아니다. candidate 로 열어 평소 경로가 표기를 찾게 하고, 옛 기각만 무효화한다.
//
// ★기본 dry-run.

import (
	"context"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DemandThresholds — 수요를 근거로 인정하는 문턱. 보수적으로 잡는다.
const (
	// DemandMinSources — 서로 다른 출처 도메인 수. 1곳이면 그 매체의 오식일 수 있다.
	DemandMinSources = 2
	// DemandMinDays — 서로 다른 날짜 수. 하루 폭주(같은 기사 재시도)를 거른다.
	DemandMinDays = 3
	// DemandWindowDays — 이 기간 안의 요청만 센다. 오래된 수요는 지금의 근거가 아니다.
	DemandWindowDays = 60
)

// DemandEvidenceResult — 한 번 돈 결과.
type DemandEvidenceResult struct {
	Found, Opened, Created, Skipped int
	Samples                         []string
}

// DrainDemandEvidence — 반복 수요가 증명된 낱말을 candidate 로 연다.
//
// 두 가지를 한다.
//   ① 기각으로 누운 행을 candidate 로 되돌린다
//   ② 원장에 아예 없는 낱말은 candidate 로 만든다
// 둘 다 active 가 아니다 — 표기 근거는 평소 경로가 찾는다.
func DrainDemandEvidence(ctx context.Context, pool *pgxpool.Pool, limit int, dry bool) DemandEvidenceResult {
	var r DemandEvidenceResult
	if pool == nil || limit <= 0 {
		return r
	}
	rows, err := pool.Query(ctx, `
WITH d AS (
  SELECT term_ko,
         count(*)                                                    AS hits,
         count(DISTINCT split_part(split_part(COALESCE(source_url,''),'//',2),'/',1)) AS sources,
         count(DISTINCT date_trunc('day', created_at))               AS days,
         mode() WITHIN GROUP (ORDER BY term_type)                    AS typ
    FROM kwave_kdb_request_terms
   WHERE created_at > now() - make_interval(days => $2)
     AND COALESCE(term_ko,'') <> ''
     AND COALESCE(term_type,'') <> ''   -- 유형이 붙은 것만. 소비자가 기사 문맥에서 분류한 것이다.
   GROUP BY 1)
SELECT d.term_ko, d.typ, d.hits, d.sources, d.days,
       COALESCE((SELECT e.status FROM kwave_entities e
                  WHERE e.canonical_ko = d.term_ko ORDER BY (e.status='active') DESC LIMIT 1), '')
  FROM d
 WHERE d.sources >= $3 AND d.days >= $4
   AND NOT EXISTS (SELECT 1 FROM kwave_entities a
                    WHERE a.status='active'
                      AND (a.canonical_ko = d.term_ko OR d.term_ko = ANY(a.aliases_ko)))
 ORDER BY d.hits DESC
 LIMIT $1`, limit, DemandWindowDays, DemandMinSources, DemandMinDays)
	if err != nil {
		log.Printf("kdb.demand-evidence: select: %v", err)
		return r
	}
	type row struct {
		ko, typ, status     string
		hits, sources, days int
	}
	var items []row
	for rows.Next() {
		var it row
		if rows.Scan(&it.ko, &it.typ, &it.hits, &it.sources, &it.days, &it.status) == nil {
			items = append(items, it)
		}
	}
	rows.Close()

	for _, it := range items {
		r.Found++
		if len(r.Samples) < 40 {
			r.Samples = append(r.Samples, it.ko+"["+it.typ+"] "+itoa(it.hits)+"회/"+itoa(it.sources)+"출처")
		}
		log.Printf("  수요근거 %-24s [%s] %d회 · %d출처 · %d일 (현재 %q)",
			it.ko, it.typ, it.hits, it.sources, it.days, it.status)
		if dry {
			if it.status == "" {
				r.Created++
			} else {
				r.Opened++
			}
			continue
		}
		note := "[demand-evidence] " + itoa(it.sources) + "개 출처가 " + itoa(it.days) +
			"일에 걸쳐 " + itoa(it.hits) + "회 요청 — 실재 근거로 인정해 후보로 연다"
		if it.status == "" {
			// 원장에 없다 — 후보로 만든다. active 가 아니다.
			if _, err := pool.Exec(ctx, `
INSERT INTO kwave_entities (canonical_ko, entity_type, status, confidence, notes, created_at, updated_at)
VALUES ($1, $2::kwave_entity_type, 'candidate', 0.40, $3, now(), now())
ON CONFLICT DO NOTHING`, it.ko, it.typ, note); err == nil {
				r.Created++
			} else {
				log.Printf("  [보류] %s 생성 실패: %v", it.ko, err)
				r.Skipped++
			}
			continue
		}
		// 기각으로 누워 있다 — 되돌린다. 운영자가 잠근 것은 건드리지 않는다.
		tag, err := pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='candidate', updated_at=now(),
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || $2
 WHERE canonical_ko=$1 AND status='rejected' AND operator_locked=false`, it.ko, note)
		if err == nil && tag.RowsAffected() > 0 {
			r.Opened++
		} else {
			r.Skipped++
		}
	}
	return r
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
