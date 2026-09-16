package kdbapi

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/rickyjoo73/kdb/internal/kdb"
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

// 옛 범위 기각은 **tombstone 근거가 아니다.**
//
// ★소비자(presslocale) 보고로 알았다. 새 유형을 열고 문서까지 고쳤는데 서버가
// 그대로 거절했다. 원인이 이 문이었다 — Tombstoned 가 원장의 `rejected` 행을 보고
// "재조회 불필요"로 답했고, 그 기각들이 전부 옛 범위로 내린 것이었다:
//
//	이재명 "한국의 실존 정치인으로 널리 알려진 인명" → 기각 "K-엔터 인물이 아님"
//	더불어민주당 · 두산 베어스 · 서울대학교
//
// 이 함수는 이미 같은 이유로 두 계열(revert-term·TTL)을 빼 두고 있었다. 기각의
// 명제가 그 이름의 **존재**를 부정하지 않으면 tombstone 이 아니다.
func TestOldScopeRejectionIsNotATombstone(t *testing.T) {
	src, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(src)
	i := strings.Index(doc, "func (s *Store) Tombstoned")
	if i < 0 {
		t.Fatal("Tombstoned 를 못 찾았다")
	}
	end := strings.Index(doc[i:], "\n}\n")
	body := doc[i : i+end]

	// 한 패턴이 다섯 표현을 전부 덮어야 한다. 하나씩 붙이면 여섯 번째가 또 나온다 —
	// 실제로 그렇게 다섯 번 새로 알았다(비-K · K-엔터테인먼트 · 비연예 · 비-엔터 · K-콘텐츠).
	re := regexp.MustCompile(`비-?K|범위 ?밖|K-엔터|K-콘텐츠|비-?엔터|비연예`)
	for _, phrase := range []string{
		"비-K(범위밖): 한국 정치인",
		"K-엔터테인먼트 인물이 아님",
		"비-K(범위밖): 전설적인 축구 선수",
		"wikidata scope 오염(비연예/오링크)",
		"K리그 프로축구단 FC서울(스포츠), 비-엔터",
		"K-콘텐츠(가수, 배우, 작품 등)가 아닌 프로축구 구단임",
	} {
		if !re.MatchString(phrase) {
			t.Errorf("옛 범위 기각 문구를 못 잡는다: %q", phrase)
		}
	}
	// ★판정은 이제 kdb.NotATombstoneSQL 한 자리에서 온다 (2026-09-16).
	//   Tombstoned 가 그것을 부르는지 보고, 세 계열이 다 들어 있는지는 그 함수에서 본다 —
	//   본문에 문구를 다시 적으면 그때부터 두 벌이 되고, 두 벌이 되면 한쪽이 뒤처진다.
	//   실제로 그렇게 TTL 이 두 곳에서 빠져 «영구 차단 세탁»이 생겼다.
	if !strings.Contains(body, `kdb.NotATombstoneSQL(`) {
		t.Error("Tombstoned 가 공용 tombstone 판정을 안 쓴다")
	}
	shared := kdb.NotATombstoneSQL("")
	for _, want := range []string{"[revert-term:reject]", "[ttl-expire:reject]", "K-콘텐츠"} {
		if !strings.Contains(shared, want) {
			t.Errorf("공용 판정이 %q 계열을 제외하지 않는다", want)
		}
	}
}

// **믿는 것**과 **긁는 것**은 다른 질문이다.
//
// ★2026-09-15. 범위를 넓히고 게이트를 다 풀었는데도 SK하이닉스·국민의힘·연세대학교가
// review 에 묶였다. 사유가 missing_source_evidence 였고, 파 보니 신뢰 판정이
// `discovery_enabled=true`(우리가 크롤링하는가)를 묻고 있었다.
//
// 그래서 등록된 소비자가 **자기 기사 URL** 을 보내도 '출처 근거 없음'이 됐다 —
// mediafine 6,545회 · issuetalk 4,126회 · kstory 3,974회 요청이 전부 그랬다.
// 발행사가 자기 기사를 가리키며 "이 고유명사가 여기 나온다"고 하는 것보다 더 나은
// 인입 근거는 없다.
func TestTrustAndCrawlAreDifferentQuestions(t *testing.T) {
	src, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(src)
	i := strings.Index(doc, "func (s *Store) isTrustedIntakeSource")
	if i < 0 {
		t.Fatal("isTrustedIntakeSource 를 못 찾았다")
	}
	end := strings.Index(doc[i:], "\n}\n")
	body := doc[i : i+end]
	if strings.Contains(body, "discovery_enabled=true") {
		t.Error("신뢰 판정이 다시 '크롤링하는가'를 묻고 있다 — 다른 질문이다")
	}
	if !strings.Contains(body, "kwave_news_whitelist") {
		t.Error("화이트리스트를 안 본다")
	}
}

// TestDocsDoNotPromiseAPermanenceWeDoNotKeep — 문서가 소비자에게 **지키지 않는 약속**을
// 하지 않는지 본다.
//
// ★계기 (2026-09-16). 문서는 `out_of_scope` 에 "재조회 불필요"라고 못박아 두었는데,
// 실제로는 두 가지로 판정이 만료된다:
//
//	① 우리 규칙이 바뀔 때 (판본 만료 — 09-15 범위 확대가 그랬다)
//	② 판정의 근거가 된 사실이 바뀔 때 (되살아난 기각 행)
//
// 소비자는 그 말을 믿고 재요청을 끊는다. 우리가 고친 것이 안 닿는다 — 실제로
// presslocale 이 "문서와 실제가 다르다"고 알려 준 것이 이 계열이었다.
// 만료가 있으면 **있다고 적는다.** 없는 척하는 것도 거짓이고, 늘 다시 물으라는 것도 거짓이다.
func TestDocsDoNotPromiseAPermanenceWeDoNotKeep(t *testing.T) {
	src, err := os.ReadFile("docs.go")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(src)

	if !strings.Contains(doc, "판정 만료") {
		t.Error("문서가 판정 만료를 설명하지 않는다 — 고친 것이 소비자에게 안 닿는다")
	}
	// 만료의 두 계기를 **둘 다** 말해야 한다. 하나만 적으면 나머지가 마술처럼 보인다.
	for _, want := range []string{"규칙이 바뀌", "사실이 바뀌"} {
		if !strings.Contains(doc, want) {
			t.Errorf("만료 계기를 안 적었다: %q", want)
		}
	}
	// 만료되지 **않는** 것도 말해야 한다 — 그래야 재조회가 언제 무의미한지 안다.
	if !strings.Contains(doc, "만료되지 않습니다") {
		t.Error("만료되지 않는 판정(unfillable)을 구분해 적지 않았다")
	}
	// 옛 범위를 단정하던 out_of_scope 설명이 남아 있으면 안 된다.
	if strings.Contains(doc, "out_of_scope</td><td>K-콘텐츠가 아니거나") {
		t.Error("out_of_scope 설명이 아직 옛 범위로 적혀 있다")
	}
}

// TestTombstoneJudgementComesFromOnePlace — "이 기각이 이름을 묻는가"를 **한 자리에서만**
// 판단한다.
//
// 이 판단은 세 곳이 필요로 한다: Tombstoned · CloseResolvedBacklog ·
// rejectedTwinStillExists. 세 곳이 각자 적었더니 그중 둘이 TTL 을 안 뺐고,
// TTL 기각이 `existing_rejected_entity` 로 세탁돼 영구 차단이 됐다(2026-09-16 오세훈).
func TestTombstoneJudgementComesFromOnePlace(t *testing.T) {
	for _, f := range []string{"api.go", "prepare_outcome.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		body := string(b)
		if !strings.Contains(body, "kdb.NotATombstoneSQL(") {
			t.Errorf("%s 가 공용 판정을 안 쓴다", f)
		}
		// 직접 적은 흔적이 남아 있으면 안 된다 — 남으면 한쪽만 고치는 날이 온다.
		for _, hard := range []string{`NOT LIKE '%[ttl-expire:reject]%'`, `NOT LIKE '%[revert-term:reject]%'`} {
			for _, line := range strings.Split(body, "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "--") || strings.HasPrefix(trimmed, "//") {
					continue
				}
				if strings.Contains(line, hard) {
					t.Errorf("%s 에 tombstone 조건을 직접 적었다: %s", f, trimmed)
				}
			}
		}
	}
}

// TestEveryServingResponseCarriesKID — **kid 를 주는 문과 안 주는 문이 갈리면 안 된다.**
//
// ★실측 (2026-09-16). lookup·entities 는 kid 를 보내는데 **prepare 만 안 보냈다.**
// 소비자가 가장 많이 쓰는 문이 prepare 다. 여기서 안 주면 "kid 로 조회하라"는 안내가
// 받은 적 없는 값을 쓰라는 말이 된다 — 동명이인은 이름으로 못 가리고 entity_id 는
// 우리 내부 UUID 다. kid 만이 소비자가 저장해 두고 다시 물을 수 있는 값이다.
func TestEveryServingResponseCarriesKID(t *testing.T) {
	for _, tc := range []struct{ name string; v any }{
		{"Entity", Entity{}},
		{"MatchedEntity", MatchedEntity{}},
		{"PrepareItem", PrepareItem{}},
	} {
		ty := reflect.TypeOf(tc.v)
		f, ok := ty.FieldByName("KID")
		if !ok {
			t.Errorf("%s 에 KID 가 없다 — 이 문으로 온 소비자는 kid 를 배울 수 없다", tc.name)
			continue
		}
		tag := f.Tag.Get("json")
		if !strings.HasPrefix(tag, "kid") {
			t.Errorf("%s.KID 의 json 이름이 %q 다 — 문서는 `kid` 라고 적혀 있다", tc.name, tag)
		}
	}
	// 그리고 실제로 채워 넣는 코드가 있어야 한다. 필드만 있고 안 채우면 늘 빈 값이다.
	b, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "KID: ent.KID") {
		t.Error("PrepareItem 에 kid 를 채우는 곳이 없다 — 필드만 있고 늘 비어 나간다")
	}
}
