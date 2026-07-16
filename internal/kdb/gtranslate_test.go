package kdb

import "testing"

func TestGTranslateExtract(t *testing.T) {
	cases := []struct{ sentence, term, want string }{
		// 기본: 템플릿 인용부 추출
		{"Released the new song 'Stay This Way'.", "스테이 디스 웨이", "Stay This Way"},
		// 소유격 아포스트로피가 내부에 있어도 최외곽 따옴표로 전체를 잡는다(실측 버그).
		{"He appeared in the entertainment show 'Kim Young-chul's Power FM'.", "김영철의 파워FM", "Kim Young-chul's Power FM"},
		// 따옴표 없음 → 실패
		{"Released the new song Stay This Way.", "스테이 디스 웨이", ""},
		// 한글 잔존(미번역) → 실패
		{"Released the new song '스테이 디스 웨이'.", "스테이 디스 웨이", ""},
		// 원문 동일(무변환) → 실패
		{"Released the new song 'OOH-AHH'.", "OOH-AHH", ""},
	}
	for _, c := range cases {
		if got := gtranslateExtract(c.sentence, c.term); got != c.want {
			t.Errorf("gtranslateExtract(%q) = %q, want %q", c.sentence, got, c.want)
		}
	}
}

func TestGTranslateSafeHit(t *testing.T) {
	cases := []struct {
		term, canonicalKo string
		aliasesKo         []string
		want              bool
	}{
		// 영문제목 엔티티(진짜 음차 복원) → 통과
		{"스테이 디스 웨이", "Stay This Way", nil, true},
		{"라이드 오어 다이", "Ride or Die", nil, true},
		// 다른 한글 고유제목 작품(동명 영문제목) → 기각 (실측: 광장→더 스퀘어 오매칭)
		{"광장", "더 스퀘어", nil, false},
		// 띄어쓰기/구두점 변형은 같은 제목 → 통과
		{"더쇼", "더 쇼", nil, true},
		{"놀면 뭐하니", "놀면 뭐하니?", nil, true},
		// aliases_ko 에 요청어가 있으면 통과
		{"광장", "더 스퀘어", []string{"광장"}, true},
	}
	for _, c := range cases {
		if got := GTranslateSafeHit(c.term, c.canonicalKo, c.aliasesKo); got != c.want {
			t.Errorf("GTranslateSafeHit(%q, %q, %v) = %v, want %v", c.term, c.canonicalKo, c.aliasesKo, got, c.want)
		}
	}
}

func TestGTranslateEligible(t *testing.T) {
	cases := []struct {
		term, ttype string
		want        bool
	}{
		{"스테이 디스 웨이", "song_album", true},
		{"오싹한 연애", "drama", true},
		{"수련", "", true},              // 무유형도 대상(범용 템플릿)
		{"유태오", "person", false},     // 인명 제외
		{"Stay This Way", "song_album", false}, // 한글 없음 — 번역 불요
	}
	for _, c := range cases {
		if got := GTranslateEligible(c.term, c.ttype); got != c.want {
			t.Errorf("GTranslateEligible(%q, %q) = %v, want %v", c.term, c.ttype, got, c.want)
		}
	}
}

func TestGTranslateFillTermType(t *testing.T) {
	cases := []struct {
		etype string
		want  string
		ok    bool
	}{
		{"song_album", "song_album", true},
		{"drama", "drama", true},
		{"movie", "movie", true},
		{"show", "show", true},
		{"event_tour", "event_tour", true},
		{"person", "person", true},  // 채움 경로에선 인명 포함(로마자 복원)
		{"group", "person", true},
		{"character", "person", true},
		{"agency", "", true},        // 일반 템플릿
		{"brand_place", "", true},
		{"channel_outlet", "", true},
		{"unknown", "", false}, // 정체불명 — 채움 제외
		{"term", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := GTranslateFillTermType(c.etype)
		if got != c.want || ok != c.ok {
			t.Errorf("GTranslateFillTermType(%q) = (%q, %v), want (%q, %v)", c.etype, got, ok, c.want, c.ok)
		}
	}
	// 채움 전용 person 템플릿이 읽기(재매칭) eligibility 를 오염시키지 않아야 한다.
	if GTranslateEligible("유태오", "person") {
		t.Error("person must stay ineligible on the rematch(read) path")
	}
	// 모든 채움 템플릿 키가 실제 템플릿을 갖는지 (없으면 빈 문장으로 호출될 위험).
	for _, c := range cases {
		if !c.ok {
			continue
		}
		if _, ok := gtranslateFillTemplates[c.want]; ok {
			continue
		}
		if _, ok := gtranslateTemplates[c.want]; !ok {
			t.Errorf("fill term type %q has no template", c.want)
		}
	}
}
