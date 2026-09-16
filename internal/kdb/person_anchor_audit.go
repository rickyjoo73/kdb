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
	"strings"
	"context"
	"log"
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
//   활성 5,830건을 전량 대조하니 person 밖에서만 500건 넘게 나왔다:
//     drama 에 붙은 사람 QID · song_album 에 붙은 사람 QID · brand_place 에 붙은 회사 QID …
//   그리고 **이름 항목(given name) QID 가 person 아닌 유형에도 57건** 붙어 있었다.
//   person/character 만 보는 감사는 그것들을 한 번도 안 봤다.
var anchorExpectedType = map[string]string{
	"Q5": "person",
	"Q215380": "group", "Q9212979": "group", "Q2088357": "group", "Q7623897": "group",
	"Q56816954": "group", "Q281643": "group", "Q641066": "group", "Q216337": "group",
	"Q11424": "movie", "Q24869": "movie", "Q506240": "movie",
	"Q5398426": "drama", "Q3464665": "drama", "Q1366112": "drama", "Q63952888": "drama",
	"Q15416": "show", "Q1555508": "show",
	"Q482994": "song_album", "Q7366": "song_album", "Q208569": "song_album", "Q169930": "song_album",
	"Q134556": "song_album", "Q211236": "song_album", "Q105543609": "song_album",
	"Q4830453": "agency", "Q891723": "agency", "Q783794": "agency", "Q18127": "agency",
	"Q1762059": "agency", "Q5354754": "agency",
	"Q1616075": "channel_outlet", "Q1002697": "channel_outlet", "Q11033": "channel_outlet",
	"Q14350": "channel_outlet", "Q868557": "channel_outlet", "Q1153191": "channel_outlet",
	"Q2001305": "channel_outlet",
	"Q132241": "event_tour", "Q182832": "event_tour", "Q1436734": "event_tour",
	"Q18342255": "event_tour", "Q618779": "event_tour",
	"Q95074": "character", "Q15632617": "character", "Q15773317": "character",
	"Q3658341": "character", "Q15773347": "character",
}

// AnchorExpectedType — P31 QID 가 말하는 우리 유형. 없으면 (,false).
//
// ★분류에서 **LLM 대신** 쓴다(2026-09-15, 운영자 지시 "가능한 gemma를 사용하지 않고").
//   같은 표를 감사와 분류가 함께 본다 — 둘이 다른 표를 보면 인입에서 통과한 유형을
//   감사가 어긋났다고 하거나 그 반대가 된다.
func AnchorExpectedType(qid string) (string, bool) {
	t, ok := anchorExpectedType[strings.TrimSpace(qid)]
	return t, ok
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
	AnchorUnknown     = "no-p31" // P31 이 비었다 — **판정하지 않는다**(D-37)
)

type PersonAnchorMismatch struct {
	ID, KO, EntityType, QID string
	Verdict, Class, Desc    string
	Tier, JA, JASource      string
	// LabelEN — QID 의 영문 라벨. 우리 canonical_en 과 나란히 놓으면
	// "앵커가 틀렸나 유형이 틀렸나"가 갈린다(0140 주석).
	LabelEN string
}

// AuditPersonAnchors — 활성 person/character 의 wikidata 앵커를 조회해 어긋난 것을 돌려준다.
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
   AND e.entity_type IN ('person','character')
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
//   tdb_mapping.go:190,346 · 여기). 하나만 달라지면 인입에서 막은 것을 감사가
//   통과시키거나 그 반대가 된다. 시험이 이 함수를 그 넷과 같은 표로 고정한다.
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
		for _, q := range instanceOf {
			if want, ok := anchorExpectedType[q]; ok {
				if want == entityType {
					return "", ""
				}
				return AnchorTypeMismatch, q
			}
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
//   NULL 로 나가 INSERT 가 죽었다. 죽으면 "봤다"는 기록이 안 남아 **다음 감사가 같은
//   QID 를 또 Fetch 한다** — 영영 끝나지 않는다. 판정을 못 하는 것(D-37)과 보지 않은
//   것은 다르다. 빈 배열로 적어 "봤고, 판정할 P31 이 없었다"를 남긴다.
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
