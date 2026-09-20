// Package commonnoun — **일반명사 구간.**
//
// ★무엇을 푸는가 (2026-09-20). 지금까지 «일반명사다»라는 판정은 적을 자리가 없어
// notes 라는 자유 문장에 섞여 들어갔다. 그런데 «연예가 아니다»(0143 으로 죽은 명제)
// 도 같은 문장에 같은 낱말로 적힌다 — 모델이 이렇게 쓰기 때문이다:
//
//	무지개: "…일반 명사로서의 '무지개'를 다루고 있으며 **K-콘텐츠**(song_album)와 무관"
//
// 죽은 명제를 되살리는 레인이 낱말로 고르니 살아 있는 판정까지 걷어 갔다. 실측으로
// 396건 중 118건이 일반명사 사유였고 43건은 **걷힘 → 재판정 → 같은 결론** 왕복을
// 마쳤다. 낱말이 아니라 **자리**로 갈라야 끝난다.
//
// ★그래서 일반명사는 «기각된 엔티티»가 아니라 **등재된 일반명사**다.
//
//	등재    같은 낱말이 다시 들어오면 LLM 없이 즉답한다(hit_count 로 수요도 보인다)
//	배제    범위 회수 레인이 이 목록에 있는 것은 건드리지 않는다
//	되돌림  고유명사로 밝혀지면 Revoke — «가끔 일반명사도 올라온다»의 반대쪽
//
// ★이 패키지는 internal/kdb 를 import 하지 않는다. kdb 와 kdb/verify 양쪽이 이것을
// 쓰기 때문이다(순환 금지).
package commonnoun

import (
	"context"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Table — 원장 테이블 이름(한 자리).
const Table = "kwave_kdb_common_nouns"

// Kind — 왜 범위 밖인가. **살아 있는 명제만** 담는다 — «연예가 아니다»는 여기 없다.
const (
	KindCommonNoun     = "common_noun"     // 일반 명사·일상어·부사 (무지개·왠지·헤엄)
	KindCategory       = "category"        // 범주어·장르어 (배우·아이돌·컴백)
	KindProduct        = "product"         // 제품·상품명 (갤럭시 S25)
	KindForeignSubject = "foreign_subject" // 해외 대상 (블리즈컨·오가와 마코토)
)

// ValidKind — 알 수 없는 종류를 원장에 넣지 않는다.
func ValidKind(k string) bool {
	switch k {
	case KindCommonNoun, KindCategory, KindProduct, KindForeignSubject:
		return true
	}
	return false
}

// Entry — 등재 1건.
type Entry struct {
	Key        string
	Surface    string
	Kind       string
	Reason     string
	DecidedBy  string
	Evidence   string
	EntityID   string // uuid 문자열. 빈 문자열이면 원장 행과 무관한 등재.
	Status     string
	HitCount   int
	FirstSeen  time.Time
	LastSeen   time.Time
	RevokedWhy string
}

// Key — 정규화 키. **인테이크 정규화와 같은 규칙**이다(공백·문장부호 제거 + 소문자).
//
// 같은 규칙을 SQL 로 쓸 일이 많아 NormKeySQL 이 짝으로 있다. 두 벌이 갈라지면
// Go 가 등재한 것을 SQL 이 못 찾는다 — 그래서 시험이 둘을 대조한다.
func Key(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// NormKeySQL — Key() 와 같은 뜻의 SQL 식. expr 은 컬럼/식(예: "e.canonical_ko").
func NormKeySQL(expr string) string {
	return `lower(regexp_replace(btrim(` + expr + `), '[[:space:][:punct:]]+', '', 'g'))`
}

// NotListedSQL — «이 낱말은 일반명사 구간에 없다»는 조건. 회수·되살림 레인이 쓴다.
//
// ★이것이 이 패키지의 핵심 쓰임이다. 범위를 넓혔다고 되살릴 때, 되살리면 안 되는
// 것을 **문구가 아니라 원장으로** 가른다.
func NotListedSQL(koExpr string) string {
	return `NOT EXISTS (SELECT 1 FROM ` + Table + ` cn
                   WHERE cn.status='confirmed' AND cn.norm_key = ` + NormKeySQL(koExpr) + `)`
}

// Record — 등재(멱등). 이미 있으면 last_seen_at·hit_count 만 올리고 사유는 보존한다.
//
// ★되돌린 것은 다시 세우지 않는다. status='revoked' 인 낱말에 같은 판정이 또 와도
// confirmed 로 돌리지 않는다 — 사람이 «이건 고유명사다»라고 판단한 것을 기계가
// 매일 밤 뒤집으면 그 판단은 없는 것과 같다. 다시 세우려면 Reinstate 를 쓴다.
func Record(ctx context.Context, pool *pgxpool.Pool, e Entry) (bool, error) {
	if pool == nil {
		return false, nil
	}
	key := Key(e.Surface)
	if key == "" {
		return false, nil
	}
	kind := e.Kind
	if !ValidKind(kind) {
		kind = KindCommonNoun
	}
	var eid any
	if strings.TrimSpace(e.EntityID) != "" {
		eid = strings.TrimSpace(e.EntityID)
	}
	var inserted bool
	err := pool.QueryRow(ctx, `
INSERT INTO `+Table+` (norm_key, surface, kind, reason, decided_by, evidence, entity_id)
VALUES ($1,$2,$3,$4,$5,$6,$7::uuid)
ON CONFLICT (norm_key) DO UPDATE
   SET hit_count    = `+Table+`.hit_count + 1,
       last_seen_at = now(),
       -- 사유가 비어 있던 옛 행에만 채운다. 이미 적힌 판단을 덮지 않는다.
       reason       = CASE WHEN COALESCE(`+Table+`.reason,'')='' THEN EXCLUDED.reason
                           ELSE `+Table+`.reason END,
       entity_id    = COALESCE(`+Table+`.entity_id, EXCLUDED.entity_id)
RETURNING (xmax = 0)`, key, strings.TrimSpace(e.Surface), kind,
		trunc(e.Reason, 300), e.DecidedBy, trunc(e.Evidence, 500), eid).Scan(&inserted)
	if err != nil {
		return false, err
	}
	return inserted, nil
}

// Lookup — 등재돼 있나(confirmed 만). 두 번째 값이 false 면 «이 구간에 없다».
func Lookup(ctx context.Context, pool *pgxpool.Pool, term string) (Entry, bool) {
	if pool == nil {
		return Entry{}, false
	}
	key := Key(term)
	if key == "" {
		return Entry{}, false
	}
	var e Entry
	err := pool.QueryRow(ctx, `
SELECT norm_key, surface, kind, reason, decided_by, status, hit_count, first_seen_at, last_seen_at
  FROM `+Table+` WHERE norm_key=$1 AND status='confirmed'`, key).Scan(
		&e.Key, &e.Surface, &e.Kind, &e.Reason, &e.DecidedBy, &e.Status,
		&e.HitCount, &e.FirstSeen, &e.LastSeen)
	if err != nil {
		return Entry{}, false
	}
	return e, true
}

// Touch — 같은 낱말이 또 들어왔다는 것만 기록(수요 관측). 없으면 아무 일도 안 한다.
func Touch(ctx context.Context, pool *pgxpool.Pool, term string) {
	if pool == nil {
		return
	}
	if key := Key(term); key != "" {
		_, _ = pool.Exec(ctx, `UPDATE `+Table+`
			SET hit_count = hit_count + 1, last_seen_at = now()
			WHERE norm_key=$1 AND status='confirmed'`, key)
	}
}

// Revoke — «실은 고유명사였다». 지우지 않고 사유를 남긴 채 내린다 — 왜 내렸는지가
// 다음 판정의 근거이기 때문이다.
func Revoke(ctx context.Context, pool *pgxpool.Pool, term, by, why string) (bool, error) {
	if pool == nil {
		return false, nil
	}
	key := Key(term)
	if key == "" {
		return false, nil
	}
	tag, err := pool.Exec(ctx, `
UPDATE `+Table+`
   SET status='revoked', revoked_reason=$2, revoked_by=$3, last_seen_at=now()
 WHERE norm_key=$1 AND status='confirmed'`, key, trunc(why, 300), by)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// Reinstate — 되돌림을 다시 세운다(운영자 전용 경로).
func Reinstate(ctx context.Context, pool *pgxpool.Pool, term, by string) (bool, error) {
	if pool == nil {
		return false, nil
	}
	key := Key(term)
	if key == "" {
		return false, nil
	}
	tag, err := pool.Exec(ctx, `
UPDATE `+Table+`
   SET status='confirmed', revoked_reason='', revoked_by=$2, last_seen_at=now()
 WHERE norm_key=$1 AND status='revoked'`, key, by)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// Stats — 구간 규모(대시보드·로그용).
type Stats struct {
	Confirmed, Revoked, Hits int
}

// Count — 등재 규모.
func Count(ctx context.Context, pool *pgxpool.Pool) Stats {
	var s Stats
	if pool == nil {
		return s
	}
	_ = pool.QueryRow(ctx, `
SELECT count(*) FILTER (WHERE status='confirmed'),
       count(*) FILTER (WHERE status='revoked'),
       COALESCE(sum(hit_count) FILTER (WHERE status='confirmed'),0)
  FROM `+Table).Scan(&s.Confirmed, &s.Revoked, &s.Hits)
	return s
}

// List — 최근 등재 n건(운영 화면·CLI).
func List(ctx context.Context, pool *pgxpool.Pool, status string, limit int) []Entry {
	if pool == nil || limit <= 0 {
		return nil
	}
	if status == "" {
		status = "confirmed"
	}
	rows, err := pool.Query(ctx, `
SELECT norm_key, surface, kind, reason, decided_by, status, hit_count, first_seen_at, last_seen_at,
       COALESCE(revoked_reason,'')
  FROM `+Table+`
 WHERE status=$1 ORDER BY hit_count DESC, last_seen_at DESC LIMIT $2`, status, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		if rows.Scan(&e.Key, &e.Surface, &e.Kind, &e.Reason, &e.DecidedBy, &e.Status,
			&e.HitCount, &e.FirstSeen, &e.LastSeen, &e.RevokedWhy) == nil {
			out = append(out, e)
		}
	}
	return out
}

// ErrNoRows — 호출자가 pgx 를 직접 import 하지 않게 재노출.
var ErrNoRows = pgx.ErrNoRows

func trunc(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// ─── 이미 내려져 있던 판정 모으기 ───────────────────────────────────────────

// BackfillResult — 한 번 돈 결과.
type BackfillResult struct {
	Checked, Recorded int
	Samples           []string
}

// BackfillFromNotes — 원장에 **이미 적혀 있는** 일반명사 판정을 이 구간으로 옮긴다.
//
// ★왜 필요한가. 구간을 만들어도 비어 있으면 아무것도 막지 못한다. 판정기는 이미
// 수백 건을 «일반 명사다»라고 판정해 두었는데 그 판정이 자유 문장 속에만 있다 —
// 오늘 그 문장을 낱말로 읽으려다 118건을 잘못 되살렸다. 문장을 한 번만 더 읽어서
// 자리로 옮기고, 그다음부터는 자리만 본다.
//
// ★**서빙 중인 행(active)은 건드리지 않는다.** 오거부가 최상위 금칙이다. 지금
// 서빙되는 이름이 일반명사로 보인다면 그건 사람이 볼 일이지 일괄 처리할 일이 아니다.
//
// ★decided_by='backfill' 로 남긴다 — 판정기가 직접 등재한 것과 구분돼야 나중에
// «이 줄은 문장에서 옮겨 온 것» 이라고 알아볼 수 있다. 되돌림은 Revoke 로 한다.
//
// pattern 은 호출부가 준다(kdb.CommonNounNotePattern). 이 패키지는 kdb 를 import
// 하지 않는다 — 문구의 유일한 원본은 scope_phrases.go 한 자리다.
func BackfillFromNotes(ctx context.Context, pool *pgxpool.Pool, pattern string, limit int, dry bool) BackfillResult {
	var r BackfillResult
	if pool == nil || limit <= 0 || strings.TrimSpace(pattern) == "" {
		return r
	}
	// 판정 구간을 둘 본다: cand-evidence 판정기와 gatekeeper 의 common-noun 검토.
	rows, err := pool.Query(ctx, `
SELECT e.id::text, e.canonical_ko, e.status::text,
       left(regexp_replace(
         COALESCE(NULLIF(split_part(e.notes, '[cand-evidence:review]', 2), ''),
                  split_part(e.notes, 'high-confidence common-noun review', 2)),
         '\s+', ' ', 'g'), 200) AS reason
  FROM kwave_entities e
 WHERE e.status <> 'active'
   AND (e.notes LIKE '%[cand-evidence:review]%' OR e.notes LIKE '%common-noun review%')
   AND COALESCE(
         NULLIF(split_part(e.notes, '[cand-evidence:review]', 2), ''),
         split_part(e.notes, 'high-confidence common-noun review', 2)) ~ $1
   AND NOT EXISTS (SELECT 1 FROM `+Table+` cn
                    WHERE cn.norm_key = `+NormKeySQL("e.canonical_ko")+`)
   -- 같은 이름이 살아서 서빙 중이면 그 이름은 고유명사로 쓰이고 있다. 등재하지 않는다.
   AND NOT EXISTS (SELECT 1 FROM kwave_entities a
                    WHERE a.canonical_ko = e.canonical_ko AND a.status='active')
 ORDER BY e.updated_at DESC
 LIMIT $2`, pattern, limit)
	if err != nil {
		return r
	}
	type row struct{ id, ko, status, reason string }
	var items []row
	for rows.Next() {
		var it row
		if rows.Scan(&it.id, &it.ko, &it.status, &it.reason) == nil {
			items = append(items, it)
		}
	}
	rows.Close()

	for _, it := range items {
		r.Checked++
		if len(r.Samples) < 40 {
			r.Samples = append(r.Samples, it.ko+" ("+it.status+") — "+trunc(it.reason, 60))
		}
		if dry {
			r.Recorded++
			continue
		}
		if _, err := Record(ctx, pool, Entry{
			Surface: it.ko, Kind: KindCommonNoun, Reason: it.reason,
			DecidedBy: "backfill", EntityID: it.id,
		}); err == nil {
			r.Recorded++
		}
	}
	return r
}
