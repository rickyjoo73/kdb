package kdb

// zhwiki_title_drain — 이미 보유한 zh.wikipedia 문서 URL 에서 zh 표기를 뽑는다. 외부호출 0.
//
// ★왜(2026-08-14, 45차 §10.5): `source_urls` 에 zh.wikipedia 문서를 **이미 갖고 있는데**
// `canonical_zh` 는 빈 active 엔티티가 67건이다. 중국어 위키백과가 그 문서에 붙인 제목이
// 곧 중국어권이 그 이름을 쓰는 방식이므로, 새로 물어볼 것 없이 우리가 가진 근거 안에
// 답이 있다:
//
//	엠블랙   → zh.wikipedia.org/wiki/MBLAQ      베이비복스 → /wiki/Baby_V.O.X
//	젠틀몬스터 → /wiki/Gentle_Monster            굿데이     → /wiki/GOOD_DAY_(組合)
//
// ★이 레인의 위험은 "값이 틀리는 것"이 아니라 **"URL 이 남의 것인 것"** 이다. 45차 §10.4
// (`제이(ENHYPEN)` 의 앵커 9개가 전부 DAY6 제이 박의 것이었다)와 43차 §4.5(별칭 일치 ≠
// 동일성)가 같은 병이고, 실제로 이 67건 안에도 세 건이 있었다:
//
//	라미   en=Haram   ← /wiki/Lami          (en 과 URL 이 서로 다른 사람을 가리킨다)
//	에이엔 en=AEN     ← /wiki/ANS_(女子團體) (AEN 은 솔로, ANS 는 걸그룹 — 다른 엔티티)
//	유정   en=Yujeong ← /wiki/磪有情        (disambig 이 (Brave Girls) 인데 문서는 최유정)
//
// 그래서 값을 쓰기 전에 **독립된 칸의 뒷받침**을 요구한다: 문서 제목이 `canonical_en` 과
// 대소문자·구두점을 접어 일치할 때만 채운다. 이 한 줄이 위 세 건을 전부 걸러낸다 —
// 세 번째는 제목이 한자라 애초에 en 과 겹칠 수가 없다. 뒷받침을 못 얻은 건은 값을
// 만들지 않고 **원장에 사유를 남긴다**(45차 §1·§4.1 이 세 번 고친 그 병 — 레인이 조용히
// 건너뛰면 그 건은 영영 "미판정"으로 경보에 쌓인다).
//
// 쓰는 값은 **문서 제목 쪽**이다(en 이 아니라). en 은 신원 확인용이고, 채우는 칸은 zh 라
// 중국어권 표기 증거를 그대로 쓰는 게 맞다. 실제로 갈리는 쪽이 반반이라 어느 한쪽이
// 일방적으로 낫지 않다 — `SISTAR19`·`EVERGLOW`·`THORNAPPLE` 은 문서 제목이 공식 표기고,
// `4minute`(공식 4Minute)·`MIGHTY MOUTH`(공식 Mighty Mouth)는 en 쪽이 낫다. 45차 §10.1 이
// 기록한 그대로 **"소스 등급이 높다"가 "값이 낫다"를 뜻하지 않으므로**, 판단을 지어내는
// 대신 출처를 정직하게 'wikipedia-sitelink'(prio 6)로 남겨 권위 중국매체 표기(prio 1~4)가
// 들어오면 자동으로 덮이게 한다.
//
// ⚠ `IsValidSpellingForLocale("zh", …)` 를 통째로 쓰지 않는다 — 그 게이트는 zh 에 **한자를
// 요구**해서 라틴 제목을 전부 거부한다. 그런데 이 저장소의 라틴 zh(`Wavve`·`HYBE Labels`)는
// 그 게이트를 안 지나는 DrainLatinKoToCJK 로 들어온 것이고, 이 레인도 같은 계열이다.
// 게이트를 넓히는 건 외부 소스 23곳에 함께 영향이 가는 별개 판단이라(45차 §2 의 교훈:
// 가드가 막던 것과 지키던 것은 다르다) 손대지 않고, 여기서 의미 있는 부분 — 폭0 서식문자·
// 한글·가나 혼입 — 만 명시적으로 본다.

import (
	"context"
	"log"
	"net/url"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5/pgxpool"
)

// zhWikiTitleField — 이 레인의 원장 field. backlog_watch.go 의 cjkFillLaneSQL 에도 같은
// 이름이 있어야 판정이 "미판정"으로 다시 세어지지 않는다.
const zhWikiTitleField = "zhwiki-title"

// zhWikiDisambigTerms — zh.wikipedia 문서 제목 끝의 괄호 중 **동음이의 꼬리표**로 확인된
// 것만. 닫힌 목록이다(0106 의 교훈) — 여기 없는 괄호는 벗기지 않고 그 건을 통째로 기각한다.
// 이름의 일부일 수 있는 괄호를 규칙으로 벗기면 `GOOD DAY` 를 얻는 대신 언젠가 이름을
// 훼손한다. 목록에 없는 꼬리표가 나오면 원장의 사유에 그 문자열이 그대로 찍히므로,
// 다음 세션이 눈으로 보고 추가하면 된다.
var zhWikiDisambigTerms = map[string]bool{
	"歌手":   true, // Gray_(歌手) · Lily_(歌手)
	"組合":   true, // Chakra_(組合) · GOOD_DAY_(組合)
	"女子團體": true, // ANS_(女子團體) · APRIL_(女子團體)
	"男子團體": true,
	"韓國組合": true, // J-Walk_(韓國組合) · Phantom_(韓國組合)
	"韓國歌手": true,
	"演員":   true,
	"藝人":   true,
	"樂團":   true,
	"專輯":   true,
	"歌曲":   true,
	"電視劇":  true,
	"電影":   true,
	"綜藝節目": true,
}

// foldForCompare — 신원 대조 전용 접기: 소문자 + 글자/숫자 외 전부 제거.
// `M.I.L.K.`/`M.I.L.K` · `Led Apple`/`Ledapple` · `The East Light`/`The EastLight.` 처럼
// **같은 이름의 표기 변형**만 통과시키고 `AEN`/`ANS` 같은 다른 이름은 걸러야 하므로,
// 구두점과 공백만 지우고 글자는 하나도 지우지 않는다.
func foldForCompare(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// zhWikiTitleFromURL — zh.wikipedia 문서 URL → 표시 제목. 못 뽑으면 (＂＂, 사유).
//
// 퍼센트 디코딩 후 `_`→공백. 네임스페이스(`Category:`)·섹션앵커(`#`)·리다이렉트 쿼리는
// 문서 제목이 아니므로 기각한다.
func zhWikiTitleFromURL(raw string) (string, string) {
	i := strings.Index(raw, "/wiki/")
	if i < 0 {
		return "", "URL 에 /wiki/ 경로가 없다: " + raw
	}
	path := raw[i+len("/wiki/"):]
	if j := strings.IndexAny(path, "?#"); j >= 0 {
		path = path[:j]
	}
	dec, err := url.PathUnescape(path)
	if err != nil {
		return "", "퍼센트 디코딩 실패: " + path
	}
	title := strings.TrimSpace(strings.ReplaceAll(dec, "_", " "))
	if title == "" {
		return "", "문서 제목이 비었다: " + raw
	}
	if strings.Contains(title, ":") {
		return "", "본문 네임스페이스가 아니다: " + title
	}
	// 끝의 동음이의 괄호는 닫힌 목록에 있을 때만 벗긴다.
	if strings.HasSuffix(title, ")") {
		k := strings.LastIndex(title, "(")
		if k < 0 {
			return "", "괄호 짝이 안 맞는다: " + title
		}
		tag := strings.TrimSpace(title[k+1 : len(title)-1])
		if !zhWikiDisambigTerms[tag] {
			return "", "허용목록 밖 동음이의 꼬리표: (" + tag + ")"
		}
		title = strings.TrimSpace(title[:k])
		if title == "" {
			return "", "꼬리표를 빼면 남는 제목이 없다: " + raw
		}
	}
	return title, ""
}

// DrainZhWikiTitle — 보유한 zh.wikipedia URL 에서 canonical_zh 빈칸을 채운다.
// dry=true 면 값을 쓰지도, 원장을 남기지도 않고 판정만 로그로 보여준다.
// 반환=(채운 셀, 원장에 남긴 기각 수).
func DrainZhWikiTitle(ctx context.Context, pool *pgxpool.Pool, dry bool) (filled, marked int) {
	if pool == nil {
		return 0, 0
	}
	// 선정: zh 빈칸 + zh.wikipedia URL 보유 + en 보유(뒷받침에 필요) + zh 오염판정 없음.
	// ★fill_input_hash 는 source_urls 를 포함하지 않는다 — URL 만 바뀐 건은 지문이 그대로라
	// FillRetryRevisitDays(90일) 뒤에야 다시 온다. en 이나 타입이 바뀌면 즉시 다시 집는다.
	rows, err := pool.Query(ctx, `
SELECT e.id::text, e.canonical_ko, e.canonical_en,
       ARRAY(SELECT u FROM unnest(e.source_urls) u WHERE u LIKE '%zh.wikipedia.org/wiki/%')
  FROM kwave_entities e
 WHERE e.status='active' AND e.operator_locked = false
   AND COALESCE(e.canonical_zh,'') = ''
   AND COALESCE(e.canonical_en,'') <> ''
   AND EXISTS (SELECT 1 FROM unnest(e.source_urls) u WHERE u LIKE '%zh.wikipedia.org/wiki/%')
   AND NOT EXISTS (SELECT 1 FROM kwave_kdb_dataqa_log d
        WHERE d.entity_id = e.id AND d.locale = 'zh'
          AND d.verdict='contaminated' AND d.reverted_at IS NULL)
   AND `+FillRetryPredicate("e", "$1"), zhWikiTitleField)
	if err != nil {
		log.Printf("kdb.zhwiki: 선정 조회: %v", err)
		return 0, 0
	}
	type cand struct {
		id, ko, en string
		urls       []string
	}
	var items []cand
	for rows.Next() {
		var c cand
		if rows.Scan(&c.id, &c.ko, &c.en, &c.urls) == nil {
			items = append(items, c)
		}
	}
	rows.Close()

	for _, c := range items {
		verdict, reason, title := zhWikiVerdict(c.en, c.urls)
		if verdict != "" {
			if dry {
				log.Printf("kdb.zhwiki: [dry] 기각 %s (%s) — %s", c.ko, verdict, reason)
				continue
			}
			MarkFillAttempt(ctx, pool, c.id, zhWikiTitleField, verdict, reason)
			marked++
			continue
		}
		if dry {
			note := ""
			if title != c.en {
				note = "  ★en 과 표기 차이: en=" + c.en
			}
			log.Printf("kdb.zhwiki: [dry] 채움 %s → zh=%s%s", c.ko, title, note)
			filled++
			continue
		}
		var applied bool
		err := pool.QueryRow(ctx, `
UPDATE kwave_entities SET canonical_zh=$2, canonical_zh_source='wikipedia-sitelink', updated_at=now()
 WHERE id=$1 AND COALESCE(canonical_zh,'')=''
 RETURNING true`, c.id, title).Scan(&applied)
		if err == nil && applied {
			filled++
			// 채워졌으면 옛 기각 기록은 지운다 — 나중에 dataqa 가 이 값을 비웠을 때
			// 그 기록이 재시도를 막는다(ClearFillAttempt 주석).
			ClearFillAttempt(ctx, pool, c.id, zhWikiTitleField)
		}
	}
	log.Printf("kdb.zhwiki: DrainZhWikiTitle filled=%d rejected=%d /%d (dry=%v)", filled, marked, len(items), dry)
	return filled, marked
}

// zhWikiVerdict — 한 엔티티의 판정. verdict 가 빈 문자열이면 통과이고 title 이 쓸 값이다.
// 순수 함수로 떼어둔 건 테스트에서 DB 없이 규칙만 검사하기 위해서다.
func zhWikiVerdict(en string, urls []string) (verdict, reason, title string) {
	if len(urls) == 0 {
		return "no-url", "zh.wikipedia URL 이 없다", ""
	}
	if len(urls) > 1 {
		// 문서가 둘이면 어느 쪽이 이 엔티티인지 규칙으로 못 고른다. 지어내지 않는다.
		return "multi-url", "zh.wikipedia 문서가 " + strings.Join(urls, " · ") + " 로 둘 이상", ""
	}
	t, why := zhWikiTitleFromURL(urls[0])
	if t == "" {
		return "bad-url", why, ""
	}
	if hasZeroWidth(t) {
		return "zero-width", "문서 제목에 폭0 서식문자: " + t, ""
	}
	if hangulRE.MatchString(t) || kanaRE.MatchString(t) {
		return "script-mismatch", "zh 값에 한글/가나 혼입: " + t, ""
	}
	if foldForCompare(t) != foldForCompare(en) {
		// ★이 레인의 핵심 가드. 문서가 남의 것일 때 여기서 걸린다.
		return "en-mismatch", "문서 제목과 en 이 다른 이름이다: 제목=" + t + " en=" + en, ""
	}
	return "", "", t
}
