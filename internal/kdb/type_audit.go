package kdb

// type_audit — 저장된 외부 근거와 entity_type 이 서로 어긋나는 항목을 **결정적으로**
// 찾아 표식만 단다. LLM 을 쓰지 않고, 타입을 바꾸지도 않는다.
//
// ★배경: 기존 타입 재검사(verify/type_retrace.go)는 대상을 **유입 경로**로 고른다 —
// active 는 `precheck_reason LIKE 'auto_evidence%'` 로 들어온 것만 본다. 그래서 다른
// 경로로 들어온 active 오분류는 영구히 재검사되지 않는다(실측: 하츄핑이 애니메이션
// 캐릭터인데 entity_type='person' 으로 서빙 중). 전수 패널은 active 10,254 × LLM 이라
// 비현실적이므로, **공짜 신호로 후보를 좁히고 판정은 기존 패널에 넘긴다.**
//
// ★역할 분리(중요): 이 감사는 "의심"만 표시한다. 실제 타입 교정은 두 모델 합의를 요구하는
// recheck-active 패널이 한다. 결정적 신호도 틀릴 수 있어서다 — 예를 들어 배우가 자기
// 이름의 다큐를 갖고 있으면 person 에 movie ref 가 정당하게 붙는다. 여기서 타입을 바로
// 바꾸면 그런 정상 케이스를 깨뜨린다("빈칸>틀린값" 과 같은 원칙).

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// typeAuditRule — 결정적 불일치 규칙. where 는 kwave_entities e 기준이며 반드시
// "근거가 명시적으로 다른 타입을 가리킬 때"만 참이어야 한다(추정 금지).
type typeAuditRule struct {
	Name  string
	Where string
	Note  string
}

var typeAuditRules = []typeAuditRule{
	{
		// TMDb url 은 /movie/ 와 /tv/ 로 매체를 구분한다. movie ref 인데 drama/show 이거나
		// tv ref 인데 movie 면 둘 중 하나가 틀렸다.
		Name: "tmdb-media",
		Where: `EXISTS (SELECT 1 FROM kwave_entity_external_refs r
		         WHERE r.entity_id=e.id AND r.provider='tmdb'
		           AND ( (r.url LIKE '%/movie/%' AND e.entity_type::text IN ('drama','show'))
		              OR (r.url LIKE '%/tv/%'    AND e.entity_type::text = 'movie') ))`,
		Note: "TMDb url 의 매체 종류가 저장 타입과 불일치",
	},
	{
		// iTunes 는 곡/음반 카탈로그다. 음반류가 아닌데 붙어 있으면 근거가 다른 것을 가리킨다.
		Name:  "itunes-nonalbum",
		Where: `EXISTS (SELECT 1 FROM kwave_entity_external_refs r WHERE r.entity_id=e.id AND r.provider='itunes') AND e.entity_type::text NOT IN ('song_album','person','group')`,
		Note:  "iTunes(곡/음반) 근거를 가진 비-음반 타입",
	},
	{
		// wikidata description 이 **작품 자체**를 서술하는데 person 으로 저장된 경우.
		//
		// ★2026-07-31 즉시 정정: 처음엔 `(film|movie|album|song|band|...)` 부분매칭으로
		// 썼다가 27건을 오탐했다 — 아이유("singer, songwriter and actress")는 songwriter 의
		// song 에, RM("leader of the boy band BTS")은 boy band 의 band 에, 연상호("film
		// director")는 film 에 걸렸다. 전부 정상 person 이다. isKEntertainerDesc 주석이
		// 경고한 "모호한 어근" 함정에 그대로 빠진 것. 표시분은 전건 회수했다.
		//
		// 올바른 판별은 어휘가 아니라 **서술 대상**이다. 인물 설명은 직업을 말하고
		// ("South Korean singer"), 작품 설명은 사물을 말한다("2022 South Korean TV series",
		// "album by BTS"). 그래서 ①연도로 시작 ②"... by <아티스트>" ③명시적 가공인물
		// ④작품 종류 명사구 일 때만 잡는다. 전부 \m\M 단어경계로 고정.
		// 실측: 이 조건으로 person 27건 오탐 → 0건, 진짜 1건(인사이더=2022 TV series) 검출.
		Name: "wikidata-desc-person",
		Where: `e.entity_type::text='person' AND EXISTS (
		         SELECT 1 FROM kwave_entity_external_refs r
		          WHERE r.entity_id=e.id AND r.provider='wikidata'
		            AND COALESCE(r.raw_payload->>'description','') <> ''
		            AND ( r.raw_payload->>'description' ~ '^[0-9]{4}\M'
		               OR r.raw_payload->>'description' ~* '\m(album|song|single|EP|film|series) by\M'
		               OR r.raw_payload->>'description' ~* '\mfictional character\M'
		               OR r.raw_payload->>'description' ~* '\m(television series|film series|manhwa|webtoon)\M' ) )`,
		Note: "wikidata 설명이 인물이 아니라 작품 자체를 서술",
	},
}

// DrainTypeConsistencyAudit — 규칙별 불일치를 찾아 `[typeaudit:mismatch:<규칙>]` 표식을
// 단다. 이미 표시했거나 판정이 끝난([typeaudit:cleared]) 항목은 건너뛴다.
// 반환=(새로 표시한 수).
func DrainTypeConsistencyAudit(ctx context.Context, pool *pgxpool.Pool, limit int) (flagged int) {
	if pool == nil {
		return 0
	}
	if limit <= 0 {
		limit = 200
	}
	for _, rule := range typeAuditRules {
		tag, err := pool.Exec(ctx, `
UPDATE kwave_entities e
   SET notes = COALESCE(NULLIF(e.notes,'') || ' · ','') || $1,
       updated_at = now()
 WHERE e.id IN (
   SELECT e2.id FROM kwave_entities e2
    WHERE e2.status IN ('active','candidate')
      AND e2.operator_locked = false
      AND COALESCE(e2.notes,'') NOT LIKE '%[typeaudit:%'
      AND `+strings.ReplaceAll(rule.Where, "e.", "e2.")+`
    LIMIT $2)`,
			fmt.Sprintf("[typeaudit:mismatch:%s] %s", rule.Name, rule.Note), limit)
		if err != nil {
			log.Printf("kdb.type-audit: %s: %v", rule.Name, err)
			continue
		}
		n := int(tag.RowsAffected())
		if n > 0 {
			flagged += n
			log.Printf("kdb.type-audit: %s → %d건 표시", rule.Name, n)
		}
	}
	return flagged
}
