package kdb

// demand_register — **소비자가 물었는데 답하지 못한 이름**을 판정 파일대로 처리한다.
//
// ★계기 (2026-09-24). 최근 7일 prepare 로 들어온 이름 4,336개 중 1,416개(33%)가 지금도
//   조회되지 않았다: candidate 로 멈춘 761 · 기각된 194 · 원장에 아예 없는 461.
//   발굴 큐는 대부분 «review» 로 끝났는데(근거 부족 210 · 유형 단서 부족 83 …) 그 검수를
//   할 사람이 없었다. 소비자는 답을 못 받고, 같은 이름을 다시 보낸다.
//
//   오너 지시: "대답 못하고 무시하면 어떻게 하자고 — 필요한 것은 등록해야지."
//
// ★판정은 사람이(또는 Claude 가) 한다. 이 파일은 **집행만** 한다 — 같은 가드와 스냅샷으로.
//   register  원장에 없는 이름 → active 로 만든다(표기는 잠정 llm-provisional)
//   promote   candidate → active
//   reopen    rejected → active(잘못 기각된 것)
//   reject    candidate 인데 고유명사가 아님 → rejected + [rk:common-noun]
//   foreign · skip  아무것도 하지 않는다(해외는 out_of_scope 가 이미 답이다)
//   상태를 바꿀 때마다 원래 상태를 dataqa_log 에 적는다. 운영자 잠금은 건드리지 않는다.

import (
	"context"
	"log"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RegisterDecision — 판정 한 줄.
type RegisterDecision struct {
	Ko, Action, Type, EN, JA, ZH, ZHHant, Reason string
}

// RegisterResult — 집행 결과.
type RegisterResult struct {
	Registered, Promoted, Reopened, Rejected, Skipped, Retyped, Cells int
}

var registerLocales = []struct{ col, loc string }{
	{"canonical_en", "en"}, {"canonical_ja", "ja"}, {"canonical_zh", "zh"}, {"canonical_zh_hant", "zh_hant"},
}

func (d RegisterDecision) value(loc string) string {
	switch loc {
	case "en":
		return strings.TrimSpace(d.EN)
	case "ja":
		return strings.TrimSpace(d.JA)
	case "zh":
		return strings.TrimSpace(d.ZH)
	case "zh_hant":
		return strings.TrimSpace(d.ZHHant)
	}
	return ""
}

// validLocaleValue — 서빙 가드와 같은 문자셋 규칙 + 간체·번체 칸의 글자체.
func validLocaleValue(loc, v string) bool {
	if v == "" || !IsValidSpellingForLocale(loc, v) {
		return false
	}
	if loc == "zh" && ContainsTradOnly(v) {
		return false
	}
	if loc == "zh_hant" && ContainsHansOnly(v) {
		return false
	}
	return true
}

// ApplyRegisterDecisions — 판정 파일을 집행한다. dry 면 세기만 한다.
func ApplyRegisterDecisions(ctx context.Context, pool *pgxpool.Pool, decs []RegisterDecision, by string, dry bool) RegisterResult {
	var r RegisterResult
	assignable := map[string]bool{}
	for _, t := range AssignableEntityTypes() {
		assignable[t] = true
	}
	for _, d := range decs {
		if ctx.Err() != nil {
			break
		}
		ko := strings.TrimSpace(d.Ko)
		act := strings.TrimSpace(d.Action)
		typ := strings.TrimSpace(d.Type)
		if ko == "" {
			r.Skipped++
			continue
		}
		// 지금 원장에 무엇이 있나(이름 정확일치). 여럿이면 candidate → rejected 순으로 하나.
		var id, status, curType string
		var locked bool
		_ = pool.QueryRow(ctx, `
SELECT id::text, status::text, entity_type::text, operator_locked FROM kwave_entities
 WHERE canonical_ko = $1 ORDER BY (status='active') DESC, (status='candidate') DESC, updated_at DESC LIMIT 1`, ko).
			Scan(&id, &status, &curType, &locked)
		if status == "active" || locked {
			r.Skipped++ // 이미 답하고 있거나 잠겼다
			continue
		}
		switch act {
		case "register", "promote", "reopen":
			if !assignable[typ] {
				r.Skipped++
				continue
			}
			if id == "" {
				// ★이미 어떤 대상의 별칭이면 새 UUID 를 만들지 않는다(I13) — 그 대상이 답한다.
				if claims, err := ClaimsForName(ctx, pool, ko); err != nil {
					r.Skipped++
					continue
				} else if _, owned := SoleAliasOwner(claims); owned {
					r.Skipped++
					continue
				}
				r.Registered++
				if dry {
					continue
				}
				if err := pool.QueryRow(ctx, `
INSERT INTO kwave_entities (canonical_ko, entity_type, status, confidence, notes, created_at, updated_at, `+NoLocaleSourceCols+`)
VALUES ($1, $2::kwave_entity_type, 'active', 0.70, $3, now(), now(), `+NoLocaleSourceVals+`)
RETURNING id::text`, ko, typ, "[claude-register] "+truncRunes(d.Reason, 120)).Scan(&id); err != nil {
					log.Printf("kdb.demand-register: %s 등록 실패: %v", ko, err)
					r.Registered--
					r.Skipped++
					continue
				}
			} else {
				if status == "candidate" {
					r.Promoted++
				} else {
					r.Reopened++
				}
				if dry {
					continue
				}
				_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_dataqa_log (entity_id, locale, old_value, old_source, verdict, reason, model)
VALUES ($1, 'status', $2, '', 'claude-register', $3, $4)`, id, status, truncRunes(act+": "+d.Reason, 200), by)
				_, _ = pool.Exec(ctx, `
UPDATE kwave_entities SET status = 'active', confidence = GREATEST(confidence, 0.70), updated_at = now(),
       notes = COALESCE(NULLIF(notes,'') || ' ','') || $2
 WHERE id = $1 AND status::text = $3 AND operator_locked = false`,
					id, "[claude-"+act+"] "+truncRunes(d.Reason, 120), status)
				if typ != curType {
					_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_dataqa_log (entity_id, locale, old_value, old_source, verdict, reason, model)
VALUES ($1, 'entity_type', $2, '', 'retrace-type-fix', $3, $4)`, id, curType, truncRunes(d.Reason, 200), by)
					if tag, err := pool.Exec(ctx, `
UPDATE kwave_entities SET entity_type = $2::kwave_entity_type, updated_at = now()
 WHERE id = $1 AND entity_type::text = $3 AND operator_locked = false`, id, typ, curType); err == nil && tag.RowsAffected() > 0 {
						r.Retyped++
					}
				}
			}
			// 표기 — 빈 칸에만, 잠정으로.
			for _, c := range registerLocales {
				v := d.value(c.loc)
				if !validLocaleValue(c.loc, v) {
					continue
				}
				tag, err := pool.Exec(ctx, `UPDATE kwave_entities SET `+c.col+` = $2, `+c.col+`_source = 'llm-provisional', updated_at = now()
 WHERE id = $1 AND COALESCE(`+c.col+`,'') = ''`, id, v)
				if err == nil && tag.RowsAffected() > 0 {
					r.Cells++
					_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_dataqa_log (entity_id, locale, old_value, old_source, verdict, reason, model)
VALUES ($1, $2, '', '', 'claude-provisional-fill', $3, $4)`, id, c.loc, "value="+v, by)
				}
			}
		case "reject":
			if status != "candidate" {
				r.Skipped++ // 원장에 없거나 이미 기각 — 할 일이 없다
				continue
			}
			r.Rejected++
			if dry {
				continue
			}
			_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_dataqa_log (entity_id, locale, old_value, old_source, verdict, reason, model)
VALUES ($1, 'status', 'candidate', '', 'claude-reject', $2, $3)`, id, truncRunes(d.Reason, 200), by)
			_, _ = pool.Exec(ctx, `
UPDATE kwave_entities SET status = 'rejected', updated_at = now(),
       notes = COALESCE(NULLIF(notes,'') || ' ','') || $2
 WHERE id = $1 AND status = 'candidate' AND operator_locked = false`,
				id, "[claude-reject] "+truncRunes(d.Reason, 120)+" "+ReasonKindTag(ReasonKindCommonNoun))
		default:
			r.Skipped++
		}
	}
	return r
}
