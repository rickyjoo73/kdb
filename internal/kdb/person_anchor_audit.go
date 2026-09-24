package kdb

// person_anchor_audit — **활성** 원장의 wikidata 앵커가 그 유형과 맞는지 읽기만 한다.
//
// ★왜 또 만드는가. 규칙은 이미 있었다.
//   resolution.go:193       person 인데 P31 에 Q5 가 없으면 기각
//   common_fill.go:246      같은 판정
//   tdb_mapping.go:190,346  같은 판정
//   wikidata.IsNameElement  "이름 그 자체" 항목(주어진 이름·성씨·동음이의)을 앵커에서 배제
// 그런데 이 넷은 전부 **들어올 때** 검사한다. 이미 active 로 앉아 있는 행은
// 아무도 다시 안 봤다. reverted_terminate_drain 이 유일하게 되돌아보지만 그쪽은
// `status='candidate' AND notes LIKE '%audit-revert%'` 만 훑는다.
//
// ★실측(운영, 2026-09-15). active person 5,113건 중 wikidata 앵커가 있는 3,583건을
// 전량 조회했더니 P31 에 Q5(사람)가 없는 것이 **110건**이었다.
//
//	가비   Q5515395   = 영화      → ja 가 `GABI/ガビ-国境の愛-` (영화 제목을 사람 일본어 표기로)
//	댄싱9  Q14749362  = 방송      → person 으로 앉아 있음
//	남궁   Q4312911   = 한국 성씨  → person
//	미나   Q69507266  = 여성의 이름 → person
//	이로하 Q107577910 = 허구의 사람 → person   ★배역
//	DK     Q85976326  = e스포츠 팀 → person
//
// 110건 **전부 verification_tier='authoritative'** 였고, 표기 출처는 대부분
// `wikidata-label` 이다. 즉 **틀린 항목에서 긁어온 이름을 가장 믿을 만한 등급으로
// 내보내고 있었다.** 소비자가 "내용은 있는데 엉뚱한 값"이라고 신고한 것이 이것이다.
//
// ★이 파일은 **읽기만 한다.** 고치지 않는다.
//   opencc 간체 교정에서 겪었다 — 제안의 절반이 틀렸는데 세어만 보고 돌릴 뻔했다.
//   그래서 먼저 눈으로 본다. 그리고 고칠 때도 대상은 **앵커이지 대상이 아니다** —
//   가비는 실존 무용가다. 틀린 것은 QID 이지 사람이 아니다(I03: 자체 ID 가 주 앵커,
//   QID 는 보조). 대상을 지우면 안 된다.

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
)

// fictionalClasses — P31 이 이것이면 **실존 인물이 아니라 배역/가상 인물**이다.
// person 에 붙으면 오염이고, character 에 붙으면 옳다.
var fictionalClasses = map[string]bool{
	"Q95074":    true, // 허구의 등장인물
	"Q15632617": true, // 허구의 사람
	"Q3658341":  true, // 문학 속 인물
	"Q15773317": true, // 텔레비전 등장인물
	"Q15773347": true, // 영화 등장인물
	"Q1114461":  true, // 만화 등장인물
	"Q97498056": true, // 애니메이션 등장인물
	"Q20085850": true, // 허구 작품 속 요정
}

// anchorExpectedType — P31 → **우리 유형**. 근거가 명확한 클래스만 적는다.
//
// ★2026-09-15 확장. 종전엔 person/character 만 봤다. 그런데 어긋남은 전 유형에 있었다 —
//
//	활성 5,830건을 전량 대조하니 person 밖에서만 500건 넘게 나왔다:
//	  drama 에 붙은 사람 QID · song_album 에 붙은 사람 QID · brand_place 에 붙은 회사 QID …
//	그리고 **이름 항목(given name) QID 가 person 아닌 유형에도 57건** 붙어 있었다.
//	person/character 만 보는 감사는 그것들을 한 번도 안 봤다.
var anchorExpectedType = map[string][]string{
	"Q5":      {"person"},
	"Q215380": {"group"}, "Q9212979": {"group"}, "Q2088357": {"group"}, "Q7623897": {"group"},
	"Q56816954": {"group"}, "Q281643": {"group"}, "Q641066": {"group"}, "Q216337": {"group"},
	"Q11424": {"movie"}, "Q24869": {"movie"}, "Q506240": {"movie"},
	// ★TV 클래스 셋은 **드라마와 예능을 못 가른다** (2026-09-16 실측).
	//   위키데이터에 그 구분이 없다 — 개그콘서트·M COUNTDOWN·검사내전이 전부
	//   Q5398426("television series") 하나다. 우리 분류가 더 잘다.
	//   단일값으로 두었더니 감사가 멀쩡한 앵커 81건을 «어긋남»으로 찍었다.
	//   못 가르는 것을 가른다고 하면 안 된다(D-37).
	"Q5398426":  {"drama", "show"}, // television series          — 둘 다
	"Q3464665":  {"drama", "show"}, // television series season   — 둘 다
	"Q15416":    {"show", "drama"}, // television program         — 둘 다
	"Q1366112":  {"drama"},         // drama television series    — 드라마 전용
	"Q63952888": {"drama"},         // anime television series    — 드라마 전용
	"Q1555508":  {"show"},          // radio program
	"Q482994":   {"song_album"}, "Q7366": {"song_album"}, "Q208569": {"song_album"}, "Q169930": {"song_album"},
	"Q134556": {"song_album"}, "Q211236": {"song_album"}, "Q105543609": {"song_album"},

	// ★기업 클래스는 **agency 와 company 양쪽**이다 (2026-09-16).
	//   0143 전에는 회사 QID 가 붙을 자리가 소속사/제작사(agency)뿐이라 단일값이었다.
	//   이제 삼성전자·SK하이닉스 같은 일반 기업이 company 로 들어온다. 둘 다 Q4830453
	//   "business enterprise" 를 쓴다 — QID 는 둘을 못 가른다. 못 가르는 것을 가른다고
	//   하면 멀쩡한 앵커가 «어긋남»으로 찍힌다(D-37).
	"Q4830453": {"agency", "company"}, "Q891723": {"agency", "company"},
	"Q783794": {"agency", "company"}, "Q18127": {"agency"},
	"Q1762059": {"agency", "company"}, "Q5354754": {"agency"}, // 영화 제작사는 제작사이자 기업이다(롯데엔터테인먼트·쇼박스)
	"Q6881511": {"company"}, "Q167037": {"company"},
	"Q210167":  {"company"}, // video game developer
	"Q2085381": {"company"}, // publishing house

	"Q1616075": {"channel_outlet"}, "Q1002697": {"channel_outlet"}, "Q11033": {"channel_outlet"},
	"Q14350": {"channel_outlet"}, "Q1153191": {"channel_outlet"},
	// ★Q868557 은 «music festival» 이다 — 채널이 아니다 (2026-09-24 정정). 이 한 줄 때문에
	//   MBC 대학가요제·대구국제뮤지컬페스티벌이 «채널로 옮기자» 는 판정을 받았다.
	"Q868557":  {"event_tour"},
	"Q2001305": {"channel_outlet"},

	// ── 2026-09-24 추가: 판단 보류 111건을 보니 둘 다 맞는데 표가 몰라서 «어긋남» 으로
	//    남은 행이 많았다(SBS·SPOTV·Viki·뉴스와이어·이디야커피·씨네21·카카오웹툰·하츄핑).
	"Q1254874":  {"channel_outlet"},                  // television network        SBS
	"Q1061197":  {"channel_outlet"},                  // radio network              SBS
	"Q561068":   {"channel_outlet"},                  // specialty channel          SPOTV
	"Q92483361": {"channel_outlet"},                  // subscription television channel
	"Q559856":   {"channel_outlet"},                  // online video platform      Viki
	"Q59152282": {"channel_outlet"},                  // video streaming service   Viki
	"Q11790928": {"channel_outlet"},                  // newswire                  뉴스와이어
	"Q24897257": {"channel_outlet"},                  // webcomic website          카카오웹툰
	"Q41298":    {"publication", "channel_outlet"},   // magazine — 출판물이자 매체(씨네21)
	"Q368290":   {"agency", "company"},               // film distributor   쇼박스
	"Q507619":   {"brand_place", "company"},          // retail chain       요아정·이디야커피
	"Q76212517": {"brand_place", "company"},          // café chain         이디야커피
	"Q12586630": {"government_body", "organization"}, // 기금관리형 준정부기관  국민연금공단
	"Q30318131": {"character"},                       // fairy in a work of fiction  하츄핑
	"Q132241":   {"event_tour"}, "Q182832": {"event_tour"}, "Q1436734": {"event_tour"},
	"Q18342255": {"event_tour"}, "Q618779": {"event_tour"},
	"Q95074": {"character"}, "Q15632617": {"character"}, "Q15773317": {"character"},
	"Q3658341": {"character"}, "Q15773347": {"character"},

	// ★0143·0146 으로 늘어난 유형 (2026-09-16).
	//
	//   유형은 문서와 DB 에 들어갔는데 **감사·분류가 보는 표에는 없었다.** 그래서
	//   국민의힘·SK하이닉스 는 brand_place 로 앉아 있었고, 새 유형으로 들어오는 앵커는
	//   «판정하지 않음»으로 조용히 지나갔다. 문서가 아는 것을 기계도 알아야 한다.
	//
	//   ★아래 QID 는 전부 **위키데이터에서 라벨·설명을 직접 확인한 것**이다.
	//     처음 적을 때 기억으로 쓴 10여 개 중 5개가 틀렸다 —
	//       Q37002670 을 political_party 로 적었으나 실제는 "unicameral legislature",
	//       Q2085381 을 government_body 로 적었으나 실제는 "publishing house",
	//       Q17489659 "group of works" · Q865493 "video game mod" 은 대상이 아니고,
	//       Q21198342 은 "manga series"(일본 만화)라 webtoon 이 아니다.
	//     못 본 것을 적으면 감사가 멀쩡한 앵커를 «어긋남»으로 찍는다(D-37).
	"Q7278": {"political_party"},

	"Q327333":   {"government_body"}, // government agency
	"Q2659904":  {"government_body"}, // government organization
	"Q192350":   {"government_body"}, // ministry
	"Q37002670": {"government_body"}, // unicameral legislature (국회)

	"Q163740":  {"organization"}, // nonprofit organization
	"Q157031":  {"organization"}, // foundation
	"Q79913":   {"organization"}, // non-governmental organization
	"Q48204":   {"organization"}, // voluntary association
	"Q3152824": {"organization"}, // cultural institution
	// 체육 단체는 협회(대한축구협회)일 수도 구단일 수도 있다 — QID 가 못 가른다.
	"Q4438121": {"organization", "sports_team"}, // sports organization

	"Q847017":   {"sports_team"}, // sports club
	"Q476028":   {"sports_team"}, // association football club
	"Q12973014": {"sports_team"}, // sports team
	"Q13393265": {"sports_team"}, // basketball team
	"Q13027888": {"sports_team"}, // baseball team
	"Q20639856": {"sports_team"}, // professional sports team

	"Q3918":    {"school"}, // university
	"Q875538":  {"school"}, // public university
	"Q902104":  {"school"}, // private university
	"Q9826":    {"school"}, // high school
	"Q159334":  {"school"}, // secondary school
	"Q3914":    {"school"}, // school
	"Q189004":  {"school"}, // college
	"Q2385804": {"school"}, // educational institution

	"Q7889":    {"game"}, // video game
	"Q7058673": {"game"}, // video game series
	"Q1121542": {"game"}, // mobile game
	"Q131436":  {"game"}, // board game

	"Q2743":  {"musical_play"}, // musical
	"Q25379": {"musical_play"}, // play

	"Q7725634":  {"publication"}, // literary work
	"Q571":      {"publication"}, // book
	"Q47461344": {"publication"}, // written work

	"Q7978994":  {"webtoon"}, // webtoon — web comics originating from South Korea
	"Q562214":   {"webtoon"}, // manhwa
	"Q74262765": {"webtoon"}, // manhwa series
	// 일반 "comic" 은 웹툰일 수도 출판만화일 수도 있다.
	"Q1004": {"webtoon", "publication"},

	// ★새 유형 앵커 레인이 실제로 만난 클래스 (2026-09-16).
	//
	//   후보 131건을 위키데이터에 전건 조회하니 **63건이 이름까지 맞는 항목을
	//   갖고 있었다.** 그런데 첫 dry-run 이 붙인 건 40건 중 6건뿐이었고, 못 붙인
	//   이유의 최다는 «표에 없는 P31» 이었다 — 위키데이터가 답을 갖고 있는데
	//   우리 표가 좁아서 «판정하지 않음»으로 지나갔다.
	//
	//   아래는 그 조회에서 **실제로 관측된** 클래스만 적은 것이다. 라벨은 전부
	//   wbgetentities 응답에서 읽었다(기억으로 적어 5개를 틀린 전례가 있다).
	//   예시는 그 클래스를 실제로 가진 우리 후보다.

	// ── 정부·행정 (한국 전용 클래스가 따로 있다)
	"Q136542063": {"government_body"}, // Korean Ministry            고용노동부·문체부
	"Q136542088": {"government_body"}, // Korean Executive Administration Agency  국세청·기상청
	"Q136543090": {"government_body"}, // Presidential Support Office of Korea    대통령실
	"Q12592228":  {"government_body"}, // district court of South Korea           서울동부지법
	"Q12813215":  {"government_body"}, // ministry of labour                      고용노동부
	"Q19973770":  {"government_body"}, // ministry of culture                     문체부
	"Q2446662":   {"government_body"}, // tourism ministry                        문체부
	"Q107099245": {"government_body"}, // sport ministry                          문체부
	"Q88590501":  {"government_body"}, // ministry of presidency                  대통령실
	"Q859482":    {"government_body"}, // secretariat                             대통령실
	"Q28060193":  {"government_body"}, // governmental meteorological service     기상청
	"Q35535":     {"government_body"}, // police                                  서울·전북경찰청
	"Q781132":    {"government_body"}, // military branch                         공군
	"Q105062392": {"government_body"}, // financial regulatory agency             금감원
	"Q11571013":  {"government_body"}, // specially designated public corporation 금감원
	"Q11484275":  {"government_body"}, // government office
	"Q8010730":   {"government_body"}, // independent organ
	"Q20857065":  {"government_body"}, // United States federal agency — 국가 관문이 따로 거른다

	// 공공기관은 «기관»일 수도 «단체»일 수도 있다. QID 가 둘을 못 가른다.
	"Q16168183":  {"government_body", "organization"}, // 위탁집행형 준정부기관  국민건강보험공단·한국관광공사
	"Q12617530":  {"government_body", "organization"}, // 준시장형 공기업        한국관광공사
	"Q125852944": {"government_body", "organization"}, // 기타공공기관          대한체육회

	// ── 도시는 «기관»이 아니다.
	//
	//   부산시·밀양시·동두천시가 government_body 로 앉아 있다(소비자 type 힌트).
	//   위키데이터는 이것들을 도시라 말한다 — 맞는 말이고, 그럼 우리 유형이 틀렸다.
	//   그래서 brand_place 로 적는다. 앵커 레인은 이걸 «유형 어긋남»으로 보고
	//   붙이지 않고, 유형 감사가 옮길 근거를 얻는다. 모른 척하는 것보다 낫다.
	"Q515":      {"brand_place"}, // city
	"Q200250":   {"brand_place"}, // metropolis
	"Q2264924":  {"brand_place"}, // port city
	"Q482821":   {"brand_place"}, // metropolitan city of South Korea
	"Q29045252": {"brand_place"}, // city of South Korea
	"Q1549591":  {"brand_place"}, // big city

	// ── 단체
	"Q43229":     {"organization"}, // organization — 최상위지만 실제로 이게 유일한 P31 인 단체가 많다
	"Q183288":    {"organization"}, // National Olympic Committee        대한체육회
	"Q2485448":   {"organization"}, // sports governing body             대한체육회
	"Q1478443":   {"organization"}, // association football federation   대한축구협회
	"Q37178026":  {"organization"}, // metaorganization                  한국박물관협회
	"Q117467133": {"organization"}, // tourism organization             한국관광공사
	"Q1302299":   {"organization"}, // youth center                      서울광역청년센터
	"Q16917":     {"organization"}, // hospital                          삼성서울병원
	"Q1813474":   {"organization"}, // teaching hospital                 삼성서울병원
	"Q33506":     {"organization"}, // museum                            국립전주박물관
	"Q207694":    {"organization"}, // art museum                        국립현대미술관 서울관
	"Q17431399":  {"organization"}, // national museum                   국립전주박물관

	// ── 학교
	"Q15936437": {"school"}, // research university   서울대·서강대·광운대
	"Q265662":   {"school"}, // national university   서울대·한국체육대

	// ── 기업
	"Q22687":  {"company"}, // bank                우리금융지주
	"Q730038": {"company"}, // credit institution  우리금융지주

	// ── 공연
	"Q58483083": {"musical_play"}, // dramatico-musical work  레 미제라블
	//   ※ Q3024240(historical country, 후백제·미리미동국)은 **일부러 안 적는다.**
	//      역사 국가를 담을 유형이 우리에게 없다. 없는 칸으로 옮길 수는 없다(D-37).
}

// genericAnchorClasses — **허용은 하지만 결정하지는 못하는** 클래스.
//
// ★허용(allow)과 결정(determine)은 다른 물음이다 (2026-09-16).
//
//	Q43229 "organization" 은 최상위라, 단체도 기업도 기관도 전부 이것을 가진다.
//	  "이 단체가 organization 이어도 되는가"  → 된다.        (AnchorTypeAllowed)
//	  "이것이 무슨 유형인가"                  → 모른다.      (soleAnchorType)
//
//	둘을 섞었더니 바로 드러났다. Q43229 을 넣자마자 catchall-retype 이
//	**네이버(기업)를 organization 으로 옮기자**고 했다 — 네이버의 P31 에 우리 표가
//	아는 클래스가 Q43229 하나뿐이기 때문이다. 넣기 전엔 «판정 못 함»으로 그냥 뒀다.
//
//	그래서 결정하는 쪽에서만 뺀다. 넣은 이유(한국방송협회·한인애국단이 이것 하나만
//	갖고 있다)는 그대로 살아 있다 — 그쪽은 우리가 유형을 이미 말했고 위키데이터는
//	«아니라고 하지 않는다»만 답하면 되는 자리다.
var genericAnchorClasses = map[string]bool{
	"Q43229": true, // organization — 단체·기업·기관이 전부 가진다
	"Q35127": true, // website — 쿠팡·네이버 같은 기업도 가진다
}

// IsGenericAnchorClass — 이 클래스로 유형을 **결정**해도 되는가(안 된다면 true).
func IsGenericAnchorClass(qid string) bool { return genericAnchorClasses[strings.TrimSpace(qid)] }

// AnchorExpectedType — P31 QID 가 말하는 우리 유형. 없으면 (,false).
//
// ★분류에서 **LLM 대신** 쓴다(2026-09-15, 운영자 지시 "가능한 gemma를 사용하지 않고").
//
//	같은 표를 감사와 분류가 함께 본다 — 둘이 다른 표를 보면 인입에서 통과한 유형을
//	감사가 어긋났다고 하거나 그 반대가 된다.
func AnchorExpectedType(qid string) (string, bool) {
	t := anchorExpectedType[strings.TrimSpace(qid)]
	if len(t) == 0 {
		return "", false
	}
	return t[0], true
}

// AnchorTypeAllowed — 이 P31 이 우리 유형을 **허용하는가**. 표에 없으면 (false,false)
// 가 아니라 (?,false) — 모르는 것은 판정하지 않는다(D-37).
func AnchorTypeAllowed(qid, entityType string) (allowed, known bool) {
	t := anchorExpectedType[strings.TrimSpace(qid)]
	if len(t) == 0 {
		return false, false
	}
	for _, w := range t {
		if w == entityType {
			return true, true
		}
	}
	return false, true
}

// PersonAnchorVerdict — 무엇이 어긋났는지.
const (
	AnchorNameElement = "name-element" // 사람이 아니라 "이름" 항목 (주어진 이름·성씨·동음이의)
	AnchorFictional   = "fictional"    // 배역/가상 인물인데 person 으로 앉아 있다
	AnchorNotHuman    = "not-human"    // P31 이 있는데 Q5 가 없다 (영화·방송·팀 …)
	AnchorHumanOnChar = "human-on-character"
	// AnchorTypeMismatch — QID 가 가리키는 유형과 우리 유형이 다르다.
	// **어느 쪽이 틀렸는지는 이 판정만으로 모른다** — 영문 라벨 증거가 갈라 준다.
	AnchorTypeMismatch = "type-mismatch"
	AnchorUnknown      = "no-p31" // P31 이 비었다 — **판정하지 않는다**(D-37)
)

type PersonAnchorMismatch struct {
	ID, KO, EntityType, QID string
	Verdict, Class, Desc    string
	Tier, JA, JASource      string
	// LabelEN — QID 의 영문 라벨. 우리 canonical_en 과 나란히 놓으면
	// "앵커가 틀렸나 유형이 틀렸나"가 갈린다(0140 주석).
	LabelEN string
}

// AuditPersonAnchors — **활성 전 유형**의 wikidata 앵커를 조회해 어긋난 것을 돌려준다.
// (이름은 person 시절 그대로다. 부르는 곳이 여럿이라 이름만 따로 바꾸지 않는다.)
// 두 번째 반환값은 실제로 조회한 건수(모수). **표본이 0인데 모집단을 0이라 말하지 않기 위해서다.**
// AnchorAuditFreshness — 이보다 최근에 본 것은 다시 조회하지 않는다.
// 저장된 의견은 늙으므로 무한정 믿지 않는다. 30일이면 위키데이터 변경을 놓치지 않으면서
// 전량 재조회(3,762회, 약 30분)를 매번 하지 않아도 된다.
const AnchorAuditFreshness = 30 * 24 * time.Hour

func AuditPersonAnchors(ctx context.Context, pool *pgxpool.Pool, cl *wikidata.Client, limit int) ([]PersonAnchorMismatch, int) {
	if pool == nil || cl == nil {
		return nil, 0
	}
	if limit <= 0 {
		limit = 200
	}
	rows, err := pool.Query(ctx, `
SELECT e.id::text, e.canonical_ko, e.entity_type::text, x.external_id,
       COALESCE(e.verification_tier,''), COALESCE(e.canonical_ja,''), COALESCE(e.canonical_ja_source,'')
  FROM kwave_entities e
  JOIN kwave_entity_external_refs x ON x.entity_id = e.id AND x.provider = 'wikidata'
 WHERE e.status = 'active'
   -- ★전 유형을 본다 (2026-09-16). 종전엔 person·character 뿐이었다.
   --   그래서 brand_place·song_album·movie 에 붙은 틀린 앵커를 **아무도 안 봤다**.
   --   실측: 그 밖 유형 2,004건 중 어긋남 279 · 이름항목 32 — 전부 외래 표기를
   --   갖고 있어 지금 서빙 중이다. 신정호(아산의 호수)는 사람 QID 가 붙어
   --   canonical_ja 로 「申正浩」를 내보내고 있었다.
   AND x.external_id ~ '^Q[0-9]+$'
 ORDER BY e.canonical_ko
 LIMIT $1`, limit)
	if err != nil {
		log.Printf("kdb.anchor-audit: select: %v", err)
		return nil, 0
	}
	type row struct{ id, ko, typ, qid, tier, ja, jaSrc string }
	var items []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.id, &r.ko, &r.typ, &r.qid, &r.tier, &r.ja, &r.jaSrc) == nil {
			items = append(items, r)
		}
	}
	rows.Close()

	var out []PersonAnchorMismatch
	checked := 0
	for _, it := range items {
		// 최근에 본 것은 저장된 판정을 쓴다. 위키데이터를 다시 부르지 않는다.
		if m, ok := recentAnchorVerdict(ctx, pool, it.id, it.qid, it.typ); ok {
			checked++
			m.KO, m.Tier, m.JA, m.JASource = it.ko, it.tier, it.ja, it.jaSrc
			if m.Verdict != "" {
				out = append(out, m)
			}
			continue
		}
		ent, err := cl.Fetch(ctx, it.qid)
		if err != nil || ent == nil {
			continue // 조회 실패는 판정이 아니다
		}
		checked++
		m := PersonAnchorMismatch{ID: it.id, KO: it.ko, EntityType: it.typ, QID: it.qid,
			Tier: it.tier, JA: it.ja, JASource: it.jaSrc, Desc: ent.Descriptions["en"]}
		if m.Desc == "" {
			m.Desc = ent.Descriptions["ko"]
		}
		m.LabelEN = ent.SourceLabels["en"]
		if m.LabelEN == "" {
			m.LabelEN = ent.Labels["en"]
		}
		m.Verdict, m.Class = anchorVerdictFor(it.typ, ent.InstanceOf)
		saveAnchorVerdict(ctx, pool, it.id, it.qid, it.typ, m, ent.InstanceOf)
		if m.Verdict != "" {
			out = append(out, m)
		}
	}
	return out, checked
}

// anchorVerdictFor — **순수 판정.** 유형과 P31 목록만 보고 어긋났는지 말한다.
//
// ★네 곳이 같은 명제를 들고 있다(resolution.go:193 · common_fill.go:246 ·
//
//	tdb_mapping.go:190,346 · 여기). 하나만 달라지면 인입에서 막은 것을 감사가
//	통과시키거나 그 반대가 된다. 시험이 이 함수를 그 넷과 같은 표로 고정한다.
//
// P31 이 비면 **판정하지 않는다** — 근거 없이 죽이지 않는다(D-37). 빈 문자열을 돌려준다.
func anchorVerdictFor(entityType string, instanceOf []string) (verdict, class string) {
	if len(instanceOf) == 0 {
		return "", ""
	}
	human := containsString(instanceOf, "Q5")
	fictional, fictionalClass := false, ""
	for _, q := range instanceOf {
		if fictionalClasses[q] {
			fictional, fictionalClass = true, q
			break
		}
	}
	// ★이름 항목은 **어떤 유형에도** 유효한 앵커가 아니다. 유형을 가리기 전에 본다.
	//   종전엔 person 분기 안에만 있어서, drama·song_album 에 붙은 이름 항목 57건을
	//   한 번도 안 봤다.
	for _, q := range instanceOf {
		if wikidata.IsNameElementClass(q) {
			return AnchorNameElement, q
		}
	}
	switch entityType {
	case "person":
		if fictional {
			return AnchorFictional, fictionalClass
		}
		if !human {
			return AnchorNotHuman, instanceOf[0]
		}
	case "character":
		if human && !fictional {
			return AnchorHumanOnChar, "Q5"
		}
	default:
		// 그 밖의 유형: QID 가 말하는 유형과 우리 유형이 맞는지 본다.
		// 매핑표에 없는 P31 은 **판정하지 않는다** — 모르는 것을 틀렸다고 하지 않는다(D-37).
		// ★결정하지 못하는 클래스로는 «어긋남» 을 말하지 않는다 (2026-09-16).
		//   Q43229("organization")은 단체·기업·기관이 전부 갖는다. 그것으로 판정하면
		//   네이버(기업)가 «어긋남» 으로 찍힌다 — 그 클래스는 아무 말도 안 한 것이다.
		//   먼저 **허용하는 클래스가 하나라도 있는지** 보고, 없을 때만 어긋남을 말한다.
		//   (종전엔 첫 번째로 아는 클래스 하나에서 바로 결론을 내, 뒤에 있는 맞는
		//    클래스를 못 봤다.)
		mismatch := ""
		for _, q := range instanceOf {
			if genericAnchorClasses[q] {
				continue
			}
			ok, known := AnchorTypeAllowed(q, entityType)
			if !known {
				continue
			}
			if ok {
				return "", ""
			}
			if mismatch == "" {
				mismatch = q
			}
		}
		if mismatch != "" {
			return AnchorTypeMismatch, mismatch
		}
	}
	return "", ""
}

// recentAnchorVerdict — 최근 판정이 있으면 그것을 쓴다. 유형이 그 사이에 바뀌었으면
// 다시 본다 — 판정은 (유형, QID) 짝에 대한 것이지 QID 하나에 대한 것이 아니다.
func recentAnchorVerdict(ctx context.Context, pool *pgxpool.Pool, id, qid, typ string) (PersonAnchorMismatch, bool) {
	var m PersonAnchorMismatch
	err := pool.QueryRow(ctx, `
SELECT verdict, class, description, label_en FROM kwave_kdb_anchor_audit
 WHERE entity_id = $1 AND provider = 'wikidata' AND external_id = $2
   AND entity_type = $3 AND checked_at > now() - $4::interval`,
		id, qid, typ, AnchorAuditFreshness.String()).Scan(&m.Verdict, &m.Class, &m.Desc, &m.LabelEN)
	if err != nil {
		return m, false
	}
	m.ID, m.QID, m.EntityType = id, qid, typ
	return m, true
}

// saveAnchorVerdict — 일치한 것도 적는다. "언제 봤는데 문제없었다"를 알아야 다시 안 본다.
// instance_of 를 원자료 그대로 남겨, 판정 규칙이 바뀌어도 다시 판정할 수 있게 한다.
//
// ★P31 이 없는 항목도 적는다(2026-09-15). instance_of 는 NOT NULL 인데 nil 슬라이스는
//
//	NULL 로 나가 INSERT 가 죽었다. 죽으면 "봤다"는 기록이 안 남아 **다음 감사가 같은
//	QID 를 또 Fetch 한다** — 영영 끝나지 않는다. 판정을 못 하는 것(D-37)과 보지 않은
//	것은 다르다. 빈 배열로 적어 "봤고, 판정할 P31 이 없었다"를 남긴다.
func saveAnchorVerdict(ctx context.Context, pool *pgxpool.Pool, id, qid, typ string, m PersonAnchorMismatch, p31 []string) {
	if p31 == nil {
		p31 = []string{}
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO kwave_kdb_anchor_audit (entity_id, provider, external_id, entity_type, verdict, class, instance_of, description, label_en, checked_at)
VALUES ($1,'wikidata',$2,$3,$4,$5,$6,$7,$8,now())
ON CONFLICT (entity_id, provider, external_id) DO UPDATE SET
  entity_type = EXCLUDED.entity_type, verdict = EXCLUDED.verdict, class = EXCLUDED.class,
  instance_of = EXCLUDED.instance_of, description = EXCLUDED.description,
  label_en = EXCLUDED.label_en, checked_at = now()`,
		id, qid, typ, m.Verdict, m.Class, p31, m.Desc, m.LabelEN); err != nil {
		log.Printf("kdb.anchor-audit: 판정 저장 실패 %s: %v", id, err)
	}
}
