package kdb

// wikidata_locale_drain — 이미 wikidata QID 를 보유한 **active** 엔티티의 로케일 빈칸을
// 위키데이터 레이블로 일괄 회수한다.
//
// ★왜 필요한가(2026-08-07 실측). ja/zh 를 못 채운다는 지적을 받고 앵커 보유 359건을
// 위키데이터 API 로 전건 조회했더니:
//
//	ja 레이블 148 · zh 레이블 112 · 어느 쪽도 없음 150
//	ja 위키백과 문서 58 · zh 위키백과 문서 98
//
// 즉 **레이블은 있는데 문서는 없는 게 ja 만 90건**이다. 그전 조사에서 나는 sitelink(문서)만
// 세고 label 을 안 세서 "ja 천장 40건"이라고 잘못 보고했다. 실제 천장은 148건이다.
//
// ★그럼 왜 안 채워졌나. wikidata-label 은 이미 이 DB 의 1위 소스(5,813칸)다. 경로가 없는
// 게 아니라 **일괄 경로가 없었다**:
//   - wikidata_person_drain 은 QID 가 **없는** candidate 에 QID 를 붙이는 승급 전용이다
//     (45행에서 wikidata ref 보유분을 명시적으로 제외한다).
//   - QID 를 **가진** 엔티티에서 레이블을 꺼내는 곳은 캐스케이드 L3 뿐인데, 그 경로는
//     엔티티당 그라운딩 60~90초에 묶여 1회 실행에 6건밖에 못 돈다. 359건이면 60회다.
//
// tmdb_locale_drain 이 TMDb 쪽에서 정확히 같은 사각지대("승급 드레인은 candidate 전용이라
// active+ref 보유분을 아무도 안 본다")를 닫은 전례가 있어 그 구조를 그대로 따른다.
//
// 이름 재검색이 아니라 확정된 QID 로 조회한다 — 캐스케이드가 QID 를 두고도 이름으로 다시
// 검색해 동명이인 가드에 18/18 전건 거부당한 게 어제 실측이다(14d034a).

import (
	"context"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
)

// wikidataLocaleTargets — 채울 로케일. Entity.Labels 는 이미 KDB 컬럼명으로 매핑돼 온다.
// ko 는 제외 — 정본을 외부 레이블로 덮는 건 이 드레인의 일이 아니다.
var wikidataLocaleTargets = []string{"en", "ja", "zh", "zh_hant", "vi", "es", "id", "pt_br"}

// wikidataOverwritableSources — 이 출처로 채워진 값은 위키데이터 레이블(prio 5)로 덮는다.
// 기계번역(gtranslate/codex-fallback, prio 8)·규칙음역(kana-rule 8, romanization 7)은
// 전부 아래다. 빈 문자열(출처 미상)도 포함.
//
// ★local-search 는 일부러 뺐다. 소스 우선순위표상으로는 덮어도 되지만(7 > 5), 이 DB 의
// local-search 에는 오너가 "공식 사이트 표기가 기준"이라고 못박은 뒤 각 사 공식 사이트에서
// 직접 확인해 넣은 소속사 영문명이 섞여 있다(0102 migration). 위키데이터 레이블은 제3자
// 표기이므로 그 판정을 뒤집으면 안 된다 — 실제로 대부분의 한국 소속사 공식 표기는 전각
// 대문자인데 위키데이터는 title case 로 적는다.
//
// ★opencc 도 뺐다. 그건 권위 있는 zh_hant 를 기계적으로 변환한 파생값이지 추측이 아니다.
// 덮으면 zh 와 zh_hant 가 서로 다른 계보가 돼 일관성만 깨진다.
var wikidataOverwritableSources = []string{"", "gtranslate", "codex-fallback", "kana-rule", "romanization"}

// wikidataLocaleRefillClause — "이 행에 아직 할 일이 남았나" 를 SQL 로 적는다.
// 로케일마다 두 가지: (1) 빈칸이거나 (2) 출처가 덮어써도 되는 것(기계번역·규칙음역·미상).
//
// ★손으로 적지 않고 wikidataLocaleTargets 에서 만들어 낸다. 손으로 적었을 때 실제로
// 어긋났다: UPDATE 는 8개 로케일을 덮는데 SELECT 는 ja/zh/zh_hant 세 개의 출처만 봤다.
// 그래서 en=gtranslate 인 행은 "빈칸도 아니고 감시 대상 출처도 아니라서" 영영 재선택되지
// 않았다 — 앵커를 새로 붙여도 영문 표기가 기계번역인 채로 남는다(2026-09-16 실측 496건,
// 조건을 맞추면 대상이 1,932 → 4,631). 두 목록이 다시 갈라지지 않도록 같은 슬라이스에서
// 뽑고, 대칭 여부는 테스트로 잠근다.
func wikidataLocaleRefillClause(alias, param string) string {
	var b strings.Builder
	for i, loc := range wikidataLocaleTargets {
		if i > 0 {
			b.WriteString("\n     OR ")
		}
		col := alias + ".canonical_" + loc
		b.WriteString("COALESCE(" + col + ",'')='' OR COALESCE(" + col + "_source,'') = ANY(" + param + ")")
	}
	return b.String()
}

// DrainWikidataLocaleFill — QID 보유 active 엔티티의 로케일 빈칸을 위키데이터 레이블로
// 채운다. 반환=(채운 셀 수, 조회한 엔티티 수).
func DrainWikidataLocaleFill(ctx context.Context, pool *pgxpool.Pool, cl *wikidata.Client, limit int) (filled, checked int) {
	relabeled := 0 // 값은 같고 출처만 올린 칸(아래 주석)
	// ★앵커부터 붙인다 (2026-09-21). 이 레인은 **QID 가 있는** 행만 채운다. 그런데 기관류는
	//   대부분 앵커가 없다 — 요청된 ja 빈칸 기준 회사 42/44 · 단체 24/28 · 기획사 14/15 ·
	//   정부기관 12/14 · 채널 13/14 가 앵커 0 이었고, 그래서 en 은 기계번역으로 채워졌다
	//   (회사 「기린그림」→"giraffe painting"). 앵커를 붙이는 DrainActiveAnchors 는 09-16 에
	//   만들어졌지만 CLI 전용·기본 dry-run 이라 **한 번 돈 뒤 아무도 안 켰다.**
	//
	//   켜기 전에 쟀다: 실제 300건을 돌려 **앵커 10건 · 10건 모두 정답**(고려제강→KISWIRE ·
	//   대한법률구조공단→Korea Legal Aid Corporation · 하트시그널2→시즌2 항목 …). 네 관문
	//   (이름 · P31 유형 · 한국 근거 · 이름항목 배제)과 QID 중복 가드가 나머지를 옳게 걸렀다.
	//
	//   붙은 앵커는 입력지문을 바꾸므로(ref 가 해시에 들어간다) 이 레인이 다음 틱에 그 행을
	//   집어 위키데이터 라벨로 기계번역을 밀어낸다. 틱당 소량만 — 검색이 행당 1초 남짓이다.
	if cl != nil {
		DrainActiveAnchors(ctx, pool, cl, activeAnchorPerTick, false)
		// ★한자 레인도 같은 틱에 (2026-09-23). 같은 위키 계열 호출이라 예산과 리듬을
		//   공유한다. 빈칸을 채우기도 하지만 주 역할은 **잠정값을 공식 한자로 바꾸는 것**이다.
		DrainKowikiHanja(ctx, pool, cl, kowikiHanjaPerTick, false)
	}
	if pool == nil || cl == nil {
		return 0, 0
	}
	if limit <= 0 {
		limit = 30
	}
	rows, err := pool.Query(ctx, `
SELECT e.id::text, e.canonical_ko, r.external_id, e.entity_type::text
  FROM kwave_entities e
  JOIN kwave_entity_external_refs r
    ON r.entity_id = e.id AND r.provider = 'wikidata' AND r.external_id <> ''
 WHERE e.status = 'active'
   AND e.operator_locked = false
   AND COALESCE(e.canonical_ko,'') <> ''
   AND (`+wikidataLocaleRefillClause("e", "$2")+`)
   AND `+FillRetryPredicate("e", "'wd-locale'")+`
 -- 빈칸이 많은 것부터. updated_at DESC 로 두면 다른 레인이 방금 만진 것을 다시 집어
 -- 백로그에 못 닿는다(tmdb-locale 에서 실제로 겪은 실패다 — 12c5060).
 ORDER BY (CASE WHEN COALESCE(e.canonical_ja,'')      = '' THEN 1 ELSE 0 END
         + CASE WHEN COALESCE(e.canonical_zh,'')      = '' THEN 1 ELSE 0 END
         + CASE WHEN COALESCE(e.canonical_zh_hant,'') = '' THEN 1 ELSE 0 END
         + CASE WHEN COALESCE(e.canonical_en,'')      = '' THEN 1 ELSE 0 END) DESC,
          e.updated_at ASC
 LIMIT $1`, limit, wikidataOverwritableSources)
	if err != nil {
		log.Printf("kdb.wd-locale: select: %v", err)
		return 0, 0
	}
	type row struct{ id, ko, qid, etype string }
	var items []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.id, &r.ko, &r.qid, &r.etype) == nil {
			items = append(items, r)
		}
	}
	rows.Close()

	for _, it := range items {
		checked++
		ent, ferr := cl.Fetch(ctx, it.qid)
		time.Sleep(200 * time.Millisecond) // Wikidata 예의
		// ★조회 실패는 기록하지 않는다. 전송 실패를 "내용 판정"으로 기록하면 위키데이터가
		// 잠깐 죽은 사이 지나간 엔티티가 "레이블 없음"으로 잠긴다 — 이 저장소가 다섯 번
		// 고친 계열의 버그다. 공회전은 FillRetryPredicate 가 막는다(입력이 그대로면 애초에
		// 재선택되지 않는다).
		if ferr != nil || ent == nil {
			log.Printf("kdb.wd-locale: 조회 실패 qid=%s — 마킹 없이 다음 회차 (%v)", it.qid, ferr)
			continue
		}
		// 이름요소/동음이의 항목에서 레이블을 꺼내면 일반명사가 정본으로 들어온다
		// (2026-07-29 실측: 같은 경로로 87건 오염). 결정적 판정이라 기록한다.
		if isName, cls := ent.IsNameElement(); isName {
			MarkFillAttempt(ctx, pool, it.id, "wd-locale", "name-element",
				"QID "+it.qid+" 는 이름요소/동음이의 항목("+cls+") — 레이블 회수 대상 아님")
			continue
		}
		// 앵커가 실제로 이 엔티티인지 확인. QID 는 여러 경로로 붙었고, 틀린 앵커로 8개
		// 로케일을 한번에 오염시키는 게 이 드레인의 최악 시나리오다. 오너 원칙 "빈칸 > 틀린값".
		if !wikidataAnchorMatches(it.ko, it.etype, ent) {
			MarkFillAttempt(ctx, pool, it.id, "wd-locale", "anchor-mismatch",
				"QID "+it.qid+" 의 ko 레이블/별칭이 정본 '"+it.ko+"' 과 불일치 — 앵커 재확인 필요")
			log.Printf("kdb.wd-locale: 앵커 불일치 id=%s ko=%q qid=%s", it.id, it.ko, it.qid)
			continue
		}

		gained := 0
		for _, loc := range wikidataLocaleTargets {
			v := strings.TrimSpace(ent.Labels[loc])
			// ★간체 칸에는 raw `zh` 를 쓰지 않는다 (2026-09-17 실측 87건 오염).
			//   위키데이터의 zh 레이블은 간체라는 보장이 없다 — 번체가 흔하다.
			//   판단은 wikidata.Entity.SimplifiedZh() 한 곳에 있다(enrich 와 공유).
			//   근거가 없으면 비워 둔다 — opencc 가 zh_hant 에서 결정적으로 변환해
			//   채우는 쪽이 진짜 간체다. 빈칸 > 틀린값.
			if loc == "zh" {
				v = ent.SimplifiedZh()
			}
			if loc == "zh_hant" {
				v = wikidataZhHant(ent.Labels)
			}
			if v == "" {
				continue
			}
			// 로케일 문자셋 검증은 기존 규칙을 그대로 쓴다 — 위키데이터 ja 레이블에 한글이
			// 그대로 복사돼 있는 항목이 실재한다.
			if !IsValidSpellingForLocale(loc, v) {
				continue
			}
			tag, uerr := pool.Exec(ctx, `
UPDATE kwave_entities
   SET canonical_`+loc+` = $2,
       canonical_`+loc+`_source = 'wikidata-label',
       updated_at = now()
 WHERE id = $1 AND status = 'active' AND operator_locked = false
   AND (COALESCE(canonical_`+loc+`,'') = '' OR COALESCE(canonical_`+loc+`_source,'') = ANY($3))
   AND lower(COALESCE(canonical_`+loc+`,'')) <> lower($2)`, it.id, v, wikidataOverwritableSources)
			// ★대소문자만 다르면 덮지 않는다 (09-21). 위키데이터 영문 라벨은 위키백과식 표기라
			//   DAY6→Day6 · PENTAGON→Pentagon · GOING SEVENTEEN→Going Seventeen 처럼 **공식
			//   표기를 지운다.** 글자가 같으면 고칠 것이 없다 — 표기는 우리 값이 맞는 경우가 많다.
			if uerr == nil && tag.RowsAffected() > 0 {
				filled++
				gained++
				continue
			}
			// ★값이 이미 라벨과 **같으면** 출처만 올린다 (2026-09-21).
			//
			//   위의 UPDATE 는 `<> $2` 라 같은 값은 건너뛴다. 그래서 기계가 우연히 맞힌 값은
			//   영영 기계 출처로 남았다. 문제는 그게 **소비자에게 지워져 나간다**는 것이다 —
			//   codex-fallback 은 provenance 「llm-only」라 hideLLMServe(기본 on)가 응답에서
			//   비운다. 맞는 값을 가지고 있으면서 빈칸을 준 셈이다. gtranslate 는 나가지만
			//   verified_only 에서 빠진다.
			//
			//   실측(표본 300개체): 기계 출처 718칸 중 **32칸이 위키데이터 라벨과 같았다**
			//   (codex 14 · gtranslate 18). 예: ja グッドモーニング大韓民国 · ユク・ジダム ·
			//   en XODIAC · Soompi · Korea Legal Aid Corporation.
			//
			//   값은 바꾸지 않는다 — 같다는 것이 곧 근거라 틀린 값을 만들 수 없다. 올리는
			//   대상은 이 레인이 원래 덮을 수 있는 출처(wikidataOverwritableSources)뿐이다.
			//   tmdb 같은 더 강한 출처는 건드리지 않는다.
			if uerr == nil {
				rtag, rerr := pool.Exec(ctx, `
UPDATE kwave_entities
   SET canonical_`+loc+`_source = 'wikidata-label', updated_at = now()
 WHERE id = $1 AND status = 'active' AND operator_locked = false
   AND canonical_`+loc+` = $2
   AND COALESCE(canonical_`+loc+`_source,'') = ANY($3)`, it.id, v, wikidataOverwritableSources)
				if rerr == nil && rtag.RowsAffected() > 0 {
					relabeled++
					gained++
				}
			}
		}
		// 정상 응답한 회차는 결과와 무관하게 기록한다 — 안 하면 같은 엔티티를 매 tick 다시
		// 조회하는 공회전이 된다. UPDATE **뒤**에 기록하므로 input_hash 가 방금 채운 값까지
		// 반영한다: 더 채울 게 남으면 다음 tick 에 지문이 달라져 자동 재방문된다.
		if gained > 0 {
			MarkFillAttempt(ctx, pool, it.id, "wd-locale", "filled",
				"위키데이터 레이블로 "+strconv.Itoa(gained)+"칸 채움")
		} else {
			MarkFillAttempt(ctx, pool, it.id, "wd-locale", "nothing-new",
				"레이블이 없거나 빈칸/덮어쓰기 대상이 아님")
		}
	}
	if checked > 0 {
		log.Printf("kdb.wd-locale: checked=%d filled=%d cells relabeled=%d cells(값 같음·출처 승격)", checked, filled, relabeled)
	}
	// 레인 성과 원장(0151). checked=검사 수, filled=원장이 바뀜 수.
	// 출처 승격도 원장이 바뀐 것이다 — applied 에 넣는다(0151 계약). 사유에 따로 센다.
	RecordCounts(ctx, pool, "wikidata-locale", false, checked, filled+relabeled, map[string]int{
		"채우지 못함": checked - filled - relabeled, "값 같음·출처 승격": relabeled,
	})
	return filled, checked
}

// wikidataAnchorMatches — 저장된 QID 가 정말 이 엔티티인지 ko **레이블**로 확인.
//
// ★별칭(alias)은 일부러 보지 않는다. 처음엔 별칭도 인정했는데, 배포 후 채워진 11건을
// 검수하니 **4건이 틀렸고 넷 다 별칭으로 통과한 것**이었다:
//
//	바이브(group)   ← Q87730005 "네이버 VIBE"  = 음악 스트리밍 서비스
//	DK(SEVENTEEN)  ← Q85976326 "Dplus Kia"   = 이스포츠 구단
//	제이(ENHYPEN)   ← Q26220991 "Jae"        = DAY6 제이 박
//	라미            ← Q114690838 "김성경"
//
// 별칭은 "그 이름으로도 불릴 수 있다"는 사전적 사실이지 동일성이 아니다. 짧은 활동명
// (DK·제이·MJ·바이브)은 별칭 목록에 흔해서, 별칭을 인정하면 동명이인 가드가 사실상 없는
// 것과 같아진다. 이 저장소가 반복해 밟은 함정이다("아몬드"→프랑스 영화, "이정후"→야구선수).
//
// 레이블만 봐도 손해가 없다는 건 실측으로 확인했다 — 앵커 보유 359건 중 레이블 일치 349건
// (97%), ko 표기 자체가 없어 판별 불가 3건. 별칭 분기가 추가로 통과시킨 것은 위 오탐뿐이었다.
//
// ko 레이블이 아예 없는 항목은 통과시킨다 — 한국 작품인데 ko 레이블이 비어 있는 경우가
// 실제로 있고, 여기서 막으면 회수 가능한 것을 근거 없이 버린다. 판별은 "불일치가 확인된
// 경우"에만 막는 방향으로 둔다.
func wikidataAnchorMatches(ko, etype string, ent *wikidata.Entity) bool {
	if wikidata.NormalizeName(ko) == "" || ent == nil {
		return false
	}
	label := strings.TrimSpace(ent.Labels["ko"])
	if label == "" {
		return true // ko 표기 자체가 없음 — 판별 불가, 막지 않는다
	}
	got := wikidata.NormalizeName(label)
	for _, name := range anchorCheckNames(ko, etype) {
		if wikidata.NormalizeName(name) == got {
			return true
		}
	}
	return false
}

// anchorCheckNames — 앵커의 ko 라벨과 대조할 **우리 쪽 이름들**. 순수 함수.
//
// ★정본 + (비사람일 때) 괄호 앞부분. **우리 별칭은 쓰지 않는다.**
//
//	09-21 에 별칭까지 넓혔다가 같은 날 거둬들였다. anchor-mismatch 82건 중 77건이 별칭·괄호로
//	맞았고 앵커 자체는 대부분 옳았지만, 실제로 채워진 값을 대조하니 **바이브(group)의 ja 가
//	「Naver VIBE」 로 바뀌었다** — 아래 시험이 막던 바로 그 오염이다. 앵커가 틀렸는데 우리
//	별칭에 그 틀린 이름이 이미 들어 있어서(틀린 앵커에서 거꾸로 들어온 것), 틀린 앵커를
//	스스로 확인해 주는 순환이 **그룹 유형에서** 일어났다. 사람·캐릭터만 막아서는 부족했다.
//
//	괄호 앞부분은 남긴다 — 우리 정본 자신의 앞부분이라 순환이 없다(펜타곤(PENTAGON)→펜타곤 ·
//	DAY6(데이식스)→DAY6). 사람·캐릭터와 시즌 괄호에는 쓰지 않는다(44회차).
//
// parenYearRE — 괄호 안의 연도(2026 · 2026년). 시즌 표지가 아니다.
var parenYearRE = regexp.MustCompile(`^\d{4}년?$`)

func anchorCheckNames(ko, etype string) []string {
	names := []string{ko}
	if etype == "person" || etype == "character" {
		return names
	}
	if m := tmdbParenRE.FindStringSubmatch(strings.TrimSpace(ko)); m != nil {
		inner := strings.TrimSpace(m[2])
		// 괄호 안이 **시즌 표지**면 바깥은 상위 시리즈 이름이다 — 쓰지 않는다. 메모(가제·연도)면
		// 바깥이 곧 진짜 제목이라 써도 된다(천천히 강렬하게(가제)→천천히 강렬하게).
		//   (tmdbNoteRE 는 「시즌2」도 메모로 치므로 여기선 쓰지 않는다 — 시험이 잡았다.)
		seasonInner := tmdbSeasonMarker(inner) != "" && !parenYearRE.MatchString(inner)
		if !seasonInner {
			names = append(names, strings.TrimSpace(m[1]))
		}
	}
	return names
}

// wikidataZhHant — zh_hant 칸에 쓸 위키데이터 라벨.
//
// ★평문 zh 라벨이 **번체 전용 글자를 품었으면** 그것이 번체의 근거다 (2026-09-21 43회차).
//
//	위키데이터는 한국 고유명사에 zh-hant 라벨을 따로 두는 일이 드물고, 번체 표기를 평문
//	`zh` 에 넣는 경우가 많다. 간체 칸은 SimplifiedZh + 문자셋 게이트가 그 값을 **옳게**
//	막는다(09-17 에 87건 오염을 낸 자리다). 그런데 막힌 값이 **번체 칸으로 가지도 않고
//	버려졌다.** 41회차 표본에서 세 건이 그랬다 — 需雲雜方(수운잡방) · 釜山長神大學校 ·
//	韓承起(한승기). 같은 행의 ja 칸은 채워져 있었다.
//
//	번체 전용 글자가 **있을 때만** 쓴다. 공통 글자로만 된 라벨(金珍妮)은 간체 칸이 이미
//	받고 opencc 가 번체 칸을 결정적으로 만든다 — 여기서 건드릴 이유가 없다. 간체 전용
//	글자가 섞였으면 호출측 문자셋 게이트(zh-hant)가 거부한다.
//
//	번체 칸이 차면 opencc 의 zh_hant→zh 방향이 간체 칸을 결정적으로 채운다. 09-17 주석이
//	말한 «진짜 간체»의 경로가 이제 입구를 갖는다.
func wikidataZhHant(labels map[string]string) string {
	if v := strings.TrimSpace(labels["zh_hant"]); v != "" {
		return v
	}
	if raw := strings.TrimSpace(labels["zh"]); raw != "" && ContainsTradOnly(raw) {
		return raw
	}
	return ""
}

// activeAnchorPerTick — wd-locale 한 틱에 앵커를 찾아볼 행 수. 대상 풀 2,193건(09-21) ·
// 행당 30일 쿨다운.
//
// ★8 → 20 (09-21). 수율이 3.3% 라 8건이면 틱당 기대 앵커가 0.26건이고, 세 틱 연속 0건이
// 흔해 원장이 「조용한 0건」 신호를 띄운다 — 정상을 경보로 만드는 크기였다. 20건이면 0.66건·
// 연속 0건 확률이 약 13% 로 떨어지고 풀을 9시간 안팎에 한 바퀴 돈다. 레인별 시간 제한은 없고
// (앞 실행이 안 끝나면 다음 틱을 건너뛸 뿐) 행당 1초 남짓이라 5분 주기에 20~30초가 더해진다.
const activeAnchorPerTick = 20

// kowikiHanjaPerTick — 한자 레인이 한 틱에 볼 행. 위키백과가 429 를 주는 속도라 천천히.
const kowikiHanjaPerTick = 12
