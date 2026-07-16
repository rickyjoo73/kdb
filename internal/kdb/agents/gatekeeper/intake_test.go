package gatekeeper

import "testing"

func TestDecideIntakeDecisionMatrix(t *testing.T) {
	validPerson := IntakeInput{
		Term:          "아이유",
		EntityType:    "person",
		SourceURL:     "https://kstory.aiinplanet.com/articles/1",
		SourceTrusted: true,
		Context:       "가수 아이유가 새 앨범을 공개했다.",
	}
	cases := []struct {
		name string
		in   IntakeInput
		want IntakeVerdict
	}{
		{"grounded person", validPerson, IntakePass},
		{"existing entity", IntakeInput{Term: "괴물", ExistingEntity: true}, IntakePass},
		{"normalized identity conflict", IntakeInput{Term: "스트레이 키즈", IdentityConflict: true}, IntakeReview},
		{"operator approval", IntakeInput{Term: "뷔", OperatorApproved: true}, IntakePass},
		{"type conflict", IntakeInput{Term: "괴물", EntityType: "movie", TypeConflict: true}, IntakeReview},
		{"clean name no proof", IntakeInput{Term: "박보검"}, IntakeReview},
		{"common noun no proof", IntakeInput{Term: "건강"}, IntakeReview},
		{"ambiguous title no proof", IntakeInput{Term: "괴물"}, IntakeReview},
		{"typed only is not proof", IntakeInput{Term: "아이유", EntityType: "person"}, IntakeReview},
		{"url only is not proof", IntakeInput{Term: "아이유", SourceURL: validPerson.SourceURL, SourceTrusted: true}, IntakeReview},
		{"typed url without context", IntakeInput{Term: "아이유", EntityType: "person", SourceURL: validPerson.SourceURL, SourceTrusted: true}, IntakeReview},
		{"typed url exact context without cue", IntakeInput{Term: "아이유", EntityType: "person", SourceURL: validPerson.SourceURL, SourceTrusted: true, Context: "아이유가 말했다"}, IntakeReview},
		{"common word cannot impersonate person", IntakeInput{Term: "건강", EntityType: "person", SourceURL: validPerson.SourceURL, SourceTrusted: true, Context: "배우 건강이 출연했다"}, IntakeReview},
		{"substring is not exact mention", IntakeInput{Term: "적", EntityType: "person", SourceURL: validPerson.SourceURL, SourceTrusted: true, Context: "배우의 실적이 공개됐다"}, IntakeReview},
		{"distant cue is not relational proof", IntakeInput{Term: "미소", EntityType: "person", SourceURL: validPerson.SourceURL, SourceTrusted: true, Context: "배우가 새 작품을 공개했다. 아주 긴 별도의 설명이 이어진 뒤 전혀 다른 문장에서 미소가 언급됐다"}, IntakeReview},
		{"category", IntakeInput{Term: "배우", EntityType: "person", SourceURL: validPerson.SourceURL, Context: "배우"}, IntakeReject},
		{"explicit term", IntakeInput{Term: "아리랑", EntityType: "term", SourceURL: validPerson.SourceURL, Context: "아리랑"}, IntakeReject},
		{"commodity ad compound", IntakeInput{Term: "합정역광고", EntityType: "group"}, IntakeReject},
		{"commodity ad compound typed person", IntakeInput{Term: "생일광고", EntityType: "person", SourceURL: validPerson.SourceURL, SourceTrusted: true, Context: "아이돌 생일광고가 걸렸다"}, IntakeReject},
		{"commodity replay compound", IntakeInput{Term: "드라마 다시보기"}, IntakeReject},
		{"commodity word at head is not commodity", IntakeInput{Term: "광고천재 이태백", EntityType: "drama"}, IntakeReview},
		{"single char never auto-passes even with full proof", IntakeInput{Term: "꽃", EntityType: "song_album", SourceURL: validPerson.SourceURL, SourceTrusted: true, Context: "신곡 '꽃'을 발표했다"}, IntakeReview},
		{"single char existing entity still passes", IntakeInput{Term: "진", EntityType: "person", ExistingEntity: true}, IntakePass},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DecideIntake(tc.in)
			if got.Verdict != tc.want {
				t.Fatalf("verdict=%s reason=%s flags=%v, want %s", got.Verdict, got.ReasonCode, got.Flags, tc.want)
			}
			if got.RuleVersion != IntakeRuleVersion {
				t.Fatalf("rule version=%q", got.RuleVersion)
			}
		})
	}
}

func TestDecideIntakeFatalCannotBeOverridden(t *testing.T) {
	for _, term := range []string{"임ㅇ원희", "qwertyzxcv12345", "박\u200b보검", "박\x00보검"} {
		got := DecideIntake(IntakeInput{
			Term: term, EntityType: "person", SourceURL: "https://example.com/a",
			Context: "배우 " + term, OperatorApproved: true, ExistingEntity: true,
		})
		if got.Verdict != IntakeReject {
			t.Errorf("fatal %q = %s (%s), want reject", term, got.Verdict, got.ReasonCode)
		}
	}
}

func TestDecideIntakeNormalizedKey(t *testing.T) {
	cases := map[string]string{
		"IZNA": "izna", "izna": "izna", "스트레이 키즈": "스트레이키즈",
		"D.P. 시즌2": "dp시즌2", " 토이  스토리 5 ": "토이스토리5",
	}
	for in, want := range cases {
		if got := DecideIntake(IntakeInput{Term: in}).NormalizedKey; got != want {
			t.Errorf("key(%q)=%q want %q", in, got, want)
		}
	}
}

func TestDecideIntakeRealNamesAndTitles(t *testing.T) {
	for _, term := range []string{"뷔", "진", "미소", "신기루", "금비"} {
		if got := DecideIntake(IntakeInput{Term: term}); got.Verdict != IntakeReview {
			t.Errorf("ungrounded real-name-shaped %q = %s, want review", term, got.Verdict)
		}
		if got := DecideIntake(IntakeInput{
			Term: term, EntityType: "person", SourceURL: "https://news.example/a", SourceTrusted: true, Context: "가수 " + term + "이 출연했다",
		}); got.Verdict != IntakePass {
			t.Errorf("grounded person %q = %s (%s), want pass", term, got.Verdict, got.ReasonCode)
		}
	}

	for _, term := range []string{
		"슬기로운 의사생활 2", "꽃 피면 달 생각하고", "D.P. 시즌2", "WHO THAT GIRL?", "DARK MOON: 달의 제단",
	} {
		if got := DecideIntake(IntakeInput{Term: term}); got.Verdict != IntakeReview {
			t.Errorf("ungrounded title %q = %s, want review", term, got.Verdict)
		}
		if got := DecideIntake(IntakeInput{
			Term: term, EntityType: "drama", SourceURL: "https://news.example/a", SourceTrusted: true, Context: "새 드라마 ‘" + term + "’가 방영된다",
		}); got.Verdict != IntakePass {
			t.Errorf("grounded title %q = %s (%s), want pass", term, got.Verdict, got.ReasonCode)
		}
	}
}

func TestDecideIntakeLiveRegressionCorpus(t *testing.T) {
	cases := []IntakeInput{
		{Term: "What is N/a?", EntityType: "show", SourceURL: "https://kstory.aiinplanet.com/a"},
		{Term: "출판사 무제 MUZE", EntityType: "show", SourceURL: "https://kstory.aiinplanet.com/a", Context: "출판사 무제 MUZE가 책을 냈다"},
		{Term: "정범 감독", EntityType: "person", SourceURL: "https://kstory.aiinplanet.com/a", Context: "정범 감독이 영화를 연출했다"},
		{Term: "서울 올림픽공원 올림픽홀", EntityType: "show", SourceURL: "https://kstory.aiinplanet.com/a", Context: "서울 올림픽공원 올림픽홀에서 예능을 촬영했다"},
		{Term: "샌디에이고 컨벤션 센터", EntityType: "show", SourceURL: "https://kstory.aiinplanet.com/a", Context: "샌디에이고 컨벤션 센터에서 쇼가 열렸다"},
		{Term: "삼청동", EntityType: "show", SourceURL: "https://kstory.aiinplanet.com/a", Context: "삼청동에서 예능을 촬영했다"},
		{Term: "제49회 토론토 국제영화제", EntityType: "show", SourceURL: "https://kstory.aiinplanet.com/a", Context: "제49회 토론토 국제영화제에서 프로그램을 공개했다"},
		{Term: "서울패션위크", EntityType: "show", SourceURL: "https://kstory.aiinplanet.com/a", Context: "서울패션위크 쇼가 열렸다"},
	}
	for _, in := range cases {
		in.SourceTrusted = true
		got := DecideIntake(in)
		if got.Verdict == IntakePass {
			t.Errorf("live regression %q unexpectedly passed (%s)", in.Term, got.ReasonCode)
		}
	}
}

func TestValidIntakeSourceURL(t *testing.T) {
	good := []string{"https://kbs.co.kr/news/1", "http://news.example:8080/a"}
	bad := []string{"", "javascript:alert(1)", "https://kbs.co.kr@evil.tld/a", "http://localhost/a", "http://127.0.0.1/a", "http://10.0.0.1/a"}
	for _, raw := range good {
		if !ValidIntakeSourceURL(raw) {
			t.Errorf("valid URL rejected: %q", raw)
		}
	}
	for _, raw := range bad {
		if ValidIntakeSourceURL(raw) {
			t.Errorf("unsafe URL accepted: %q", raw)
		}
	}
}

func TestConfiguredTrustedIntakeSourceURLExactHost(t *testing.T) {
	t.Setenv("KDB_INTAKE_TRUSTED_SOURCE_HOSTS", "kbs.co.kr,kstory.aiinplanet.com")
	if !ConfiguredTrustedIntakeSourceURL("https://www.kbs.co.kr/news/1") {
		t.Fatal("exact configured source was not trusted")
	}
	for _, spoof := range []string{"https://kbs.co.kr.evil.tld/a", "https://kbs.co.kr@evil.tld/a"} {
		if ConfiguredTrustedIntakeSourceURL(spoof) {
			t.Fatalf("spoof source trusted: %s", spoof)
		}
	}
}

func FuzzDecideIntakeFailClosed(f *testing.F) {
	for _, seed := range []string{"아이유", "건강", "임ㅇ원희", "D.P. 시즌2", "\x00"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, term string) {
		got := DecideIntake(IntakeInput{Term: term})
		if got.Verdict != IntakePass && got.Verdict != IntakeReview && got.Verdict != IntakeReject {
			t.Fatalf("invalid verdict %q", got.Verdict)
		}
		if got.RuleVersion == "" || got.ReasonCode == "" {
			t.Fatalf("decision lacks audit fields: %+v", got)
		}
	})
}
