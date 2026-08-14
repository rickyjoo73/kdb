package kdb

// backlog_watch — "어떤 조건의 엔티티가 어느 레인에도 안 닿고 늙어가는가"를 상시 계측한다.
//
// ★왜 만드는가(오너 지시 2026-07-31 "땜방이 아니라 전반적으로 해결될 배선"):
// 같은 날 병목 진단에서 **동일한 결함을 네 번** 발견했다.
//   ① ondemand      — 셀렉트가 term 을 통과시키는데 승급 함수는 term 을 거부 → 22일 공회전
//   ② retype-stuck  — 대상이 'autoverify_type_reassigned' 플래그에 묶여 term/unknown 미포함
//   ③ revert-term   — 승급 가드와 이름검색 드레인 사이 사각지대에 191건 68일 방치
//   ④ tmdb-locale   — 승급 드레인이 candidate 전용이라 active 로케일 빈칸 193건 미도달
// 공통 원인은 하나다: **레인의 대상 선정이 "고치려는 조건" 이 아니라 부수 속성(유입경로·
// notes 표식·ref 보유 여부·status)에 묶여 있다.** 그래서 조건을 만족하는데도 어떤 레인도
// 집지 않는 집합이 조용히 쌓인다. 넷 다 사람이 손으로 찾았다 — 그게 문제다.
//
// 이 워치는 개별 결함을 고치지 않는다. **결함이 생겼다는 사실을 자동으로 드러낸다.**
// 조건별로 (백로그 크기 · 가장 오래 안 움직인 항목의 나이 · 임계 초과 수)를 주기적으로
// 기록한다. 어떤 레인이 자기 백로그를 못 덮으면 "oldest_days" 가 단조 증가하므로,
// 지표만 보면 위 네 사례가 전부 사전에 드러났을 것이다.
//
// 판정은 하지 않는다(데이터 변경 0). 계측과 경보만 한다 — 이 워치 자체가 또 하나의
// 손볼 대상이 되지 않도록.

import (
	"context"
	"log"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// backlogCondition — 감시할 "고쳐야 할 상태". where 는 kwave_entities e 기준 술어이고,
// staleCol 은 "마지막으로 이 항목이 움직인 시각" 으로 볼 컬럼이다.
//
// 조건은 레인 이름이 아니라 **상태** 로 정의한다. 레인 구현이 바뀌어도 감시가 따라
// 무너지지 않게 하려는 것 — 레인에 묶으면 이 파일이 또 같은 병에 걸린다.
type backlogCondition struct {
	Name      string
	Where     string
	WarnDays  int // 이 나이를 넘긴 항목이 있으면 경보
	StaleCol  string
	Rationale string
}

// cjkFillLaneSQL — CJK 빈칸을 채울 수 있는 레인들의 원장 field 값. 여기 빠진 레인이
// 생기면 그 레인이 판정한 항목이 "미판정"으로 계속 경보에 남는다 — 새 CJK 레인을
// 만들면 이 목록에 추가할 것.
// (2026-08-08) 'latin-origin' 추가 — 한글 없는 원제의 CJK 승계 레인. 이 레인은 종전에
// 성공만 기록하고 기각은 안 남겨, 그 레인이 판정한 건이 계속 "미판정"으로 잡혔다.
// (2026-08-14) 'zhwiki-title' 추가 — 보유한 zh.wikipedia URL 에서 zh 를 뽑는 레인.
const cjkFillLaneSQL = `'mt-fill:ja','mt-fill:zh','tmdb-locale','wd-locale','tmdb-anchor','latin-origin','zhwiki-title'`

// noCurrentCJKVerdict — 위 레인들 중 **지금 입력에 대해** 판정을 내린 곳이 하나도 없는지.
// FillRetryPredicate 와 같은 규칙(지문 일치 + FillRetryRevisitDays 창)을 쓴다. 여기가
// 따로 놀면 "경보는 울리는데 어느 레인도 그 항목을 집지 않는" 상태가 다시 생긴다 —
// 이 파일이 감시하려는 바로 그 병이다.
// enFillLaneSQL — canonical_en **빈칸 자체**에 판정을 내리는 레인의 원장 field.
// cjkFillLaneSQL 과 같은 규칙이고 같은 주의사항이 붙는다 — 새 en 레인을 만들면 여기 추가할 것.
//
// ★일부러 좁게 잡았다. 'latin-origin' 은 여기 없다: 그 레인(MarkLatinOriginRejects)이
// 판정하는 건 **CJK 칸**이지 en 이 아니다. 원장에 이름이 있다고 해서 이 질문에 답한 건
// 아니다 — 넣으면 en 을 아무도 안 본 건이 "판정 완료"로 위장된다.
// 컬럼이름 스타일 field('canonical_en' 1,157건, localfill/enrich 계열)도 뺐다. 45차 §4 가
// "누가 어느 field 로 쓰는지 전수 확인이 안 됐다"고 남긴 항목이라, 확인 전에 판정으로
// 인정하면 지표가 조용히 관대해진다. 실측상 없어도 설명력은 충분하다(en 빈칸 47건 전부
// gt-fill:en 으로 설명된다 — 2026-08-14).
const enFillLaneSQL = `'gt-fill:en'`

// noCurrentENVerdict — 위 레인 중 지금 입력에 대해 en 빈칸을 판정한 곳이 없는지.
func noCurrentENVerdict(alias string) string {
	return `NOT EXISTS (SELECT 1 FROM kwave_kdb_enrich_attempts a
                    WHERE a.entity_id = ` + alias + `.id
                      AND a.field IN (` + enFillLaneSQL + `)
                      AND a.input_hash = ` + alias + `.fill_input_hash
                      AND a.last_attempt_at > now() - interval '` + strconv.Itoa(FillRetryRevisitDays) + ` days')`
}

func noCurrentCJKVerdict(alias string) string {
	return `NOT EXISTS (SELECT 1 FROM kwave_kdb_enrich_attempts a
                    WHERE a.entity_id = ` + alias + `.id
                      AND a.field IN (` + cjkFillLaneSQL + `)
                      AND a.input_hash = ` + alias + `.fill_input_hash
                      AND a.last_attempt_at > now() - interval '` + strconv.Itoa(FillRetryRevisitDays) + ` days')`
}

var backlogConditions = []backlogCondition{
	{
		Name:      "candidate-stuck",
		Where:     `e.status='candidate' AND e.operator_locked=false`,
		WarnDays:  21, // = candidate TTL(ddc77d5). TTL 이 도는 한 초과는 0 이어야 한다 → 초과 = TTL 고장.
		StaleCol:  `COALESCE(e.last_enriched_at, e.created_at)`,
		Rationale: "승급도 기각도 안 된 채 정체 — ①②③ 유형",
	},
	{
		// ★2026-08-07 재정의. 종전 조건은 "CJK 빈칸" 전부를 세어 1,931건을 보고했는데,
		// 그중 **실제로 어떤 레인이 집을 수 있는 건 30건(1.6%)** 이었다. 나머지 1,901건은
		// 이미 모든 CJK 레인이 지금 입력에 대해 판정을 끝낸 것이다 — 위키데이터 우물
		// 실측(nothing-new 1,248), TMDb 무매칭 384, 구글번역 게이트 기각 2,232.
		//
		// 즉 이 지표는 "고쳐야 할 배선"이 아니라 **원본 부재라는 사실**을 세고 있었다.
		// 98%가 손댈 수 없는 값인 지표는 무시되고, 그러면 진짜 30건이 그 더미에 묻힌다 —
		// never-examined-gap 이 94% 거짓으로 재정의된 것과 정확히 같은 구조다.
		//
		// 실제로 30건에는 진짜 신호가 있었다: `Time's Tickin'` `Don't Save Me` `It's 2PM`
		// 처럼 **한글이 없는 제목**은 mt-fill 의 `canonical_ko ~ '[가-힣]'` 조건에 걸려
		// 어느 CJK 레인도 손대지 않는다. 종전 지표로는 1,931 더미에 묻혀 보이지 않았다.
		// (`BE (Delلuxe Edition)` 처럼 아랍문자가 섞인 오염 제목도 같이 드러났다.)
		//
		// 이름을 바꿔 정의 변경이 조용히 묻히지 않게 한다. 종전 값은 아래 -raw 로 유지.
		Name: "active-cjk-actionable",
		Where: `e.status='active' AND e.operator_locked=false AND COALESCE(e.canonical_ko,'')<>''
   AND (COALESCE(e.canonical_ja,'')='' OR COALESCE(e.canonical_zh,'')='' OR COALESCE(e.canonical_zh_hant,'')='')
   AND ` + noCurrentCJKVerdict("e"),
		WarnDays:  60,
		StaleCol:  `e.created_at`,
		Rationale: "CJK 빈칸인데 아직 어느 레인도 판정 안 함 — 배선 구멍(④ 유형)",
	},
	{
		// 종전 정의 그대로. 수치를 숨기지 않기 위해 남긴다 — 위 재정의가 무언가를 가리는지
		// 두 값의 차이로 항상 확인할 수 있어야 한다. 경보는 걸지 않는다(대부분 원본 부재).
		// ★이 값이 큰 것은 결함이 아니다. 세 경로(위키데이터·TMDb·구글번역)를 전수 실측해
		// 우물이 마른 것을 확인했다 — 상세는 docs/runbooks/KDB.handoff-20260807.md §3·§5.
		Name:      "active-cjk-gap-raw",
		Where:     `e.status='active' AND (COALESCE(e.canonical_ja,'')='' OR COALESCE(e.canonical_zh,'')='' OR COALESCE(e.canonical_zh_hant,'')='')`,
		WarnDays:  36500,
		StaleCol:  `e.created_at`,
		Rationale: "CJK 빈칸 전체(원본 부재분 포함) — 참고값",
	},
	{
		// ★2026-08-14 재정의. 종전 조건은 "라틴 빈칸" 전부를 세어 85건을 ⚠ 로 보고하면서
		// 사유에 **"규칙으로 채워지는 층이라 남으면 배선 문제"** 라고 단언했다. 전수 분류하니
		// 297칸(85 엔티티) 중 **설명 안 되는 칸이 0** 이었다:
		//
		//	en 자체가 빈칸        182칸/47건 — 전건 gt-fill:en 판정 완료(복사 원본이 없다)
		//	en 이 codex-fallback   74칸/26건 — DrainRomanizeLatin 이 일부러 전파 안 함
		//	unknown/term           30칸/9건  — 전파 대상 아님
		//	MR 로마자 가드         11칸/3건  — 45차 §2 가 의도적으로 세운 격리(안영민·양현아·유성원)
		//
		// 즉 이 지표도 **"고쳐야 할 배선"이 아니라 "원본 부재"를 세고 있었다** — 44차 §3 이
		// active-cjk-gap-raw 에서, 42차가 never-examined-gap 에서 고친 것과 같은 병의 세 번째
		// 판이다. 그런데 CJK 쪽과 달리 이건 **사유가 거짓을 적극적으로 주장**해서 더 나쁘다:
		// 다음 세션이 있지도 않은 구멍을 찾아 나선다(실제로 2026-08-14 세션이 그렇게 했다).
		//
		// 조건은 DrainRomanizeLatin 의 가드를 그대로 뒤집어 쓴다 — 레인이 "안 채운다"고
		// 판정한 이유를 지표가 모르면 두 규칙이 또 갈라진다. 종전 값은 아래 -raw 로 유지.
		Name: "active-latin-actionable",
		Where: `e.status='active' AND e.operator_locked=false
   AND (
     -- ①복사 원본(en)이 없는데 그 사실에 대한 판정도 없다 → 1건이 5칸을 막는 진짜 구멍
     ( COALESCE(e.canonical_en,'')='' AND e.entity_type NOT IN ('unknown','term')
       AND ` + noCurrentENVerdict("e") + ` )
     -- ②전파 가드를 전부 통과하는데 라틴 칸이 비어 있다 → 레인이 집었어야 하는데 안 집었다
     OR ( e.entity_type NOT IN ('unknown','term')
          AND COALESCE(e.canonical_en,'')<>'' AND e.canonical_en ~ '` + latinOriginPattern + `'
          AND COALESCE(e.canonical_en_source,'') NOT IN ('codex-fallback','')` + latinPropagateSQLGuard + `
          AND EXISTS (SELECT 1 FROM (VALUES ('vi',e.canonical_vi),('es',e.canonical_es),
                                            ('id',e.canonical_id),('pt_br',e.canonical_pt_br)) v(loc,val)
                       WHERE COALESCE(v.val,'')=''
                         AND NOT EXISTS (SELECT 1 FROM kwave_kdb_dataqa_log d
                              WHERE d.entity_id=e.id AND d.locale=v.loc
                                AND d.verdict='contaminated' AND d.reverted_at IS NULL)) )
   )`,
		WarnDays:  60,
		StaleCol:  `e.created_at`,
		Rationale: "라틴 빈칸인데 어느 가드로도 설명 안 됨 — 진짜 배선 구멍",
	},
	{
		// 종전 정의 그대로. 수치를 숨기지 않기 위해 남긴다 — 위 재정의가 무언가를 가리는지
		// 두 값의 차이로 항상 확인할 수 있어야 한다. 경보는 걸지 않는다(대부분 원본 부재).
		Name:      "active-latin-gap-raw",
		Where:     `e.status='active' AND (COALESCE(e.canonical_en,'')='' OR COALESCE(e.canonical_vi,'')='' OR COALESCE(e.canonical_es,'')='' OR COALESCE(e.canonical_id,'')='' OR COALESCE(e.canonical_pt_br,'')='')`,
		WarnDays:  36500,
		StaleCol:  `e.created_at`,
		Rationale: "라틴 로케일 빈칸 전체(원본 부재분 포함) — 참고값",
	},
	{
		Name:      "active-no-anchor",
		Where:     `e.status='active' AND NOT EXISTS (SELECT 1 FROM kwave_entity_external_refs r WHERE r.entity_id=e.id) AND COALESCE(array_length(e.source_urls,1),0)=0`,
		WarnDays:  60,
		StaleCol:  `e.created_at`,
		Rationale: "서빙 중인데 권위 앵커 전무 — 오염 재심 대상",
	},
	{
		// ★active-no-anchor 의 부분집합. 둘을 나눠 세는 이유(2026-07-31):
		// no-anchor 4,346 중 대다수는 뉴스근거로 승급된 것이고(최근 7일 유입의 95%가
		// [cand-evidence]), 그건 "앵커 없음"이지 "근거 없음"이 아니다. 두 명제를 한 지표로
		// 뭉치면 ①진짜 무근거가 뉴스근거 더미에 묻히고 ②근거 URL 을 채우는 것만으로
		// 지표가 내려가 개선한 것처럼 보인다. 앵커 확보와 근거 추적은 각각 세야 정직하다.
		Name: "active-no-evidence",
		Where: `e.status='active'
   AND NOT EXISTS (SELECT 1 FROM kwave_entity_external_refs r WHERE r.entity_id=e.id)
   AND COALESCE(array_length(e.source_urls,1),0)=0
   AND NOT EXISTS (SELECT 1 FROM kwave_kdb_evidence_refs v WHERE v.entity_id=e.id)`,
		WarnDays:  60,
		StaleCol:  `e.created_at`,
		Rationale: "서빙 중인데 앵커도 근거 URL 도 없음 — 되짚을 수 있는 게 아무것도 없는 진짜 사각지대",
	},
	{
		Name:      "type-mismatch",
		Where:     `e.status IN ('active','candidate') AND COALESCE(e.notes,'') LIKE '%[typeaudit:mismatch%' AND COALESCE(e.notes,'') NOT LIKE '%[typeaudit:cleared%'`,
		WarnDays:  14,
		StaleCol:  `e.updated_at`,
		Rationale: "결정적 감사가 타입 불일치로 표시 — 판정 레인이 받아야 함",
	},
	{
		// ★2026-07-31 재정의. 종전 조건은 "enrich 시도 이력 없음"만 봤고 370건을 보고했는데,
		// 실측하니 346건(94%)이 8개 로케일이 전부 채워진 완성 엔티티였다(블랙핑크·무신사·
		// CJ제일제당 등 05-23 시드 유입분). 드레인이 손댈 게 없어서 안 건드린 것이지
		// 커버리지 구멍이 아니다 — "시도 이력 없음"과 "안 채워짐"은 다른 명제다.
		//
		// 94% 가 거짓인 지표는 무시되고, 그러면 진짜 23건이 그 더미에 묻힌다. ①번 ondemand
		// 공회전이 레인 로그를 100% 노이즈로 만들어 실제 결함 셋을 가렸던 것과 같은 구조다.
		// 지표를 낮추려는 게 아니라 지표가 가리키던 명제를 실측에 맞춘 것이라, 이름도 바꿔
		// 정의 변경이 조용히 묻히지 않게 한다(종전 raw 값은 아래 never-examined-raw 로 유지).
		Name: "never-examined-gap",
		Where: `e.status IN ('active','candidate')
   AND NOT EXISTS (SELECT 1 FROM kwave_kdb_enrich_attempts a WHERE a.entity_id=e.id)
   AND (COALESCE(e.canonical_en,'')='' OR COALESCE(e.canonical_ja,'')='' OR COALESCE(e.canonical_zh,'')=''
     OR COALESCE(e.canonical_zh_hant,'')='' OR COALESCE(e.canonical_vi,'')='' OR COALESCE(e.canonical_es,'')=''
     OR COALESCE(e.canonical_id,'')='' OR COALESCE(e.canonical_pt_br,'')='')`,
		WarnDays:  60,
		StaleCol:  `e.created_at`,
		Rationale: "빈칸이 있는데 어떤 드레인도 한 번도 시도 안 함 — 소스 천장이 아니라 배선 구멍",
	},
	{
		// 종전 정의 그대로. 수치를 숨기지 않기 위해 남긴다 — 위 재정의가 무언가를 가리는지
		// 두 값의 차이로 항상 확인할 수 있어야 한다. 경보는 걸지 않는다(대부분 정상 완성분).
		Name:      "never-examined-raw",
		Where:     `e.status IN ('active','candidate') AND NOT EXISTS (SELECT 1 FROM kwave_kdb_enrich_attempts a WHERE a.entity_id=e.id)`,
		WarnDays:  36500,
		StaleCol:  `e.created_at`,
		Rationale: "시도 이력 없음 전체(완성분 포함) — 참고값",
	},
}

// BacklogSnapshot — 조건 1건의 계측 결과.
type BacklogSnapshot struct {
	Name       string
	Total      int
	OverWarn   int
	OldestDays int
}

// ─────────────────────────────────────────────────────────────────────────────
// 불변식 감시 (2026-08-02) — 백로그 감시가 못 잡는 계열을 담당한다.
//
// ★왜 필요한가: 위 백로그 감시는 "얼마나 많이·얼마나 오래 쌓였나"(양)를 재고, 경보는
// **나이 임계 초과**로만 낸다. 그런데 오늘 찾은 결함은 양의 문제가 아니었다 —
// verification_tier='evidenced' 인데 되짚을 근거가 없는 3,995건이 그것이고, 이건
// `active-no-evidence` 로 **정확히 같은 크기로 이미 계측되고 있었는데도** 경보가 없었다.
//
// 원인 두 겹(둘 다 실측):
//   ① 나이 기준이 e.updated_at 이었다. ddc77d5 가 "레인들이 결론 없이 계속 건드려서
//      updated_at 은 정체 지표로 못 쓴다"고 이미 결론냈는데(candidate 604 중 603 이
//      21일 내 갱신) 8개 조건 중 6개가 그 컬럼에 걸려 있었다. 해당 집합의 83%가 7일 내
//      갱신 상태라 임계를 넘을 수가 없다.
//   ② 임계가 90일인데 **DB 수명이 70일**이었다. 어떤 행도 DB 보다 오래될 수 없으므로
//      그 조건들은 이 DB 역사상 한 번도 울릴 수 없었다. 우연이 아니라 구조적으로 불가능.
//   → 6시간마다 "active-no-evidence total=4361 oldest=63d (임계 90d 내)" 가 찍혔고,
//     이건 **건강해 보인다**. 계측이 있었는데 아무도 못 봤다는 게 이번 결함의 본질이다.
//
// 그래서 불변식은 **나이를 보지 않는다.** "참이면 안 되는 상태"를 세고 count>0 이면 경보한다.
// 도입 시점 값(Baseline)을 같이 찍어 증감을 즉시 보이게 한다 — 큰 잔여 위반이 있어도
// Δ 가 보이면 노이즈가 아니라 진행 지표가 된다. 위반 0 짜리는 회귀 가드로 남긴다.
type invariant struct {
	Name      string
	Where     string // 위반 조건(kwave_entities e 기준). 이 술어에 걸리면 명제가 깨진 것.
	Baseline  int    // 도입 시점(2026-08-02) 실측 위반 수. 이보다 늘면 회귀.
	Rationale string
}

var invariants = []invariant{
	{
		// 이 시스템에서 가장 큰 거짓 주장. verification_tier 는 API 응답 필드라
		// (kdbapi/api.go) 소비자는 "독립 확증됨"을 받고 검증할 방법이 없다.
		Name: "evidenced-unretrievable",
		Where: `e.status='active' AND e.verification_tier='evidenced'
   AND NOT EXISTS (SELECT 1 FROM kwave_entity_external_refs r WHERE r.entity_id=e.id)
   AND COALESCE(array_length(e.source_urls,1),0)=0
   AND NOT EXISTS (SELECT 1 FROM kwave_kdb_evidence_refs v WHERE v.entity_id=e.id)`,
		Baseline:  3995,
		Rationale: "evidenced 인데 되짚을 근거가 하나도 없음 — 확증 주장에 지시대상이 없다",
	},
	{
		// ★위 지표만으로는 신규 유입 위반이 안 보인다. 3,995 짜리 더미에서 Δ+1 은
		// 눈에 띄지 않는데, 정작 고칠 수 있는 건 신규분뿐이다(소급분은 재검색이 필요해
		// 별건 — 핸드오프 §6-1). 그래서 같은 명제를 7일 창으로 한 번 더 센다.
		//
		// 2026-08-02 기제2 배포 시점 실측: 7일 414 / 1일 0. 414 는 근거 대장(5ed2af4,
		// 07-31)이 생기기 전 유입이라 창이 지나면 빠진다. 기제2 이후 이 값이 0 을
		// 유지하지 못하면 "적재 못 하면 승급 안 함" 계약이 어딘가에서 새고 있다는 뜻이다.
		Name: "evidenced-unretrievable-new",
		Where: `e.status='active' AND e.verification_tier='evidenced'
   AND e.created_at > now() - interval '7 days'
   AND NOT EXISTS (SELECT 1 FROM kwave_entity_external_refs r WHERE r.entity_id=e.id)
   AND COALESCE(array_length(e.source_urls,1),0)=0
   AND NOT EXISTS (SELECT 1 FROM kwave_kdb_evidence_refs v WHERE v.entity_id=e.id)`,
		Baseline:  414,
		Rationale: "최근 7일 유입 중 되짚기 불가 — 소급분과 분리해 신규 계약 위반만 본다",
	},
	{
		Name: "authoritative-no-ref",
		Where: `e.status='active' AND e.verification_tier='authoritative'
   AND NOT EXISTS (SELECT 1 FROM kwave_entity_external_refs r
                    WHERE r.entity_id=e.id AND r.provider IN (` + AuthoritativeIdentityProviderSQLList() + `))`,
		Baseline:  0,
		Rationale: "authoritative 인데 권위 ref 없음 — 최상위 티어의 정의 위반(회귀 가드)",
	},
	{
		// 2026-08-02 acccac8 이 고친 결함의 회귀 가드. canonical_*_source 8개 컬럼의
		// DB 기본값이 'wikidata-label' 이고 그게 강한소스 목록에 있어서, 값이 빈 슬롯의
		// 기본값만으로 evidenced 가 나오고 있었다(1,443건).
		Name: "strongsource-empty",
		Where: `e.status='active' AND e.verification_evidence='strong-source'
   AND NOT EXISTS (SELECT 1 FROM (VALUES
         (e.canonical_en_source, e.canonical_en), (e.canonical_ja_source, e.canonical_ja),
         (e.canonical_zh_source, e.canonical_zh), (e.canonical_zh_hant_source, e.canonical_zh_hant),
         (e.canonical_vi_source, e.canonical_vi), (e.canonical_es_source, e.canonical_es),
         (e.canonical_id_source, e.canonical_id), (e.canonical_pt_br_source, e.canonical_pt_br)) v(src, val)
        WHERE v.src IN (` + StrongEvidenceSourceSQLList() + `) AND COALESCE(v.val,'') <> '')`,
		Baseline:  0,
		Rationale: "근거가 'strong-source' 인데 그 라벨이 붙은 값이 전부 빈칸 — 라벨≠값(회귀 가드)",
	},
	{
		// ★"포인터를 버리고 값만 쓴다" 계열 — 이 시스템에서 세 번째로 발견된 같은 결함이다.
		//   ① cand-evidence 가 기사 URL 을 버림      → 5ed2af4 로 수정
		//   ② verify 스윕이 gemma 근거 문자열을 덮음 → acccac8 로 수정
		//   ③ enrich orchestrator 가 MBID 를 버림    → 미수정(이 지표가 그것)
		// runMusicBrainz 는 FindAliases 로 아티스트를 찾아 표기를 채우면서 그 MBID 를
		// external_refs 에 남기지 않는다. 값은 'musicbrainz' 라벨을 달고 있는데 어느
		// 아티스트였는지 되짚을 수 없다. kofic 85·netflix 6·disney 1 도 같은 모양.
		// ★처방은 강등이 아니라 **ref 기록**이다 — 값의 출처는 실재하고 기록만 안 한 것이라
		// 강등하면 사실을 지우는 쪽으로 틀린다.
		Name: "api-source-no-ref",
		Where: `e.status='active' AND EXISTS (
     SELECT 1 FROM unnest(ARRAY['tmdb','kofic','kmdb','musicbrainz','netflix','disney','itunes','naver-people']) p
      WHERE p = ANY(ARRAY[e.canonical_en_source, e.canonical_ja_source, e.canonical_zh_source,
                          e.canonical_zh_hant_source, e.canonical_vi_source, e.canonical_es_source,
                          e.canonical_id_source, e.canonical_pt_br_source])
        AND NOT EXISTS (SELECT 1 FROM kwave_entity_external_refs r
                         WHERE r.entity_id=e.id AND r.provider=p))`,
		Baseline:  573,
		Rationale: "권위 API 라벨을 단 값이 있는데 그 provider ref 가 없음 — 식별자를 버렸다(되짚기 불가)",
	},
	{
		// ★위 불변식이 못 잡는 반대쪽 실수(2026-08-03). kofic_drain 은 ref 를 남기긴
		// 했는데 external_id 자리에 **영어 제목**을 넣었다(실측 30건 전부 "Star Wars"·
		// "Aladdin"·"Button"), url 은 검색 첫 페이지. 행이 존재하니 api-source-no-ref 는
		// 통과했고, 그래서 아무도 못 봤다 — 식별자 자리에 식별자 아닌 걸 넣는 실수는
		// 안 넣는 실수보다 나쁘다. 계측이 "있나?"만 물으면 "무엇이 있나?"는 영영 안 묻는다.
		// KOBIS movieCd 는 숫자열이므로 판정은 결정론이다. 수리공은 RepairKoficDecorativeRefs.
		Name: "apiref-not-identifier",
		Where: `e.status='active' AND EXISTS (
     SELECT 1 FROM kwave_entity_external_refs r
      WHERE r.entity_id=e.id AND r.provider='kofic' AND r.external_id !~ '^[0-9]+$')`,
		Baseline:  30,
		Rationale: "kofic ref 의 external_id 가 movieCd(숫자)가 아님 — 식별자 자리에 제목이 들어갔다",
	},
	{
		// ★30분은 "얼마나 오래 깨졌나" 임계가 아니라 **정상 과도기 제외**다. 승급 직후
		// verification_tier 는 비어 있고 verify 스윕(기본 10분, KDB_VERIFY_SWEEP_INTERVAL_SECONDS)
		// 이 채운다. 유예 없이 세면 방금 승급된 건이 항상 잡혀 지표가 상시 거짓이 되고,
		// 그러면 669874b(94% 오탐이던 never-examined)처럼 아무도 안 보게 된다.
		// 명제 자체는 여전히 이분법이다 — "스윕 주기를 한참 넘겼는데도 티어가 없다".
		// 도입 시 실측: 유예 없이 2건(생성 143초·130초 전, 둘 다 정상 과도기), 유예 후 0건.
		Name: "tier-missing",
		Where: `e.status='active' AND e.created_at < now() - interval '30 minutes'
   AND COALESCE(e.verification_tier,'') NOT IN ('authoritative','evidenced','unverified')`,
		Baseline:  0,
		Rationale: "승급 30분 넘게 티어 없이 서빙 — 스윕이 못 닿고 있다(회귀 가드)",
	},
}

// InvariantSnapshot — 불변식 1건의 계측 결과.
type InvariantSnapshot struct {
	Name      string
	Violations int
	Baseline  int
}

// WatchBacklogs — 조건별 백로그를 계측해 로그로 남기고 스냅샷을 반환한다.
// 데이터는 변경하지 않는다.
func WatchBacklogs(ctx context.Context, pool *pgxpool.Pool) []BacklogSnapshot {
	if pool == nil {
		return nil
	}
	out := make([]BacklogSnapshot, 0, len(backlogConditions))
	for _, c := range backlogConditions {
		q := `
SELECT count(*),
       count(*) FILTER (WHERE ` + c.StaleCol + ` < now() - make_interval(days => $1)),
       COALESCE(EXTRACT(day FROM now() - min(` + c.StaleCol + `))::int, 0)
  FROM kwave_entities e
 WHERE ` + c.Where
		var s BacklogSnapshot
		s.Name = c.Name
		if err := pool.QueryRow(ctx, q, c.WarnDays).Scan(&s.Total, &s.OverWarn, &s.OldestDays); err != nil {
			log.Printf("kdb.backlog-watch: %s: %v", c.Name, err)
			continue
		}
		out = append(out, s)
		if s.OverWarn > 0 {
			// 경보: 임계를 넘긴 항목이 있다 = 이 조건을 담당하는 레인이 백로그를 못 덮고 있다.
			log.Printf("kdb.backlog-watch: ⚠ %s total=%d over_%dd=%d oldest=%dd — %s",
				c.Name, s.Total, c.WarnDays, s.OverWarn, s.OldestDays, c.Rationale)
		} else {
			log.Printf("kdb.backlog-watch: %s total=%d oldest=%dd (임계 %dd 내)",
				c.Name, s.Total, s.OldestDays, c.WarnDays)
		}
	}
	warnDecorativeThresholds(ctx, pool)
	return out
}

// WatchInvariants — "참이면 안 되는 상태"를 세고 count>0 이면 경보한다. 나이는 보지 않는다.
// 데이터는 변경하지 않는다.
func WatchInvariants(ctx context.Context, pool *pgxpool.Pool) []InvariantSnapshot {
	if pool == nil {
		return nil
	}
	out := make([]InvariantSnapshot, 0, len(invariants))
	for _, iv := range invariants {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM kwave_entities e WHERE `+iv.Where).Scan(&n); err != nil {
			log.Printf("kdb.invariant-watch: %s: %v", iv.Name, err)
			continue
		}
		out = append(out, InvariantSnapshot{Name: iv.Name, Violations: n, Baseline: iv.Baseline})
		if n == 0 {
			log.Printf("kdb.invariant-watch: %s 위반=0 ✓", iv.Name)
			continue
		}
		// Δ 를 같이 찍는다. 큰 잔여 위반이 있어도 증감이 보이면 노이즈가 아니라 진행 지표다.
		delta := n - iv.Baseline
		sign := "+"
		if delta < 0 {
			sign, delta = "-", -delta
		}
		log.Printf("kdb.invariant-watch: ⚠ %s 위반=%d (도입시 %d, Δ%s%d) — %s",
			iv.Name, n, iv.Baseline, sign, delta, iv.Rationale)
	}
	return out
}

// warnDecorativeThresholds — ★임계가 DB 수명보다 길면 그 조건은 구조적으로 울릴 수 없다.
// 2026-08-02 에 실제로 그랬다(임계 90일 / DB 수명 70일, 세 조건). 우연히 안 울린 것과
// 울릴 수 없는 것은 다른데, 로그만 보면 둘이 똑같이 "임계 내"로 보인다.
// 이 결함 계열이 다시 생기면 스스로 드러나도록 워치가 자기 임계를 점검한다.
func warnDecorativeThresholds(ctx context.Context, pool *pgxpool.Pool) {
	var lifeDays int
	if err := pool.QueryRow(ctx,
		`SELECT COALESCE(EXTRACT(day FROM now()-min(created_at))::int, 0) FROM kwave_entities`).Scan(&lifeDays); err != nil {
		return
	}
	for _, c := range backlogConditions {
		if c.WarnDays >= 36500 { // 참고값(never-examined-raw)은 의도적 무경보라 제외.
			continue
		}
		if c.WarnDays >= lifeDays {
			log.Printf("kdb.backlog-watch: ⚠ %s 임계 %dd 가 DB 수명 %dd 이상 — 이 조건은 울릴 수 없다(장식 임계)",
				c.Name, c.WarnDays, lifeDays)
		}
	}
}

// backlogWatchInterval — 계측 주기. 지표라 자주 볼 필요 없다.
func backlogWatchInterval() time.Duration { return 6 * time.Hour }

// BacklogWatchInterval — 외부(cmd) 노출용.
func BacklogWatchInterval() time.Duration { return backlogWatchInterval() }
