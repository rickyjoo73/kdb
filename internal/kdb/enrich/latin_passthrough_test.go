package enrich

import "testing"

// TestLatinPassthrough — 라틴 승계 가드. 2026-08-15 에 mt-fill 이 `영문 그대로·미번역`
// 으로 기각하던 칸을 여는 분기이고, **잘못 열면 그대로 오염**이라 네 가드(타입 · 구글
// 합치 · 증거 독립성 · 값 정합성)가 각각 살아 있는지 본다.
//
// 인자 순서: (entityType, ko, en, enSource, mt)
func TestLatinPassthrough(t *testing.T) {
	// strong — 기계 추측이 아닌 en 출처. 이 경우 ko 에 라틴이 없어도 승계한다.
	const strong = "wikidata-label"

	cases := []struct {
		name                   string
		typ, ko, en, enSrc, mt string
		wantVal                string
		wantOK                 bool
	}{
		// ── 통과 ①: en 출처가 기계 추측이 아니다 ──
		{"강한 en 출처", "group", "카드", "KARD", strong, "KARD", "KARD", true},
		{"대소문자 접기", "group", "빅플로", "Bigflo", strong, "BIGFLO", "Bigflo", true},
		{"구두점 접기", "group", "밀크", "M.I.L.K.", "local-usage", "M.I.L.K", "M.I.L.K.", true},
		{"공백 접기", "agency", "스튜디오지니", "Studio Genie", "musicbrainz", "StudioGenie", "Studio Genie", true},

		// ── 통과 ②: ko 자체가 라틴을 품는다(en 출처가 약해도 성립) ──
		{"ko 에 라틴 — 접미", "group", "몬스타X", "MONSTA X", "gtranslate", "MONSTA X", "MONSTA X", true},
		{"ko 에 라틴 — 괄호", "group", "갓세븐(GOT7)", "GOT7", "gtranslate", "GOT7", "GOT7", true},
		{"ko 에 라틴 — 접두", "channel_outlet", "MBC드라마넷", "MBC Dramanet", "gtranslate", "MBC Dramanet", "MBC Dramanet", true},

		// ── 증거 독립성 가드: 순환논증 차단 ──
		// 08-15 1차 적용에서 85칸 중 61칸이 en_source='gtranslate' 였다. 우리 en 도 구글이
		// 만든 값이라 "구글이 우리 en 과 같다"가 증거가 아니었고, 한국어 이름의 로마자가
		// zh 칸에 들어갔다.
		{"순환: gtranslate en + 한글 ko", "brand_place", "서순라길", "Seosunra-gil", "gtranslate", "Seosunra-gil", "", false},
		{"순환: SK재원", "agency", "에스케이재원", "SK Jaewon", "gtranslate", "SK Jaewon", "", false},
		{"순환: codex 추측", "agency", "잼리퍼블릭", "Jam Republic", "codex-fallback", "Jam Republic", "", false},
		{"순환: 출처 없음", "group", "케이보이즈", "K-Boys", "", "K-Boys", "", false},
		{"순환: romanization", "agency", "파인하우스필름", "Finehouse Film", "romanization", "Finehouse Film", "", false},

		// ── 값 정합성 가드: 깨진 en 을 복제하지 않는다 ──
		// `아이들(G)I-DLE` 의 canonical_en 이 `G)I-DLE` 로 이미 깨져 있었고 1차 적용이
		// 그 깨짐을 zh 로 복제했다.
		{"괄호 불균형", "group", "아이들(G)I-DLE", "G)I-DLE", strong, "G)I-DLE", "", false},
		{"여는 괄호만", "group", "테스트(A", "Test (A", strong, "Test (A", "", false},
		{"구두점으로 시작", "group", "테스트", "-Test", strong, "-Test", "", false},
		{"균형 잡힌 괄호는 통과", "group", "티엑스티(TXT)", "Tomorrow X Together (TXT)", strong,
			"Tomorrow X Together (TXT)", "Tomorrow X Together (TXT)", true},

		// ── 타입 가드: 제목류·인물류는 한자/가나가 정답 ──
		// 실측: drama 1% · movie 1% · person 3% · character 2% 만 라틴이었다.
		{"드라마 제외", "drama", "오징어게임", "Squid Game", strong, "Squid Game", "", false},
		{"영화 제외", "movie", "기생충", "Parasite", strong, "Parasite", "", false},
		{"인물 제외", "person", "강소연", "Kang So-yeon", strong, "Kang So-yeon", "", false},
		{"쇼 제외", "show", "런닝맨", "Running Man", strong, "Running Man", "", false},
		{"song_album 제외", "song_album", "필스페셜", "Feel Special", strong, "Feel Special", "", false},
		{"event_tour 제외", "event_tour", "롤라팔루자", "Lollapalooza", strong, "Lollapalooza", "", false},

		// ── 구글 합치 가드: 구글이 뜻으로 풀면 승계하지 않는다 ──
		{"구글이 직역", "group", "들고양이들", "Wild Cats", strong, "流浪猫", "", false},
		{"구글이 다른 라틴", "agency", "더스튜디오엠이", "Studio ME", strong, "Studio M", "", false},
		{"구글이 한자 음차", "group", "프림로즈", "Primrose", strong, "普里姆罗斯", "", false},
		// 실측 사례: 우리 en=UmTube, 구글=EomTube — 로마자 방식이 달라 불일치 → 안 채운다.
		{"로마자 방식 불일치", "channel_outlet", "엄튜브", "UmTube", strong, "EomTube", "", false},

		// ── 순수성 가드 ──
		{"en 에 한글 혼입", "group", "뮤지엄피", "뮤지엄P", strong, "뮤지엄P", "", false},
		{"mt 에 가나 혼입", "group", "밀크", "Milk", strong, "Milkミルク", "", false},

		// ── 빈값·최소길이 ──
		{"en 빈값", "group", "비투비", "", strong, "BTOB", "", false},
		{"mt 빈값", "group", "비투비", "BTOB", strong, "", "", false},
		{"1자 라틴", "group", "엠", "M", strong, "M", "", false},
		{"숫자만", "group", "이공이사", "2024", strong, "2024", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := latinPassthrough(c.typ, c.ko, c.en, c.enSrc, c.mt)
			if ok != c.wantOK || got != c.wantVal {
				t.Errorf("latinPassthrough(%q,%q,%q,%q,%q) = (%q,%v); want (%q,%v)",
					c.typ, c.ko, c.en, c.enSrc, c.mt, got, ok, c.wantVal, c.wantOK)
			}
		})
	}
}

// TestLatinPassthroughReturnsOurSpelling — 승계값은 구글의 mt 가 아니라 우리
// canonical_en 이어야 한다. 검증된 철자·대소문자가 그쪽이기 때문이다.
func TestLatinPassthroughReturnsOurSpelling(t *testing.T) {
	got, ok := latinPassthrough("group", "레드벨벳", "Red Velvet", "wikidata-label", "RED VELVET")
	if !ok {
		t.Fatal("승계되어야 한다")
	}
	if got != "Red Velvet" {
		t.Errorf("승계값 = %q; want %q (구글 대소문자가 아니라 우리 canonical_en)", got, "Red Velvet")
	}
}
