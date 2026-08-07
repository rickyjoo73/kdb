package kdb

// tmdb_anchor_drain — **active** 작품(movie/drama/show)에 TMDb 앵커(external_ref)만
// 붙인다. 승급도 로케일 채움도 하지 않는다.
//
// ★왜 필요한가(핸드오프 43차 §5, 2026-08-07 실측): 앵커를 찾아주는 드레인이 전부
// `status='candidate'` 전용이라, 한 번 active 가 되면 **아무도 그 엔티티의 앵커를 찾지
// 않는다**. tmdb_drain.go 는 39행에서 candidate 로, 51행에서 "TMDb ref 없음"으로 대상을
// 좁힌다 — 즉 "active + 앵커 없음"은 어느 레인도 보지 않았다. tmdb_locale_drain 이 로케일에
// 대해 닫은 것과 **똑같은 사각지대가 한 층 위에** 있었다.
//
// 실측 대상: ja 또는 zh 가 빈칸이고 TMDb ref 가 없는 active 작품 409건
// (show 287 · drama 81 · movie 41). TMDb 는 이 DB 에서 CJK 를 가장 많이 만든 소스이고
// (ja 685 · zh 1,435 · zh_hant 1,279) 수율이 증명된 유일한 경로다 — 위키데이터 쪽 우물은
// 전수 실측으로 말랐음을 확인했다(§3).
//
// ★로케일은 여기서 채우지 않는다. ref 를 INSERT 하면 `trg_kdb_fill_hash_refs` 트리거가
// fill_input_hash 를 갱신하고, 그러면 FillRetryPredicate 가 그 엔티티를 다시 집어 이미
// 도는 tmdb-locale 레인이 공식 현지제목을 회수한다. 채움 책임을 한 곳에 두는 게 맞다
// (같은 값을 두 레인이 쓰면 출처·우선순위 판정이 갈라진다).
//
// ★가드: 매칭은 SearchExactKoreanID 하나만 쓴다 — 정규화 정확일치 + 유일성 + 원작언어 ko.
// 승급 드레인의 SearchExactID 보다 **엄격하기만 하다**. 앵커 1건이 틀리면 tmdb-locale 이
// 곧바로 7칸을 오염시키므로(승급은 en 1칸이었다) 여기서 느슨해질 여지를 두지 않는다.
// 전례: "아몬드"→프랑스 영화, "이정후"→야구선수, 그리고 이번 세션의 별칭 가드 사고(§4.5).

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyjoo73/kdb/internal/kdb/tmdb"
)

// TMDbAnchorStats — 한 회차 판정 분포. 채택되지 않은 이유를 전부 세어 로그에 남긴다
// (조용한 누락 금지 — "0건 채움"이 무매칭 때문인지 가드 때문인지 구분되어야 한다).
type TMDbAnchorStats struct {
	Checked    int // 조회한 엔티티
	Anchored   int // ref 를 붙인 엔티티
	NoMatch    int // 정규화 정확일치 결과 없음
	Ambiguous  int // 동명작 2건+ → 보류
	Foreign    int // 정확·유일 일치했으나 원작언어가 ko 가 아님 → 보류
	SeasonOnly int // 일치한 항목이 우리 시즌이 아니라 상위 시리즈 → 보류
	Failed     int // TMDb 호출 실패 — 마킹하지 않고 다음 회차
}

// DrainTMDbAnchors — active 작품의 TMDb 앵커 사각지대를 닫는다.
// dry=true 면 검색·판정·로그만 하고 DB 쓰기가 전혀 없다(원장 마킹도 안 한다).
func DrainTMDbAnchors(ctx context.Context, pool *pgxpool.Pool, cl *tmdb.Client, token string, limit int, dry bool) TMDbAnchorStats {
	var st TMDbAnchorStats
	if pool == nil || cl == nil || strings.TrimSpace(token) == "" {
		return st
	}
	if limit <= 0 {
		limit = 10
	}
	// 대상: active·미잠금 작품 중 ja/zh 빈칸이면서 TMDb ref 가 없는 것.
	// canonical_ko 조건은 승급 드레인(tmdb_drain.go:48)과 맞춘다 — 한글이 있어야 ko 검색이
	// 의미가 있고, 과도하게 긴 문자열은 제목이 아니라 설명문인 경우가 많다.
	rows, err := pool.Query(ctx, `
SELECT e.id::text, e.canonical_ko, e.entity_type::text
  FROM kwave_entities e
 WHERE e.status = 'active'
   AND e.operator_locked = false
   AND e.entity_type IN ('movie','drama','show')
   AND e.canonical_ko ~ '[가-힣]'
   AND char_length(e.canonical_ko) BETWEEN 2 AND 60
   AND (COALESCE(e.canonical_ja,'') = '' OR COALESCE(e.canonical_zh,'') = '')
   AND NOT EXISTS (SELECT 1 FROM kwave_entity_external_refs r
                    WHERE r.entity_id = e.id AND r.provider = 'tmdb')
   AND `+FillRetryPredicate("e", "'tmdb-anchor'")+`
 -- 빈칸이 많은 것부터. 동수면 오래 방치된 것 우선(tmdb_locale_drain 과 같은 정렬 — 다른
 -- 레인이 방금 건드린 항목을 먼저 집으면 정작 오래된 백로그에 못 닿는다).
 ORDER BY (CASE WHEN COALESCE(e.canonical_ja,'') = '' THEN 1 ELSE 0 END
         + CASE WHEN COALESCE(e.canonical_zh,'') = '' THEN 1 ELSE 0 END) DESC,
          e.updated_at ASC
 LIMIT $1`, limit)
	if err != nil {
		log.Printf("kdb.tmdb-anchor: select: %v", err)
		return st
	}
	type row struct{ id, ko, typ string }
	var items []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.id, &r.ko, &r.typ) == nil {
			items = append(items, r)
		}
	}
	rows.Close()

	for _, it := range items {
		if ctx.Err() != nil {
			break
		}
		st.Checked++
		mid, ambiguous, foreign, seasonOnly, serr := cl.SearchExactKoreanID(ctx, token, it.ko, it.typ)
		time.Sleep(300 * time.Millisecond) // TMDb 예의

		// ★호출 실패는 원장에 남기지 않는다. 전송 실패를 내용 판정으로 기록하면 TMDb 가
		// 잠깐 죽은 사이 지나간 작품이 "앵커 없음"으로 잠긴다 — 이 저장소가 반복해 고친
		// 계열이다(tmdb_locale_drain.go:101, wikidata_locale_drain 가드 3번).
		if serr != nil {
			st.Failed++
			log.Printf("kdb.tmdb-anchor: 조회 실패 ko=%q — 마킹 없이 다음 회차 (%v)", it.ko, serr)
			continue
		}

		switch {
		case ambiguous:
			st.Ambiguous++
			log.Printf("kdb.tmdb-anchor%s: 동명작 다수 보류 ko=%q(%s)", dryTag(dry), it.ko, it.typ)
			if !dry {
				MarkFillAttempt(ctx, pool, it.id, "tmdb-anchor", "ambiguous",
					"TMDb 정확일치가 2건 이상 — 동명작 보류")
			}
			continue
		case foreign:
			st.Foreign++
			log.Printf("kdb.tmdb-anchor%s: 원작언어 비ko 보류 ko=%q(%s)", dryTag(dry), it.ko, it.typ)
			if !dry {
				MarkFillAttempt(ctx, pool, it.id, "tmdb-anchor", "foreign-original",
					"정확·유일 일치했으나 original_language≠ko — 한국어 개봉제목 충돌로 보고 보류")
			}
			continue
		case seasonOnly:
			// TMDb 가 시리즈를 항목 하나로 접어 우리 시즌의 제목을 alt 표기로만 갖고 있다.
			// 붙이면 프랜차이즈 제목이 시즌 칸에 들어간다 — 빈칸이 낫다.
			st.SeasonOnly++
			log.Printf("kdb.tmdb-anchor%s: 상위 시리즈만 일치 보류 ko=%q(%s)", dryTag(dry), it.ko, it.typ)
			if !dry {
				MarkFillAttempt(ctx, pool, it.id, "tmdb-anchor", "season-only",
					"alt 표기로만 일치했고 회차 표지가 TMDb 주제목에 없음 — 상위 시리즈 항목으로 보고 보류")
			}
			continue
		case mid == 0:
			st.NoMatch++
			if !dry {
				MarkFillAttempt(ctx, pool, it.id, "tmdb-anchor", "no-match",
					"TMDb 검색에 정규화 정확일치 결과 없음")
			}
			continue
		}

		if dry {
			st.Anchored++
			log.Printf("kdb.tmdb-anchor[dry]: 앵커후보 %s(%s) → tmdb#%d %s", it.ko, it.typ, mid,
				fmt.Sprintf("https://www.themoviedb.org/%s/%d", tmdbMediaPath(it.typ), mid))
			continue
		}

		// ref 부착. 승급 드레인과 같은 confidence(0.8) — 같은 가드를 통과한 값이다.
		// ★이 INSERT 가 trg_kdb_fill_hash_refs 를 통해 fill_input_hash 를 바꾸고, 그래서
		// tmdb-locale 레인이 다음 tick 에 이 엔티티를 집는다. 로케일은 거기서 채운다.
		tag, ierr := pool.Exec(ctx, `
INSERT INTO kwave_entity_external_refs (entity_id, provider, external_id, url, confidence, raw_payload, fetched_at)
VALUES ($1,'tmdb',$2,$3,0.8,'{}',now())
ON CONFLICT DO NOTHING`, it.id, fmt.Sprintf("%d", mid),
			fmt.Sprintf("https://www.themoviedb.org/%s/%d", tmdbMediaPath(it.typ), mid))
		if ierr != nil {
			// 쓰기 실패도 내용 판정이 아니다 — 마킹하지 않는다.
			st.Failed++
			log.Printf("kdb.tmdb-anchor: ref insert 실패 ko=%q: %v", it.ko, ierr)
			continue
		}
		if tag.RowsAffected() > 0 {
			st.Anchored++
			log.Printf("kdb.tmdb-anchor: 앵커 부착 %s(%s) → tmdb#%d", it.ko, it.typ, mid)
		}
		MarkFillAttempt(ctx, pool, it.id, "tmdb-anchor", "anchored",
			fmt.Sprintf("TMDb 정확·유일·원작ko 일치 → tmdb#%d", mid))
	}

	if st.Checked > 0 {
		log.Printf("kdb.tmdb-anchor%s: checked=%d anchored=%d (no-match=%d 동명작=%d 비ko=%d 상위시리즈=%d 실패=%d)",
			dryTag(dry), st.Checked, st.Anchored, st.NoMatch, st.Ambiguous, st.Foreign, st.SeasonOnly, st.Failed)
	}
	return st
}

func dryTag(dry bool) string {
	if dry {
		return "[dry]"
	}
	return ""
}
