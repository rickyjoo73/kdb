package enrich

// mt_translit_fill.go — 오너 지시 2026-07-21: "직역은 버리고 구글번역으로 (음차) 채움".
// ja/zh 빈칸을 구글번역으로 돌린 뒤 gemma 음차/직역 게이트로 거른다: 음차(소리옮김)만
// 채우고 직역(뜻옮김)·깨짐은 버린다. 라이브 실증 근거: ko→zh 는 고유명사를 뜻으로 직역
// (롱샷→远距离拍摄), ko→ja 는 외래어/인명만 음차되고 순한국어 제목은 직역(놀면뭐하니→
// 遊ぶと…). 게이트로 직역을 버려 "틀린값보다 빈칸" 을 지킨다. source=gtranslate(prio8,
// machine-translation) — 상위 공식소스가 자동 업그레이드, verified_only 제외(applyEmptyOnly).

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb"
	"github.com/rickyjoo73/kdb/internal/kdb/gemma"
)

var mtHangulRe = regexp.MustCompile(`[가-힣]`)

// errGateNoVerdict — 게이트가 응답은 줬는데 판정이 비어 있는 경우. 내용판정이 아니므로
// 원장에 기록하지 않고 다음 회차로 넘긴다(전송실패와 같은 취급).
var errGateNoVerdict = errors.New("게이트가 판정을 내지 않음")

// mtBrokenKoRe — 번역 소스로 부적합한 오염 ko(깨진 토큰·플레이스홀더). 오너 지시
// 2026-07-26: "이상없는 것만 번역해서 채워라" — 소스가 오염이면 번역 전 스킵한다.
var mtBrokenKoRe = regexp.MustCompile(`(?i)(collapsedone|undefined|\bnull\b|�|\\u[0-9a-f]{4})`)

// titleTypes — 제목류. 오너 승인 2026-07-26: 제목은 en 과 동일하게 '뜻-번역'을 서빙값으로
// 허용한다(청혼→求婚). 게이트는 translit|literal 둘 다 채우고 bad(깨짐·무의미·미번역)만
// 버린다("이상없는 것만 채움"). 이름류(person 등)는 음차만(literal 버림) 유지 — 아래 loop 참조.
var titleTypes = map[string]bool{
	"drama": true, "movie": true, "show": true, "song_album": true,
	"event_tour": true, "agency": true, "brand_place": true,
	"channel_outlet": true, "term": true,
}

// latinPassthroughTypes — 라틴 승계를 허용하는 타입.
//
// ★근거(2026-08-15 권위출처 실측). 한글 ko + 라틴 en 인 active 엔티티에서 **권위 소스가
// 실제로 준 zh/ja 가 라틴이었던 비율**:
//
//	group 71%/58% · channel_outlet 48/39 · agency 43/29 · brand_place 35/15
//	vs  drama 1/4 · movie 1/6 · person 3/2 · character 2/1 · show 10/16 · event_tour 8/25
//
// 제목류·인물류는 한자·가나가 정답이다(`신화→神话` `우주소녀→宇宙少女` `동방신기→東方神起`
// `봄여름가을겨울→春夏秋冬`). 반대로 라틴이 정답인 쪽은 ko 가 **외래어 음차**인 경우다
// (`나인뮤지스→Nine Muses` `카드→KARD` `키스오브라이프→KISS OF LIFE`).
//
// 진짜 판별자는 타입이 아니라 "ko 가 한국어 의미를 갖느냐 외래어 음차냐"이고, 타입은 그
// 대리일 뿐이다. 그래서 타입만으로 채우지 않고 아래 latinPassthrough 의 구글 합치 조건을
// 함께 요구한다 — 대리지표 하나로 쓰기에는 group 조차 29%가 한자다.
var latinPassthroughTypes = map[string]bool{
	"group": true, "agency": true, "channel_outlet": true, "brand_place": true,
}

// mtCJKRe — 한자·가나·한글 중 하나라도 있는가(라틴 순수성 검사용).
var mtCJKRe = regexp.MustCompile(`[\p{Han}\p{Hiragana}\p{Katakana}\p{Hangul}]`)

var mtNonAlnumRe = regexp.MustCompile(`[^0-9A-Za-z]+`)

// mtNormLatin — 대소문자·구두점·공백을 접은 비교키. `M.I.L.K.` 와 `M.I.L.K`,
// `Nine Muses` 와 `NINE MUSES` 를 같게 본다.
func mtNormLatin(s string) string {
	return strings.ToLower(mtNonAlnumRe.ReplaceAllString(s, ""))
}

// latinPassthroughWeakEnSources — canonical_en 자체가 **기계 추측**인 출처.
//
// ★2026-08-15 1차 적용의 실패에서 추가했다. 조건을 "우리 en == 구글 번역" 하나로 두고
// 돌렸더니 85칸이 채워졌는데 **그중 61칸의 canonical_en_source 가 'gtranslate'** 였다.
// 즉 우리 en 도 구글이 만든 값이라 "구글이 우리 en 과 같은 답을 냈다"는 게 **순환논증**
// 이었다 — 같은 엔진이 같은 입력에 같은 답을 낸 것을 두 출처의 합치로 읽은 것이다.
//
// 그 결과가 값에 그대로 나왔다: `서순라길→Seosunra-gil` `에스케이재원→SK Jaewon`
// `꼬끄드허양일→Coque de Heo Yang-il`. 전부 **한국어 이름의 로마자**이지 중국어 표기가
// 아니다. 라틴이 정답인 쪽(`GOT7` `MONSTA X` `RBW`)과 섞여 들어왔다.
var latinPassthroughWeakEnSources = map[string]bool{
	"": true, "gtranslate": true, "codex-fallback": true, "romanization": true,
}

// latinPassthroughHasLatin — 라틴 글자를 하나라도 포함하는가.
func latinPassthroughHasLatin(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool {
		return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
	})
}

// latinWellFormed — 이름 표기로 쓸 수 있는 꼴인가. 괄호 균형과 시작 문자를 본다.
//
// ★필요해진 이유(08-15 실측): canonical_en 이 이미 깨져 있는 건이 있고(`아이들(G)I-DLE`
// 의 en 이 `G)I-DLE`), 승계가 그 깨짐을 zh 칸으로 **복제**했다. 승계는 값을 만들지 않으니
// 안전하다고 생각했는데, 원본이 깨져 있으면 깨짐을 퍼뜨린다.
func latinWellFormed(s string) bool {
	depth := 0
	for _, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	if depth != 0 {
		return false
	}
	first := []rune(s)[0]
	return (first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z') ||
		(first >= '0' && first <= '9') || first == '('
}

// latinPassthrough — 기계번역 결과가 canonical_en 과 같은 라틴 표기면 승계값을 준다.
//
// 구글이 ko→zh/ja 번역으로 라틴 문자열을 돌려주고 그게 우리 canonical_en 과 일치하면
// "번역 실패"가 아니라 **그 이름의 현지 표기가 라틴**이라는 뜻이다. 종전에는 게이트가
// `영문 그대로·미번역` 으로 기각해 128칸(zh 99 · ja 29)이 비어 있었다.
//
// 가드가 넷이다:
//   - 타입 — 사전확률(latinPassthroughTypes 주석의 권위출처 실측).
//   - 구글 합치 — 건별 증거. 구글이 뜻으로 풀었으면(`뮤지엄피→博物馆P`) 여기서 안 걸린다.
//   - **증거 독립성** — 위 합치가 순환이 아니어야 한다. 아래 참조.
//   - 값 정합성 — 깨진 en 을 복제하지 않는다.
//
// 증거 독립성이 성립하는 경우는 둘 중 하나다:
//
//	① canonical_en 이 기계 추측이 아닌 출처에서 왔다(wikidata-label·local-usage·
//	   musicbrainz·correction-verified 등). 그러면 en 과 구글은 서로 다른 출처다.
//	② canonical_ko 자체가 라틴을 품고 있다 — `몬스타X` `갓세븐(GOT7)` `MBC드라마넷`
//	   `SHgold네트웍스`. 이름의 라틴 형태가 **한국 표기 안에 이미 있다**는 직접 증거이고,
//	   구글이나 우리 en 과 무관하게 성립한다.
//
// 반환값은 mt 가 아니라 en 이다 — 우리 canonical_en 이 검증된 철자이고 대소문자도 그쪽이
// 정본이다(`Lun8` vs 구글 `LUN8`).
func latinPassthrough(entityType, ko, en, enSource, mt string) (string, bool) {
	if !latinPassthroughTypes[entityType] {
		return "", false
	}
	en, mt = strings.TrimSpace(en), strings.TrimSpace(mt)
	if en == "" || mt == "" {
		return "", false
	}
	// 양쪽 다 CJK/한글이 없어야 한다. 부분 음차(`뮤지엄P`·`俊한`)를 걸러낸다.
	if mtCJKRe.MatchString(en) || mtCJKRe.MatchString(mt) {
		return "", false
	}
	k := mtNormLatin(en)
	if len(k) < 2 || k != mtNormLatin(mt) {
		return "", false
	}
	// 숫자만인 값은 이름 표기로 못 쓴다(`2024`). 라틴 글자가 하나는 있어야 한다.
	if !latinPassthroughHasLatin(k) {
		return "", false
	}
	if !latinWellFormed(en) {
		return "", false
	}
	if latinPassthroughWeakEnSources[strings.TrimSpace(enSource)] && !latinPassthroughHasLatin(ko) {
		return "", false
	}
	return en, true
}

var translitSchema = []byte(`{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "kind": {"type": "string", "enum": ["translit", "literal", "bad"]},
    "reason": {"type": "string"}
  },
  "required": ["kind"]
}`)

var mtTargetLang = map[string]string{"ja": "ja", "zh": "zh-CN"}

// nameTypes — 관대 게이트를 적용할 이름 타입. person(인물명) 전용이다: 한국 인명을
// 중국어로 옮기면 한자 음차, 일본어면 가나 음차 — 정의상 '음차'이므로 "애매하면
// literal(버림)" 규칙이 유효한 인명을 오거부한다(2026-07-26 실측: 하츄핑→哈楚平 을
// 엄격 게이트가 literal 로 버림, 관대 게이트가 회수). 관대=애매하면 translit.
//
// group/character 는 제외한다: 뜻-이름이 존재하고(솔개 트리오→风筝三重奏=kite trio,
// 특수임무국→特别任务局=special task bureau) 관대 게이트가 이를 오채움했다(실측). 명백히
// 발음인 그룹명(예스위아→耶斯维亚)은 엄격 게이트도 그대로 통과하므로 손실 없다. 제목
// 타입(drama/movie/show/song_album/event_tour 등)은 엄격 유지 — "빈칸>틀린값".
var nameTypes = map[string]bool{"person": true}

// judgeTranslit — gemma 판정: 기계번역 결과가 원어의 음차(소리옮김)인지 직역(뜻옮김)인지.
// 반환 kind: translit(음차, 채움가능) | literal(직역, 버림) | bad(깨짐/무의미, 버림).
// entityType 이 이름 타입이면 관대 게이트(애매하면 translit) — 이름은 음차가 기본.
//
// ★err — 게이트를 **부르지 못한** 경우(gemma 장애·타임아웃·파싱실패)다. 종전에는 이때도
// "literal"(버림)을 반환했고, 호출측은 그걸 내용판정으로 믿고 30일 쿨다운을 찍었다.
// 즉 gemma 가 죽어 있는 동안 지나간 멀쩡한 후보가 "직역이라 버림"으로 낙인찍혔다
// (gemma 는 08-06 하루에만 두 번 OFFLINE — 02:14 deadline, 02:45 DNS, 34분).
// 이제 err 를 분리해 호출측이 마킹 없이 다음 회차로 넘긴다.
func judgeTranslit(ctx context.Context, ko, mt, locale, entityType string) (kind, reason string, err error) {
	locName := map[string]string{"ja": "일본어", "zh": "중국어(간체)"}[locale]
	isName := nameTypes[entityType]
	var b strings.Builder
	b.WriteString("당신은 한국 고유명사(인물·그룹·작품·브랜드)의 현지표기 검수관입니다.\n")
	b.WriteString("아래 기계번역이 원어의 '음차'인지 '직역'인지 판정하세요.\n\n")
	if isName {
		b.WriteString("※ 이 항목은 **인물 이름**입니다. 한국 인명의 현지표기는 '음차'가 기본입니다.\n\n")
	}
	b.WriteString("원어(한국어): " + ko + "\n")
	b.WriteString("기계번역(" + locName + "): " + mt + "\n\n")
	b.WriteString("정의:\n")
	b.WriteString("- translit(음차) = 원어의 '소리'를 옮긴 표기. 고유명사 현지표기로 사용 가능.\n")
	b.WriteString("  예: 롱샷→ロングショット, 김현민→キム・ヒョンミン, 멜론티켓→メロンチケット.\n")
	b.WriteString("- literal(직역) = 원어의 '뜻'을 번역한 것. 고유명사 표기로 부적합(틀린 값).\n")
	b.WriteString("  예: 롱샷→远距离拍摄(원거리촬영), 멜론티켓→蜜瓜票(멜론표), 놀면 뭐하니→遊ぶと何してるの.\n")
	b.WriteString("- bad = 깨짐·무의미·원어 그대로(미번역).\n\n")
	if isName {
		b.WriteString("규칙: 이름은 음차가 기본이다. 한국 인명/그룹명을 한자·가나로 옮긴 것은 translit.\n")
		b.WriteString("명백히 '뜻'으로 번역됐거나(별명의 의미 등) 깨진 경우만 literal/bad, **애매하면 translit**.\n")
	} else {
		b.WriteString("규칙: 발음이 원어와 대응하면 translit. 의미로 번역됐으면 literal. **애매하면 literal**(틀린 표기를 넣느니 비우는 게 낫다).\n")
	}
	b.WriteString("JSON 한 개만: {\"kind\":\"translit|literal|bad\",\"reason\":\"근거 한 줄\"}\n")

	cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	raw, cerr := gemma.Complete(cctx, b.String(), translitSchema)
	if cerr != nil {
		return "", "", cerr // 게이트 미호출 — 판정 아님. 마킹하지 말 것.
	}
	var v struct {
		Kind   string `json:"kind"`
		Reason string `json:"reason"`
	}
	if uerr := json.Unmarshal(raw, &v); uerr != nil {
		return "", "", uerr // 응답이 깨짐 — 역시 판정 아님.
	}
	if v.Kind == "" {
		return "", "", errGateNoVerdict
	}
	return v.Kind, strings.TrimSpace(v.Reason), nil
}

var titleGateSchema = []byte(`{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "ok": {"type": "boolean"},
    "reason": {"type": "string"}
  },
  "required": ["ok"]
}`)

// judgeTitleTranslation — 제목류(작품·브랜드·행사) 기계번역이 '제목으로 쓸 만한 깔끔한
// 현지 표기'인지(ok=true) 아니면 '문장·오역·깨짐'인지(ok=false) 판정한다. 오너 지시
// 2026-07-26 "이상없는 것만 번역해서 채워라". 구글번역은 제목을 문장으로 풀거나(한 페이지가
// 될 수 있게→"这样它就可以只有一页了。") 오역(펜타포트→五角大楼=펜타곤)하는 사례가 많아,
// 일반 음차게이트로는 못 거른다. 휴리스틱(문장부호 종결) + LLM 판정을 이중으로 건다.
//
// ★err 의 의미는 judgeTranslit 과 같다 — "나쁜 번역"이 아니라 "물어보지 못했다".
// 07-26 대량 백필이 남긴 bad-title(zh 1,327 · ja 1,049) 중 몇 %가 이 경로였는지는
// 원장에 reason 이 없어 지금은 알 수 없다(0100 이 last_reason 을 추가한 이유).
func judgeTitleTranslation(ctx context.Context, ko, mt, locale string) (ok bool, reason string, err error) {
	t := strings.TrimSpace(mt)
	// 휴리스틱 ①: 서술 종결부호로 끝나면 제목이 아니라 문장 → 버림.
	for _, suf := range []string{"。", "．", ".", "…", "！", "!", "？", "?"} {
		if strings.HasSuffix(t, suf) {
			return false, "문장 종결부호(제목 아님)", nil
		}
	}
	// 휴리스틱 ②: 원어 대비 과도하게 길면(문장으로 풀림) 의심 → 버림.
	//   ko 를 문자수로 재고, 번역이 ko 의 3배+ 이며 12자 초과면 문장 가능성 큼.
	if rc, tc := len([]rune(ko)), len([]rune(t)); tc > 12 && tc >= rc*3 {
		return false, "원어 대비 과장(문장 의심)", nil
	}
	locName := map[string]string{"ja": "일본어", "zh": "중국어(간체)"}[locale]
	var b strings.Builder
	b.WriteString("당신은 한국 작품/브랜드/행사 '제목'의 현지표기 검수관입니다.\n")
	b.WriteString("아래 기계번역이 **제목으로 그대로 쓸 수 있는 깔끔한 표기**인지 판정하세요.\n\n")
	b.WriteString("원어(한국어 제목): " + ko + "\n")
	b.WriteString("기계번역(" + locName + "): " + mt + "\n\n")
	b.WriteString("ok=true (채움): 원어 제목의 뜻/발음을 옮긴 자연스러운 현지 제목 표기.\n")
	b.WriteString("  예: 청혼→求婚, 펜트하우스2→顶层公寓2, 포도밭→葡萄园, 아워 스토리→我们的故事.\n")
	b.WriteString("ok=false (버림): 다음 중 하나라도 해당하면 false.\n")
	b.WriteString("  - 문장으로 풀림(서술문·설명문): 한 페이지가 될 수 있게→\"这样它就可以只有一页了\".\n")
	b.WriteString("  - 오역(엉뚱한 뜻): 펜타포트→五角大楼(펜타곤 건물), 순이엔티→太阳娱乐(순이≠태양).\n")
	b.WriteString("  - 깨짐·무의미·원어 그대로(미번역)·번역기 오류.\n")
	b.WriteString("**애매하면 false**(틀린 제목을 넣느니 비우는 게 낫다).\n")
	b.WriteString("JSON 한 개만: {\"ok\":true|false,\"reason\":\"근거 한 줄\"}\n")

	cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	raw, cerr := gemma.Complete(cctx, b.String(), titleGateSchema)
	if cerr != nil {
		return false, "", cerr // 게이트 미호출 — 판정 아님. 마킹하지 말 것.
	}
	var v struct {
		OK     bool   `json:"ok"`
		Reason string `json:"reason"`
	}
	if uerr := json.Unmarshal(raw, &v); uerr != nil {
		return false, "", uerr
	}
	return v.OK, strings.TrimSpace(v.Reason), nil
}

// MTTranslitFill — active 엔티티의 locale(ja|zh) 빈칸을 구글번역→음차게이트로 채운다.
// dry=true 는 판정·로그만(DB 미변경 — 테스트/미리보기). 반환 (filled, discarded, processed).
// mtExcludedTypesSQL — 음차채움에서 타입으로 제외하는 집합. **선정 쿼리와 아래 판정
// 쿼리가 이 하나를 같이 쓴다** — 두 벌로 나뉘면 "제외는 하는데 판정은 안 남기는" 상태가
// 다시 생긴다. unknown 은 정체 미확정, term 은 일반어(고유명사 아님)라 대상이 아니다.
const mtExcludedTypesSQL = `'unknown','term'`

// markTypeExcluded — 타입 때문에 이 레인이 **구조적으로 손댈 수 없는** 건에 판정을 남긴다.
//
// ★왜(2026-08-08): 선정 쿼리가 unknown/term 을 제외하기만 하고 원장에 아무 흔적도 남기지
// 않아, 그 건들이 backlog-watch 의 `active-cjk-actionable` 에 **영원히 "아직 어느 레인도
// 판정 안 함"** 으로 남았다. 실측 term 7건(zh)·4건(ja)이 그 상태였다.
//
// 같은 날 romanize.go 의 라틴 원제 레인에서 고친 것과 **정확히 같은 구조의 병**이다 —
// 레인이 성공만 기록하고 기각을 안 남기면, 지표는 "손댈 수 있는 것"과 "판정이 끝난 것"을
// 구분하지 못한다(44차 §3 이 지표 쪽에서 잡아낸 바로 그 혼동).
//
// 낙인 걱정은 없다: fill_input_hash 가 entity_type 을 포함하므로(0104) 타입이 나중에
// 확정되면 지문이 바뀌어 판정이 자동으로 풀리고 레인이 다시 집는다.
func markTypeExcluded(ctx context.Context, pool *pgxpool.Pool, col, attemptField string, dry bool) {
	if pool == nil || dry {
		return
	}
	rows, err := pool.Query(ctx, `
SELECT id::text, entity_type FROM kwave_entities e
 WHERE status='active' AND operator_locked=false
   AND COALESCE(`+col+`,'')='' AND canonical_ko ~ '[가-힣]'
   AND entity_type IN (`+mtExcludedTypesSQL+`)
   AND `+kdb.FillRetryPredicate("e", "$1"), attemptField)
	if err != nil {
		log.Printf("kdb.mt-translit: 타입제외 판정 조회: %v", err)
		return
	}
	type row struct{ id, typ string }
	var todo []row
	for rows.Next() {
		var id, typ string
		if rows.Scan(&id, &typ) == nil {
			todo = append(todo, row{id, typ})
		}
	}
	rows.Close()
	for _, r := range todo {
		kdb.MarkFillAttempt(ctx, pool, r.id, attemptField, "type-excluded",
			"entity_type="+r.typ+" 는 음차채움 대상 아님")
	}
	if len(todo) > 0 {
		log.Printf("kdb.mt-translit: %s 타입제외 판정 %d건(unknown/term — 값 변경 없음)",
			attemptField, len(todo))
	}
}

func (o *Orchestrator) MTTranslitFill(ctx context.Context, locale string, limit int, dry bool) (filled, discarded, processed int) {
	target, ok := mtTargetLang[locale]
	if !ok {
		log.Printf("kdb.mt-translit: 미지원 locale=%q (ja|zh 만)", locale)
		return 0, 0, 0
	}
	if !gemma.Configured() {
		log.Printf("kdb.mt-translit: gemma 미설정 — 중단(음차게이트 불가)")
		return 0, 0, 0
	}
	g := kdb.NewGTranslator(o.Pool)
	if g == nil {
		log.Printf("kdb.mt-translit: KDB_GTRANSLATE_KEY 미설정 — 중단")
		return 0, 0, 0
	}
	col := "canonical_" + locale
	// 시도추적 키 — 구글이 못 옮기는 고유명사·직역으로 버려진 항목이 매 회차 updated_at DESC
	// 리스트 머리에 재등장해 드레인이 수렴 못 하던 버그(2026-07-26) 수정.
	//
	// 재선택 제외 규칙은 2026-08-06 부터 kdb.FillRetryPredicate 하나로 통일됐다. 종전
	// 규칙(30d 쿨다운 + 3회 exhausted)은 "시간이 지났나"만 봐서, 07-26 대량 백필 한 번이
	// 백로그 전체의 시도권을 하루에 소진시켰다 — 이후 이 드레인은 예산 250 을 받고 3건을
	// 집었다(08-06 실측: 대상 1,568 중 선정 0). 새 규칙은 "입력이 바뀌었나"를 본다.
	attemptField := "mt-fill:" + locale
	// 타입으로 제외되는 건에 먼저 판정을 남긴다 — 안 남기면 영영 "미판정"으로 경보에 쌓인다.
	markTypeExcluded(ctx, o.Pool, col, attemptField, dry)
	rows, err := o.Pool.Query(ctx, `
SELECT id::text FROM kwave_entities e
 WHERE status='active' AND operator_locked=false
   AND COALESCE(`+col+`,'')='' AND canonical_ko ~ '[가-힣]'
   AND entity_type NOT IN (`+mtExcludedTypesSQL+`)
   AND `+kdb.FillRetryPredicate("e", "$2")+`
 ORDER BY updated_at DESC
 LIMIT $1`, limit, attemptField)
	if err != nil {
		log.Printf("kdb.mt-translit: query: %v", err)
		return 0, 0, 0
	}
	var ids []string
	for rows.Next() {
		var s string
		if rows.Scan(&s) == nil {
			ids = append(ids, s)
		}
	}
	rows.Close()
	log.Printf("kdb.mt-translit: %s 음차채움 시작 (%d건, dry=%v)", locale, len(ids), dry)

	for _, idStr := range ids {
		if ctx.Err() != nil {
			break
		}
		uid, perr := uuid.Parse(idStr)
		if perr != nil {
			continue
		}
		snap, serr := loadSnapshot(ctx, o.Pool, uid)
		if serr != nil || snap == nil || snap.Values[locale] != "" {
			continue
		}
		processed++
		// 소스 오염 사전차단 — 깨진 ko 는 번역하지 않는다(오너 "이상없는 것만"). 쿨다운 마킹해
		// 재선택 제외.
		if mtBrokenKoRe.MatchString(snap.Ko) {
			discarded++
			if !dry {
				kdb.MarkFillAttempt(ctx, o.Pool, idStr, attemptField, "broken-source",
					"ko 에 깨진 토큰/플레이스홀더")
			}
			continue
		}
		isTitle := titleTypes[snap.EntityType]
		mt, terr := g.TranslateRawTo(ctx, snap.Ko, target)
		if terr != nil {
			continue // 일시 장애 — 다음 회차
		}
		if mt == "" { // 미번역(구글이 모르는 고유명사) — 결정적 실패.
			discarded++
			if !dry {
				kdb.MarkFillAttempt(ctx, o.Pool, idStr, attemptField, "untranslatable",
					"구글이 번역을 내놓지 않음")
			}
			continue
		}
		// ★라틴 승계는 게이트보다 먼저 본다(2026-08-15). 게이트는 라틴 결과를
		// `영문 그대로·미번역` 으로 기각하도록 설계돼 있어서, 뒤에 두면 영영 못 지난다.
		// 조건·근거는 latinPassthrough 주석 참조(타입 사전확률 + 구글 합치, 가드 둘).
		if pv, ok := latinPassthrough(snap.EntityType, snap.Ko, snap.Values["en"], snap.Sources["en"], mt); ok {
			if dry {
				log.Printf("kdb.mt-translit[dry]: 라틴승계 %q → %s=%q (mt=%q, type=%s)",
					snap.Ko, locale, pv, mt, snap.EntityType)
				filled++
				continue
			}
			applied, _ := o.applyEmptyOnly(ctx, snap, map[string][]string{locale: {pv}}, kdb.SourceRomanization)
			if len(applied) > 0 {
				filled++
				kdb.ClearFillAttempt(ctx, o.Pool, idStr, attemptField)
				log.Printf("kdb.mt-translit: %q → %s=%q (라틴승계)", snap.Ko, locale, pv)
			} else {
				// 여기 오면 대개 dataqa 가 이미 오염으로 비운 값을 다시 넣으려 한 것이다.
				discarded++
				kdb.MarkFillAttempt(ctx, o.Pool, idStr, attemptField, "guard-reject",
					"라틴승계 applyEmptyOnly 가드 기각: "+pv)
			}
			continue
		}
		// 수용 규칙(타입별):
		//   이름류 → judgeTranslit: 음차(translit)만 채움. 직역/깨짐 버림.
		//   제목류 → judgeTitleTranslation: 제목으로 쓸 만한 깔끔한 번역(ok)만 채움. 문장·오역·
		//            깨짐(bad) 버림. 구글이 제목을 문장으로 풀거나(한 페이지가 될 수 있게→"…了。")
		//            오역(펜타포트→五角大楼=펜타곤)하는 걸 걸러 "이상없는 것만" 채운다.
		var accept bool
		var kind, reason, mode string
		var gerr error
		if isTitle {
			mode = "뜻번역"
			accept, reason, gerr = judgeTitleTranslation(ctx, snap.Ko, mt, locale)
			if !accept {
				kind = "bad-title"
			}
		} else {
			mode = "음차"
			kind, reason, gerr = judgeTranslit(ctx, snap.Ko, mt, locale, snap.EntityType)
			accept = kind == "translit"
		}
		// ★게이트를 부르지 못했으면 판정이 없는 것이다 — 마킹 없이 다음 회차로 넘긴다.
		// 종전에는 gemma 장애가 "직역/나쁜제목"으로 기록돼 멀쩡한 후보에 30일 쿨다운이
		// 찍혔다. gemma 는 상시로 죽는다(08-06 하루 두 번, 34분). 이건 이 저장소가 이미
		// 네 번 고친 결함과 같은 계열이다 — a022567, enricher/layers.go transport 분기.
		if gerr != nil {
			log.Printf("kdb.mt-translit: 게이트 미호출 %q — 마킹 없이 다음 회차 (%v)", snap.Ko, gerr)
			continue
		}
		if !accept { // 버림 — 게이트가 실제로 내린 판정이다. 같은 입력이면 재시도 무의미.
			discarded++
			if dry {
				log.Printf("kdb.mt-translit[dry]: 버림(%s) %q → %q (%s)", kind, snap.Ko, mt, reason)
			} else {
				kdb.MarkFillAttempt(ctx, o.Pool, idStr, attemptField, kind, reason)
			}
			continue
		}
		if dry {
			log.Printf("kdb.mt-translit[dry]: 채움후보(%s) %q → %q", mode, snap.Ko, mt)
			filled++
			continue
		}
		applied, _ := o.applyEmptyOnly(ctx, snap, map[string][]string{locale: {mt}}, kdb.SourceGTranslate)
		if len(applied) > 0 {
			filled++
			// 채워졌으면 옛 실패 기록을 지운다 — 남겨두면 나중에 dataqa 가 이 값을
			// 오염으로 비웠을 때 그 기록이 재채움을 막는다.
			kdb.ClearFillAttempt(ctx, o.Pool, idStr, attemptField)
			log.Printf("kdb.mt-translit: %q → %s=%q (%s)", snap.Ko, locale, mt, mode)
		} else {
			discarded++ // 문자셋/suppress 가드 기각 또는 동시 채움 — 가드는 결정적이다.
			kdb.MarkFillAttempt(ctx, o.Pool, idStr, attemptField, "guard-reject",
				"applyEmptyOnly 가드 기각 또는 동시 채움")
		}
	}
	log.Printf("kdb.mt-translit: %s 완료 filled=%d discarded=%d /%d (dry=%v)", locale, filled, discarded, processed, dry)
	return filled, discarded, processed
}

// markMTAttempt 는 2026-08-06 에 kdb.MarkFillAttempt 로 흡수됐다. 레인마다 자기 쿨다운
// 규칙(30d + 3회 exhausted)을 들고 있던 게 문제의 뿌리였다 — 여섯 레인이 여섯 규칙을
// 가졌고, 전부 "시간이 지났나"만 봤다. 규칙은 이제 kdb/fill_retry.go 한 곳에 있다.
