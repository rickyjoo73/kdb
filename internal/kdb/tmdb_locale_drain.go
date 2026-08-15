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

// tmdbLocaleTargets — translations 기반으로 채우는 로케일과 canonical/source 컬럼.
// en 은 규칙이 하나 달라 여기 넣지 않고 아래 en 블록이 따로 담당한다.
var tmdbLocaleTargets = []string{"ja", "zh", "zh_hant", "vi", "es", "id", "pt_br"}

// tmdbOverwritableSources — 이 출처로 채워진 값은 TMDb 로 덮어쓴다. 기계번역·LLM 추측·
// 음역 파생은 권위 현지제목보다 아래다. 빈 문자열(출처 미상)도 포함.
var tmdbOverwritableSources = []string{"", "gtranslate", "codex-fallback", "romanization", "local-search"}

// tmdbEnOverwritable — en 만 **tmdb 로 채운 칸도** 다시 읽는다. 종전에는 canonical_en 을
// 쓰는 레인이 전부 `COALESCE(canonical_en,'')=''` 가드였고(tmdb_drain·kmdb_drain·
// musicbrainz_drain·itunes_drain·romanize), 승급 드레인은 candidate 전용에 tmdb ref
// 보유분을 제외까지 했다 — 그래서 **active + tmdb ref 보유 + en 이미 채움** 조합은 어떤
// 레인도 다시 보지 않았다(08-15 실측 1,382건). 그 사이 TMDb 가 방영 전 직역 가제를
// 공식명으로 갈아치우면 우리 값은 가제로 굳는다(`유부녀 킬러`: Married Woman Killer →
// A Bona Fide Killer, 06-30 교체). 자기 소스가 스스로 정정한 것을 되읽는 경로다.
var tmdbEnOverwritable = append(append([]string{}, tmdbOverwritableSources...), "tmdb")

// tmdbLocaleFillCond / tmdbLocaleGapOrder — 선정 조건과 정렬을 **기록 대상 목록에서
// 생성**한다.
//
// ★왜 생성하는가(08-15): 손으로 적은 WHERE 가 `ja/zh/zh_hant` 3개만 검사하는 동안 기록은
// 7개 로케일에 하고 있었다. 그래서 ja/zh/zh_hant 가 모두 상위 출처로 차 있고 vi/es/id/
// pt_br 만 비어 있는 작품은 **영영 선택되지 않았다** — 실측 672건. `유부녀 킬러` 가 vi·
// es·id 를 TMDb 에서 못 받은 것도 이 구멍이다(ja=rss, zh/zh_hant=tmdb 로 차 있었다).
// 목록에서 생성하면 대상이 늘 때 조건이 자동으로 따라온다.
func tmdbLocaleFillCond(srcParam, enSrcParam string) string {
	parts := make([]string, 0, len(tmdbLocaleTargets)*2+2)
	for _, loc := range tmdbLocaleTargets {
		parts = append(parts,
			"COALESCE(e.canonical_"+loc+",'')=''",
			"COALESCE(e.canonical_"+loc+"_source,'')="+srcParam)
	}
	parts = append(parts,
		"COALESCE(e.canonical_en,'')=''",
		"COALESCE(e.canonical_en_source,'')="+enSrcParam)
	return "(" + strings.Join(parts, "\n     OR ") + ")"
}

func tmdbLocaleGapOrder() string {
	parts := make([]string, 0, len(tmdbLocaleTargets))
	for _, loc := range tmdbLocaleTargets {
		parts = append(parts, "CASE WHEN COALESCE(e.canonical_"+loc+",'')='' THEN 1 ELSE 0 END")
	}
	return strings.Join(parts, "\n         + ")
}

// tmdbEnAcceptable — en 칸에 넣어도 되는 표기인가. 라틴 문자가 하나라도 있고 한글·가나·
// 한자가 섞이지 않아야 한다. TMDb 는 en 번역이 없으면 기본 응답 name 에 원제를 되비추는데
// (실측 `고딩형사`·`너와 함께라면`), 그게 en 칸에 들어가면 원제 복사가 된다.
func tmdbEnAcceptable(s string) bool {
	hasLatin := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			hasLatin = true
		case r >= 0xAC00 && r <= 0xD7A3, // 한글 음절
			r >= 0x3040 && r <= 0x30FF, // 가나
			r >= 0x4E00 && r <= 0x9FFF: // 한자
			return false
		}
	}
	return hasLatin
}

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
SELECT e.id::text, e.canonical_ko, e.entity_type::text, r.external_id,
       COALESCE(e.canonical_en,''), COALESCE(e.canonical_en_source,'')
  FROM kwave_entities e
  JOIN kwave_entity_external_refs r ON r.entity_id = e.id AND r.provider = 'tmdb'
 WHERE e.status = 'active'
   AND e.operator_locked = false
   AND e.entity_type IN ('movie','drama','show')
   -- 채울 여지가 있는 것만: 빈칸이거나 덮어쓰기 가능한 저신뢰 출처.
   -- 조건은 기록 대상에서 생성한다(tmdbLocaleFillCond 주석 참조 — 손으로 적었을 때
   -- WHERE 가 3개, 기록이 7개로 갈라져 672건이 사각지대였다).
   AND `+tmdbLocaleFillCond("ANY($2)", "ANY($3)")+`
   -- 재선택 제외는 2026-08-06 부터 FillRetryPredicate 로 통일. 종전 30일 쿨다운은
   -- 07-31 에 636건을 하루에 소진시켜, 이후 이 드레인은 대상 255건 중 **0건**을 집었다
   -- (08-06 실측: checked=1 filled=0 / checked=2 filled=0). 앵커가 있는 작품인데도
   -- 공식 현지제목을 가져올 기회가 30일 동안 없었다.
   AND `+FillRetryPredicate("e", "'tmdb-locale'")+`
 -- ★실제 빈칸이 많은 것부터. updated_at DESC 로 두면 다른 드레인이 방금 처리한 항목을
 -- 먼저 집어(첫 배치 실측: 20건 중 대부분이 이미 채워짐 → filled=0) 정작 오래된 빈칸
 -- 백로그에 못 닿는다. 빈칸 수 우선, 동수면 오래 방치된 것 우선.
 ORDER BY (`+tmdbLocaleGapOrder()+`) DESC,
          e.updated_at ASC
 LIMIT $1`, limit, tmdbOverwritableSources, tmdbEnOverwritable)
	if err != nil {
		log.Printf("kdb.tmdb-locale: select: %v", err)
		return 0, 0
	}
	type row struct{ id, ko, typ, tmdbID, en, enSrc string }
	var items []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.id, &r.ko, &r.typ, &r.tmdbID, &r.en, &r.enSrc) == nil {
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

		gained := 0
		if n, eerr := drainTMDbEnglish(ctx, pool, cl, token, tid, it.id, it.ko, it.typ, it.en, it.enSrc); eerr != nil {
			log.Printf("kdb.tmdb-locale[en]: 조회 실패 id=%s — 마킹 없이 다음 회차 (%v)", it.tmdbID, eerr)
			continue
		} else {
			gained += n
			filled += n
		}

		if len(titles) == 0 {
			// TMDb 가 정상 응답했는데 현지제목이 없다 — 결정적 판정이다. en 재동기는 위에서
			// 이미 시도했으므로(별 엔드포인트) 여기서 건너뛰면 안 된다.
			if gained == 0 {
				MarkFillAttempt(ctx, pool, it.id, "tmdb-locale", "no-local-title",
					"TMDb translations/alternative_titles 에 대상 로케일 없음")
			}
			continue
		}

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

// drainTMDbEnglish — 한 작품의 canonical_en 을 TMDb 현재 공식명과 맞춘다. 반환=(채운 칸 수).
// error 는 **전송실패**일 때만 반환한다(호출측이 마킹 없이 넘어가도록).
//
// 가드가 셋이고 각각 실측 근거가 있다(2026-08-15 전수 감사, 대상 1,382건 중 낡음 19건):
//
//	① 교차확인 — OfficialEnglish 가 기본 응답 name 과 translations en 이 일치할 때만
//	   값을 준다. TMDb 는 en 번역이 없으면 name 에 원제를 되비추는데(`고딩형사`,
//	   `너와 함께라면`), 그게 en 칸에 들어가면 원제 복사가 된다.
//	② 표기 — tmdbEnAcceptable. ①을 통과해도 한글·CJK 가 섞인 값은 en 칸에 안 넣는다.
//	③ 원작 언어 — **이미 값이 있는 칸을 덮을 때만** ko 를 요구한다. 이 조건이 오부착
//	   앵커 3건을 잡았다(`소울메이트`→Soulm8te[en 2026], `헌트`→The Hunt[미국 2020],
//	   `범죄자`→犯罪者[ja]). 가드가 없었으면 멀쩡한 en 3칸을 덮어썼다.
//	   빈칸 채움에는 요구하지 않는다 — KDB 는 한국 작품 전용이 아니라서(실측 73건:
//	   `어느 가족`/万引き家族, `추락의 해부`, `와호장룡`) ko 를 요구하면 정당한 외국작이
//	   영영 빈칸으로 남는다.
//
// 구값은 버리지 않고 aliases_en 에 보존한다 — 되돌릴 수 있고 검색 재현율도 지킨다.
func drainTMDbEnglish(ctx context.Context, pool *pgxpool.Pool, cl *tmdb.Client, token string,
	tid int, entityID, ko, entityType, curEn, curSrc string) (int, error) {
	curEn = strings.TrimSpace(curEn)
	if curEn != "" && !containsString(tmdbEnOverwritable, curSrc) {
		return 0, nil // 상위 소스(operator/media/wikidata/kmdb 등) 불가침
	}

	title, origLang, err := cl.OfficialEnglish(ctx, token, tid, entityType)
	time.Sleep(250 * time.Millisecond) // TMDb 예의
	if err != nil {
		return 0, err
	}
	if title == "" {
		MarkFillAttempt(ctx, pool, entityID, "tmdb-en", "no-en-title",
			"TMDb 기본명과 en 번역이 불일치하거나 en 번역 없음 — 원제 되비침 방지")
		return 0, nil
	}
	if !tmdbEnAcceptable(title) {
		MarkFillAttempt(ctx, pool, entityID, "tmdb-en", "not-latin",
			"TMDb 현재명이 en 칸에 부적합한 표기: "+title)
		return 0, nil
	}
	if title == curEn {
		return 0, nil
	}
	if curEn != "" && origLang != "ko" {
		// 오부착 의심 — 덮지 않고 남긴다("내가 지어낸 값으로 근거 있는 값을 덮지 않는다").
		MarkFillAttempt(ctx, pool, entityID, "tmdb-en", "origin-guard",
			"기존 en 을 덮으려는데 TMDb 원작 언어가 "+origLang+" — 앵커 오부착 의심, 보류")
		return 0, nil
	}

	tag, uerr := pool.Exec(ctx, `
UPDATE kwave_entities
   SET canonical_en = $2,
       canonical_en_source = 'tmdb',
       aliases_en = CASE WHEN $3 = '' OR $3 = ANY(aliases_en) THEN aliases_en
                         ELSE array_append(aliases_en, $3) END,
       updated_at = now()
 WHERE id = $1 AND status = 'active' AND operator_locked = false
   AND COALESCE(canonical_en,'') = $3
   AND (COALESCE(canonical_en,'') = '' OR COALESCE(canonical_en_source,'') = ANY($4))`,
		entityID, title, curEn, tmdbEnOverwritable)
	if uerr != nil || tag.RowsAffected() == 0 {
		return 0, nil
	}
	log.Printf("kdb.tmdb-en: %s %q → %q (tmdb %d, orig=%s)", ko, curEn, title, tid, origLang)
	return 1, nil
}

// containsString — 작은 목록용. slices.Contains 를 쓰지 않는 건 이 파일이 기존 코드와
// 같은 최소 의존성을 유지하기 위해서다.
func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
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
