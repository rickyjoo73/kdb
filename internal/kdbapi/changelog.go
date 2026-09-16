package kdbapi

// changelog — **규칙 판본과 그것이 소비자에게 뜻하는 것**을 한 자리에 둔다.
//
// ★왜 필요한가 (2026-09-16). 우리 쪽 통로는 한 방향뿐이었다:
//
//	소비자 → 우리   /v1/corrections   30일 3,820건 — 잘 돌고 있다
//	우리 → 소비자   /docs (pull)      — 바뀐 걸 알릴 방법이 없다
//
//   그런데 `out_of_scope` 는 문서상 "재조회 불필요"라 소비자가 **캐시하고 다시 안 묻는다.**
//   우리가 고쳐도 안 닿는다. 실제로 그렇게 됐다 — 범위를 넓히고 문서까지 고쳤는데
//   소비자가 "문서와 실제가 다르다"고 알려 줘서 알았다.
//
//   사람이 메일로 통보하면 그 순간만 맞고 다음 변경 때 또 사람이 필요하다.
//   소비자는 기계다(7일간 /v1/entities 1,227 · match 554 · lookup/bulk 304 · prepare 222).
//   그래서 **판본을 응답에 실어** 소비자가 스스로 알아채게 한다.
//
// ★이 표가 유일한 원본이다. `/v1/changelog` 도 `/docs` 의 판본 이력도 여기서 만든다 —
//   두 벌을 손으로 적으면 어긋나는 날이 온다(이 저장소에서 이미 여러 번 겪었다).

import (
	"html"
	"net/http"
	"strings"

	"github.com/rickyjoo73/kdb/internal/kdb/agents/gatekeeper"
)

// RuleChange — 규칙 판본 하나. `reask` 가 이 구조의 핵심이다 — 소비자가 알아야 하는 것은
// "무엇이 바뀌었나"가 아니라 **"내가 다시 물어야 하나"** 다.
type RuleChange struct {
	Version string `json:"version"`
	Date    string `json:"date"`
	Summary string `json:"summary"`
	// Affects — 이 변경으로 **만료되는** 종결 상태. 소비자가 캐시한 그 답들이 낡았다.
	Affects []string `json:"affects,omitempty"`
	// Reask — 캐시한 종결을 버리고 다시 물어야 하는가.
	Reask bool `json:"reask"`
	// Details — 사람이 읽는 줄. docs 의 변경 이력에 그대로 들어간다.
	Details []string `json:"details,omitempty"`
}

// ruleChanges — **최신이 맨 앞.** 판본을 올리면 여기 한 줄을 반드시 적는다
// (안 적으면 회귀가 실패한다 — changelog_test.go).
var ruleChanges = []RuleChange{
	{
		Version: "scope-korea-v5-20260916",
		Date:    "2026-09-16",
		Summary: "type=term 기각 폐지 · 문맥 단서로 유형 추론 · 되살림이 TTL 시계를 다시 시작",
		Affects: []string{"out_of_scope", "review"},
		Reask:   true,
		Details: []string{
			"<b><code>type: \"term\"</code> 으로 보내신 것을 더는 기각하지 않습니다.</b> " +
				"<code>term</code> 은 «일반어다»가 아니라 «어느 칸인지 모르겠다»는 뜻인데 우리가 전자로 읽었습니다. " +
				"30일 실측 195건이 이 규칙으로 종결됐고 표본이 거의 전부 고유명사였습니다 — " +
				"게임(엑소스 히어로즈·탭! 탭! 레이서즈), 웹툰(무기의 신), 뮤지컬(환상동화), 팬덤명(유애나), 회사(강수그룹). " +
				"진짜 일반어(가수·배우·합정역광고)는 그대로 막힙니다.",
			"<b>본문에 유형 단서가 있으면 그것을 씁니다.</b> <code>context</code> 안 표제어 주변에 " +
				"유형 단서가 <b>한 종류만</b> 있으면 그 유형으로 접수합니다. 단서가 갈리면 쓰지 않습니다.",
			"<b>되살린 대상이 다시 죽지 않습니다.</b> 옛 범위로 기각됐다가 되살아난 행이 " +
				"«오래 미결»로 몇 분 만에 다시 종결되고 있었습니다(실측 25분). 되살림이 그 시계를 다시 시작시킵니다.",
			"<b>기각 판정의 근거가 사라지면 판정도 사라집니다.</b> «같은 이름의 기각 행이 있다»를 " +
				"이유로 닫힌 요청은, 그 행이 되살아나면 다시 판단합니다.",
			"<b><code>/v1/prepare</code> 응답에 <code>kid</code> 가 실립니다.</b> " +
				"종전엔 <code>/v1/lookup</code>·<code>/v1/entities</code> 만 보냈습니다.",
		},
	},
	{
		Version: "scope-korea-v4-20260915",
		Date:    "2026-09-15",
		Summary: "범위 확대 — 정치·경제·시사·스포츠 · 유형 10종 추가",
		Affects: []string{"out_of_scope"},
		Reask:   true,
		Details: []string{
			"<b>범위가 K-엔터테인먼트에서 «한국의 인물·작품·조직·기관»으로 넓어졌습니다.</b> " +
				"그전에 «K-엔터테인먼트가 아님»을 이유로 내린 기각은 <b>전부 만료</b>되었습니다 — " +
				"이재명·차범근·더불어민주당·서울대학교가 여기 해당했습니다.",
			"유형 10종 추가: <code>political_party</code> <code>government_body</code> <code>company</code> " +
				"<code>organization</code> <code>sports_team</code> <code>school</code> " +
				"<code>game</code> <code>musical_play</code> <code>webtoon</code> <code>publication</code>.",
			"사람은 유형을 늘리지 않습니다 — 선수·정치인·기업인은 전부 <code>person</code> 이고 " +
				"무슨 영역인지는 <code>occupation_domain</code> 이 따로 듭니다.",
		},
	},
}

// CurrentRuleVersion — 지금 적용 중인 규칙 판본. 모든 응답의 `X-KDB-Rules` 와 같은 값이다.
func CurrentRuleVersion() string { return gatekeeper.IntakeRuleVersion }

type changelogResponse struct {
	Current string       `json:"current"`
	Changes []RuleChange `json:"changes"`
	// HowToUse — 이 값을 어떻게 쓰라는 것인지 응답 안에서 말한다. 문서를 따로 읽어야
	// 뜻을 아는 값은 안 읽힌다.
	HowToUse string `json:"how_to_use"`
}

func (h *handler) changelog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, changelogResponse{
		Current: CurrentRuleVersion(),
		Changes: ruleChanges,
		HowToUse: "모든 응답의 X-KDB-Rules 헤더가 이 current 값입니다. " +
			"그 값이 마지막으로 보신 것과 다르면, 그 사이 변경들의 affects 에 있는 상태로 " +
			"캐시해 두신 답(out_of_scope 등)은 만료된 것이므로 다시 물어 주세요. " +
			"어느 것을 다시 물어야 하는지는 GET /v1/my/changes 가 대신 계산해 드립니다.",
	})
}

// rulesHeader — 모든 응답에 규칙 판본을 싣는다.
//
// ★X-KDB-Version 과 다르다. 그건 «데이터셋이 바뀌었나»(엔티티 수·최종수정)라 매 요청
// 바뀌고, 그래서 «규칙이 바뀌었나»의 신호로는 쓸 수 없다. 이건 규칙이 바뀔 때만 바뀐다.
func rulesHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-KDB-Rules", CurrentRuleVersion())
		next.ServeHTTP(w, r)
	})
}

// renderRuleChangesHTML — docs 의 변경 이력에 들어갈 줄. `/v1/changelog` 과 **같은 표**에서
// 만든다 — 손으로 두 벌 적으면 어긋난다.
func renderRuleChangesHTML() string {
	var b strings.Builder
	for _, c := range ruleChanges {
		b.WriteString(`<tr><td>` + html.EscapeString(c.Date) +
			`<br><span class="sub"><code>` + html.EscapeString(c.Version) + `</code></span></td><td>`)
		if c.Reask {
			b.WriteString(`<b class="warn">★ 이 변경으로 옛 판정이 만료되었습니다 — ` +
				html.EscapeString(strings.Join(c.Affects, "·")) +
				` 로 받아 두신 답은 다시 물어 주세요.</b><br>`)
		}
		for i, d := range c.Details {
			if i > 0 {
				b.WriteString("<br>")
			}
			b.WriteString(d) // Details 는 우리가 쓴 HTML 이다(소비자 입력 아님).
		}
		b.WriteString("</td></tr>\n")
	}
	return b.String()
}
