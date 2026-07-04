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
// 반환: (upgraded, processed, err).
func EvidencePass(ctx context.Context, pool *pgxpool.Pool, n int) (int, int, error) {
	nv, err := naver.New()
	if err != nil {
		return 0, 0, fmt.Errorf("verify evidence: %w", err)
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
		return 0, 0, fmt.Errorf("verify evidence query: %w", err)
	}
	var ents []evEntity
	for rows.Next() {
		var e evEntity
		if err := rows.Scan(&e.id, &e.ko, &e.etype, &e.role, &e.works); err != nil {
			rows.Close()
			return 0, 0, err
		}
		ents = append(ents, e)
	}
	rows.Close()

	log.Printf("kdb.verify.evidence: start (unverified=%d)", len(ents))
	upgraded, processed := 0, 0
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
		if !v.Confirmed {
			continue
		}
		reason := strings.TrimSpace(v.Reason)
		ev := "search+gemma"
		if reason != "" {
			ev = "search+gemma: " + truncateRunes(reason, 80)
		}
		// unverified 인 동안만 승급(그새 권위앵커 붙었으면 결정론 스윕이 이미 올림 → 덮지 않음).
		tag, uerr := pool.Exec(ctx, `
			UPDATE kwave_entities
			   SET verification_tier='evidenced', verification_evidence=$2, verified_tier_at=now()
			 WHERE id=$1 AND verification_tier='unverified'`, e.id, ev)
		if uerr == nil && tag.RowsAffected() > 0 {
			upgraded++
			log.Printf("  [upgraded] %s (%s) ← %s", e.ko, e.etype, ev)
		}
	}
	log.Printf("kdb.verify.evidence: done upgraded=%d/%d", upgraded, processed)
	return upgraded, processed, nil
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

type verifyResult struct {
	Confirmed bool   `json:"confirmed"`
	Reason    string `json:"reason"`
}

var verifyJudgeSchema = []byte(`{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "confirmed": {"type": "boolean"},
    "reason": {"type": "string"}
  },
  "required": ["confirmed"]
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
	b.WriteString("당신은 한국 대중문화(K-콘텐츠) 고유명사의 '실재성 검증기'입니다.\n")
	b.WriteString("아래 한국 엔티티가 검색결과(뉴스)에서 실제로 확인되는, 실재하는 K-엔티티인지 판정하세요.\n\n")
	b.WriteString("한국어 정식명: " + e.ko + "\n")
	b.WriteString("종류: " + e.etype + "\n")
	if e.role != "" {
		b.WriteString("역할/직업: " + e.role + "\n")
	}
	if len(e.works) > 0 {
		b.WriteString("대표작/소속: " + strings.Join(e.works, ", ") + "\n")
	}
	b.WriteString("\n검색결과(뉴스 제목 — 스니펫):\n")
	for i, h := range vi.hits {
		if i >= 8 {
			break
		}
		b.WriteString("- " + h + "\n")
	}
	b.WriteString("\n판정 규칙(엄격):\n")
	b.WriteString("1. 검색결과가 '위에 명시된 바로 그 한국 " + e.etype + "'에 관한 것이면 confirmed=true.\n")
	b.WriteString("2. 동명이의(다른 인물/사물)·무관한 결과뿐이거나, 실재를 뒷받침하는 근거가 없으면 confirmed=false.\n")
	b.WriteString("3. 억지 추론 금지 — 검색결과에 실제 근거가 있을 때만 confirmed=true.\n")
	b.WriteString("4. reason = 판정 근거 한국어 한 줄(어느 스니펫이 근거인지).\n")
	b.WriteString("JSON 한 개만 출력: {\"confirmed\":true|false,\"reason\":\"...\"}\n")
	return b.String()
}
