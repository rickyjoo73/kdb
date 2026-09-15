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

// PersonAnchorVerdict — 무엇이 어긋났는지.
const (
	AnchorNameElement = "name-element" // 사람이 아니라 "이름" 항목 (주어진 이름·성씨·동음이의)
	AnchorFictional   = "fictional"    // 배역/가상 인물인데 person 으로 앉아 있다
	AnchorNotHuman    = "not-human"    // P31 이 있는데 Q5 가 없다 (영화·방송·팀 …)
	AnchorHumanOnChar = "human-on-character"
	AnchorUnknown     = "no-p31" // P31 이 비었다 — **판정하지 않는다**(D-37)
)

type PersonAnchorMismatch struct {
	ID, KO, EntityType, QID string
	Verdict, Class, Desc    string
	Tier, JA, JASource      string
}

// AuditPersonAnchors — 활성 person/character 의 wikidata 앵커를 조회해 어긋난 것을 돌려준다.
// 두 번째 반환값은 실제로 조회한 건수(모수). **표본이 0인데 모집단을 0이라 말하지 않기 위해서다.**
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
		if len(ent.InstanceOf) == 0 {
			continue // P31 이 없으면 판단 불가 — 근거 없이 죽이지 않는다
		}
		human := containsString(ent.InstanceOf, "Q5")
		fictional := false
		for _, q := range ent.InstanceOf {
			if fictionalClasses[q] {
				fictional, m.Class = true, q
				break
			}
		}
		switch {
		case it.typ == "person" && fictional:
			m.Verdict = AnchorFictional
		case it.typ == "person":
			if nameEl, cls := ent.IsNameElement(); nameEl {
				m.Verdict, m.Class = AnchorNameElement, cls
			} else if !human {
				m.Verdict, m.Class = AnchorNotHuman, ent.InstanceOf[0]
			}
		case it.typ == "character" && human && !fictional:
			m.Verdict, m.Class = AnchorHumanOnChar, "Q5"
		}
		if m.Verdict != "" {
			out = append(out, m)
		}
	}
	return out, checked
}

