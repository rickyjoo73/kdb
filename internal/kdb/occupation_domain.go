package kdb

// occupation_domain — **무슨 영역의 사람인가.** 유형(person)과 따로 든다.
//
// ★왜 필요해졌나 (운영자 결정 2026-09-15).
//   presslocale 이 종합지(specialtimes.co.kr) 기사를 보내기 시작하면서 정치·시사·
//   스포츠 고유명사가 섞여 들어왔다. 실측 답변률이 이렇게 갈렸다:
//
//     www.famtimes.co.kr     (연예 전문)  52건 중 즉시 47  →  90%
//     www.specialtimes.co.kr (종합지)    338건 중 즉시 33  →  10%
//
//   운영자 방침은 "정치·경제·시사·스포츠 모두 해결되니 분류를 제대로 주자" 다.
//   막을 것이 아니라 **제대로 분류해서 서빙**한다.
//
// ★왜 유형(enum)을 안 늘리나.
//   정치인도 가수도 존재론적으로 person 이다. 다른 것은 **영역**이지 종류가 아니다.
//   유형을 늘리면 동명이인 가드·병합·앵커 감사·게이트 분기가 새 유형마다 갈라진다.
//   영역을 따로 들면 그 셋이 그대로 살고, 동명이인 가드는 오히려 세진다
//   (박찬호 야구선수 ≠ 박찬호 가수 — 이름도 유형도 같지만 영역이 다르다).
//
// ★왜 P106 인가.
//   위키데이터의 직업 속성이다. 권위 출처이고 우리가 판단할 게 없다. 그리고
//   **QID 는 식별자라 모호하지 않다** — Q82955 는 politician 이지 다른 무엇도 아니다.
//   (같은 날 한자 성씨표로 같은 일을 하려다 1,287건을 잘못 잡았다. 강은 姜·康·強 이
//   다 쓰여 표 자체가 틀렸다. QID 에는 그 문제가 없다.)
//
// ★모르는 QID 는 판정하지 않는다(D-37). 빈 문자열을 돌려주고, 원자료(P106 QID 목록)는
//   그대로 저장해 둔다 — 표가 늘어나면 다시 판정할 수 있다.

// 영역 값. 소비자가 이 문자열로 거른다.
const (
	DomainEntertainment = "entertainment"
	DomainSports        = "sports"
	DomainPolitics      = "politics"
	DomainBusiness      = "business"
	DomainAcademia      = "academia"
	DomainMedia         = "media"
	DomainArts          = "arts" // 문학·미술·공예 등 공연/영상 밖의 예술
)

// occupationDomains — P106 QID → 영역. 흔한 것만 담는다. 없는 QID 는 판정하지 않는다.
var occupationDomains = map[string]string{
	// ── 연예
	"Q177220":   DomainEntertainment, // singer
	"Q33999":    DomainEntertainment, // actor
	"Q10800557": DomainEntertainment, // film actor
	"Q10798782": DomainEntertainment, // television actor
	"Q2405480":  DomainEntertainment, // voice actor
	"Q2252262":  DomainEntertainment, // rapper
	"Q639669":   DomainEntertainment, // musician
	"Q36834":    DomainEntertainment, // composer
	"Q753110":   DomainEntertainment, // songwriter
	"Q488205":   DomainEntertainment, // singer-songwriter
	"Q5716684":  DomainEntertainment, // dancer
	"Q4610556":  DomainEntertainment, // model
	"Q245068":   DomainEntertainment, // comedian
	"Q2059704":  DomainEntertainment, // television presenter
	"Q947873":   DomainEntertainment, // television presenter (alt)
	"Q2526255":  DomainEntertainment, // film director
	"Q3282637":  DomainEntertainment, // film producer
	"Q28389":    DomainEntertainment, // screenwriter
	"Q130857":   DomainEntertainment, // disc jockey
	"Q183945":   DomainEntertainment, // record producer
	"Q806349":   DomainEntertainment, // bandleader
	"Q855091":   DomainEntertainment, // guitarist
	"Q386854":   DomainEntertainment, // drummer
	"Q1259917":  DomainEntertainment, // pianist (연주)
	"Q12377274": DomainEntertainment, // choreographer

	// ── 스포츠
	"Q937857":   DomainSports, // association football player
	"Q10871364": DomainSports, // baseball player
	"Q3665646":  DomainSports, // basketball player
	"Q13381863": DomainSports, // volleyball player
	"Q11513337": DomainSports, // athletics competitor
	"Q12299841": DomainSports, // cricketer
	"Q10843402": DomainSports, // swimmer
	"Q13382519": DomainSports, // badminton player
	"Q13381376": DomainSports, // ice hockey player
	"Q11303721": DomainSports, // speed skater
	"Q13382576": DomainSports, // short track speed skater
	"Q4009406":  DomainSports, // figure skater
	"Q11774891": DomainSports, // golfer
	"Q12840545": DomainSports, // handball player
	"Q13141064": DomainSports, // taekwondo athlete
	"Q11124885": DomainSports, // judoka
	"Q13365117": DomainSports, // boxer
	"Q10873124": DomainSports, // chess/go player 계열
	"Q12039558": DomainSports, // go player
	"Q4351403":  DomainSports, // esports player
	"Q628099":   DomainSports, // association football manager
	"Q18811739": DomainSports, // archer
	"Q13382355": DomainSports, // table tennis player
	"Q10833314": DomainSports, // tennis player

	// ── 정치·행정
	"Q82955":   DomainPolitics, // politician
	"Q30461":   DomainPolitics, // president
	"Q83307":   DomainPolitics, // minister
	"Q486839":  DomainPolitics, // member of parliament
	"Q30185":   DomainPolitics, // mayor
	"Q1055894": DomainPolitics, // statesman
	"Q193391":  DomainPolitics, // diplomat
	"Q16533":   DomainPolitics, // judge
	"Q40348":   DomainPolitics, // lawyer
	"Q3242115": DomainPolitics, // civil servant

	// ── 경제
	"Q131524":  DomainBusiness, // entrepreneur
	"Q43845":   DomainBusiness, // businessperson
	"Q1231865": DomainBusiness, // chief executive officer
	"Q806798":  DomainBusiness, // banker
	"Q188094":  DomainBusiness, // economist

	// ── 학계
	"Q1622272": DomainAcademia, // university teacher
	"Q901":     DomainAcademia, // scientist
	"Q1650915": DomainAcademia, // researcher
	"Q201788":  DomainAcademia, // historian
	"Q4964182": DomainAcademia, // philosopher
	"Q39631":   DomainAcademia, // physician
	"Q169470":  DomainAcademia, // physicist

	// ── 언론
	"Q1930187":  DomainMedia, // journalist
	"Q1607826":  DomainMedia, // news presenter
	"Q11030014": DomainMedia, // announcer
	"Q1234713":  DomainMedia, // theologian 계열 아님 — 편집자
	"Q3427922":  DomainMedia, // editor

	// ── 예술(공연·영상 밖)
	"Q36180":   DomainArts, // writer
	"Q49757":   DomainArts, // poet
	"Q6625963": DomainArts, // novelist
	"Q1028181": DomainArts, // painter
	"Q1281618": DomainArts, // sculptor
	"Q33231":   DomainArts, // photographer
	"Q3391743": DomainArts, // visual artist
	"Q1114448": DomainArts, // cartoonist

	// ★뒤채움 1차분 400명이 실제로 들고 온 P106 (2026-09-16).
	//
	//   조회 400명 중 영역이 나온 것이 326명(81.5%)이었고, 나머지 51명의 P106 을
	//   세어 보니 몇 개가 반복해서 나왔다 — Q2259451(연극 배우) 하나가 30명이다.
	//   라벨은 전부 wbgetentities 응답에서 읽었다.
	//
	//   이 표는 "흔한 것만" 담는다는 원칙 그대로다. 아래는 전부 **우리 원장에서
	//   실제로 관측된** 직업이고, 한 번도 안 나온 직업은 여전히 안 적는다.

	// ── 연예
	"Q2259451":  DomainEntertainment, // stage actor              연극 배우 (30명)
	"Q60723829": DomainEntertainment, // pop singer
	"Q44508716": DomainEntertainment, // television personality
	"Q55960555": DomainEntertainment, // recording artist
	"Q822146":   DomainEntertainment, // lyricist
	"Q2490358":  DomainEntertainment, // choreographer
	"Q27658988": DomainEntertainment, // reality television participant
	"Q6399436":  DomainEntertainment, // video jockey
	"Q3455803":  DomainEntertainment, // director — 창작물 감독

	// ── 스포츠
	"Q18200514": DomainSports, // short-track speed skater
	"Q10866633": DomainSports, // speed skater
	"Q15117302": DomainSports, // volleyball player
	"Q13219587": DomainSports, // figure skater
	"Q13382533": DomainSports, // taekwondo athlete
	"Q13415036": DomainSports, // rugby player
	"Q3186699":  DomainSports, // Go professional      프로 바둑 기사
	"Q4379701":  DomainSports, // professional gamer   프로게이머

	// ── 언론
	"Q1371925":   DomainMedia, // announcer      아나운서
	"Q135301631": DomainMedia, // broadcaster
	"Q17125263":  DomainMedia, // YouTuber

	// ── 정치
	"Q8125919":  DomainPolitics, // political adviser
	"Q11499147": DomainPolitics, // political activist
	"Q1476215":  DomainPolitics, // human rights defender
	"Q47064":    DomainPolitics, // military personnel — 행정·공직 계열로 둔다

	// ── 예술
	"Q482980":  DomainArts, // author
	"Q483501":  DomainArts, // artist
	"Q3501317": DomainArts, // fashion designer

	// ── 학계
	"Q16831721": DomainAcademia, // ethologist

	//   ※ Q46069542(former comfort women)는 **직업이 아니다.** 겪은 일이지 하는 일이
	//      아니고, 그것으로 사람을 분류하면 안 된다. 같은 사람의 다른 P106
	//      (Q1476215 인권운동가)이 영역을 말해 준다. 적지 않는다.
	//   ※ Q488111(pornographic film actor)·Q11737267(catechist)도 안 적는다 —
	//      전자는 우리 원장에 1건이고 분류가 그 사람에 대한 판단으로 읽힌다,
	//      후자는 직업 영역 어디에도 안 맞는다. 모르는 것은 비워 둔다(D-37).
}

// OccupationDomain — P106 QID 목록에서 영역 하나를 고른다.
//
// 여럿이면 **가장 흔한 것**을 쓰고, 동수면 아래 순서로 가른다. 한 사람이 배우이면서
// 정치인일 수 있는데(실제로 있다), 그때 무엇으로 부를지는 임의가 아니라 고정이어야
// 같은 입력에 같은 답이 나온다.
//
// 아무 QID 도 표에 없으면 빈 문자열이다 — 모르는 것을 지어내지 않는다(D-37).
func OccupationDomain(p106 []string) string {
	if len(p106) == 0 {
		return ""
	}
	count := map[string]int{}
	for _, q := range p106 {
		if d := occupationDomains[q]; d != "" {
			count[d]++
		}
	}
	if len(count) == 0 {
		return ""
	}
	// 동수 가름 순서(고정). 연예를 앞에 두는 이유는 KDB 의 주 대상이라 그렇다 —
	// 배우 겸 정치인은 KDB 안에서 배우로 불리는 편이 소비자에게 덜 놀랍다.
	order := []string{
		DomainEntertainment, DomainSports, DomainPolitics,
		DomainBusiness, DomainMedia, DomainAcademia, DomainArts,
	}
	best, bestN := "", 0
	for _, d := range order {
		if count[d] > bestN {
			best, bestN = d, count[d]
		}
	}
	return best
}

// ── 성별 ──────────────────────────────────────────────────────────────────
//
// ★왜 드는가 (운영자 지시 2026-09-15: "사람들 직업 성별도 분류할거니?").
//
//	두 군데에 쓴다.
//	  ① 동명이인 가름 — 이름도 유형도 같은 두 사람을 가르는 신호가 하나 더 생긴다.
//	  ② 현지 표기 — 경칭·호칭이 성별로 갈리는 언어가 있다(es: Sr./Sra.).
//
// ★위키데이터 P21 을 그대로 쓴다. 우리가 추정하지 않는다 — 이름에서 성별을 추측하는
//
//	것은 **틀리는 종류의 판단**이고(지민·현우·서연 모두 양성), 틀리면 사람에 대한
//	사실을 잘못 적는 것이라 표기 오류보다 무겁다. 모르면 빈 문자열이다(D-37).
const (
	GenderMale   = "male"
	GenderFemale = "female"
	GenderOther  = "other" // 논바이너리·트랜스젠더·인터섹스 등 위키데이터가 따로 든 값
)

// genderQIDs — P21 QID → 값. 위키데이터가 실제로 쓰는 것만 담는다.
var genderQIDs = map[string]string{
	"Q6581097":  GenderMale,   // male
	"Q6581072":  GenderFemale, // female
	"Q1097630":  GenderOther,  // intersex
	"Q48270":    GenderOther,  // non-binary
	"Q1052281":  GenderOther,  // trans woman
	"Q2449503":  GenderOther,  // trans man
	"Q189125":   GenderOther,  // transgender person
	"Q179294":   GenderOther,  // eunuch
	"Q15145778": GenderMale,   // cisgender male
	"Q15145779": GenderFemale, // cisgender female
}

// Gender — P21 QID 목록에서 값 하나를 고른다. 모르면 빈 문자열이다.
//
// 여럿이면 **첫 번째로 아는 것**을 쓴다. P21 이 여럿인 경우는 대개 전환 이력이라
// 순서가 의미를 갖는다 — 우리가 재배열하지 않는다.
func Gender(p21 []string) string {
	for _, q := range p21 {
		if g := genderQIDs[q]; g != "" {
			return g
		}
	}
	return ""
}
