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
// ★2026-08-16: 대상에 **등급 사각지대**를 더했다. 위 선정은 "로케일 빈칸"만 봤고, 그
// 조건은 08-16 시점 **잔여 0건**으로 말랐다. 그런데 붙이는 ref 는 `tmdb` —
// authoritativeIdentityProviders 라 **그 자체가 등급 근거**다. ja/zh 가 이미 찬
// unverified 작품 204건(show 138 · drama 45 · movie 21)은 어느 조건에도 안 걸려
// 아무도 TMDb 에 물어보지 않았다. 레인은 매 tick 0건을 집으며 놀고 있었다.
// 노트 19·22 와 같은 형태 — **소스가 없는 게 아니라 그 백로그에 안 겨눠져 있었다.**
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
	"regexp"
	"strings"
	"time"
	"unicode"

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
	// 대상: active·미잠금 작품 중 TMDb ref 가 없고, ①ja/zh 가 비었거나 ②등급이 unverified 인 것.
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
   AND (
        -- (A) 로케일 사각지대 — 종전 대상.
        COALESCE(e.canonical_ja,'') = '' OR COALESCE(e.canonical_zh,'') = ''
        -- (B) 등급 사각지대(2026-08-16). ja/zh 가 이미 찬 unverified 작품은 (A) 에 안
        --     걸려 **아무도 TMDb 에 물어보지 않았다**. 이 레인이 붙이는 tmdb ref 는
        --     authoritativeIdentityProviders 라 그 자체로 등급 근거인데, 선정이 로케일
        --     빈칸만 봤다. 실측(08-16): (A) 잔여 0건 · (B) 204건 — 레인은 놀고 백로그는
        --     남아 있었다. 19·22번과 같은 형태다(소스가 아니라 겨냥의 문제).
        OR e.verification_tier = 'unverified'
   )
   AND NOT EXISTS (SELECT 1 FROM kwave_entity_external_refs r
                    WHERE r.entity_id = e.id AND r.provider = 'tmdb')
   AND `+FillRetryPredicate("e", "'tmdb-anchor'")+`
 -- 빈칸이 많은 것부터. 동수면 오래 방치된 것 우선(tmdb_locale_drain 과 같은 정렬 — 다른
 -- 레인이 방금 건드린 항목을 먼저 집으면 정작 오래된 백로그에 못 닿는다).
 -- ★(B) 는 빈칸이 0 이라 항상 (A) 뒤에 선다. 의도한 순서다 — (A) 한 건은 로케일 7칸을
 -- 막고 있고, 유입이 신규 active 작품뿐이라 (B) 를 굶길 만큼 쌓이지 않는다.
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
	// 레인 성과 원장(0151). ★2026-09-21 43회차에 배선했다 — 이 레인은 매 틱 돌면서도
	// 원장에 아무것도 남기지 않아, 317건을 전부 no-match 로 판정해 둔 사실을 아무도 몰랐다.
	RecordCounts(ctx, pool, "tmdb-anchor", dry, st.Checked, st.Anchored, map[string]int{
		"정확일치 없음": st.NoMatch, "동명작 보류": st.Ambiguous, "원작 비ko": st.Foreign,
		"상위 시리즈": st.SeasonOnly, "호출 실패": st.Failed,
	})

	// 같은 TMDb 레이트 예산으로 변형 질의 패스를 이어 돈다. main.go 를 건드리지 않으려고
	// 여기서 부른다 — 이 함수는 이미 매 틱 불린다.
	if ctx.Err() == nil {
		DrainTMDbAnchorVariants(ctx, pool, cl, token, 4, dry)
	}
	return st
}

// ─── 변형 질의 패스 (2026-09-21 43회차) ──────────────────────────────────────
//
// ★왜. active 작품 중 TMDb 앵커가 없는 것이 zh 빈칸의 사각지대다(zh 빈칸 show·drama·movie
//	461건 중 317건). 이 레인은 그 317건을 **전부 이미 봤고 315건을 no-match 로 판정**했다.
//	그런데 표본에 `꽃파당(조선혼담공작소 꽃파당)` 이 있었다 — TMDb 에 분명히 있는 드라마다.
//	가드가 틀린 게 아니라 **질의 형태가 틀렸다**: 우리 canonical_ko 에 괄호 부제가 붙어
//	정규화 정확일치가 깨진다. `냉부해` 같은 약칭은 TMDb 에 정식 제목(냉장고를 부탁해)으로 있다.
//
// ★측정(43회차, 변형 보유 no-match 표본 30건). 같은 가드(정확·유일·원작 ko)로 변형을
//	다시 태우자 **5건이 붙었고 5건 모두 정답**이었다(천천히 강렬하게·냉장고를 부탁해·안단테·
//	나는 가수다·EBS 스페이스 공감). 동명작 2건은 가드가 옳게 보류했다. 42회차에 zhwiki
//	표제어 검색이 오매칭 100%였던 것과 정반대다 — 차이는 가드다.
//
// ★판정은 새로 만들지 않는다. 변형마다 SearchExactKoreanID(정규화 정확일치 + 유일 +
//	원작 ko + 상위 시리즈 배제)를 그대로 태운다. 바뀌는 것은 **무엇을 물어보나** 뿐이다.
//
// ★변형끼리 서로 다른 작품을 가리키면 보류한다. 두 이름이 다른 답을 내면 그중 하나는
//	우리 개체가 아니다.
//
// ★쓰지 않는 변형: 콜론 앞부분. `흑백요리사: 요리 계급 전쟁 시즌2` → `흑백요리사` 는
//	**상위 시리즈**(시즌 1)에 붙을 수 있다. 앵커 하나가 틀리면 tmdb-locale 이 7칸을
//	한꺼번에 오염시킨다(tmdb.go:300 주석). 그래서 표본에서 본 그 모양을 아예 만들지 않는다.

// tmdbNoteRE — 괄호 안에 들어가는 **이름이 아닌 메모**. 이것으로 검색하면 엉뚱한
// 작품이 걸린다(`가제` 라는 이름의 작품이 있을 수 있다).
var tmdbNoteRE = regexp.MustCompile(`(?i)^(가제|임시\s*제목|임시|시즌\s*\d+|파트\s*\d+|part\.?\s*\d+|\d{4}|\d{4}년|\d+부)$`)

// tmdbParenRE — 끝에 붙은 괄호 하나. `X(Y)` · `X (Y)` · `X（Y）`.
var tmdbParenRE = regexp.MustCompile(`^(.+?)\s*[(（]([^()（）]+)[)）]\s*$`)

// tmdbAnchorVariants — 원래 제목으로 못 찾은 작품을 **다시 물어볼 이름들**. 순수 함수.
//
// 괄호 바깥, 괄호 안(메모·라틴 전용 제외), 저장된 별칭. 원래 제목과 같은 것·한글이
// 없는 것·두 글자 미만은 뺀다(검색이 ko-KR 이고, 한국 원작의 original_name 은 한글이다).
// 최대 4개 — TMDb 레이트 예산을 지킨다.
func tmdbAnchorVariants(ko string, aliases []string) []string {
	ko = strings.TrimSpace(ko)
	var cand []string
	if m := tmdbParenRE.FindStringSubmatch(ko); m != nil {
		cand = append(cand, strings.TrimSpace(m[1]))
		if in := strings.TrimSpace(m[2]); !tmdbNoteRE.MatchString(in) {
			cand = append(cand, in)
		}
	}
	cand = append(cand, aliases...)

	// ★시즌·속편 표지가 있으면, 변형도 **같은 표지를 가져야** 한다 (2026-09-21 44회차).
	//
	//	콜론 앞부분만 막았더니 **같은 상위 시리즈 이름이 별칭으로 저장된 경로**로 새어
	//	들어왔다. 운영에서 12건 중 2건이 그렇게 틀렸다:
	//	  부부클리닉-사랑과 전쟁2      ← 별칭 「부부클리닉 사랑과 전쟁」 → tmdb#1293(시즌1)
	//	  흑백요리사: 요리 계급 전쟁2   ← 별칭 「흑백요리사」          → tmdb#245684(시리즈)
	//	두 번째는 zh 가 黑白厨师：烹饪阶级战争2 에서 시리즈 제목으로 덮였다 — 시즌 칸에
	//	프랜차이즈 제목이 들어간 것이다(tmdb.go 가 「빈칸이 낫다」고 한 바로 그 모양).
	//
	//	같은 ID 에 두 개체가 붙는 것만으로 막으면 안 된다 — 같은 12건 중 5건은 원장에
	//	**같은 작품이 두 개체로 중복**돼 있던 것(나가수/나는 가수다 · 냉부해/냉장고를
	//	부탁해 …)이라 앵커는 맞았다. 틀린 원인은 「표지를 떨군 변형」 하나다.
	marker := tmdbSeasonMarker(ko)
	seen := map[string]bool{normTitleKey(ko): true}
	var out []string
	for _, c := range cand {
		c = strings.TrimSpace(c)
		if len([]rune(c)) < 2 || !hangulRE.MatchString(c) || tmdbNoteRE.MatchString(c) {
			continue
		}
		if marker != "" && tmdbSeasonMarker(c) != marker {
			continue // 표지를 떨군 변형은 상위 시리즈를 가리킨다
		}
		k := normTitleKey(c)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, c)
		if len(out) == 4 {
			break
		}
	}
	return out
}

// tmdbSeasonRE — 제목 끝의 시즌·속편 표지. `…전쟁2` · `… 시즌 2` · `…3기` · `…2부` ·
// `… Part 2` · `… II`. 앞에 오는 숫자(2026 …)는 표지가 아니다.
var tmdbSeasonRE = regexp.MustCompile(`(?i)(?:시즌\s*(\d+)|(\d+)\s*(?:기|부)|part\.?\s*(\d+)|파트\s*(\d+)|\s(ii|iii|iv|v)|(\d+))\s*$`)

// tmdbSeasonMarker — 제목 끝 시즌·속편 표지를 정규화해 돌려준다(없으면 ""). 순수 함수.
func tmdbSeasonMarker(s string) string {
	m := tmdbSeasonRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return ""
	}
	for _, g := range m[1:] {
		if g != "" {
			switch strings.ToLower(g) {
			case "ii":
				return "2"
			case "iii":
				return "3"
			case "iv":
				return "4"
			case "v":
				return "5"
			}
			return strings.TrimLeft(g, "0")
		}
	}
	return ""
}

// normTitleKey — 중복 판정용 키(공백·부호 제거, 소문자).
func normTitleKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// DrainTMDbAnchorVariants — 원래 제목으로 no-match 였던 active 작품을 변형 이름으로 다시 찾는다.
func DrainTMDbAnchorVariants(ctx context.Context, pool *pgxpool.Pool, cl *tmdb.Client, token string, limit int, dry bool) {
	if pool == nil || cl == nil || strings.TrimSpace(token) == "" || limit <= 0 {
		return
	}
	rows, err := pool.Query(ctx, `
SELECT e.id::text, e.canonical_ko, e.entity_type::text, COALESCE(e.aliases_ko,'{}')
  FROM kwave_entities e
 WHERE e.status = 'active' AND e.operator_locked = false
   AND e.entity_type IN ('movie','drama','show')
   AND NOT EXISTS (SELECT 1 FROM kwave_entity_external_refs r WHERE r.entity_id = e.id AND r.provider = 'tmdb')
   -- 원래 제목으로는 이미 물어봤고 없었다. 그 판정이 있는 것만 다시 본다.
   AND EXISTS (SELECT 1 FROM kwave_kdb_enrich_attempts a
                WHERE a.entity_id = e.id AND a.field = 'tmdb-anchor' AND a.last_source = 'no-match')
   AND (e.canonical_ko ~ '[(（]' OR COALESCE(array_length(e.aliases_ko,1),0) > 0)
   AND `+FillRetryPredicate("e", "'tmdb-anchor-v'")+`
 ORDER BY (CASE WHEN COALESCE(e.canonical_ja,'') = '' THEN 1 ELSE 0 END
         + CASE WHEN COALESCE(e.canonical_zh,'') = '' THEN 1 ELSE 0 END) DESC,
          e.updated_at ASC
 LIMIT $1`, limit)
	if err != nil {
		log.Printf("kdb.tmdb-anchor-v: select: %v", err)
		return
	}
	type row struct {
		id, ko, typ string
		aliases     []string
	}
	var items []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.id, &r.ko, &r.typ, &r.aliases) == nil {
			items = append(items, r)
		}
	}
	rows.Close()

	checked, anchored := 0, 0
	reasons := map[string]int{}
	for _, it := range items {
		if ctx.Err() != nil {
			break
		}
		vs := tmdbAnchorVariants(it.ko, it.aliases)
		if len(vs) == 0 {
			// 물어볼 변형이 없다(별칭이 전부 원래 제목과 같거나 메모뿐). 다시 뽑히지 않게 남긴다.
			if !dry {
				MarkFillAttempt(ctx, pool, it.id, "tmdb-anchor-v", "no-variant", "다시 물어볼 이름이 없음")
			}
			continue
		}
		checked++
		ids := map[int]string{}
		held, failed := "", false
		for _, q := range vs {
			mid, ambiguous, foreign, seasonOnly, serr := cl.SearchExactKoreanID(ctx, token, q, it.typ)
			time.Sleep(300 * time.Millisecond) // TMDb 예의
			switch {
			case serr != nil:
				failed = true
			case ambiguous:
				held = "동명작 보류"
			case foreign:
				held = "원작 비ko"
			case seasonOnly:
				held = "상위 시리즈"
			case mid > 0:
				ids[mid] = q
			}
		}
		// ★호출 실패는 판정이 아니다 — 마킹하지 않고 다음 회차에 다시 본다.
		if failed && len(ids) == 0 {
			reasons["호출 실패"]++
			continue
		}
		verdict, reason := "no-match", "변형 이름으로도 정확·유일 일치 없음"
		var mid int
		var via string
		switch {
		case len(ids) > 1:
			verdict, reason = "ambiguous", "변형들이 서로 다른 작품을 가리킨다 — 보류"
			reasons["변형끼리 불일치"]++
		case len(ids) == 1:
			for k, v := range ids {
				mid, via = k, v
			}
		case held != "":
			verdict, reason = "held", held
			reasons[held]++
		default:
			reasons["정확일치 없음"]++
		}
		if mid == 0 {
			if !dry {
				MarkFillAttempt(ctx, pool, it.id, "tmdb-anchor-v", verdict, reason)
			}
			continue
		}
		if dry {
			anchored++
			log.Printf("kdb.tmdb-anchor-v[dry]: 앵커후보 %s(%s) ← %q → tmdb#%d", it.ko, it.typ, via, mid)
			continue
		}
		tag, ierr := pool.Exec(ctx, `
INSERT INTO kwave_entity_external_refs (entity_id, provider, external_id, url, confidence, raw_payload, fetched_at)
VALUES ($1,'tmdb',$2,$3,0.8,$4,now())
ON CONFLICT DO NOTHING`, it.id, fmt.Sprintf("%d", mid),
			fmt.Sprintf("https://www.themoviedb.org/%s/%d", tmdbMediaPath(it.typ), mid),
			fmt.Sprintf(`{"via_variant":%q}`, via))
		if ierr != nil {
			reasons["호출 실패"]++
			log.Printf("kdb.tmdb-anchor-v: ref insert 실패 ko=%q: %v", it.ko, ierr)
			continue
		}
		if tag.RowsAffected() > 0 {
			anchored++
			log.Printf("kdb.tmdb-anchor-v: 앵커 부착 %s(%s) ← %q → tmdb#%d", it.ko, it.typ, via, mid)
		}
		MarkFillAttempt(ctx, pool, it.id, "tmdb-anchor-v", "anchored",
			fmt.Sprintf("변형 %q 로 TMDb 정확·유일·원작ko 일치 → tmdb#%d", via, mid))
	}
	RecordCounts(ctx, pool, "tmdb-anchor-variant", dry, checked, anchored, reasons)
}

func dryTag(dry bool) string {
	if dry {
		return "[dry]"
	}
	return ""
}
