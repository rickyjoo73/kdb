package kdb

// keyword_triage.go — 오염 키워드 판별 에이전트 (오너 승인 2026-07-17).
//
// 역할: 심사 게이트에 들어온 키워드가 "고유명사 후보"인지 "문장형·조합어·오염"인지
// gemma 가 구조 판별한다. 규칙(어미 패턴)으로 못 잡는 오염(실측: "남파 트레이더
// 김철수씨의 근황")을 잡아 검색 쿼터 낭비와 보류 적체를 막는다.
//
// 오거부 방지 원칙(오너 최상위 금칙): 판별은 보수적 — 명백한 문장·서술·조합·비K 만
// garbage. 애매하면 entity_candidate 로 두어 기존 근거수집 경로가 판단한다.
// 킬스위치: KDB_TRIAGE_ENABLED=0. gemma 미설정/오류 시 무동작(기존 흐름 보존).

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb/gemma"
)

type triageVerdict struct {
	Verdict string `json:"verdict"` // entity_candidate | garbage
	Reason  string `json:"reason"`
}

var triageSchema = []byte(`{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "verdict": {"type": "string", "enum": ["entity_candidate", "garbage"]},
    "reason": {"type": "string"}
  },
  "required": ["verdict"]
}`)

// TriageEnabled — 기본 on, KDB_TRIAGE_ENABLED=0 으로 끔.
func TriageEnabled() bool {
	return os.Getenv("KDB_TRIAGE_ENABLED") != "0" && gemma.Configured()
}

// LooksPhraseLike — 검색 전에 triage 를 먼저 태울 가치가 있는 형태(문장/구 절 의심).
// 공백 토큰 4개 이상 또는 20자 초과. 정상 작품명 대부분은 이보다 짧다 — 걸려도
// triage 가 entity_candidate 면 기존 경로로 그대로 간다(손해는 gemma 1콜뿐).
func LooksPhraseLike(ko string) bool {
	return len(strings.Fields(ko)) >= 4 || utf8.RuneCountInString(ko) > 20
}

// TriageKeyword — gemma 구조 판별. garbage=true 는 "명백한 비고유명사"만.
// 실패/미설정 시 (false, "") — 호출측은 기존 흐름 유지.
func TriageKeyword(ctx context.Context, ko, typeHint string) (garbage bool, reason string) {
	if !TriageEnabled() {
		return false, ""
	}
	var b strings.Builder
	b.WriteString("당신은 한국 대중문화(K-콘텐츠) 고유명사 사전의 '키워드 오염 판별기'입니다.\n")
	b.WriteString("아래 키워드가 사전에 등재할 수 있는 '단일 고유명사'(인물·그룹·작품·방송·행사·브랜드 이름)인지 판별하세요.\n\n")
	b.WriteString("키워드: " + ko + "\n")
	if t := strings.TrimSpace(typeHint); t != "" && t != "unknown" {
		b.WriteString("요청자 힌트(불신 가능): " + t + "\n")
	}
	b.WriteString("\n판별 규칙(보수적 — 확실할 때만 garbage):\n")
	b.WriteString("★대전제: 한국 대중문화 제목은 문장·구어 형태가 아주 흔하다 — '죽어도 좋아', '이번 생은 처음이라', '내꺼중에 최고', '대충 캠퍼스로맨스임'은 전부 실제 작품 제목이다. **문장/서술 형태라는 이유만으로 garbage 판정 금지.**\n")
	b.WriteString("판단 질문: \"이 문자열이 노래·드라마·예능·웹드라마 제목으로 발표됐을 가능성이 조금이라도 있는가?\" — 있으면 entity_candidate.\n")
	b.WriteString("1. garbage 는 오직 다음만: ①인물명+조사+일상어의 뉴스·근황 서술(\"~씨의 근황\", \"~가 밝힌\") ②검색·안내·후기·상거래 텍스트(\"~하는 방법\", \"~후기\", \"~가격\", \"~예매\") ③서로 다른 고유명사·개념의 나열 조합(\"아이유 콘서트 티켓\") ④단독 대명사·조사·숫자 나부랭이 ⑤명백한 비한국 일반 상식어(국가명, 원소명 등).\n")
	b.WriteString("2. entity_candidate = 그 외 전부: 낯선 신인명, 문장형 제목, 긴 제목(\"꽃 피면 달 생각하고\"), 시리즈명(\"반짝반짝 캐치! 티니핑\"), 구어체 제목, 외래어 표기. 실존 여부는 판단하지 말 것 — 제목일 개연성만 본다.\n")
	b.WriteString("3. 모호하면 무조건 entity_candidate. 실재하는 제목/이름을 garbage 로 판정하는 것이 이 시스템의 최악의 오류다. garbage 는 100% 확신할 때만.\n")
	b.WriteString("JSON 한 개만: {\"verdict\":\"entity_candidate|garbage\",\"reason\":\"근거 한 줄\"}\n")

	cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	raw, err := gemma.Complete(cctx, b.String(), triageSchema)
	if err != nil {
		return false, ""
	}
	var v triageVerdict
	if json.Unmarshal(raw, &v) != nil {
		return false, ""
	}
	if v.Verdict == "garbage" {
		return true, strings.TrimSpace(v.Reason)
	}
	return false, ""
}

// TriageExhaustedBacklog — 자동검증이 포기(autoverify_exhausted)한 보류를 오염 판별
// 에이전트로 일괄 선별한다(오너 지시 07-17: "미해결 보류 빠르게"). garbage 확정만
// 기각하고, 나머지는 'triage_kept' 태그를 달아 운영자 몫으로 명시(재판정 방지).
// 반환: (기각, 유지, 처리수).
func TriageExhaustedBacklog(ctx context.Context, pool *pgxpool.Pool, n int) (rejected, kept, processed int) {
	if !TriageEnabled() {
		log.Printf("kdb.triage: 비활성(KDB_TRIAGE_ENABLED=0 또는 gemma 미설정)")
		return 0, 0, 0
	}
	// 대상: 근거 수집이 한 번 이상 실패한 보류 전부(백오프 대기 포함) — 재시도를
	// 기다릴 가치가 없는 쓰레기를 미리 걷어낸다(07-17 확장: exhausted 뿐 아니라 miss 포함).
	rows, err := pool.Query(ctx, `
SELECT id, entity_ko, requested_entity_type::text
FROM kwave_entity_research_queue q
WHERE precheck_status='review'
  AND (precheck_flags && ARRAY['autoverify_exhausted'] OR precheck_flags && ARRAY['autoverify_miss'])
  AND NOT (precheck_flags && ARRAY['triage_kept'])
  AND NOT EXISTS (SELECT 1 FROM kwave_entities e
                   WHERE e.status='active' AND (e.canonical_ko=q.entity_ko OR q.entity_ko=ANY(e.aliases_ko)))
ORDER BY created_at DESC LIMIT $1`, n)
	if err != nil {
		log.Printf("kdb.triage: exhausted query: %v", err)
		return 0, 0, 0
	}
	type item struct {
		id      uuid.UUID
		ko, typ string
	}
	var items []item
	for rows.Next() {
		var it item
		if rows.Scan(&it.id, &it.ko, &it.typ) == nil {
			items = append(items, it)
		}
	}
	rows.Close()
	log.Printf("kdb.triage: exhausted 선별 시작 (%d건)", len(items))
	for _, it := range items {
		if ctx.Err() != nil {
			break
		}
		processed++
		if garbage, reason := TriageKeyword(ctx, it.ko, it.typ); garbage {
			if rejectByTriage(ctx, pool, it.id, it.ko, reason) {
				rejected++
			}
			continue
		}
		// 고유명사 후보 유지 — 운영자 검토 몫으로 태그(무한 재판정 방지).
		_, _ = pool.Exec(ctx, `
UPDATE kwave_entity_research_queue
   SET precheck_flags=array_append(precheck_flags,'triage_kept')
 WHERE id=$1 AND NOT (precheck_flags && ARRAY['triage_kept'])`, it.id)
		kept++
	}
	log.Printf("kdb.triage: exhausted 선별 완료 rejected=%d kept(운영자몫)=%d /%d", rejected, kept, processed)
	return rejected, kept, processed
}

// TriageStuckCandidates — 생성됐지만 채움이 안 되는 정체 candidate 오염 선별
// (오너 지적 07-17: "승인돼도 못 채워지는 것들에 오염이 있을 것"). 3일+ 정체·저신뢰
// candidate 를 gemma 가 판별: garbage → rejected(스냅샷 남겨 복원 가능), 후보 → 유지 태그.
// active 는 건드리지 않는다(그쪽은 EvidenceVerifier/TypeVerifier/dataqa 담당).
func TriageStuckCandidates(ctx context.Context, pool *pgxpool.Pool, n int) (rejected, kept, processed int) {
	if !TriageEnabled() {
		log.Printf("kdb.triage: 비활성(KDB_TRIAGE_ENABLED=0 또는 gemma 미설정)")
		return 0, 0, 0
	}
	rows, err := pool.Query(ctx, `
SELECT id::text, canonical_ko, entity_type::text
FROM kwave_entities
WHERE status='candidate' AND operator_locked=false
  AND confidence <= 0.45 AND created_at < now()-interval '3 days'
  AND COALESCE(notes,'') NOT LIKE '%[triage:%'
ORDER BY created_at DESC LIMIT $1`, n)
	if err != nil {
		log.Printf("kdb.triage: candidates query: %v", err)
		return 0, 0, 0
	}
	type item struct{ id, ko, typ string }
	var items []item
	for rows.Next() {
		var it item
		if rows.Scan(&it.id, &it.ko, &it.typ) == nil {
			items = append(items, it)
		}
	}
	rows.Close()
	log.Printf("kdb.triage: 정체 candidate 선별 시작 (%d건)", len(items))
	for _, it := range items {
		if ctx.Err() != nil {
			break
		}
		processed++
		garbage, reason := TriageKeyword(ctx, it.ko, it.typ)
		if !garbage {
			_, _ = pool.Exec(ctx, `
UPDATE kwave_entities SET notes = CASE WHEN COALESCE(notes,'')='' THEN '[triage:kept]' ELSE notes || ' [triage:kept]' END
 WHERE id=$1 AND COALESCE(notes,'') NOT LIKE '%[triage:%'`, it.id)
			kept++
			continue
		}
		// 복원 스냅샷 후 기각 (dataqa_log revert 규약과 동일 — 오판 시 되돌림 가능).
		_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_dataqa_log (entity_id, locale, old_value, old_source, verdict, reason, model)
VALUES ($1::uuid, 'status', 'candidate', '', 'triage-candidate-reject', $2, 'gemma')`,
			it.id, "triage: "+reason)
		tag, uerr := pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='rejected',
       notes = CASE WHEN COALESCE(notes,'')='' THEN $2 ELSE notes || ' ' || $2 END,
       updated_at=now()
 WHERE id=$1 AND status='candidate'`, it.id, "[triage:reject] "+truncRunesTriage(reason, 60))
		if uerr == nil && tag.RowsAffected() > 0 {
			rejected++
			log.Printf("kdb.triage: candidate 오염 기각 ko=%q — %s", it.ko, reason)
		}
	}
	log.Printf("kdb.triage: 정체 candidate 선별 완료 rejected=%d kept=%d /%d", rejected, kept, processed)
	return rejected, kept, processed
}

func truncRunesTriage(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// rejectByTriage — triage 판정으로 review row 를 기각 종결한다(운영자는 발굴 큐에서
// 승인 버튼으로 언제든 복원 가능 — tombstone 아님).
func rejectByTriage(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, ko, reason string) bool {
	tag, err := pool.Exec(ctx, `
UPDATE kwave_entity_research_queue
   SET precheck_status='reject', precheck_reason='triage_garbage',
       resolution_status='rejected_precheck', last_outcome='precheck_reject',
       last_error=NULLIF($2,''),
       status='done', finished_at=COALESCE(finished_at, now())
 WHERE id=$1 AND precheck_status='review'`, id, "triage: "+reason)
	if err != nil || tag.RowsAffected() == 0 {
		return false
	}
	log.Printf("kdb.triage: 오염 기각 ko=%q — %s", ko, reason)
	return true
}
