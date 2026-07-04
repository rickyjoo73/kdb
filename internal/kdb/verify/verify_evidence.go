package verify

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb/agents"
	"github.com/rickyjoo73/kdb/internal/kdb/codexcli"
	"github.com/rickyjoo73/kdb/internal/kdb/naver"
	"github.com/rickyjoo73/kdb/internal/kdb/websearch"
)

// EvidencePass — unverified 상위 n 개를 검색근거(네이버 news + SearXNG 폴백) + gemma 판정으로
// evidenced 로 업그레이드 시도한다(handoff 27차 §6 증분2 tier2). 권위앵커가 없는 롱테일 전용.
//
// ★설계 근거(실측): encyc 단독 역할토큰 매칭은 노이지(아이유→"국제단위" 등) → encyc 대신
// news(+역할어)를 "증거"로 모아 gemma 가 실재성/동일성을 판정한다(판정 주체=gemma, 네이버=조연).
// 쿼터(1,000/일) 캡 = n(엔티티당 news 1콜). 결정론 스윕이 강등하지 못하게 evidence='search+gemma%'.
//
// 반환: (real 승급, contam 의심 플래그, processed, err).
func EvidencePass(ctx context.Context, pool *pgxpool.Pool, n int) (int, int, int, error) {
	nv, err := naver.New()
	if err != nil {
		return 0, 0, 0, fmt.Errorf("verify evidence: %w", err)
	}
	judge := newVerifyJudge()

	// unverified 우선순위: confidence DESC(임계 0.75 에 근접할수록 실재 가능성↑) → 최근 갱신.
	rows, err := pool.Query(ctx, `
		SELECT e.id::text, e.canonical_ko, e.entity_type::text,
		       COALESCE(d.primary_role::text,''), COALESCE(d.notable_works,'{}'::text[])
		  FROM kwave_entities e
		  LEFT JOIN kwave_entity_person_details d ON d.entity_id = e.id
		 WHERE e.status='active' AND e.verification_tier='unverified' AND e.canonical_ko <> ''
		 ORDER BY e.confidence DESC, e.updated_at DESC
		 LIMIT $1`, n)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("verify evidence query: %w", err)
	}
	var ents []evEntity
	for rows.Next() {
		var e evEntity
		if err := rows.Scan(&e.id, &e.ko, &e.etype, &e.role, &e.works); err != nil {
			rows.Close()
			return 0, 0, 0, err
		}
		ents = append(ents, e)
	}
	rows.Close()

	log.Printf("kdb.verify.evidence: start (unverified=%d)", len(ents))
	upgraded, flagged, processed := 0, 0, 0
	for _, e := range ents {
		processed++
		hits := gatherEvidence(ctx, nv, e)
		if len(hits) < 2 {
			continue // 근거 부족 — 판정 보류(다음 라운드/운영자 검토)
		}
		v, err := judgeVerify(ctx, judge, e, hits)
		if err != nil {
			log.Printf("  [err] %s (%s): %v", e.ko, e.etype, err)
			continue
		}
		reason := strings.TrimSpace(v.Reason)
		identity := strings.TrimSpace(v.Identity)
		switch v.Verdict {
		case "real":
			// 기사 맥락으로 실존 확정 → evidenced 승급 + 특정된 정체를 근거에 기록.
			ev := "search+gemma"
			if identity != "" {
				ev = "search+gemma: " + truncateRunes(identity, 70)
			} else if reason != "" {
				ev = "search+gemma: " + truncateRunes(reason, 70)
			}
			// unverified 인 동안만 승급(그새 권위앵커 붙었으면 결정론 스윕이 이미 올림 → 덮지 않음).
			tag, uerr := pool.Exec(ctx, `
				UPDATE kwave_entities
				   SET verification_tier='evidenced', verification_evidence=$2, verified_tier_at=now()
				 WHERE id=$1 AND verification_tier='unverified'`, e.id, ev)
			if uerr == nil && tag.RowsAffected() > 0 {
				upgraded++
				log.Printf("  [real] %s (%s) → %s", e.ko, e.etype, ev)
			}
		case "contaminated":
			// 기사 맥락이 다른 정체를 보임/대표작 모순/K-아님 → 자동 기각(오너 지시). 완전 삭제가
			// 아니라 rejected tombstone(서빙 제거) + dataqa_log revert 스냅샷 → 오판 시 복원 가능.
			if rejectContaminated(ctx, pool, e.id, identity, reason) {
				flagged++
				log.Printf("  [contam→reject] %s (%s) — 기사는 %q: %s", e.ko, e.etype, identity, truncateRunes(reason, 55))
			}
		default: // unclear
			// 유지 — 근거 부족. 다음 라운드나 운영자 검토.
		}
	}
	log.Printf("kdb.verify.evidence: done real=%d rejected=%d /%d", upgraded, flagged, processed)
	return upgraded, flagged, processed, nil
}

// rejectContaminated — 기사 맥락이 명확한 오염(다른 정체/대표작 모순/K-아님)으로 판정한 엔티티를
// 자동 기각한다(오너 지시). ★완전 삭제(DELETE) 아님 — status='rejected' tombstone 으로 서빙에서만
// 제거하고, kwave_kdb_dataqa_log 에 이전 상태를 스냅샷(locale='status', verdict='retrace-contam-reject')
// 해 gemma 오판 시 복원 가능. active 인 동안만(멱등). revert: 그 로그로 status='active' 되돌림.
func rejectContaminated(ctx context.Context, pool *pgxpool.Pool, id, identity, reason string) bool {
	var curStatus, curTier string
	if err := pool.QueryRow(ctx,
		`SELECT status, COALESCE(verification_tier,'') FROM kwave_entities WHERE id=$1`, id).
		Scan(&curStatus, &curTier); err != nil || curStatus != "active" {
		return false
	}
	detail := strings.TrimSpace(identity + " / " + reason)
	// 1) revert 스냅샷.
	_, _ = pool.Exec(ctx, `
		INSERT INTO kwave_kdb_dataqa_log (entity_id, locale, old_value, old_source, verdict, reason, model)
		VALUES ($1, 'status', $2, $3, 'retrace-contam-reject', $4, 'gemma')`,
		id, curStatus, curTier, truncateRunes(detail, 200))
	// 2) 기각 + notes 사유(멱등).
	note := "[retrace:contam-reject] 기사=" + truncateRunes(detail, 80)
	tag, err := pool.Exec(ctx, `
		UPDATE kwave_entities
		   SET status='rejected',
		       notes = CASE WHEN COALESCE(notes,'')='' THEN $2 ELSE notes || ' ' || $2 END,
		       updated_at=now()
		 WHERE id=$1 AND status='active'`, id, note)
	return err == nil && tag.RowsAffected() > 0
}

// RevertContamRejects — 역추적 자동기각을 되돌린다(gemma 오판 복구). dataqa_log 의
// retrace-contam-reject(미revert) 항목을 찾아 엔티티 status 를 이전값(active)으로 복원하고
// 로그를 revert 처리. 반환=복원 수.
func RevertContamRejects(ctx context.Context, pool *pgxpool.Pool, n int) (int, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, entity_id::text, old_value FROM kwave_kdb_dataqa_log
		 WHERE verdict='retrace-contam-reject' AND reverted_at IS NULL
		 ORDER BY created_at DESC LIMIT $1`, n)
	if err != nil {
		return 0, err
	}
	type logRow struct {
		logID    int64
		entityID string
		oldVal   string
	}
	var logs []logRow
	for rows.Next() {
		var lr logRow
		if err := rows.Scan(&lr.logID, &lr.entityID, &lr.oldVal); err != nil {
			rows.Close()
			return 0, err
		}
		logs = append(logs, lr)
	}
	rows.Close()
	restored := 0
	for _, lr := range logs {
		tag, err := pool.Exec(ctx, `
			UPDATE kwave_entities SET status=$2, updated_at=now()
			 WHERE id=$1 AND status='rejected'`, lr.entityID, lr.oldVal)
		if err == nil && tag.RowsAffected() > 0 {
			restored++
		}
		_, _ = pool.Exec(ctx, `UPDATE kwave_kdb_dataqa_log SET reverted_at=now() WHERE id=$1`, lr.logID)
	}
	log.Printf("kdb.verify.revert: restored=%d/%d", restored, len(logs))
	return restored, nil
}

type evEntity struct {
	id, ko, etype, role string
	works               []string
}

// gatherEvidence — 네이버 news(+역할어) 검색으로 증거 코퍼스(제목 — 스니펫 라인)를 모은다.
// 결과가 빈약하면 SearXNG 로 보강. 각 엔티티당 네이버 1콜(쿼터 절약).
func gatherEvidence(ctx context.Context, nv *naver.Client, e evEntity) []string {
	query := e.ko
	if w := roleWord(e.etype); w != "" {
		query = e.ko + " " + w
	}
	var hits []string
	if res, err := nv.Search(ctx, "news", query, 5); err == nil {
		for _, it := range res.Items {
			line := strings.TrimSpace(stripTagsLocal(it.Title) + " — " + stripTagsLocal(it.Description))
			if line != "" && line != "—" {
				hits = append(hits, line)
			}
		}
	}
	if len(hits) < 2 { // 네이버 빈약 → SearXNG 보강
		if res, _, err := websearch.Default().Search(ctx, query, "ko", 6); err == nil {
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

// roleWord — entity_type 별 news 쿼리에 덧붙일 역할/유형어(동명이의 잡음 억제). person 은
// 이름만으로 news 가 잘 나와 생략(직업이 다양). 나머지는 유형어로 특정.
func roleWord(etype string) string {
	switch etype {
	case "drama":
		return "드라마"
	case "movie":
		return "영화"
	case "show":
		return "예능"
	case "song_album":
		return "노래"
	case "group":
		return "그룹"
	case "agency":
		return "엔터테인먼트"
	case "channel_outlet":
		return "방송"
	case "event_tour":
		return "공연"
	case "brand_place":
		return "브랜드"
	}
	return ""
}

func stripTagsLocal(s string) string {
	s = strings.ReplaceAll(s, "<b>", "")
	s = strings.ReplaceAll(s, "</b>", "")
	return strings.TrimSpace(s)
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ── gemma 정체성 판정기 ────────────────────────────────────────────────────

// verifyResult — gemma 의 기사맥락 판별. verdict 3분기:
//   real         : 뉴스가 이 이름을 위 역할/대표작의 실존 K-엔티티로 뒷받침 → evidenced 승급.
//   contaminated : 뉴스가 다른 정체를 명확히 보이거나 저장된 대표작/역할과 모순 → 오염 의심 플래그.
//   unclear      : 근거 부족·무관뿐 → 유지(다음 라운드/운영자).
// identity = 특정된 정체(누구/무엇). 오너 방향: 단어가 아니라 기사 맥락으로 정체를 특정한다.
type verifyResult struct {
	Verdict  string `json:"verdict"`
	Identity string `json:"identity"`
	Reason   string `json:"reason"`
}

var verifyJudgeSchema = []byte(`{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "verdict": {"type": "string", "enum": ["real", "contaminated", "unclear"]},
    "identity": {"type": "string"},
    "reason": {"type": "string"}
  },
  "required": ["verdict"]
}`)

type verifyInput struct {
	e    evEntity
	hits []string
}

func newVerifyJudge() *agents.Base {
	r := codexcli.NewRunner().
		WithProvider(codexcli.RoleProvider("VERIFY", "gemma")).
		WithEffort(codexcli.RoleEffort("VERIFY", "low"))
	return agents.NewBase(r, agents.LLMRole{
		Role:   agents.Role("Verify"),
		Schema: verifyJudgeSchema,
		BuildPrompt: func(in any) (string, error) {
			vi, ok := in.(verifyInput)
			if !ok {
				return "", fmt.Errorf("verify: bad input")
			}
			return buildVerifyPrompt(vi), nil
		},
	})
}

func judgeVerify(ctx context.Context, judge *agents.Base, e evEntity, hits []string) (verifyResult, error) {
	var res verifyResult
	err := judge.CallJSON(ctx, verifyInput{e: e, hits: hits}, &res)
	return res, err
}

func buildVerifyPrompt(vi verifyInput) string {
	e := vi.e
	var b strings.Builder
	b.WriteString("당신은 한국 대중문화(K-콘텐츠) 고유명사의 '기사맥락 판별기'입니다.\n")
	b.WriteString("아래 한국 엔티티가 검색된 뉴스 기사에서 어떻게 확인되는지 맥락으로 판별하세요.\n\n")
	b.WriteString("한국어 정식명: " + e.ko + "\n")
	b.WriteString("종류: " + e.etype + "\n")
	if e.role != "" {
		b.WriteString("우리 DB의 역할/직업: " + e.role + "\n")
	}
	if len(e.works) > 0 {
		b.WriteString("우리 DB의 대표작/소속: " + strings.Join(e.works, ", ") + "\n")
	}
	b.WriteString("\n검색된 뉴스(제목 — 스니펫):\n")
	for i, h := range vi.hits {
		if i >= 8 {
			break
		}
		b.WriteString("- " + h + "\n")
	}
	b.WriteString("\n판별 규칙(기사 맥락 기준, 엄격):\n")
	b.WriteString("★핵심: '실존하는 한국 대중문화(K-콘텐츠) 엔티티인가'만 본다. 세부 역할/대표작이 우리 DB와 달라도 실존 K-엔티티면 real 이다(메타데이터는 나중에 보강). 기각(contaminated)은 아예 K-엔티티가 아닐 때만.\n")
	b.WriteString("1. verdict=real: 뉴스가 이 이름을 실존하는 한국 대중문화 " + e.etype + "(가수·배우·아이돌·그룹·작품 등)로 뒷받침. 우리 DB의 역할/작품과 세부가 달라도 실존 K-" + e.etype + "면 real. identity=기사로 특정한 정체(예: SF9 멤버, OO의 노래).\n")
	b.WriteString("2. verdict=contaminated: 이 이름이 '한국 대중문화 엔티티가 아예 아님' — 해외 인물, 일반 단어/명사, 의약품·스포츠·정치 등 무관 분야, 또는 저장된 종류(" + e.etype + ")가 근본적으로 틀림(예: 넷플릭스 다큐멘터리인데 노래·앨범으로 저장). identity=기사가 말하는 실제 정체.\n")
	b.WriteString("3. verdict=unclear: 근거 부족·무관 결과뿐·동명이인 뒤섞여 특정 불가. 억지 추론 금지 — 확실할 때만 real/contaminated.\n")
	b.WriteString("4. reason = 판별 근거 한국어 한 줄(어느 스니펫이 근거인지).\n")
	b.WriteString("JSON 한 개만: {\"verdict\":\"real|contaminated|unclear\",\"identity\":\"...\",\"reason\":\"...\"}\n")
	return b.String()
}
