package disambiguator

// repair.go — 과거 병합이 버린 신원 근거를 되찾아 승자에게 옮기는 **일회성 백필**.
//
// ★배경(2026-08-15): applyMerge 가 패자를 은퇴시키면서 `aliases_ko` 와 person_details 만
// 옮기고 **외부 식별자·source_urls·로케일 표기·검증등급을 패자와 함께 죽였다.** 승자는
// LLM 의 `same_as` 가 지목하고 가드는 `wellFormed` 하나뿐이라, 근거 없는 쪽이 승자로
// 뽑히면 근거가 통째로 사라진다. 실측:
//
//	MONSTA X  (urls 9 · wikidata ref 1 · authoritative) → rejected
//	몬스타엑스 (urls 9 · ref 1)                          → rejected
//	몬스타X   (0 · 0 · unverified)                       → active(서빙 중)
//
// 재발 방지는 applyMerge 안의 carryEvidence 가 담당한다. 이 파일은 **이미 벌어진 것**을
// 되돌린다. 규칙을 SQL 로 다시 쓰지 않고 같은 carryEvidence 를 부르는 게 요점이다 —
// 규칙을 손으로 두 번 쓰면 한쪽이 갈라진다(이 저장소가 반복해서 밟은 계열).
//
// 연결고리는 `aliases_ko` 다. applyMerge 가 패자의 canonical_ko 를 승자의 aliases_ko 에
// 넣지만, 별칭 일치만으로 과거 병합 관계를 확정하지는 않는다(동명이인 보호).

import (
	"context"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// mergePair — 병합 한 쌍(패자 → 승자).
type mergePair struct {
	loserID, winnerID     uuid.UUID
	loserKo, winnerKo     string
	loserRefs, loserURLs  int
	winnerRefs, winnerURL int
}

// RepairMergedEvidence — 병합에서 버려진 근거를 승자로 옮긴다.
//
// dry=true 면 대상만 세고 쓰지 않는다. 반환 (복구, 조사).
// 별칭 일치는 조사 후보일 뿐이다. 실제 쓰기에는 같은 안정 식별자가 필요하다.
// dry-run 수량은 후보 수이며 검증을 통과한 복구 수가 아니다.
func RepairMergedEvidence(ctx context.Context, pool *pgxpool.Pool, limit int, dry bool) (repaired, scanned int) {
	if pool == nil {
		return 0, 0
	}
	if limit <= 0 {
		limit = 200
	}
	rows, err := pool.Query(ctx, `
SELECT l.id, w.id, l.canonical_ko, w.canonical_ko,
       (SELECT count(*) FROM kwave_entity_external_refs r WHERE r.entity_id=l.id),
       COALESCE(array_length(l.source_urls,1),0),
       (SELECT count(*) FROM kwave_entity_external_refs r WHERE r.entity_id=w.id),
       COALESCE(array_length(w.source_urls,1),0)
  FROM kwave_entities w
  JOIN kwave_entities l
    ON l.canonical_ko = ANY(w.aliases_ko) AND l.status='rejected' AND l.id <> w.id
 WHERE w.status='active' AND w.operator_locked = false
   -- 패자가 실제로 뭔가 들고 있을 때만. 빈 패자는 옮길 게 없다.
   AND ((SELECT count(*) FROM kwave_entity_external_refs r WHERE r.entity_id=l.id) > 0
        OR COALESCE(array_length(l.source_urls,1),0) > 0)
 -- 승자가 가난한 것부터 — 서빙 중인데 근거가 없는 건이 가장 급하다.
 ORDER BY ((SELECT count(*) FROM kwave_entity_external_refs r WHERE r.entity_id=w.id)
           + COALESCE(array_length(w.source_urls,1),0)) ASC
 LIMIT $1`, limit)
	if err != nil {
		log.Printf("kdb.disambig-repair: 조회: %v", err)
		return 0, 0
	}
	var pairs []mergePair
	for rows.Next() {
		var p mergePair
		if rows.Scan(&p.loserID, &p.winnerID, &p.loserKo, &p.winnerKo,
			&p.loserRefs, &p.loserURLs, &p.winnerRefs, &p.winnerURL) == nil {
			pairs = append(pairs, p)
		}
	}
	rows.Close()

	for _, p := range pairs {
		scanned++
		if dry {
			log.Printf("kdb.disambig-repair[dry]: %q(ref %d·url %d, rejected) → %q(ref %d·url %d, active)",
				p.loserKo, p.loserRefs, p.loserURLs, p.winnerKo, p.winnerRefs, p.winnerURL)
			repaired++
			continue
		}
		if err := repairEvidenceAtomically(ctx, pool, p); err != nil {
			log.Printf("kdb.disambig-repair: skipped %s → %s: %v", p.loserID, p.winnerID, err)
			continue
		}
		repaired++
		log.Printf("kdb.disambig-repair: %q → %q 근거 승계", p.loserKo, p.winnerKo)
	}
	log.Printf("kdb.disambig-repair: done repaired=%d /%d (dry=%v)", repaired, scanned, dry)
	return repaired, scanned
}

func repairEvidenceAtomically(ctx context.Context, pool *pgxpool.Pool, p mergePair) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, err := beginMergeTransaction(ctx, pool)
	if err != nil {
		return err
	}
	defer rollbackMerge(tx)
	if err := lockMergePair(ctx, tx, p.loserID, p.winnerID, true); err != nil {
		return err
	}
	ms, err := readMembers(ctx, tx, []uuid.UUID{p.loserID, p.winnerID})
	if err != nil {
		return err
	}
	byID := map[uuid.UUID]member{}
	for _, m := range ms {
		byID[m.id] = m
	}
	if err := mergeEvidenceGate(ctx, tx, byID[p.loserID], byID[p.winnerID]); err != nil {
		return err
	}
	if err := carryEvidence(ctx, tx, p.loserID, p.winnerID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE kwave_entities SET
 notes=CASE WHEN position($2 IN COALESCE(notes,''))>0 THEN notes ELSE
 COALESCE(NULLIF(notes,'') || ' · ','') || $2 END WHERE id=$1`, p.winnerID,
		"[merge-evidence-repair] source_entity="+p.loserID.String())
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
