package corrections

import (
	"os"
	"strings"
	"testing"
)

// ★**빈칸이 «정확》할 수는 없다** (2026-09-16 실측).
//
//	종전엔 현재 값이 비었는지 보지 않고 verdict=="current" 면 기각했다. 소비자가
//	일본어 표기를 보내 줬는데 "현재 값이 정확합니다" 로 거절하고 그 자리를 빈칸으로
//	남겼다. 기각 통보를 받은 소비자는 다시 보내지 않는다.
//
//	실측: 기각 중 현재 값이 비었던 394건 가운데 지금도 비어 있는 것이 18건,
//	그중 17건이 ja 다(도시의 거리·아미새·여우비·봉숭아학당…). 6월 24일 것도
//	아직 빈칸이다.
func TestEmptyCurrentIsNeverRejected(t *testing.T) {
	b, err := os.ReadFile("verify.go")
	if err != nil {
		t.Fatalf("verify.go 를 못 읽었다: %v", err)
	}
	src := string(b)
	guard := strings.Index(src, `case v.Verdict == "current" && strings.TrimSpace(cur) == "":`)
	reject := strings.Index(src, `case v.Verdict == "current" && v.Confidence >= 0.7:`)
	if guard < 0 {
		t.Fatal("빈칸 가드가 없다 — 소비자가 준 답을 버리고 빈칸을 유지한다")
	}
	if reject < 0 {
		t.Fatal("기각 분기를 못 찾았다")
	}
	if guard > reject {
		t.Error("빈칸 가드가 기각 분기보다 뒤에 있다 — switch 는 위에서 걸린다, 가드가 무력하다")
	}
	// 빈칸일 때는 기각이 아니라 보류여야 한다.
	// ★검사 구간을 **그 case 안으로** 끊는다. 넓게 잡으면 바로 다음 case 의
	//   `rejected` 를 읽고 엉뚱하게 실패한다(처음에 그렇게 짰다).
	blk := src[guard:]
	if k := strings.Index(blk[len(`case v.Verdict == "current" && strings.TrimSpace(cur) == "":`):], "\n\tcase "); k > 0 {
		blk = blk[:k]
	}
	if strings.Contains(blk, `"rejected"`) {
		t.Error("빈칸인데 기각한다 — 빈칸이 정확하다는 말이 된다")
	}
	if !strings.Contains(blk, `"pending"`) {
		t.Error("빈칸일 때 운영자에게 안 넘긴다 — 신고가 그냥 사라진다")
	}
}

// ★원장에 적는 모델 이름이 **실제로 판정한 모델**이어야 한다.
//
//	2026-09-16 에 codex 를 폐기했는데 이 파일은 계속 "codex 검증" 이라 적고 있었다 —
//	원장에 608건, 폐기 당일에도 20건. 이름표가 사실과 다르면 다음 사람이 그것을 믿고
//	엉뚱한 곳을 판다.
func TestVerdictLabelIsNotHardcodedCodex(t *testing.T) {
	b, err := os.ReadFile("verify.go")
	if err != nil {
		t.Fatalf("verify.go 를 못 읽었다: %v", err)
	}
	src := string(b)
	for _, line := range strings.Split(src, "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "//") {
			continue // 주석은 사연이다
		}
		if strings.Contains(l, `"codex 검증`) || strings.Contains(l, `'codex 검증`) {
			t.Errorf("판정 라벨에 codex 가 박혀 있다 — 판정하는 것은 gemma 다: %s", l)
		}
	}
	if !strings.Contains(src, "func modelLabel()") {
		t.Error("모델 이름을 실제 라우팅에서 가져오지 않는다")
	}
}

func minI(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestEveryLedgerBranchNamesTheJudge — 원장에 쓰는 모든 분기가 «누가 판정했는지》를
// 적는지 소스에서 확인한다.
//
// 왜 소스를 읽는 테스트인가: 이 결함은 **분기를 빠뜨려서** 생긴다. 한 분기만 라벨이
// 없어도 그 경로로 나간 건은 판정자를 영영 알 수 없다. 실제 피해가 두 번 있었다.
//
//	2026-06-13~09-16  "codex 검증:" 을 고정 문자열로 박아 610건이 거짓 라벨
//	2026-09-17        고친 줄 알았는데 verify.go 의 current 분기 하나가 라벨 자체
//	                  없이 남아 있었다 — codex 를 켜고 첫 판정에서 드러났다
//
// by 는 «실제로 답한 공급자»(RunP 반환값)이고 modelLabel() 은 «어디로 보내라고 설정돼
// 있는가»다. 원장에는 반드시 전자를 적어야 한다.
func TestEveryLedgerBranchNamesTheJudge(t *testing.T) {
	for _, f := range []string{"verify.go", "review.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("%s 읽기 실패: %v", f, err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			trimmed := strings.TrimSpace(line)
			if !strings.Contains(trimmed, `s.finalize(ctx,`) &&
				!strings.Contains(trimmed, `s.finalizeApply(ctx,`) {
				continue
			}
			// 상태만 바꾸고 문구를 쓰지 않는 호출은 대상이 아니다.
			if !strings.Contains(trimmed, `"`) {
				continue
			}
			if strings.Contains(trimmed, "by+") || strings.Contains(trimmed, "by +") {
				continue
			}
			// 판정자가 없는 종결 문구가 남아 있다 — 다만 «검증» 이라는 말이 없는
			// 순수 상태 전이(예: verifying 표시)는 허용한다.
			if strings.Contains(trimmed, "검증") || strings.Contains(trimmed, "정확") {
				t.Errorf("%s:%d 판정자 이름 없이 원장에 쓴다 — by+ 를 붙여라\n  %s",
					f, i+1, trimmed)
			}
		}
	}
}

// TestModelLabelIsNotWrittenToLedger — 설정값(modelLabel)을 원장에 적지 못하게 한다.
func TestModelLabelIsNotWrittenToLedger(t *testing.T) {
	for _, f := range []string{"verify.go", "review.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("%s 읽기 실패: %v", f, err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if !strings.Contains(line, "modelLabel()") {
				continue
			}
			if strings.Contains(line, "s.finalize") || strings.Contains(line, "resolution") {
				t.Errorf("%s:%d 설정값을 원장에 적는다 — 실제 판정자(by)를 써라\n  %s",
					f, i+1, strings.TrimSpace(line))
			}
		}
	}
}

// TestTransliterationPolicyKeepsOfficialTitleFirst — 음역 허용 문구가 순서를 뒤집지
// 않는지 본다(오너 지시 2026-09-17 "인정해").
//
// 허용 자체는 옳다: 공식 제목이 없는 작품은 기다려도 생기지 않고 그 사이 빈칸으로
// 나간다(대기 25건 중 12건이 이 경우였다). 위험한 것은 **순서가 흐려지는 것**이다 —
// 음역이 실존 공식 제목을 밀어내면 «빈칸 > 틀린값» 위반이 된다.
func TestTransliterationPolicyKeepsOfficialTitleFirst(t *testing.T) {
	p := buildVerifyPrompt("한집살림", "show", "ja", "", "ハンジプ・サルリム", nil)

	if !strings.Contains(p, "transliteration of the Korean is ACCEPTABLE") {
		t.Error("음역 허용 문구가 없다 — 공식 제목 없는 작품이 영영 빈칸으로 남는다")
	}
	if !strings.Contains(p, "official title always wins over a transliteration") {
		t.Error("우선순위 문구가 없다 — 음역이 공식 제목을 밀어낼 수 있다")
	}
	if !strings.Contains(p, "NO official/established localized title exists") {
		t.Error("«공식 제목이 없을 때만» 이라는 조건이 없다 — 무조건 허용이 된다")
	}
	// 종전의 금지 문구가 남아 있으면 두 지시가 충돌한다.
	if strings.Contains(p, "not romanization") {
		t.Error("«not romanization» 금지 문구가 남아 있다 — 허용 문구와 충돌한다")
	}
	// 지어낸 음역까지 받아서는 안 된다.
	if !strings.Contains(p, "do not accept an invented or mistaken one") {
		t.Error("지어낸 음역을 막는 문구가 없다")
	}
}

// TestStripInvisibleRescuesRealSuggestion — 보이지 않는 문자 하나로 맞는 제안을
// 버리지 않는지 본다.
//
// 실사례(2026-09-17 실측): 「오싹한 연애」 의 일본어 제목 «恋は命がけ» 정정이 8월 16일
// 부터 한 달 넘게 운영자 대기였다. 끝에 U+FEFF 가 한 글자 붙어 문자셋 가드를 통과하지
// 못했고, 원장에는 «ja 문자셋 가드 미통과» 라고만 남았다. 화면으로는 절대 못 찾는다.
//
// ★이 파일에 그 문자를 **리터럴로** 적었다가 Go 컴파일러가 거부했다
// ("illegal byte order mark"). 보이지 않는 문자는 이렇게 사람과 도구를 동시에 속인다.
// 그래서 전부 \u 이스케이프로 적는다 — 읽는 사람이 무엇이 들어 있는지 볼 수 있게.
func TestStripInvisibleRescuesRealSuggestion(t *testing.T) {
	const (
		bom  = "\uFEFF" // byte order mark / zero-width no-break space
		zwsp = "\u200B" // zero-width space
		lrm  = "\u200E" // left-to-right mark
		rlm  = "\u200F" // right-to-left mark
		shy  = "\u00AD" // soft hyphen
	)
	cases := []struct{ name, in, want string }{
		{"BOM 꼬리 (실사례 id=3110)", "恋は命がけ" + bom, "恋は命がけ"},
		{"제로폭 공백 삽입", "Stay" + zwsp + "Awake", "StayAwake"},
		{"방향 표식 + 앞뒤 공백", "  " + lrm + "Confidential Assignment 2" + rlm + "  ", "Confidential Assignment 2"},
		{"소프트 하이픈", "Hanjip" + shy + "Sallim", "HanjipSallim"},
		{"멀쩡한 값은 그대로", "ハニービーズ", "ハニービーズ"},
		{"내부 공백은 보존", "Battle Line Serenade", "Battle Line Serenade"},
	}
	for _, c := range cases {
		if got := stripInvisible(c.in); got != c.want {
			t.Errorf("%s: stripInvisible(%q) = %q, 기대 %q", c.name, c.in, got, c.want)
		}
	}
	// 가드를 느슨하게 한 것이 아님을 확인 — 정리는 «보이지 않는 문자》만 대상이고
	// 내용 문자는 건드리지 않는다. 한글이 섞인 ja 제안은 정리 후에도 여전히 막혀야 한다.
	mixed := stripInvisible("2026 SUNG SI KYUNG with friends [자, 오늘은]" + bom)
	if strings.ContainsAny(mixed, bom+zwsp+lrm+rlm+shy) {
		t.Error("보이지 않는 문자가 남았다")
	}
	if !strings.Contains(mixed, "자") {
		t.Errorf("내용 문자를 지웠다 — 정리는 보이지 않는 문자만 대상이다: %q", mixed)
	}
}

// TestReapUsesVerifyingSinceNotCreatedAt — 검증 회수가 «접수 시각》이 아니라 «검증
// 시작 시각》을 보는지 소스에서 확인한다.
//
// created_at 으로 회수하면 접수된 지 10분 넘은 정정은 검증에 들어가는 **즉시** 회수
// 대상이 된다. codex 판정은 수십 초가 걸리므로 판정 중에 pending 으로 되돌려지고,
// 재검증 레인이 같은 건을 다시 집는다 — 같은 정정을 두 번 판정하고 LLM 예산이 두 배로
// 나간다. 실측(2026-09-17): 대기 25건 재큐에 codex 60회가 24분에 소진(건당 2.4회).
//
// 소스를 읽는 테스트인 이유: 이 결함은 **실패하지 않는다.** 판정은 그대로 나오고 예산만
// 조용히 두 배로 샌다. 동작 테스트로는 잡히지 않는다.
func TestReapUsesVerifyingSinceNotCreatedAt(t *testing.T) {
	src, err := os.ReadFile("review.go")
	if err != nil {
		t.Fatalf("review.go 읽기 실패: %v", err)
	}
	body := string(src)

	// 회수 UPDATE 를 찾는다.
	const marker = "SET status='pending', resolution='검증 미완료(프로세스 재시작)"
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatal("verifying 회수 UPDATE 를 찾지 못했다 — 테스트가 낡았거나 회수가 사라졌다")
	}
	// 그 UPDATE 의 WHERE 절만 본다(다음 백틱까지).
	rest := body[i:]
	if end := strings.Index(rest, "`)"); end > 0 {
		rest = rest[:end]
	}
	if !strings.Contains(rest, "verifying_since") {
		t.Error("회수 조건이 verifying_since 를 보지 않는다 — 검증 중인 건을 되돌려 " +
			"같은 정정을 두 번 판정하게 된다(LLM 예산 2배)")
	}
	if strings.Contains(rest, "created_at <") && !strings.Contains(rest, "COALESCE(verifying_since, created_at)") {
		t.Error("회수 조건이 created_at(접수 시각)을 그대로 쓴다 — 검증 시작 시각이어야 한다")
	}

	// 선점하는 쪽이 시각을 남기지 않으면 위 조건이 항상 NULL 을 본다.
	if !strings.Contains(body, "verifying_since=now()") {
		t.Error("재검증 선점이 verifying_since 를 남기지 않는다 — 회수 조건이 무력해진다")
	}
}

// TestRecordStampsVerifyingSince — 적재 경로도 verifying 이면 시각을 남기는지.
func TestRecordStampsVerifyingSince(t *testing.T) {
	src, err := os.ReadFile("corrections.go")
	if err != nil {
		t.Fatalf("corrections.go 읽기 실패: %v", err)
	}
	body := string(src)
	if !strings.Contains(body, "verifying_since") {
		t.Fatal("적재 INSERT 가 verifying_since 를 채우지 않는다")
	}
	if !strings.Contains(body, "CASE WHEN $8 = 'verifying' THEN now() ELSE NULL END") {
		t.Error("verifying 일 때만 시각을 남기는 조건이 없다 — pending/proposed 에 시각이 " +
			"박히면 회수가 엉뚱한 행을 집는다")
	}
}
