package kdb

// kana.go — 한국 인명(person/group/character)의 canonical_ja 빈칸을 한글→가타카나 결정적
// 변환으로 채운다. 외부호출 0·벌크안전. 로마자 변환(romanize.go)의 ja 대응물 — 한국 인명을
// 일본 매체가 표기하는 관례(안재현→アン・ジェヒョン)를 규칙으로 재현한다(기계번역 아님).
//
// ★검증(2026-07-19): wikidata 권위 ja 정답 1,850쌍 대조 음운정확 ~78%(최고정밀 부분집합 88%).
// 나머지 오차는 일본 위키 자체의 유성음화·분리기호 편집 비일관 + 예명 성 생략이라 규칙 천장.
// ★가드: source='kana-rule'(prio 8 = gtranslate 동급 최하위) → 모든 권위·매체·wiki 소스가
// 자동 업그레이드. provenance='rule-transliteration' → verified_only 소비자엔 노출 안 함(빈칸).
// 빈칸만 채움(기존값 불가침). dataqa_log 스냅샷으로 전건 복원 가능.

import (
	"context"
	"log"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
)

var kanaSurnames = func() map[string]bool {
	m := map[string]bool{}
	for _, r := range "김이박최정강조윤장임한오서신권황안송전홍고문양손배백허유남심노하곽성차주우구나민진지엄채원천방공현함변염여추도소석선설마길연위표명기반라왕금옥육인맹제모탁국어은편용예경봉사부가복태목형계피감동" {
		m[string(r)] = true
	}
	for _, s := range []string{"남궁", "황보", "선우", "제갈", "사공", "서문", "독고"} {
		m[s] = true
	}
	return m
}()

var kanaCho = []string{"ㄱ", "ㄲ", "ㄴ", "ㄷ", "ㄸ", "ㄹ", "ㅁ", "ㅂ", "ㅃ", "ㅅ", "ㅆ", "ㅇ", "ㅈ", "ㅉ", "ㅊ", "ㅋ", "ㅌ", "ㅍ", "ㅎ"}
var kanaJung = []string{"ㅏ", "ㅐ", "ㅑ", "ㅒ", "ㅓ", "ㅔ", "ㅕ", "ㅖ", "ㅗ", "ㅘ", "ㅙ", "ㅚ", "ㅛ", "ㅜ", "ㅝ", "ㅞ", "ㅟ", "ㅠ", "ㅡ", "ㅢ", "ㅣ"}
var kanaJong = []string{"", "ㄱ", "ㄲ", "ㄳ", "ㄴ", "ㄵ", "ㄶ", "ㄷ", "ㄹ", "ㄺ", "ㄻ", "ㄼ", "ㄽ", "ㄾ", "ㄿ", "ㅀ", "ㅁ", "ㅂ", "ㅄ", "ㅅ", "ㅆ", "ㅇ", "ㅈ", "ㅊ", "ㅋ", "ㅌ", "ㅍ", "ㅎ"}

var kanaUnv = map[string]string{"ㄱ": "カ", "ㄲ": "カ", "ㄴ": "ナ", "ㄷ": "タ", "ㄸ": "タ", "ㄹ": "ラ", "ㅁ": "マ", "ㅂ": "パ", "ㅃ": "パ", "ㅅ": "サ", "ㅆ": "サ", "ㅈ": "チ", "ㅉ": "チ", "ㅊ": "チ", "ㅋ": "カ", "ㅌ": "タ", "ㅍ": "パ", "ㅎ": "ハ"}
var kanaVcd = map[string]string{"ㄱ": "ガ", "ㄲ": "カ", "ㄴ": "ナ", "ㄷ": "ダ", "ㄸ": "タ", "ㄹ": "ラ", "ㅁ": "マ", "ㅂ": "バ", "ㅃ": "パ", "ㅅ": "サ", "ㅆ": "サ", "ㅈ": "ジ", "ㅉ": "チ", "ㅊ": "チ", "ㅋ": "カ", "ㅌ": "タ", "ㅍ": "パ", "ㅎ": "ハ"}

var kanaIRow = map[string]string{"カ": "キ", "ガ": "ギ", "サ": "シ", "ザ": "ジ", "タ": "チ", "ダ": "ヂ", "ナ": "ニ", "ハ": "ヒ", "バ": "ビ", "パ": "ピ", "マ": "ミ", "ラ": "リ"}
var kanaURow = map[string]string{"カ": "ク", "ガ": "グ", "サ": "ス", "ザ": "ズ", "タ": "ツ", "ダ": "ヅ", "ナ": "ヌ", "ハ": "フ", "バ": "ブ", "パ": "プ", "マ": "ム", "ラ": "ル"}
var kanaERow = map[string]string{"カ": "ケ", "ガ": "ゲ", "サ": "セ", "ザ": "ゼ", "タ": "テ", "ダ": "デ", "ナ": "ネ", "ハ": "ヘ", "バ": "ベ", "パ": "ペ", "マ": "メ", "ラ": "レ"}
var kanaORow = map[string]string{"カ": "コ", "ガ": "ゴ", "サ": "ソ", "ザ": "ゾ", "タ": "ト", "ダ": "ド", "ナ": "ノ", "ハ": "ホ", "バ": "ボ", "パ": "ポ", "マ": "モ", "ラ": "ロ"}
var kanaYA = map[string]string{"カ": "キャ", "ガ": "ギャ", "ナ": "ニャ", "ハ": "ヒャ", "バ": "ビャ", "パ": "ピャ", "マ": "ミャ", "ラ": "リャ", "サ": "シャ"}
var kanaYU = map[string]string{"カ": "キュ", "ガ": "ギュ", "ナ": "ニュ", "ハ": "ヒュ", "バ": "ビュ", "パ": "ピュ", "マ": "ミュ", "ラ": "リュ", "サ": "シュ"}
var kanaYO = map[string]string{"カ": "キョ", "ガ": "ギョ", "ナ": "ニョ", "ハ": "ヒョ", "バ": "ビョ", "パ": "ピョ", "マ": "ミョ", "ラ": "リョ", "サ": "ショ"}

var kanaJongMap = map[string]string{"": "", "ㄱ": "ク", "ㄲ": "ク", "ㄴ": "ン", "ㄷ": "ッ", "ㄹ": "ル", "ㅁ": "ム", "ㅂ": "プ", "ㅇ": "ン", "ㅅ": "ッ", "ㅆ": "ッ", "ㅈ": "ッ", "ㅊ": "ッ", "ㅌ": "ッ", "ㅍ": "プ", "ㅋ": "ク", "ㅎ": "", "ㄳ": "ク", "ㄵ": "ン", "ㄶ": "ン", "ㄺ": "ク", "ㄻ": "ム", "ㄼ": "ル", "ㅀ": "ル", "ㄽ": "ル", "ㄾ": "ル", "ㄿ": "プ", "ㅄ": "プ"}

var kanaZeroVowel = map[string]string{"ㅏ": "ア", "ㅐ": "エ", "ㅑ": "ヤ", "ㅒ": "イェ", "ㅓ": "オ", "ㅔ": "エ", "ㅕ": "ヨ", "ㅖ": "イェ", "ㅗ": "オ", "ㅘ": "ワ", "ㅙ": "ウェ", "ㅚ": "ウェ", "ㅛ": "ヨ", "ㅜ": "ウ", "ㅝ": "ウォ", "ㅞ": "ウェ", "ㅟ": "ウィ", "ㅠ": "ユ", "ㅡ": "ウ", "ㅢ": "ウイ", "ㅣ": "イ"}

func kanaDecomp(ch rune) (string, string, string) {
	o := int(ch) - 0xAC00
	return kanaCho[o/588], kanaJung[(o%588)/28], kanaJong[o%28]
}

func kanaOrDefault(m map[string]string, k, def string) string {
	if v, ok := m[k]; ok {
		return v
	}
	return def
}

func kanaSyl(cho, jung string, voiced bool) string {
	if cho == "ㅇ" {
		return kanaZeroVowel[jung]
	}
	base := kanaUnv[cho]
	if voiced {
		base = kanaVcd[cho]
	}
	if base == "チ" || base == "ジ" {
		c := base
		m := map[string]string{"ㅏ": c + "ャ", "ㅐ": c + "ェ", "ㅑ": c + "ャ", "ㅒ": c + "ェ", "ㅓ": c + "ョ", "ㅔ": c + "ェ", "ㅕ": c + "ョ", "ㅖ": c + "ェ", "ㅗ": c + "ョ", "ㅘ": c + "ャ", "ㅙ": c + "ェ", "ㅚ": c + "ェ", "ㅛ": c + "ョ", "ㅜ": c + "ュ", "ㅝ": c + "ョ", "ㅞ": c + "ェ", "ㅟ": c, "ㅠ": c + "ュ", "ㅡ": c, "ㅢ": c, "ㅣ": c}
		return m[jung]
	}
	if cho == "ㅎ" && (jung == "ㅘ" || jung == "ㅙ" || jung == "ㅚ") {
		return "ファ"
	}
	if cho == "ㅎ" && jung == "ㅝ" {
		return "フォ"
	}
	switch jung {
	case "ㅏ":
		return base
	case "ㅐ", "ㅔ":
		return kanaOrDefault(kanaERow, base, base)
	case "ㅑ":
		return kanaOrDefault(kanaYA, base, base+"ャ")
	case "ㅒ", "ㅖ":
		return kanaOrDefault(kanaERow, base, base)
	case "ㅓ", "ㅗ":
		return kanaOrDefault(kanaORow, base, base)
	case "ㅕ", "ㅛ":
		return kanaOrDefault(kanaYO, base, base+"ョ")
	case "ㅜ", "ㅡ":
		return kanaOrDefault(kanaURow, base, base)
	case "ㅠ":
		return kanaOrDefault(kanaYU, base, base+"ュ")
	case "ㅣ":
		return kanaOrDefault(kanaIRow, base, base)
	case "ㅘ":
		return kanaOrDefault(kanaURow, base, base) + "ァ"
	case "ㅙ", "ㅚ", "ㅞ":
		return kanaOrDefault(kanaURow, base, base) + "ェ"
	case "ㅝ":
		return kanaOrDefault(kanaURow, base, base) + "ォ"
	case "ㅟ":
		return kanaOrDefault(kanaURow, base, base) + "ィ"
	case "ㅢ":
		return kanaOrDefault(kanaIRow, base, base)
	}
	return base
}

func kanaWord(w string, surname bool) string {
	var b strings.Builder
	i := 0
	for _, ch := range w {
		if ch < '가' || ch > '힣' {
			b.WriteRune(ch)
			i++
			continue
		}
		cho, jung, jong := kanaDecomp(ch)
		voiced := !(surname && i == 0)
		b.WriteString(kanaSyl(cho, jung, voiced))
		b.WriteString(kanaJongMap[jong])
		i++
	}
	return b.String()
}

// NameToKatakana — 한글 인명을 가타카나로 결정 변환한다. 성씨 확정 시 성・이름 분리(・),
// 2글자/비성씨는 단일어(예명 관례). 한글 외 문자는 그대로 통과.
func NameToKatakana(ko string) string {
	ko = strings.TrimSpace(ko)
	if ko == "" {
		return ""
	}
	if strings.Contains(ko, " ") {
		parts := strings.Fields(ko)
		out := make([]string, 0, len(parts))
		for i, w := range parts {
			sur := i == 0 && kanaFirstIsSurname(w)
			out = append(out, kanaWord(w, sur))
		}
		return strings.Join(out, "・")
	}
	r := []rune(ko)
	first := string(r[0])
	if len(r) >= 2 {
		two := string(r[:2])
		if kanaSurnames[two] {
			if len(r) > 2 {
				return kanaWord(two, true) + "・" + kanaWord(string(r[2:]), false)
			}
			return kanaWord(ko, true)
		}
	}
	if kanaSurnames[first] && len(r) >= 3 {
		return kanaWord(first, true) + "・" + kanaWord(string(r[1:]), false)
	}
	return kanaWord(ko, kanaSurnames[first])
}

func kanaFirstIsSurname(w string) bool {
	r := []rune(w)
	if len(r) == 0 {
		return false
	}
	return kanaSurnames[string(r[0])]
}

// isPureHangul — 전부 한글(+공백)인지. 라틴/숫자 섞인 예명(M!LK 등)은 변환 대상 아님.
func isPureHangul(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	for _, ch := range s {
		if ch == ' ' {
			continue
		}
		if ch < '가' || ch > '힣' {
			return false
		}
	}
	return true
}

// DrainKanaFillPersons — person/group/character 의 ja 빈칸을 가타카나 규칙으로 채운다.
// 대상: status=active, 순수 한글 canonical_ko, canonical_ja 빈칸. 결정적·벌크안전(외부호출 0).
// 각 채움은 dataqa_log(verdict='kana-rule-fill') 스냅샷 → verify-entities revert-contam 아닌
// 별도 복원 경로. source='kana-rule'(prio8)이라 이후 권위/매체/wiki 값이 자동 업그레이드.
// 반환: (채운 셀 수).
func DrainKanaFillPersons(ctx context.Context, pool *pgxpool.Pool, limit int) (filled int) {
	if pool == nil {
		return 0
	}
	if limit <= 0 {
		limit = 500
	}
	rows, err := pool.Query(ctx, `
SELECT id::text, canonical_ko
  FROM kwave_entities
 WHERE status='active' AND operator_locked=false
   AND entity_type IN ('person','group','character')
   AND COALESCE(canonical_ja,'')=''
   AND canonical_ko ~ '^[가-힣 ]+$'
 ORDER BY updated_at DESC
 LIMIT $1`, limit)
	if err != nil {
		log.Printf("kdb.kana: select: %v", err)
		return 0
	}
	type row struct{ id, ko string }
	var items []row
	for rows.Next() {
		var it row
		if rows.Scan(&it.id, &it.ko) == nil {
			items = append(items, it)
		}
	}
	rows.Close()

	for _, it := range items {
		if ctx.Err() != nil {
			break
		}
		if !isPureHangul(it.ko) {
			continue
		}
		kana := NameToKatakana(it.ko)
		// 품질 가드: 변환 실패/한글 잔존/공백만 → 스킵.
		if strings.TrimSpace(kana) == "" || strings.ContainsAny(kana, "가나다라마바사아자차카타파하") {
			continue
		}
		if !kanaIsKatakana(kana) {
			continue
		}
		// 스냅샷(복원용).
		_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_dataqa_log (entity_id, locale, old_value, old_source, verdict, reason, model)
VALUES ($1::uuid, 'ja', '', '', 'kana-rule-fill', $2, 'kana-rule')`, it.id, "규칙변환: "+kana)
		tag, uerr := pool.Exec(ctx, `
UPDATE kwave_entities
   SET canonical_ja=$2, canonical_ja_source='kana-rule', updated_at=now()
 WHERE id=$1::uuid AND status='active' AND COALESCE(canonical_ja,'')=''`, it.id, kana)
		if uerr == nil && tag.RowsAffected() > 0 {
			filled++
		}
	}
	log.Printf("kdb.kana: DrainKanaFillPersons filled=%d /%d", filled, len(items))
	return filled
}

// kanaIsKatakana — 결과가 가타카나(+・ー)로만 이뤄졌는지 최종 검증(오염 방지).
func kanaIsKatakana(s string) bool {
	if utf8.RuneCountInString(s) == 0 {
		return false
	}
	for _, ch := range s {
		if ch == '・' || ch == 'ー' {
			continue
		}
		if ch >= 0x30A0 && ch <= 0x30FF { // Katakana block
			continue
		}
		return false
	}
	return true
}
