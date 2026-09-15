package kdb

// kana_surname_audit — **일본어 칸에 다른 사람의 표기가 박힌 것**을 성씨로 잡는다.
//
// ★왜 성씨인가, 왜 가나인가 (2026-09-15).
//   같은 외국어 값이 여러 대상에 붙은 것을 세어 보다가 이것을 봤다:
//
//     ja  ハ・ジョンウ    김성훈 · 하정우 · 하종우 · 하지우
//     ja  キム・ソヒョン   김서형 · 김소현 · 김소현(뮤지컬)
//     ja  ソラ          김용선 · 김현정 · 설아 · 소라 · 솔라
//
//   김성훈에게 **하정우의 일본어 표기**가 붙어 있다. 그런데 "같은 값이 여럿에 붙었다"만
//   으로는 오염이라 말할 수 없다 — GD·권지용·지드래곤은 같은 사람이라 값이 같은 게 맞다.
//
//   가르는 근거가 하나 있다. **가나는 음역이라 성씨가 1:1 이다.** 김→キム, 하→ハ 는
//   변종이 없다. (한자는 안 된다 — 강은 姜·康·強 이 다 쓰이고, 간체·번체까지 갈린다.
//   실제로 한자 성씨표로 재려다 1,287건을 잡았는데 대부분이 내 표가 틀린 것이었다.)
//
//   그래서 이 감사는 **가나만** 본다. 규칙은 NameToKatakana 를 그대로 쓴다 — 표를 따로
//   만들면 그 표가 틀리는 것이 이 파일이 잡으려는 오염보다 흔하다.
//
// ★기본 dry-run. 무엇이 바뀌는지 먼저 눈으로 본다.

import (
	"context"
	"log"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// KanaSurnameMismatch — 한 건의 어긋남.
type KanaSurnameMismatch struct {
	ID, KO, JA, Source, WantJA string
}

// KanaSurnameResult — 감사 결과.
type KanaSurnameResult struct {
	Checked, Mismatched, Cleared int
	BySource                     map[string]int
	Samples                      []KanaSurnameMismatch
}

// kanaSurnameOf — "キム・ソヒョン" → "キム". 구분점이 없으면 빈 문자열(성씨를 못 가름).
func kanaSurnameOf(ja string) string {
	ja = strings.TrimSpace(ja)
	if i := strings.Index(ja, "・"); i > 0 {
		return ja[:i]
	}
	return ""
}

// AuditKanaSurnames — ja 칸의 성씨가 canonical_ko 의 성씨와 어긋나는 행을 찾는다.
//
// dry=false 면 그 칸을 **비운다.** 값을 지어내 채우지 않는다 — 어긋났다는 것만 알지
// 무엇이 맞는지는 모르기 때문이다(D-37). 빈칸은 kana-rule 드레인이 규칙으로 다시 채운다.
func AuditKanaSurnames(ctx context.Context, pool *pgxpool.Pool, limit int, dry bool) KanaSurnameResult {
	r := KanaSurnameResult{BySource: map[string]int{}}
	if pool == nil || limit <= 0 {
		return r
	}
	rows, err := pool.Query(ctx, `
SELECT id::text, canonical_ko, canonical_ja, COALESCE(canonical_ja_source,'')
  FROM kwave_entities
 WHERE status='active' AND entity_type='person' AND operator_locked=false
   AND canonical_ko ~ '^[가-힣]{2,5}$'
   AND COALESCE(canonical_ja,'') <> ''
   AND canonical_ja LIKE '%・%'
 ORDER BY canonical_ko
 LIMIT $1`, limit)
	if err != nil {
		log.Printf("kdb.kana-audit: select: %v", err)
		return r
	}
	type row struct{ id, ko, ja, src string }
	var items []row
	for rows.Next() {
		var it row
		if rows.Scan(&it.id, &it.ko, &it.ja, &it.src) == nil {
			items = append(items, it)
		}
	}
	rows.Close()

	for _, it := range items {
		want := NameToKatakana(it.ko)
		wantSur, gotSur := kanaSurnameOf(want), kanaSurnameOf(it.ja)
		// 규칙이 성씨를 못 가른 이름(예명·복성 밖)은 판정하지 않는다 — 근거가 없다.
		if wantSur == "" || gotSur == "" {
			continue
		}
		r.Checked++
		if wantSur == gotSur {
			continue
		}
		r.Mismatched++
		r.BySource[it.src]++
		if len(r.Samples) < 40 {
			r.Samples = append(r.Samples, KanaSurnameMismatch{
				ID: it.id, KO: it.ko, JA: it.ja, Source: it.src, WantJA: want})
		}
		log.Printf("  %-12s ja=%-20s (%s)  성씨 %s≠%s  규칙값 %s",
			it.ko, it.ja, it.src, gotSur, wantSur, want)
		if dry {
			continue
		}
		// 값을 지어내지 않는다. 비운다 — kana-rule 드레인이 규칙으로 다시 채운다.
		if _, err := pool.Exec(ctx, `
UPDATE kwave_entities SET canonical_ja=NULL, canonical_ja_source=NULL, updated_at=now()
 WHERE id=$1 AND canonical_ja=$2`, it.id, it.ja); err == nil {
			r.Cleared++
			recordKanaSurnameClear(ctx, pool, it.id, it.ko, it.ja, it.src)
		}
	}
	return r
}

func recordKanaSurnameClear(ctx context.Context, pool *pgxpool.Pool, id, ko, ja, src string) {
	_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_recheck_log (entity_id, term_ko, verdict, evidence, created_at)
VALUES ($1,$2,'kana-surname-clear',$3,now())`,
		id, ko, "ja="+ja+" source="+src+" — 성씨가 canonical_ko 와 어긋나 비움")
}
