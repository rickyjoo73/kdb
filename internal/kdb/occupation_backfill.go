package kdb

// occupation_backfill — **이미 만들어 둔 칸을 채운다.**
//
// ★실측 (2026-09-16). 활성 인물 5,407건 중 직업 영역이 채워진 것이 **78건(1.4%)**.
//
//	미상 5,329 · 연예 41 · 스포츠 27 · 정치 5 · 경제 3 · 미디어 1 · 학계 1
//
//	칸(0142)도 판정표(occupation_domain.go)도 어제 만들었다. 그런데 그 값을 쓰는
//	곳이 **enrich 캐스케이드 한 군데뿐**이라(orchestrator.go:908), 어제 이후 그
//	경로를 탄 78건만 채워졌다. 3,600여 건은 위키데이터 앵커를 이미 갖고 있는데
//	아무도 P106 을 물으러 가지 않았다.
//
//	"장치는 있는데 아무도 안 켠" 경우를 어제만 세 번 만났다 — TTL 시계, 앵커 표,
//	관리 화면 유형 목록. 같은 계열이다.
//
// ★이 레인은 **이름을 검색하지 않는다.** 확정된 QID 로만 묻는다.
//   그래서 동명이인 위험이 구조적으로 없다 — wikidata_locale_drain 과 같은 자리다.
//
// ★그리고 **묶어서 묻는다.** wbgetentities 는 ids 를 50개까지 받는다.
//   한 건씩 Fetch 하면 3,600 × 350ms ≈ 21분이고 9개 locale 라벨과 sitelink 까지
//   매번 받아 온다 — 필요한 건 P106/P21 두 줄인데. 묶으면 73회면 끝난다.
//
// ★모르는 QID 는 판정하지 않는다(D-37). 원자료(P106 목록)는 그대로 저장한다 —
//   표가 늘어나면 다시 판정할 수 있고, **표를 늘릴 근거도 거기서 나온다.**

import (
	"context"
	"log"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
)

// OccupationFillResult — 한 번 돈 결과.
type OccupationFillResult struct {
	Checked int // 조회한 인물 수
	Fetched int // 위키데이터가 claims 를 돌려준 수
	Domain  int // 영역까지 판정된 수
	Raw     int // 원자료(P106)는 받았으나 표에 없어 영역이 안 나온 수
	Gender  int // 성별을 채운 수
	NoP106  int // 위키데이터에 직업이 아예 없다(사람이 아닌 항목일 수 있다)
	Failed  int // 조회를 **못 했다**. 없는 것과 다르다.

	// UnknownQIDs — 표에 없는 P106 QID → 몇 번 나왔나. 표를 늘릴 근거다.
	UnknownQIDs map[string]int
}

// occupationBatch — 한 번에 묻는 인원. wbgetentities 상한이 50이다.
const occupationBatch = 50

// DrainOccupationDomain — 앵커 보유 활성 인물의 직업 영역·성별을 위키데이터 P106/P21 로 채운다.
func DrainOccupationDomain(ctx context.Context, pool *pgxpool.Pool, cl *wikidata.Client, limit int, dry bool) OccupationFillResult {
	r := OccupationFillResult{UnknownQIDs: map[string]int{}}
	if pool == nil || cl == nil || limit <= 0 {
		return r
	}
	rows, err := pool.Query(ctx, `
SELECT e.id::text, e.canonical_ko, x.external_id
  FROM kwave_entities e
  JOIN kwave_entity_external_refs x
    ON x.entity_id = e.id AND x.provider = 'wikidata' AND x.external_id ~ '^Q[1-9][0-9]*$'
 WHERE e.status = 'active'
   AND e.entity_type = 'person'
   AND e.operator_locked = false
   AND COALESCE(e.occupation_domain,'') = ''
   AND cardinality(e.occupation_qids) = 0
   AND NOT EXISTS (SELECT 1 FROM kwave_kdb_enrich_attempts a
                    WHERE a.entity_id = e.id AND a.field = 'wdoccup'
                      AND a.last_attempt_at > now() - interval '30 days')
 ORDER BY e.updated_at DESC
 LIMIT $1`, limit)
	if err != nil {
		log.Printf("kdb.occupation-fill: select: %v", err)
		return r
	}
	type item struct{ id, ko, qid string }
	var items []item
	byQID := map[string][]item{} // 같은 QID 를 여럿이 가리킬 수 있다(동일인 병합 전)
	for rows.Next() {
		var it item
		if rows.Scan(&it.id, &it.ko, &it.qid) == nil {
			items = append(items, it)
			byQID[it.qid] = append(byQID[it.qid], it)
		}
	}
	rows.Close()
	r.Checked = len(items)
	if len(items) == 0 {
		return r
	}

	qids := make([]string, 0, len(byQID))
	for q := range byQID {
		qids = append(qids, q)
	}
	for i := 0; i < len(qids); i += occupationBatch {
		end := i + occupationBatch
		if end > len(qids) {
			end = len(qids)
		}
		chunk := qids[i:end]
		claims, cerr := cl.BatchClaims(ctx, chunk, []string{"P106", "P21"})
		if cerr != nil {
			// ★못 한 것을 없다고 적지 않는다. 이 묶음은 통째로 실패로 센다.
			for _, q := range chunk {
				r.Failed += len(byQID[q])
			}
			log.Printf("kdb.occupation-fill: 묶음 조회 실패(%d건): %v", len(chunk), cerr)
			continue
		}
		for _, q := range chunk {
			c := claims[q]
			p106, p21 := c["P106"], c["P21"]
			domain, gender := OccupationDomain(p106), Gender(p21)
			for _, qid := range p106 {
				if occupationDomains[qid] == "" {
					r.UnknownQIDs[qid]++
				}
			}
			for _, it := range byQID[q] {
				if len(p106) == 0 && len(p21) == 0 {
					r.NoP106++
					// 위키데이터가 직업을 안 적어 뒀다. 쿨다운만 걸어 되묻지 않는다.
					if !dry {
						markOccupationAttempt(ctx, pool, it.id)
					}
					continue
				}
				r.Fetched++
				switch {
				case domain != "":
					r.Domain++
				case len(p106) > 0:
					r.Raw++ // 원자료는 있는데 표에 없다 — 표를 늘릴 근거
				}
				if gender != "" {
					r.Gender++
				}
				if dry {
					if domain != "" && r.Domain <= 30 {
						log.Printf("  %-18s %s → %s %s", it.ko, q, domain, gender)
					} else if domain == "" && len(p106) > 0 && r.Raw <= 20 {
						log.Printf("  [영역미상] %-18s %s P106=%s", it.ko, q, strings.Join(p106, ","))
					}
					continue
				}
				// 원자료를 **항상** 적는다. 영역이 안 나와도 적어 둬야 표가 늘었을 때
				// 다시 판정할 수 있다(0142 가 칸을 둘로 나눈 이유).
				if _, err := pool.Exec(ctx, `
UPDATE kwave_entities
   SET occupation_qids   = CASE WHEN cardinality($2::text[]) > 0 THEN $2::text[] ELSE occupation_qids END,
       occupation_domain = CASE WHEN $3 <> '' THEN $3 ELSE occupation_domain END,
       gender_qids       = CASE WHEN cardinality($4::text[]) > 0 THEN $4::text[] ELSE gender_qids END,
       gender            = CASE WHEN $5 <> '' THEN $5 ELSE gender END,
       updated_at = now()
 WHERE id = $1 AND operator_locked = false`,
					it.id, p106, domain, p21, gender); err != nil {
					log.Printf("kdb.occupation-fill: 저장 실패 %s: %v", it.ko, err)
					continue
				}
				markOccupationAttempt(ctx, pool, it.id)
			}
		}
	}
	return r
}

// markOccupationAttempt — 쿨다운. 직업이 없는 항목을 30일마다 한 번만 다시 묻는다.
func markOccupationAttempt(ctx context.Context, pool *pgxpool.Pool, id string) {
	_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_enrich_attempts (entity_id, field, attempts, last_attempt_at, last_source)
VALUES ($1,'wdoccup',1,now(),'wikidata')
ON CONFLICT (entity_id, field) DO UPDATE
   SET attempts = kwave_kdb_enrich_attempts.attempts + 1, last_attempt_at = now()`, id)
}

// TopUnknownOccupations — 표에 없는 P106 을 많이 나온 순으로. 표를 늘릴 때 어디부터
// 볼지 알려 준다. 세는 것과 고치는 것을 섞지 않는다 — 판정표는 사람이 확인해서 적는다.
func TopUnknownOccupations(m map[string]int, n int) []string {
	type kv struct {
		q string
		c int
	}
	var all []kv
	for q, c := range m {
		all = append(all, kv{q, c})
	}
	for i := 0; i < len(all); i++ {
		for j := i + 1; j < len(all); j++ {
			if all[j].c > all[i].c || (all[j].c == all[i].c && all[j].q < all[i].q) {
				all[i], all[j] = all[j], all[i]
			}
		}
	}
	out := make([]string, 0, n)
	for i := 0; i < len(all) && i < n; i++ {
		out = append(out, all[i].q+"×"+itoa(all[i].c))
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
