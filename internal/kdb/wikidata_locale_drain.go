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

// DrainWikidataLocaleFill — QID 보유 active 엔티티의 로케일 빈칸을 위키데이터 레이블로
// 채운다. 반환=(채운 셀 수, 조회한 엔티티 수).
func DrainWikidataLocaleFill(ctx context.Context, pool *pgxpool.Pool, cl *wikidata.Client, limit int) (filled, checked int) {
	if pool == nil || cl == nil {
		return 0, 0
	}
	if limit <= 0 {
		limit = 30
	}
	rows, err := pool.Query(ctx, `
SELECT e.id::text, e.canonical_ko, r.external_id
  FROM kwave_entities e
  JOIN kwave_entity_external_refs r
    ON r.entity_id = e.id AND r.provider = 'wikidata' AND r.external_id <> ''
 WHERE e.status = 'active'
   AND e.operator_locked = false
   AND COALESCE(e.canonical_ko,'') <> ''
   AND (
     COALESCE(e.canonical_en,'')='' OR COALESCE(e.canonical_ja,'')='' OR COALESCE(e.canonical_zh,'')=''
     OR COALESCE(e.canonical_zh_hant,'')='' OR COALESCE(e.canonical_vi,'')='' OR COALESCE(e.canonical_es,'')=''
     OR COALESCE(e.canonical_id,'')='' OR COALESCE(e.canonical_pt_br,'')=''
     OR COALESCE(e.canonical_ja_source,'')      = ANY($2)
     OR COALESCE(e.canonical_zh_source,'')      = ANY($2)
     OR COALESCE(e.canonical_zh_hant_source,'') = ANY($2)
   )
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
	type row struct{ id, ko, qid string }
	var items []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.id, &r.ko, &r.qid) == nil {
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
		if !wikidataAnchorMatches(it.ko, ent) {
			MarkFillAttempt(ctx, pool, it.id, "wd-locale", "anchor-mismatch",
				"QID "+it.qid+" 의 ko 레이블/별칭이 정본 '"+it.ko+"' 과 불일치 — 앵커 재확인 필요")
			log.Printf("kdb.wd-locale: 앵커 불일치 id=%s ko=%q qid=%s", it.id, it.ko, it.qid)
			continue
		}

		gained := 0
		for _, loc := range wikidataLocaleTargets {
			v := strings.TrimSpace(ent.Labels[loc])
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
   AND COALESCE(canonical_`+loc+`,'') <> $2`, it.id, v, wikidataOverwritableSources)
			if uerr == nil && tag.RowsAffected() > 0 {
				filled++
				gained++
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
		log.Printf("kdb.wd-locale: checked=%d filled=%d cells", checked, filled)
	}
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
func wikidataAnchorMatches(ko string, ent *wikidata.Entity) bool {
	want := wikidata.NormalizeName(ko)
	if want == "" || ent == nil {
		return false
	}
	label := strings.TrimSpace(ent.Labels["ko"])
	if label == "" {
		return true // ko 표기 자체가 없음 — 판별 불가, 막지 않는다
	}
	return wikidata.NormalizeName(label) == want
}
