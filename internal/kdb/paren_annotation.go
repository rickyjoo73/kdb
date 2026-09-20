package kdb

// paren_annotation — **출처의 동음이의 주석이 표기 자리에 그대로 들어온 것**을 걷는다.
//
// ★계기(2026-09-20). 오늘 되살린 행을 눈으로 보다가 발견했다.
//
//	김병욱  canonical_zh = "金炳旭 (1965年)"        (wikidata-label)
//	박태준  canonical_zh = "朴泰俊 (跆拳道运动员)"   (wikidata-label)
//	임수연  canonical_zh = "林秀妍（音译）"          (opencc)
//	이새롬  canonical_zh = "李赛纶（独立条目）"      (opencc)
//
//	«(1965년)»·«(태권도 선수)» 는 위키백과가 **같은 이름이 여럿일 때 문서를 가르려고**
//	붙인 것이다. 사람 이름이 아니다. «(음역)»·«(독립 항목)» 에 이르면 아예 위키의
//	내부 사정이다. 이걸 그대로 내보내면 번역 쪽은 그게 공식 표기인 줄 안다.
//
//	실측: active 기준 zh 128 · zh_hant 121 · ja 235 · en 117 — 합계 601칸.
//
// ★그런데 괄호가 다 주석은 아니다. 이름 자체에 괄호가 있는 것이 있다.
//
//	f(x)         에프엑스 — **이게 그룹 이름이다**
//	ALL(H)OURS   올아워즈 — 마찬가지
//	Amigo (Feat. 民秀)                 — 곡 제목의 일부
//	献给小事的诗（Boy With Luv）        — 곡 제목의 일부
//
// ★가르는 기준을 «내 판단»으로 두지 않는다. 두 가지 관측만 쓴다.
//
//	① **한국어 정본에 괄호가 있는가.** 있으면 제목의 일부다 — 실측 128건 중 84건이
//	   그랬다. 없는데 번역 칸에만 생겼다면 그건 **출처가 붙인 것**이다.
//	② **괄호 안의 말이 동음이의 주석 어휘인가.** 직업·연도·매체종류·위키 내부용어.
//	   이 목록에 없으면 손대지 않는다.
//
//	②가 없으면 f(x) 가 f 가 되고, Jin（金硕珍）이 Jin 이 된다 — 하필 **버려야 할 쪽을
//	남기는** 실수다. 가장 위험한 실패라 어휘 목록으로 막는다.
//
// ★그래도 못 고치는 것이 남는다. 그건 그대로 둔다.
//
//	Korea International Exhibition Center（韩国国际展览中心）  킨텍스
//	  → 괄호 **안**이 맞는 값이다. 머리를 남기면 중국어 칸에 라틴이 남는다.
//	Jin（金硕珍） · Bobby (金知元） · Crystal (郑秀晶)
//	  → 같은 계열. 어휘 목록에 안 걸리므로 자동으로 건너뛴다.
//
//	이것들은 «어느 쪽이 맞는가»를 사람이 봐야 한다. 빈칸이 틀린 값보다 낫고,
//	지금 값이 틀린 값보다 낫다고 말할 수 없으므로 — 건드리지 않고 보고만 한다.

import (
	"context"
	"log"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// trailingParenRe — 값 **끝**에 붙은 괄호 덩어리. 전각·반각 둘 다.
//
// ★끝에 있는 것만 본다. ALL(H)OURS 는 괄호 뒤에 OURS 가 있어 여기서 이미 빠진다.
var trailingParenRe = regexp.MustCompile(`^(.*?)[ _\x{3000}]*[(（]([^()（）]*)[)）]\s*$`)

// annotationVocab — 괄호 안이 **동음이의 주석**이라고 말하는 말들.
//
// ★어휘를 쓰는 이유는 구조만으로는 f(x) 를 못 지키기 때문이다(위 주석 참조).
// 새 표현을 만나면 여기만 고친다 — 판단을 코드 여기저기 흩지 않는다.
var annotationVocab = []string{
	// 직업·역할 (간체)
	"演员", "歌手", "导演", "制作人", "政治人物", "喜剧演员", "运动员", "主持人",
	"作家", "模特", "选手", "教练", "记者", "企业家", "学者", "画家", "棋手",
	// 직업·역할 (번체)
	"演員", "導演", "製作人", "喜劇演員", "運動員", "主持人", "選手", "記者",
	"企業家", "學者", "畫家", "棋手",
	// 매체·단체 종류
	"乐团", "樂團", "乐队", "樂隊", "组合", "組合", "电竞队伍", "電競隊伍",
	"平台", "单曲", "單曲", "专辑", "專輯", "歌曲", "电视剧", "電視劇",
	"电影", "電影", "节目", "節目", "综艺", "綜藝", "漫画", "漫畫", "小说", "小說",
	// 위키 내부 용어 — 표기가 아니라 편집 사정이다
	"音译", "音譯", "独立条目", "獨立條目", "消歧义", "消歧義", "消除歧义",
	// 일본어
	//
	// ★일본어 칸의 괄호는 **대부분 정상이다.** 한자·라틴 표기 뒤에 가타카나 읽기를
	//   붙이는 것이 일본어 관행이다 — 推奴（チュノ） · 農心 (ノンシム) · 朱蒙（チュモン）.
	//   그래서 여기 어휘는 «직업·매체 종류»로만 좁게 둔다. 읽기 병기를 걷으면
	//   일본 독자가 읽을 방법을 잃는다.
	"俳優", "歌手", "声優", "タレント", "アイドル", "野球選手", "サッカー選手",
	"曖昧さ回避", "ミュージシャン", "バンド", "技術者", "放送社", "キャラクター",
	"コメディアン", "政治家", "実業家", "監督", "プロデューサー", "漫画家", "作曲家",
	// 영어
	"actor", "actress", "singer", "band", "musician", "footballer", "politician",
	"disambiguation", "album", "song", "film", "TV series", "group",
	"comedian", "engineer", "broadcaster", "character", "director", "producer",
}

// yearOnlyRe — 괄호 안이 연도뿐인 것. "1965年" · "1965" · "생년 1965년".
var yearOnlyRe = regexp.MustCompile(`^\s*(生于\s*)?[0-9]{4}\s*(年|年生|년|-|–)?\s*$`)

// isSourceAnnotation — 괄호 안의 말이 동음이의 주석인가.
func isSourceAnnotation(inner string) bool {
	s := strings.TrimSpace(inner)
	if s == "" {
		return false
	}
	if yearOnlyRe.MatchString(s) {
		return true
	}
	low := strings.ToLower(s)
	for _, v := range annotationVocab {
		if strings.Contains(low, strings.ToLower(v)) {
			return true
		}
	}
	return false
}

// StripSourceAnnotation — 출처가 붙인 동음이의 주석을 걷는다.
//
//	koCanonical  한국어 정본. 여기에 괄호가 있으면 제목의 일부라 보고 손대지 않는다.
//	locale       표기가 앉은 칸(en·ja·zh·zh_hant…). 결과의 유효성 검사에 쓴다.
//	value        지금 값.
//
// 돌려주는 bool 은 «걷었는가». false 면 value 를 그대로 두라는 뜻이다.
//
//	entityType   사람인가 작품인가. 사람의 중국어 칸에서 «한자 없는 머리»를 남기면
//	             틀린 값을 **그럴듯하게 다듬는** 것이 된다 — 아래 주석 참조.
func StripSourceAnnotation(locale, entityType, koCanonical, value string) (string, bool) {
	v := strings.TrimSpace(value)
	if v == "" {
		return value, false
	}
	// ① 한국어에 괄호가 있으면 제목의 일부다.
	if strings.ContainsAny(koCanonical, "(（") {
		return value, false
	}
	m := trailingParenRe.FindStringSubmatch(v)
	if m == nil {
		return value, false
	}
	head, inner := strings.TrimSpace(m[1]), m[2]
	if head == "" {
		return value, false
	}
	// ② 괄호 안이 주석 어휘여야 한다.
	if !isSourceAnnotation(inner) {
		return value, false
	}
	// 남는 머리가 그 칸에서 유효한 표기여야 한다. 아니면 손대지 않는다 —
	// 지금 값이 나쁘다고 더 나쁜 값으로 바꿀 이유는 없다.
	if !IsValidSpellingForLocale(locale, head) {
		return value, false
	}
	// ★사람의 중국어 칸에 한자가 하나도 없으면 그건 값이 아니라 **구멍**이다.
	//
	//   전소현 canonical_zh = "Yuna (演员)" 를 걷으면 "Yuna" 가 된다. 주석은 사라지지만
	//   사람 이름의 중국어 표기가 라틴이라는 사실은 그대로다 — 오히려 «Wavve» 같은
	//   정상적인 라틴 브랜드명처럼 보여 **구멍이 눈에 덜 띈다.** 틀린 값을 그럴듯하게
	//   다듬는 일은 하지 않는다. 보이는 채로 둔다.
	//
	//   단체·작품은 다르다 — Nell · T1 · The Rose · UNIVERSE 는 중국어권에서도 라틴
	//   이름을 그대로 쓴다. 그래서 person 에만 건다.
	if entityType == "person" && (locale == "zh" || locale == "zh-hant" || locale == "zh_hant") &&
		!containsHan(head) {
		return value, false
	}
	return head, true
}

// containsHan — 한자가 하나라도 있는가.
func containsHan(s string) bool {
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
}

// ParenAnnotationResult — 한 번 돈 결과.
type ParenAnnotationResult struct {
	Checked, Stripped, Held int
	Samples                 []string
}

// parenAnnotationCols — 볼 칸과 그 locale 이름.
var parenAnnotationCols = []struct{ col, locale string }{
	{"canonical_en", "en"},
	{"canonical_ja", "ja"},
	{"canonical_zh", "zh"},
	{"canonical_zh_hant", "zh-hant"},
}

// DrainParenAnnotations — active 행에서 출처 주석을 걷는다. 기본 dry-run.
func DrainParenAnnotations(ctx context.Context, pool *pgxpool.Pool, limit int, dry bool) ParenAnnotationResult {
	var r ParenAnnotationResult
	if pool == nil {
		return r
	}
	if limit <= 0 {
		limit = 500
	}
	for _, c := range parenAnnotationCols {
		rows, err := pool.Query(ctx, `
SELECT id::text, canonical_ko, entity_type::text, COALESCE(`+c.col+`,'')
  FROM kwave_entities
 WHERE status='active' AND operator_locked=false
   AND COALESCE(`+c.col+`,'') ~ '[(（]'
 ORDER BY canonical_ko`)
		if err != nil {
			log.Printf("kdb.paren-annot: select %s: %v", c.col, err)
			continue
		}
		type item struct{ id, ko, typ, val string }
		var items []item
		for rows.Next() {
			var it item
			if rows.Scan(&it.id, &it.ko, &it.typ, &it.val) == nil {
				items = append(items, it)
			}
		}
		rows.Close()

		for _, it := range items {
			if r.Checked >= limit {
				break
			}
			r.Checked++
			fixed, ok := StripSourceAnnotation(c.locale, it.typ, it.ko, it.val)
			if !ok {
				r.Held++
				continue
			}
			if len(r.Samples) < 60 {
				r.Samples = append(r.Samples, c.col+" "+it.ko+": "+it.val+" → "+fixed)
			}
			log.Printf("  [%s] %-20s %q → %q", c.col, it.ko, it.val, fixed)
			if dry {
				r.Stripped++
				continue
			}
			tag, uerr := pool.Exec(ctx, `
UPDATE kwave_entities
   SET `+c.col+`=$2, updated_at=now(),
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || $3
 WHERE id=$1 AND `+c.col+`=$4 AND operator_locked=false`,
				it.id, fixed, "[paren-annot] 출처 동음이의 주석 제거: "+it.val, it.val)
			if uerr == nil && tag.RowsAffected() > 0 {
				r.Stripped++
			}
		}
	}
	return r
}
