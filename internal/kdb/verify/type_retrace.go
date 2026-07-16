package verify

// type_retrace.go — 타입 힌트 오염 역추적 배치 (오너 승인 2026-07-16).
//
// 실측 문제: 소비자 요청의 잘못된 requested_entity_type 이 자동승인(auto_evidence)
// 경로에서 검증 없이 엔티티 타입으로 굳는다 — 가수 박학기·이승재가 song_album 으로,
// 그룹 유리상자가 person 으로, 개그맨 문천식이 character 로 active 서빙됨.
//
// 이 배치는 auto_evidence 로 생성된 엔티티를 이름만으로(저장 타입 미주입 = 무편향)
// 뉴스 재검색하고, gemma 가 기사 맥락으로 실제 유형을 재판정한다:
//   실제 유형 = 저장 유형        → 확인(변경 없음)
//   실제 유형 ≠ 저장 유형(구체)  → entity_type 교정 + dataqa_log 스냅샷(revert 가능)
//   K-엔티티 아님                → 기본 리뷰 플래그(오거부 방지 — EvidencePass 와 동일 정책,
//                                  KDB_VERIFY_AUTO_REJECT=1 시에만 tombstone)
//
// 네이버 쿼터: 엔티티당 news 1콜 — n 으로 캡. CLI: `kdb-app type-retrace [n]`.

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb/agents"
	"github.com/rickyjoo73/kdb/internal/kdb/codexcli"
	"github.com/rickyjoo73/kdb/internal/kdb/naver"
	"github.com/rickyjoo73/kdb/internal/kdb/websearch"
)

// typeRetraceTypes — gemma 가 고를 수 있는 유형. entity_type enum 과 동기.
var typeRetraceTypes = map[string]bool{
	"person": true, "group": true, "drama": true, "movie": true, "show": true,
	"song_album": true, "agency": true, "channel_outlet": true, "event_tour": true,
	"brand_place": true, "character": true,
}

type typeRetraceResult struct {
	Verdict    string `json:"verdict"`     // real | contaminated | unclear
	ActualType string `json:"actual_type"` // typeRetraceTypes 중 하나 (real 일 때)
	Identity   string `json:"identity"`
	Reason     string `json:"reason"`
}

var typeRetraceSchema = []byte(`{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "verdict": {"type": "string", "enum": ["real", "contaminated", "unclear"]},
    "actual_type": {"type": "string", "enum": ["person","group","drama","movie","show","song_album","agency","channel_outlet","event_tour","brand_place","character",""]},
    "identity": {"type": "string"},
    "reason": {"type": "string"}
  },
  "required": ["verdict"]
}`)

func newTypeRetraceJudge() *agents.Base {
	r := codexcli.NewRunner().
		WithProvider(codexcli.RoleProvider("VERIFY", "gemma")).
		WithEffort(codexcli.RoleEffort("VERIFY", "low"))
	return agents.NewBase(r, agents.LLMRole{
		Role:   agents.Role("TypeRetrace"),
		Schema: typeRetraceSchema,
		BuildPrompt: func(in any) (string, error) {
			vi, ok := in.(verifyInput)
			if !ok {
				return "", fmt.Errorf("type-retrace: bad input")
			}
			return buildTypeRetracePrompt(vi), nil
		},
	})
}

func buildTypeRetracePrompt(vi verifyInput) string {
	e := vi.e
	var b strings.Builder
	b.WriteString("당신은 한국 대중문화(K-콘텐츠) 고유명사의 '유형 판별기'입니다.\n")
	b.WriteString("아래 이름의 실제 유형을 뉴스 기사 맥락으로 판별하세요. 저장된 유형은 소비자 힌트라 틀렸을 수 있습니다.\n\n")
	b.WriteString("이름: " + e.ko + "\n")
	b.WriteString("저장된 유형(의심): " + e.etype + "\n")
	b.WriteString("\n검색된 뉴스(제목 — 스니펫):\n")
	for i, h := range vi.hits {
		if i >= 8 {
			break
		}
		b.WriteString("- " + h + "\n")
	}
	b.WriteString("\n판별 규칙:\n")
	b.WriteString("1. verdict=real: 실존하는 한국 대중문화 엔티티다. actual_type=기사가 보여주는 실제 유형 — person(실존 인물: 가수·배우·개그맨·PD 등), group(그룹·팀·단체), drama, movie, show(예능·방송프로그램), song_album(곡·앨범 제목), agency(기획사·제작사), channel_outlet(채널·매체), event_tour(공연·시상식·행사), brand_place(브랜드·장소), character(가상 인물·캐릭터). 저장 유형과 같으면 그대로 적는다.\n")
	b.WriteString("2. ★혼동 주의: 사람 이름(가수·배우·개그맨)은 그의 곡/작품이 아니라 person 이다. 실존 인물은 character(가상 캐릭터)가 아니라 person 이다. 여러 명이 뭉친 팀은 group.\n")
	b.WriteString("3. verdict=contaminated: 한국 대중문화 엔티티가 아예 아님(해외 인물, 일반 단어, 광고·상품, 무관 분야). identity=실제 정체.\n")
	b.WriteString("4. verdict=unclear: 근거 부족·동명이인 뒤섞임 — 억지 추론 금지. actual_type 은 확실할 때만.\n")
	b.WriteString("5. reason=근거 한 줄(어느 스니펫인지).\n")
	b.WriteString("JSON 한 개만: {\"verdict\":\"...\",\"actual_type\":\"...\",\"identity\":\"...\",\"reason\":\"...\"}\n")
	return b.String()
}

// gatherEvidenceBare — 이름만으로 무편향 검색(저장 타입의 roleWord 를 주입하지 않는다).
func gatherEvidenceBare(ctx context.Context, nv *naver.Client, ko string) []string {
	var hits []string
	if res, err := nv.Search(ctx, "news", ko, 5); err == nil {
		for _, it := range res.Items {
			line := strings.TrimSpace(stripTagsLocal(it.Title) + " — " + stripTagsLocal(it.Description))
			if line != "" && line != "—" {
				hits = append(hits, line)
			}
		}
	}
	if len(hits) < 2 {
		if res, _, err := websearch.Default().Search(ctx, ko, "ko", 6); err == nil {
			for _, r := range res {
				line := strings.TrimSpace(r.Title + " — " + r.Snippet)
				if line != "" && line != "—" {
					hits = append(hits, line)
				}
			}
		}
	}
	if len(hits) > 8 {
		hits = hits[:8]
	}
	return hits
}

// TypeRetracePass — auto_evidence 경로 생성 엔티티 n 건의 타입을 재판정·교정한다.
// 반환: (교정, 확인, 오염플래그, 처리수).
func TypeRetracePass(ctx context.Context, pool *pgxpool.Pool, n int) (fixed, confirmed, flagged, processed int, err error) {
	nv, err := naver.NewFromSettings(ctx, pool)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("type-retrace: %w", err)
	}
	judge := newTypeRetraceJudge()

	// 대상: auto_evidence 승인으로 생긴 active/candidate. 최근 것부터(오염 유입이 최근일수록
	// 소비자 힌트 의존이 컸다). 이미 처리한 것([retrace:type]) 제외. operator_locked 제외.
	rows, qerr := pool.Query(ctx, `
		SELECT DISTINCT ON (e.id) e.id::text, e.canonical_ko, e.entity_type::text
		  FROM kwave_entities e
		  JOIN kwave_entity_research_queue q
		    ON q.entity_ko = e.canonical_ko AND q.precheck_reason LIKE 'auto_evidence%'
		 WHERE e.status IN ('active','candidate') AND e.operator_locked = false
		   AND COALESCE(e.notes,'') NOT LIKE '%[retrace:type%'
		   AND COALESCE(e.notes,'') NOT LIKE '%[retrace:review]%'
		 ORDER BY e.id, e.created_at DESC
		 LIMIT $1`, n)
	if qerr != nil {
		return 0, 0, 0, 0, fmt.Errorf("type-retrace query: %w", qerr)
	}
	var ents []evEntity
	for rows.Next() {
		var e evEntity
		if rows.Scan(&e.id, &e.ko, &e.etype) == nil {
			ents = append(ents, e)
		}
	}
	rows.Close()

	log.Printf("kdb.verify.type-retrace: start (targets=%d)", len(ents))
	for _, e := range ents {
		if ctx.Err() != nil {
			break
		}
		processed++
		hits := gatherEvidenceBare(ctx, nv, e.ko)
		if len(hits) < 2 {
			continue // 근거 부족 — 판정 보류
		}
		var res typeRetraceResult
		if jerr := judge.CallJSON(ctx, verifyInput{e: e, hits: hits}, &res); jerr != nil {
			log.Printf("  [err] %s (%s): %v", e.ko, e.etype, jerr)
			continue
		}
		actual := strings.TrimSpace(strings.ToLower(res.ActualType))
		switch res.Verdict {
		case "real":
			if !typeRetraceTypes[actual] || actual == e.etype {
				confirmed++
				markTypeChecked(ctx, pool, e.id)
				continue
			}
			// 타입 교정 — dataqa_log 스냅샷으로 revert 가능하게.
			detail := strings.TrimSpace(res.Identity + " / " + res.Reason)
			_, _ = pool.Exec(ctx, `
				INSERT INTO kwave_kdb_dataqa_log (entity_id, locale, old_value, old_source, verdict, reason, model)
				VALUES ($1, 'entity_type', $2, '', 'retrace-type-fix', $3, 'gemma')`,
				e.id, e.etype, truncateRunes(detail, 200))
			note := "[retrace:type-fix " + e.etype + "→" + actual + "] " + truncateRunes(detail, 60)
			tag, uerr := pool.Exec(ctx, `
				UPDATE kwave_entities
				   SET entity_type=$2::kwave_entity_type,
				       notes = CASE WHEN COALESCE(notes,'')='' THEN $3 ELSE notes || ' ' || $3 END,
				       updated_at=now()
				 WHERE id=$1 AND entity_type=$4::kwave_entity_type`, e.id, actual, note, e.etype)
			if uerr == nil && tag.RowsAffected() > 0 {
				fixed++
				log.Printf("  [type-fix] %s: %s → %s (%s)", e.ko, e.etype, actual, truncateRunes(detail, 55))
			}
		case "contaminated":
			// EvidencePass 와 동일 정책: 기본은 리뷰 플래그(오거부 방지), 하드기각은 opt-in.
			if os.Getenv("KDB_VERIFY_AUTO_REJECT") == "1" {
				if rejectContaminated(ctx, pool, e.id, res.Identity, res.Reason) {
					flagged++
					log.Printf("  [contam→reject] %s (%s) — %s", e.ko, e.etype, truncateRunes(res.Identity, 55))
				}
			} else if flagContamReview(ctx, pool, e.id, res.Identity, res.Reason) {
				flagged++
				log.Printf("  [contam→review] %s (%s) — %s", e.ko, e.etype, truncateRunes(res.Identity, 55))
			}
		default:
			// unclear — 유지.
		}
	}
	log.Printf("kdb.verify.type-retrace: done fixed=%d confirmed=%d flagged=%d /%d", fixed, confirmed, flagged, processed)
	return fixed, confirmed, flagged, processed, nil
}

// markTypeChecked — 확인 완료 태그(재처리·쿼터 재소모 방지).
func markTypeChecked(ctx context.Context, pool *pgxpool.Pool, id string) {
	_, _ = pool.Exec(ctx, `
		UPDATE kwave_entities
		   SET notes = CASE WHEN COALESCE(notes,'')='' THEN $2 ELSE notes || ' ' || $2 END
		 WHERE id=$1 AND COALESCE(notes,'') NOT LIKE '%[retrace:type-ok]%'`,
		id, "[retrace:type-ok]")
}
