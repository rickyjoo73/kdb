package kdb

// anchor_judge — 저장된 앵커 어긋남을 **gpt-6-luna 가 가린다**: 앵커가 틀렸나, 유형이 틀렸나.
//
// ★계기 (2026-09-23). anchor-enforce 를 돌리니 435건 전부가 «검수로» 갔다. 자동으로
//   떼는 것은 name-element 하나뿐이고, 나머지는 «앵커와 유형 중 어느 쪽이 틀렸는지 근거만으로
//   못 가린다». 맞는 말이다 — 같은 날 실측에 둘 다 있었다:
//     앵커가 틀림   단발머리(조용필 곡)에 걸그룹 QID → en "Bob Girls"(wikidata-label)
//     유형이 틀림   수애(배우)가 movie, 리안(안무가)이 drama
//   그런데 «사람에게 보낸다» 의 사람이 없었다. 435건이 «검증» 등급으로 계속 나갔다.
//
// ★판정은 근거를 보고 한다: 우리 이름·유형·표기·소비자 힌트 옆에 그 QID 의 ko 라벨·
//   kowiki 제목·영문 라벨·설명을 놓고 «같은 대상인가» 를 묻는다. 모르면 unclear 로 두게 한다.
//
// ★집행 규칙
//   anchor_wrong  앵커를 떼고 그 앵커에서 온(wikidata-label) 칸을 비운다. **비우기 전에 원값을
//                 dataqa_log 에 적는다**(withdrawOneAnchor 는 칸 이름만 남긴다).
//                 wikidata ref 가 정확히 하나일 때만 — 여럿이면 어느 것이 출처인지 못 가린다.
//   type_wrong    앵커의 P31 이 새 유형을 **허용할 때만** 옮긴다(허용 표가 모르면 옮기지 않는다).
//   unclear       그대로 둔다.
//   GPT 가 아닌 모델이 답했으면(상한 소진 → gemma) **아무것도 하지 않는다.**
//
// 기본 dry.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyjoo73/kdb/internal/kdb/codexcli"
	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
)

// AnchorJudgeResult — 한 번 돈 결과.
type AnchorJudgeResult struct {
	Checked, AnchorWrong, TypeWrong, Unclear, Skipped, NotGPT int
	CellsCleared                                              int
	Samples                                                   []string
}

type anchorJudgeAnswer struct {
	Verdict    string `json:"verdict"` // anchor_wrong | type_wrong | unclear
	ActualType string `json:"actual_type"`
	Reason     string `json:"reason"`
}

func anchorJudgeSchema() []byte {
	var enum strings.Builder
	for _, t := range AssignableEntityTypes() {
		enum.WriteString(`"` + t + `",`)
	}
	enum.WriteString(`""`)
	return []byte(`{"type":"object","additionalProperties":false,
"properties":{"verdict":{"type":"string","enum":["anchor_wrong","type_wrong","unclear"]},
"actual_type":{"type":"string","enum":[` + enum.String() + `]},
"reason":{"type":"string"}},
"required":["verdict","actual_type","reason"]}`)
}

type anchorJudgeRow struct {
	PersonAnchorMismatch
	EN, ZH, Notes string
	Locked        bool
	RefCount      int
}

func buildAnchorJudgePrompt(r anchorJudgeRow, ent *wikidata.Entity) string {
	var b strings.Builder
	b.WriteString("한국 고유명사 DB 의 한 항목에 위키데이터 항목이 근거(앵커)로 붙어 있는데, 둘의 종류가 어긋난다.\n")
	b.WriteString("둘이 **같은 대상**인지 가려라.\n\n")
	b.WriteString("[우리 항목]\n")
	b.WriteString("이름(ko): " + r.KO + "\n유형: " + r.EntityType + "\n")
	if r.EN != "" {
		b.WriteString("영문 표기: " + r.EN + "\n")
	}
	if r.JA != "" {
		b.WriteString("일본어 표기: " + r.JA + "\n")
	}
	if r.ZH != "" {
		b.WriteString("중국어 표기: " + r.ZH + "\n")
	}
	if n := strings.TrimSpace(r.Notes); n != "" {
		b.WriteString("메모(소비자가 기사에서 보낸 유형 힌트 등): " + truncRunes(n, 300) + "\n")
	}
	b.WriteString("\n[위키데이터 항목 " + r.QID + "]\n")
	b.WriteString("ko 라벨: " + ent.Labels["ko"] + "\n")
	b.WriteString("한국어 위키백과 문서 제목: " + ent.SiteTitles["kowiki"] + "\n")
	b.WriteString("영문 라벨: " + ent.Labels["en"] + "\n")
	b.WriteString("영문 설명: " + ent.Descriptions["en"] + "\n")
	if d := ent.Descriptions["ko"]; d != "" {
		b.WriteString("한국어 설명: " + d + "\n")
	}
	b.WriteString("감사가 본 어긋남: " + r.Verdict + "\n\n")
	b.WriteString("판정:\n")
	b.WriteString("- anchor_wrong: 위키데이터 항목이 **다른 대상**이다(이름만 같은 동명이인·동명 작품·일반 낱말). actual_type 은 빈칸.\n")
	b.WriteString("- type_wrong: **같은 대상**인데 우리 유형이 틀렸다. actual_type 에 맞는 유형을 적는다.\n")
	b.WriteString("  (person=실존 인물, character=가상 인물, group=그룹·팀, song_album=곡·음반, drama/movie/show=작품, company=기업, agency=기획사·레이블 …)\n")
	b.WriteString("- unclear: 근거로 가를 수 없다. 억지로 고르지 마라 — 틀린 판정은 서빙 값을 망친다.\n")
	b.WriteString("주의: 사람 이름·흔한 낱말은 동명이 많다. 우리 표기(영문·일본어)가 그 위키데이터 항목에서 온 값일 수 있으니, 표기 일치만으로 같은 대상이라 하지 마라.\n")
	b.WriteString("reason 은 한 줄.\n")
	return b.String()
}

// DrainAnchorJudge — 저장된 어긋남 판정을 GPT 로 가려 집행한다.
func DrainAnchorJudge(ctx context.Context, pool *pgxpool.Pool, cl *wikidata.Client, limit int, dry bool) AnchorJudgeResult {
	var res AnchorJudgeResult
	if pool == nil || cl == nil || limit <= 0 {
		return res
	}
	stored, err := StoredAnchorVerdicts(ctx, pool, limit)
	if err != nil {
		log.Printf("kdb.anchor-judge: 선정 실패: %v", err)
		return res
	}
	runner := codexcli.NewRunner().
		WithProvider(codexcli.RoleProvider("ANCHORJUDGE", "codex")).
		WithEffort(codexcli.RoleEffort("ANCHORJUDGE", "medium"))
	schema := anchorJudgeSchema()
	assignable := map[string]bool{}
	for _, t := range AssignableEntityTypes() {
		assignable[t] = true
	}

	for _, m := range stored {
		if ctx.Err() != nil {
			break
		}
		if m.Verdict == AnchorNameElement {
			continue // anchor-enforce 의 몫
		}
		r := anchorJudgeRow{PersonAnchorMismatch: m}
		if err := pool.QueryRow(ctx, `
SELECT COALESCE(e.canonical_en,''), COALESCE(e.canonical_zh,''), COALESCE(e.notes,''), e.operator_locked,
       (SELECT count(*) FROM kwave_entity_external_refs x WHERE x.entity_id = e.id AND x.provider = 'wikidata')
  FROM kwave_entities e WHERE e.id = $1`, m.ID).Scan(&r.EN, &r.ZH, &r.Notes, &r.Locked, &r.RefCount); err != nil {
			res.Skipped++
			continue
		}
		if r.Locked {
			res.Skipped++
			continue
		}
		ent, ferr := cl.Fetch(ctx, m.QID)
		if ferr != nil || ent == nil {
			res.Skipped++
			continue
		}
		res.Checked++
		raw, by, rerr := runner.RunP(ctx, buildAnchorJudgePrompt(r, ent), schema)
		if rerr != nil {
			res.Skipped++
			log.Printf("  [err] %s: %v", m.KO, rerr)
			continue
		}
		if !strings.HasPrefix(by, "codex(") {
			res.NotGPT++ // gemma 로 내려간 답으로는 집행하지 않는다
			continue
		}
		var a anchorJudgeAnswer
		if json.Unmarshal(raw, &a) != nil {
			res.Skipped++
			continue
		}
		line := fmt.Sprintf("%s [%s] %s %q → %s %s — %s", m.KO, m.EntityType, m.QID, ent.Descriptions["en"], a.Verdict, a.ActualType, truncRunes(a.Reason, 80))
		if len(res.Samples) < 80 {
			res.Samples = append(res.Samples, line)
		}
		applyAnchorVerdict(ctx, pool, &res, m, r.RefCount, ent.InstanceOf, a, by, dry, assignable)
	}
	return res
}

// applyAnchorVerdict — 판정 하나를 집행한다. anchor-judge(GPT)와 anchor-apply(검토된 파일)가
// **같은 가드·같은 스냅샷**을 쓰게 한 자리에 둔다.
func applyAnchorVerdict(ctx context.Context, pool *pgxpool.Pool, res *AnchorJudgeResult, m PersonAnchorMismatch,
	refCount int, p31 []string, a anchorJudgeAnswer, by string, dry bool, assignable map[string]bool) {
	switch a.Verdict {
	case "anchor_wrong":
		if refCount != 1 {
			res.Skipped++
			return
		}
		res.AnchorWrong++
		cells, err := anchorSourcedCells(ctx, pool, m.ID)
		if err != nil {
			return
		}
		res.CellsCleared += len(cells)
		if dry {
			return
		}
		// 비우기 전에 원값을 남긴다 — 되돌릴 수 있어야 한다.
		for _, c := range cells {
			_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_dataqa_log (entity_id, locale, old_value, old_source, verdict, reason, model)
SELECT id, $2, `+c+`, COALESCE(`+c+`_source,''), 'anchor-judge-clear', $3, $4 FROM kwave_entities WHERE id = $1`,
				m.ID, strings.TrimPrefix(c, "canonical_"), truncRunes(m.QID+" "+a.Reason, 200), by)
		}
		if _, err := withdrawOneAnchor(ctx, pool, m, cells); err != nil {
			log.Printf("kdb.anchor-judge: %s 철회 실패: %v", m.KO, err)
		}
	case "type_wrong":
		want := strings.TrimSpace(a.ActualType)
		if !assignable[want] || want == m.EntityType {
			res.Unclear++
			return
		}
		// 앵커의 종류가 새 유형을 허용하는지 P31 로 확인한다(모르면 옮기지 않는다).
		ok := false
		for _, q := range p31 {
			if al, kn := AnchorTypeAllowed(q, want); kn && al {
				ok = true
				break
			}
		}
		if !ok {
			res.Unclear++
			return
		}
		res.TypeWrong++
		if dry {
			return
		}
		_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_dataqa_log (entity_id, locale, old_value, old_source, verdict, reason, model)
VALUES ($1, 'entity_type', $2, '', 'retrace-type-fix', $3, $4)`,
			m.ID, m.EntityType, truncRunes(m.QID+" "+a.Reason, 200), by)
		_, _ = pool.Exec(ctx, `
UPDATE kwave_entities SET entity_type = $2::kwave_entity_type, updated_at = now(),
       notes = COALESCE(NULLIF(notes,'') || ' ','') || $4
 WHERE id = $1 AND entity_type::text = $3 AND operator_locked = false`,
			m.ID, want, m.EntityType, "[retrace:type-fix "+m.EntityType+"→"+want+"] anchor-judge "+m.QID)
	default:
		res.Unclear++
	}
}

// AnchorDecision — 검토를 거친 판정 한 줄(파일에서 읽는다).
type AnchorDecision struct {
	EntityID, QID, Verdict, ActualType, Reason string
}

// ApplyAnchorDecisions — 사람이(또는 Claude 가) 검토한 판정 파일을 집행한다.
// 대상이 여전히 «저장된 어긋남»이고 운영자 잠금이 아닐 때만 손댄다.
func ApplyAnchorDecisions(ctx context.Context, pool *pgxpool.Pool, cl *wikidata.Client, decs []AnchorDecision, by string, dry bool) AnchorJudgeResult {
	var res AnchorJudgeResult
	if pool == nil || cl == nil {
		return res
	}
	assignable := map[string]bool{}
	for _, t := range AssignableEntityTypes() {
		assignable[t] = true
	}
	for _, d := range decs {
		if ctx.Err() != nil {
			break
		}
		var m PersonAnchorMismatch
		var locked bool
		var refCount int
		err := pool.QueryRow(ctx, `
SELECT e.id::text, e.canonical_ko, e.entity_type::text, a.external_id, a.verdict, COALESCE(a.description,''),
       e.operator_locked,
       (SELECT count(*) FROM kwave_entity_external_refs x WHERE x.entity_id = e.id AND x.provider = 'wikidata')
  FROM kwave_entities e
  JOIN kwave_kdb_anchor_audit a ON a.entity_id = e.id AND a.external_id = $2 AND a.verdict <> ''
       AND a.entity_type = e.entity_type::text
 WHERE e.id = $1 AND e.status = 'active'
   AND EXISTS (SELECT 1 FROM kwave_entity_external_refs x WHERE x.entity_id = e.id
                AND x.provider = 'wikidata' AND x.external_id = $2)`, d.EntityID, d.QID).
			Scan(&m.ID, &m.KO, &m.EntityType, &m.QID, &m.Verdict, &m.Desc, &locked, &refCount)
		if err != nil || locked {
			res.Skipped++ // 이미 고쳐졌거나 유형이 바뀌었거나 잠겼다
			continue
		}
		ent, ferr := cl.Fetch(ctx, m.QID)
		if ferr != nil || ent == nil {
			res.Skipped++
			continue
		}
		res.Checked++
		applyAnchorVerdict(ctx, pool, &res, m, refCount, ent.InstanceOf,
			anchorJudgeAnswer{Verdict: d.Verdict, ActualType: d.ActualType, Reason: d.Reason}, by, dry, assignable)
	}
	return res
}
