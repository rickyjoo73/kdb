package kdb

// apiref_recover — api-source-no-ref 소급분 회수 레인.
//
// 무엇을 고치나: canonical_*_source 에 'musicbrainz'/'kofic' 라벨을 달고 있으면서
// 그 provider 의 external_ref 가 없는 active 엔티티(2026-08-03 실측 musicbrainz 484 ·
// kofic 85). 값의 출처는 실재하는데 **식별자만 버려진** 상태다.
//
// ★처방이 강등이 아닌 이유. 라벨은 거짓 주장이 아니라 불완전한 기록이다. 강등하면
// 실재하는 사실을 지우는 쪽으로 틀린다. 그래서 재검색해서 식별자를 **다시 찾아
// 적는다**. 앞단 누수는 별도로 막았다(orchestrator/enricher/kofic_drain, 같은 커밋).
//
// ★부수효과로 재검증이 된다. 재검색이 같은 이름으로 아티스트/영화를 확정하지 못하면
// 그 라벨은 지금 근거로 뒷받침되지 않는다는 뜻이다. 그건 "기록 누락"과 다른 사실이므로
// dataqa 대장에 'apiref-unresolved' 로 남겨 나중에 세어볼 수 있게 한다. 강등은 여기서
// 하지 않는다 — 재검색 실패는 개명·MB 등재삭제·표기변경으로도 생기고, 그 판단은
// 이 레인의 몫이 아니다.
//
// ★티어에 미치는 영향을 숨기지 않는다. musicbrainz/kofic 은 authoritativeIdentityProviders
// 라서 ref 가 생기면 다음 verify 스윕이 그 엔티티를 evidenced → authoritative 로 올린다.
// 그건 회수의 부작용이 아니라 목적이다 — 원래 권위앵커였는데 기록이 없어 한 칸 아래로
// 서빙되고 있었다. 다만 규모가 크므로 로그에 회수 건수를 남겨 사후에 세도록 했다.

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb/kofic"
	"github.com/rickyjoo73/kdb/internal/kdb/musicbrainz"
)

// apiRefRecoverCooldown — 재검색 실패분 재조회 간격. 실패는 대개 지속적이라
// 짧게 잡으면 레인이 같은 건을 계속 갈아 신규 유입분을 못 본다.
const apiRefRecoverCooldown = "30 days"

// missingRefQuery — provider 라벨은 있는데 그 provider ref 가 없는 active 엔티티.
// {{provider}} 는 코드 상수만 들어간다(호출부 두 곳 모두 리터럴).
func missingRefQuery(provider, cooldownField string) string {
	return `
SELECT e.id::text, e.canonical_ko
  FROM kwave_entities e
 WHERE e.status='active'
   AND COALESCE(e.canonical_ko,'') <> ''
   AND '` + provider + `' = ANY(ARRAY[e.canonical_en_source, e.canonical_ja_source,
        e.canonical_zh_source, e.canonical_zh_hant_source, e.canonical_vi_source,
        e.canonical_es_source, e.canonical_id_source, e.canonical_pt_br_source])
   AND NOT EXISTS (SELECT 1 FROM kwave_entity_external_refs r
                    WHERE r.entity_id=e.id AND r.provider='` + provider + `')
   AND NOT EXISTS (SELECT 1 FROM kwave_kdb_enrich_attempts a
                    WHERE a.entity_id=e.id AND a.field='` + cooldownField + `'
                      AND a.last_attempt_at > now() - interval '` + apiRefRecoverCooldown + `')
 ORDER BY e.created_at DESC
 LIMIT $1`
}

type refTarget struct{ id, ko string }

func loadRefTargets(ctx context.Context, pool *pgxpool.Pool, q string, limit int) []refTarget {
	rows, err := pool.Query(ctx, q, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []refTarget
	for rows.Next() {
		var t refTarget
		if rows.Scan(&t.id, &t.ko) == nil && strings.TrimSpace(t.ko) != "" {
			out = append(out, t)
		}
	}
	return out
}

// markRefAttempt — 쿨다운 기록. 조회 **전에** 찍는다 — 중간에 죽어도 같은 건을
// 무한 재시도하지 않는다.
func markRefAttempt(ctx context.Context, pool *pgxpool.Pool, id, field, source string) {
	_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_enrich_attempts (entity_id, field, attempts, last_attempt_at, last_source)
VALUES ($1::uuid,$2,1,now(),$3)
ON CONFLICT (entity_id, field) DO UPDATE
   SET attempts=kwave_kdb_enrich_attempts.attempts+1, last_attempt_at=now(), last_source=EXCLUDED.last_source`,
		id, field, source)
}

// markRefUnresolved — 재검색으로 식별자를 되찾지 못한 건을 대장에 남긴다. 강등하지
// 않는다(위 주석 참조) — 세어보기 위한 기록이다.
func markRefUnresolved(ctx context.Context, pool *pgxpool.Pool, id, provider, ko string) {
	_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_dataqa_log (entity_id, locale, old_value, old_source, verdict, reason, model)
VALUES ($1::uuid, '-', $2, $3, 'apiref-unresolved', $4, 'apiref-recover')`,
		id, ko, provider,
		provider+" 라벨은 있으나 재검색으로 식별자 확인 실패 — 기록 누락이 아니라 근거 미확인")
}

// RecoverMusicBrainzRefs — musicbrainz 라벨 보유 · ref 없는 active 를 재검색해
// MBID 를 적재한다. 반환 (회수, 미확인, 시도).
func RecoverMusicBrainzRefs(ctx context.Context, pool *pgxpool.Pool, cl *musicbrainz.Client, limit int) (recovered, unresolved, checked int) {
	if pool == nil || cl == nil || limit <= 0 {
		return 0, 0, 0
	}
	targets := loadRefTargets(ctx, pool, missingRefQuery("musicbrainz", "mbref-recover"), limit)
	for _, t := range targets {
		if ctx.Err() != nil {
			break
		}
		checked++
		markRefAttempt(ctx, pool, t.id, "mbref-recover", "musicbrainz")
		// FindAliases 는 정규화 이름 일치 가드를 통과한 후보만 돌려준다 — 값을 만들
		// 때 쓴 것과 같은 판정이라 회수가 판정을 느슨하게 만들지 않는다.
		mbid, _, err := cl.FindAliases(ctx, t.ko)
		if err != nil {
			continue // transient — 쿨다운이 있으니 30일 뒤 재시도
		}
		if mbid == "" {
			unresolved++
			markRefUnresolved(ctx, pool, t.id, "musicbrainz", t.ko)
			continue
		}
		if RecordAPIRef(ctx, pool, t.id, "musicbrainz", mbid, musicbrainz.ArtistURL(mbid), 0.800) {
			recovered++
			log.Printf("kdb.apiref-recover[musicbrainz]: %q <- %s", t.ko, mbid)
		}
	}
	if checked > 0 {
		log.Printf("kdb.apiref-recover[musicbrainz]: 시도=%d 회수=%d 미확인=%d", checked, recovered, unresolved)
	}
	return recovered, unresolved, checked
}

// RecoverKoficRefs — kofic 라벨 보유 · ref 없는 active 영화를 재검색해 movieCd 를
// 적재한다. 반환 (회수, 미확인, 시도).
func RecoverKoficRefs(ctx context.Context, pool *pgxpool.Pool, cl *kofic.Client, key string, limit int) (recovered, unresolved, checked int) {
	if pool == nil || cl == nil || strings.TrimSpace(key) == "" || limit <= 0 {
		return 0, 0, 0
	}
	targets := loadRefTargets(ctx, pool, missingRefQuery("kofic", "koficref-recover"), limit)
	for _, t := range targets {
		if ctx.Err() != nil {
			break
		}
		checked++
		markRefAttempt(ctx, pool, t.id, "koficref-recover", "kofic")
		_, movieCd, err := cl.Enrich(ctx, key, t.ko)
		time.Sleep(500 * time.Millisecond) // KOFIC 예의(kofic_drain 과 동일)
		if err != nil {
			continue
		}
		if movieCd == "" {
			unresolved++
			markRefUnresolved(ctx, pool, t.id, "kofic", t.ko)
			continue
		}
		if RecordAPIRef(ctx, pool, t.id, "kofic", movieCd, kofic.MovieURL(movieCd), 0.800) {
			recovered++
			log.Printf("kdb.apiref-recover[kofic]: %q <- %s", t.ko, movieCd)
		}
	}
	if checked > 0 {
		log.Printf("kdb.apiref-recover[kofic]: 시도=%d 회수=%d 미확인=%d", checked, recovered, unresolved)
	}
	return recovered, unresolved, checked
}

// RepairKoficDecorativeRefs — external_id 에 식별자가 아니라 **영어 제목**이 들어간
// 기존 kofic ref(2026-08-03 실측 30건 전부)를 movieCd 로 교정한다.
//
// 이 계열은 api-source-no-ref 가 못 잡는다 — 행이 존재하니 통과한다. 불변식
// 'apiref-not-identifier' 를 따로 세운 이유이고, 이 함수가 그 상대편 수리공이다.
// 재검색 실패 시 행을 지우지 않는다(지우면 통과하던 게 위반으로 바뀌어 신호가 뒤집힌다).
func RepairKoficDecorativeRefs(ctx context.Context, pool *pgxpool.Pool, cl *kofic.Client, key string, limit int) (repaired, checked int) {
	if pool == nil || cl == nil || strings.TrimSpace(key) == "" || limit <= 0 {
		return 0, 0
	}
	rows, err := pool.Query(ctx, `
SELECT e.id::text, e.canonical_ko
  FROM kwave_entity_external_refs r
  JOIN kwave_entities e ON e.id=r.entity_id
 WHERE r.provider='kofic' AND r.external_id !~ '^[0-9]+$'
   AND COALESCE(e.canonical_ko,'') <> ''
   AND NOT EXISTS (SELECT 1 FROM kwave_kdb_enrich_attempts a
                    WHERE a.entity_id=e.id AND a.field='koficref-repair'
                      AND a.last_attempt_at > now() - interval '`+apiRefRecoverCooldown+`')
 LIMIT $1`, limit)
	if err != nil {
		return 0, 0
	}
	var targets []refTarget
	for rows.Next() {
		var t refTarget
		if rows.Scan(&t.id, &t.ko) == nil {
			targets = append(targets, t)
		}
	}
	rows.Close()

	for _, t := range targets {
		if ctx.Err() != nil {
			break
		}
		checked++
		markRefAttempt(ctx, pool, t.id, "koficref-repair", "kofic")
		_, movieCd, err := cl.Enrich(ctx, key, t.ko)
		time.Sleep(500 * time.Millisecond)
		if err != nil || movieCd == "" {
			continue
		}
		if RecordAPIRef(ctx, pool, t.id, "kofic", movieCd, kofic.MovieURL(movieCd), 0.800) {
			repaired++
			log.Printf("kdb.apiref-repair[kofic]: %q 제목→코드 %s", t.ko, movieCd)
		}
	}
	if checked > 0 {
		log.Printf("kdb.apiref-repair[kofic]: 시도=%d 교정=%d", checked, repaired)
	}
	return repaired, checked
}
