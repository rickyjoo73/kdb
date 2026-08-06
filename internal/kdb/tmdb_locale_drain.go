package kdb

// tmdb_locale_drain — 이미 TMDb id 를 보유한 **active** 작품의 로케일 빈칸을 TMDb 공식
// 현지 제목(translations + alternative_titles)으로 채운다.
//
// ★왜 필요한가(2026-07-31 실측): 기존 tmdb_drain.go 는 **candidate 승급 전용**이다 —
// `status='candidate'`(39행) · TMDb ref 가 이미 있으면 제외(51행) · canonical_en 만
// 채움(114행). 그래서 "active + TMDb ref 보유 + ja/zh 빈칸" 상태는 **어떤 드레인도 보지
// 않았다**. 실측 192건이 그 상태로 방치돼 있었다(show 84 · drama 50 · movie 58).
//
// 소스는 실재한다 — 더 글로리의 ja `ザ・グローリー ～輝かしき復讐～` / zh `黑暗荣耀` 는
// 전부 TMDb 가 채운 값이고, drama/movie ja 를 채운 소스 1위가 tmdb(589건)다. 즉 배급사가
// 정한 공식 현지 제목(부제 포함)이 TMDb 에 들어온다. 넷플릭스·디즈니 오리지널의 현지
// 제목도 같은 경로로 들어오므로, 별도 OTT 스크래핑보다 이 회수가 먼저다.
//
// ★MT 대체 효과: drama/movie ja 의 2위 소스가 gtranslate(292건)다 — 권위소스로 채울 수
// 있는 자리를 기계번역이 선점했다. 이 드레인은 빈칸뿐 아니라 **기계번역/codex 로 채워진
// 셀도 덮어쓴다**(source_priority 상 tmdb 가 상위). 그 외 소스(operator/media/wikidata)는
// 건드리지 않는다.
//
// 이름 재검색(Enrich)이 아니라 확정된 id 로 조회(EnrichByID)한다 — 동명작 오매칭을 새로
// 만들지 않기 위해서다(실측 전례: "아몬드"→프랑스 영화, 이정후→야구선수).

import (
	"context"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyjoo73/kdb/internal/kdb/tmdb"
)

// tmdbLocaleTargets — 채울 로케일과 canonical/source 컬럼. en 은 기존 승급 드레인이
// 담당하므로 제외한다(중복 작업 회피).
var tmdbLocaleTargets = []string{"ja", "zh", "zh_hant", "vi", "es", "id", "pt_br"}

// tmdbOverwritableSources — 이 출처로 채워진 값은 TMDb 로 덮어쓴다. 기계번역·LLM 추측·
// 음역 파생은 권위 현지제목보다 아래다. 빈 문자열(출처 미상)도 포함.
var tmdbOverwritableSources = []string{"", "gtranslate", "codex-fallback", "romanization", "local-search"}

// DrainTMDbLocaleFill — active 작품의 로케일 빈칸/저신뢰 셀을 TMDb 공식 제목으로 채운다.
// 반환=(채운 셀 수, 조회한 엔티티 수).
func DrainTMDbLocaleFill(ctx context.Context, pool *pgxpool.Pool, cl *tmdb.Client, token string, limit int) (filled, checked int) {
	if pool == nil || cl == nil || strings.TrimSpace(token) == "" {
		return 0, 0
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := pool.Query(ctx, `
SELECT e.id::text, e.canonical_ko, e.entity_type::text, r.external_id
  FROM kwave_entities e
  JOIN kwave_entity_external_refs r ON r.entity_id = e.id AND r.provider = 'tmdb'
 WHERE e.status = 'active'
   AND e.operator_locked = false
   AND e.entity_type IN ('movie','drama','show')
   -- 채울 여지가 있는 것만: 빈칸이거나 덮어쓰기 가능한 저신뢰 출처.
   AND (
     COALESCE(e.canonical_ja,'')='' OR COALESCE(e.canonical_zh,'')='' OR COALESCE(e.canonical_zh_hant,'')=''
     OR COALESCE(e.canonical_ja_source,'')      = ANY($2)
     OR COALESCE(e.canonical_zh_source,'')      = ANY($2)
     OR COALESCE(e.canonical_zh_hant_source,'') = ANY($2)
   )
   -- 재선택 제외는 2026-08-06 부터 FillRetryPredicate 로 통일. 종전 30일 쿨다운은
   -- 07-31 에 636건을 하루에 소진시켜, 이후 이 드레인은 대상 255건 중 **0건**을 집었다
   -- (08-06 실측: checked=1 filled=0 / checked=2 filled=0). 앵커가 있는 작품인데도
   -- 공식 현지제목을 가져올 기회가 30일 동안 없었다.
   AND `+FillRetryPredicate("e", "'tmdb-locale'")+`
 -- ★실제 빈칸이 많은 것부터. updated_at DESC 로 두면 다른 드레인이 방금 처리한 항목을
 -- 먼저 집어(첫 배치 실측: 20건 중 대부분이 이미 채워짐 → filled=0) 정작 오래된 빈칸
 -- 백로그에 못 닿는다. 빈칸 수 우선, 동수면 오래 방치된 것 우선.
 ORDER BY (CASE WHEN COALESCE(e.canonical_ja,'')      = '' THEN 1 ELSE 0 END
         + CASE WHEN COALESCE(e.canonical_zh,'')      = '' THEN 1 ELSE 0 END
         + CASE WHEN COALESCE(e.canonical_zh_hant,'') = '' THEN 1 ELSE 0 END) DESC,
          e.updated_at ASC
 LIMIT $1`, limit, tmdbOverwritableSources)
	if err != nil {
		log.Printf("kdb.tmdb-locale: select: %v", err)
		return 0, 0
	}
	type row struct{ id, ko, typ, tmdbID string }
	var items []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.id, &r.ko, &r.typ, &r.tmdbID) == nil {
			items = append(items, r)
		}
	}
	rows.Close()

	for _, it := range items {
		tid := parsePositiveInt(it.tmdbID)
		if tid == 0 {
			continue
		}
		checked++
		titles, ferr := cl.EnrichByID(ctx, token, tid, it.typ)
		time.Sleep(250 * time.Millisecond) // TMDb 예의
		// ★TMDb 호출 자체가 실패했으면 기록하지 않는다. 종전에는 "성공·실패 무관하게"
		// 기록해서, TMDb 가 잠깐 죽은 사이 지나간 작품이 "현지제목 없음"으로 30일 잠겼다.
		// 공회전 걱정은 FillRetryPredicate 가 대신 막는다 — 입력이 그대로면 애초에
		// 재선택되지 않으므로, 실패분을 마킹하지 않아도 매 tick 재조회되지 않는다.
		if ferr != nil {
			log.Printf("kdb.tmdb-locale: 조회 실패 id=%s — 마킹 없이 다음 회차 (%v)", it.tmdbID, ferr)
			continue
		}
		if len(titles) == 0 {
			// TMDb 가 정상 응답했는데 현지제목이 없다 — 결정적 판정이다.
			MarkFillAttempt(ctx, pool, it.id, "tmdb-locale", "no-local-title",
				"TMDb translations/alternative_titles 에 대상 로케일 없음")
			continue
		}

		gained := 0
		for _, loc := range tmdbLocaleTargets {
			vals, ok := titles[loc]
			if !ok || len(vals) == 0 {
				continue
			}
			v := strings.TrimSpace(vals[0])
			if v == "" {
				continue
			}
			// 빈칸이거나 덮어쓰기 가능한 출처일 때만 기록. 상위 소스(operator/media/
			// wikidata/netflix 등)는 불가침 — 권위 역전 방지.
			tag, uerr := pool.Exec(ctx, `
UPDATE kwave_entities
   SET canonical_`+loc+` = $2,
       canonical_`+loc+`_source = 'tmdb',
       updated_at = now()
 WHERE id = $1 AND status = 'active' AND operator_locked = false
   AND (COALESCE(canonical_`+loc+`,'') = '' OR COALESCE(canonical_`+loc+`_source,'') = ANY($3))
   AND COALESCE(canonical_`+loc+`,'') <> $2`, it.id, v, tmdbOverwritableSources)
			if uerr == nil && tag.RowsAffected() > 0 {
				filled++
				gained++
			}
		}
		// ★TMDb 가 정상 응답한 회차는 결과와 무관하게 기록한다 — 마킹을 건너뛰면 같은
		// 작품을 매 tick 다시 조회하는 공회전이 된다. 마킹은 위 UPDATE 들 **뒤**에 하므로
		// input_hash 가 방금 채운 값까지 반영한다: 더 채울 게 남았으면 다음 tick 에
		// 지문이 달라져 자동으로 재방문되고, 없으면 조용해진다.
		if gained > 0 {
			MarkFillAttempt(ctx, pool, it.id, "tmdb-locale", "filled",
				"TMDb 공식 현지제목으로 "+strconv.Itoa(gained)+"칸 채움")
		} else {
			MarkFillAttempt(ctx, pool, it.id, "tmdb-locale", "nothing-new",
				"TMDb 제목이 있으나 빈칸/덮어쓰기 대상이 아님")
		}
	}
	if checked > 0 {
		log.Printf("kdb.tmdb-locale: checked=%d filled=%d cells", checked, filled)
	}
	return filled, checked
}

// parsePositiveInt — external_id 는 text 컬럼이라 숫자 변환이 필요하다. 비숫자는 0.
func parsePositiveInt(s string) int {
	s = strings.TrimSpace(s)
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
		if n > 1<<30 {
			return 0
		}
	}
	return n
}
