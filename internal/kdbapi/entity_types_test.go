package kdbapi

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// 정치·경제·시사·스포츠 유형이 **API 에서 받아들여져야** 한다.
//
// ★운영자 지시 (2026-09-15): "KDB가 정치·경제·스포츠까지 받게 되면 정당·정부기관·
// 기업·선수 같은 유형 목록을 모두 준비해야지. 그래야 문서도 업데이트하고."
func TestNewCivicTypesAreAccepted(t *testing.T) {
	for _, typ := range []string{
		"political_party", "government_body", "company",
		"organization", "sports_team", "school",
		"game", "musical_play", "webtoon", "publication",
	} {
		if !validEntityType(typ) {
			t.Errorf("%s 를 API 가 거부한다 — 소비자가 보내도 400 이 난다", typ)
		}
	}
	// 기존 유형이 하나라도 빠지면 안 된다.
	for _, typ := range []string{
		"person", "group", "show", "drama", "movie", "song_album", "agency",
		"channel_outlet", "brand_place", "event_tour", "character", "term", "unknown",
	} {
		if !validEntityType(typ) {
			t.Errorf("기존 유형 %s 가 사라졌다", typ)
		}
	}
	// 사람은 늘리지 않았다 — 선수·정치인은 person + occupation_domain 이다.
	for _, typ := range []string{"athlete", "politician", "businessperson"} {
		if validEntityType(typ) {
			t.Errorf("%s 가 유형으로 들어왔다 — 배우 겸 정치인을 어느 칸에 넣을지 정할 수 없다", typ)
		}
	}
}

// 문서가 **실제로 받는 유형**을 전부 적어야 한다. 안 적으면 소비자는 못 쓴다.
//
// ★운영자가 문서 갱신을 명시적으로 요구했다. 코드만 고치고 문서를 두면 이 변경은
// 소비자에게 없는 것과 같다.
func TestDocsListEveryAcceptedType(t *testing.T) {
	src, err := os.ReadFile("docs.go")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(src)
	for _, typ := range []string{
		"person", "group", "show", "drama", "movie", "song_album", "agency",
		"channel_outlet", "brand_place", "event_tour", "character", "term",
		"political_party", "government_body", "company", "organization",
		"sports_team", "school", "game", "musical_play", "webtoon", "publication",
	} {
		if !strings.Contains(doc, "<code>"+typ+"</code>") {
			t.Errorf("문서에 %s 가 없다 — 받기는 받는데 쓰는 법을 안 알려준다", typ)
		}
	}
	// 영역 값도 문서에 있어야 소비자가 거를 수 있다.
	for _, d := range []string{"entertainment", "sports", "politics", "business"} {
		if !strings.Contains(doc, "<code>"+d+"</code>") {
			t.Errorf("문서에 영역 %s 가 없다", d)
		}
	}
	// 범위 확대가 문서에 반영됐는지 — 종전 "일반 기업·제품" 금지 문구가 남아 있으면 모순이다.
	if strings.Contains(doc, "일반 행정지명(서울·부산), 일반 기업·제품") {
		t.Error("범위 밖 규정이 옛 문구 그대로다 — 기업을 받으면서 기업을 보내지 말라고 한다")
	}
}

// entityColumns 와 entityColumnsQualified 는 **같은 목록의 쌍둥이**다. 하나만 고치면
// 스캐너가 한 칸 어긋나 "number of field descriptions must equal number of destinations"
// 로 죽는다 — 정확히 2026-09-15 에 occupation_domain 을 넣으며 낸 사고다.
//
// 칼럼 이름을 뽑아 나란히 놓고 비교한다. 별칭(e.)만 떼면 두 목록은 글자까지 같아야 한다.
func TestTheTwoColumnListsStayInSync(t *testing.T) {
	norm := func(src string) []string {
		var out []string
		for _, line := range strings.Split(src, "\n") {
			line = strings.TrimSpace(line)
			line = strings.TrimSuffix(line, ",")
			line = strings.TrimSuffix(line, "`")
			if line == "" || strings.HasPrefix(line, "//") {
				continue
			}
			line = strings.ReplaceAll(line, "e.", "")
			out = append(out, line)
		}
		return out
	}
	a, b := norm(entityColumns), norm(entityColumnsQualified)
	if len(a) != len(b) {
		t.Fatalf("칸 수가 다르다: entityColumns %d · entityColumnsQualified %d\n%v\n%v", len(a), len(b), a, b)
	}
	for i := range a {
		if a[i] != b[i] {
			t.Errorf("%d번째 칸이 다르다:\n  entityColumns          %s\n  entityColumnsQualified %s", i, a[i], b[i])
		}
	}
}

// 스캐너 둘도 **칸 수가 목록과 같아야** 한다. 목록만 늘리고 스캐너를 두면 같은 사고다.
func TestScannersMatchTheColumnCount(t *testing.T) {
	count := func(src string) int {
		n := 0
		for _, line := range strings.Split(src, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "//") || strings.HasPrefix(line, "`") {
				continue
			}
			n++
		}
		return n
	}
	cols := count(entityColumns)

	src, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	start := strings.Index(body, "func scanEntity(row entityScanner)")
	end := strings.Index(body[start:], "\n}\n")
	if start < 0 || end < 0 {
		t.Skip("scanEntity 를 못 찾았다")
	}
	scans := strings.Count(body[start:start+end], "&ent.")
	if scans != cols {
		t.Errorf("scanEntity 가 %d칸을 읽는데 entityColumns 는 %d칸이다", scans, cols)
	}
}

// kid — 자체 ID 가 주 앵커다(I03). **모든 문으로 나가야** 소비자가 그 값을 배운다.
//
// ★운영자 지적 (2026-09-15): "kid 도 같이 보내주도록 하던지, 외부에서 아이디값을
// 어떻게 파악하겠니? 외부에서 아이디조회는 가능할까?"
//
// 두 가지를 다 고정한다 — 나가는가, 그리고 그것으로 물을 수 있는가.
func TestKIDGoesOutEveryDoorAndCanBeAskedBack(t *testing.T) {
	// ① 두 응답 구조체 모두에 kid 가 있어야 한다. 한쪽만 있으면 그 문으로 들어온
	//    소비자는 kid 를 영영 못 배운다.
	for _, typ := range []any{Entity{}, MatchedEntity{}} {
		rt := reflect.TypeOf(typ)
		f, ok := rt.FieldByName("KID")
		if !ok {
			t.Fatalf("%s 에 KID 가 없다", rt.Name())
		}
		if tag := f.Tag.Get("json"); tag != "kid" {
			t.Errorf("%s.KID 의 json 태그가 %q 다 — omitempty 면 빈 값일 때 사라져 소비자가 못 배운다", rt.Name(), tag)
		}
	}

	// ② kid 꼴을 알아봐야 한다. query 자리에 넣어도 받는다.
	for _, s := range []string{"K0000001", "K1234567", "k0004821"} {
		if !looksLikeKID.MatchString(s) {
			t.Errorf("%q 를 kid 로 못 알아본다", s)
		}
	}
	// 이름과 겹치면 안 된다 — 사람 이름이 kid 로 오인되면 엉뚱한 행이 나간다.
	for _, s := range []string{"K", "K123", "K12345678", "KIA", "아이유", "BTS", "K-POP"} {
		if looksLikeKID.MatchString(s) {
			t.Errorf("%q 를 kid 로 잘못 본다", s)
		}
	}
}

// 문서가 kid 사용법을 알려야 한다. 안 적으면 소비자는 그 값을 받고도 쓸 줄 모른다.
func TestDocsExplainKID(t *testing.T) {
	src, err := os.ReadFile("docs.go")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(src)
	for _, want := range []string{"kid", "/v1/lookup", "동명이인"} {
		if !strings.Contains(doc, want) {
			t.Errorf("문서에 %q 가 없다", want)
		}
	}
}

// 문서가 **스스로와 모순되면 안 된다.**
//
// ★2026-09-15 사고. 새 유형 6종을 유형 표에 넣고 배포했는데, 바로 위 §1 은
// "KDB 는 K-엔터테인먼트 고유명사만 다룹니다"를 그대로 말하고 있었다. 소비자가
// 가장 먼저 읽는 문단이 표와 정반대였다. 소비자(presslocale)가 알려 줘서 알았다.
//
// 표만 고치고 서술을 두면, 고친 것이 소비자에게 닿지 않는다.
func TestDocsDoNotContradictTheCurrentScope(t *testing.T) {
	src, err := os.ReadFile("docs.go")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(src)

	// 옛 범위를 단정하는 문구가 남아 있으면 안 된다.
	for _, stale := range []string{
		"K-엔터테인먼트 고유명사</b>만 다룹니다",
		"한국 대중문화 엔티티</b>인가",
		"비-K 인물·작품·서비스",
		"일반 행정지명(서울·부산), 일반 기업·제품",
	} {
		if strings.Contains(doc, stale) {
			t.Errorf("문서에 옛 범위 문구가 남아 있다: %q", stale)
		}
	}

	// 새 범위를 실제로 말해야 한다.
	for _, want := range []string{
		"한국 대상",       // 판별 기준
		"범위 확대",       // 무엇이 바뀌었는지
		"occupation_domain", // 정치인·선수를 어떻게 구분하는지
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("문서가 새 범위를 안 말한다: %q 가 없다", want)
		}
	}
}
