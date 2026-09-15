package kdb

// person_retype_drain — **사람인데 작품·그룹으로 분류된 행**의 유형을 person 으로 되돌린다.
//
// ★왜 이것만 일괄로 고쳐도 되는가 (2026-09-15 전량 실측).
//   활성 5,830건의 QID 를 전부 조회해 우리 유형과 대조했더니 590건이 어긋났다.
//   그런데 **P31 은 "그 QID 가 무엇인지"만 말한다.** "우리 행이 무엇인지"는 안 말한다.
//   그래서 P31 하나로 유형을 고치면 정확히 반대로 망가지는 무리가 있었다:
//
//     movie  전우치   en=`Woochi: The Demon Slayer`  QID = 전설 속 도사
//     movie  황해     en=`The Yellow Sea`            QID = 배우
//     movie  덕혜옹주  en=`The Last Princess`         QID = 실제 옹주
//     drama  광개토대왕 QID = 실제 왕
//   ← 영화 제목은 맞고, 그 영화가 **다룬** 실존 인물에게 QID 가 붙은 것이다.
//     여기서 유형을 person 으로 바꾸면 영화가 사람이 된다.
//
//   그래서 근거를 **셋 겹친다.** 셋이 같은 방향일 때만 고친다.
//     ① QID 의 P31 에 Q5(사람)가 있다
//     ② canonical_ko 가 한국 인명꼴이다 (성씨 + 2~4자)
//     ③ canonical_en 이 인명 로마자꼴이다 (`Kim Seung-jin` / `In-taek Yoo`)
//   실측: 183건 중 셋 다 맞는 것 **121건**, 엇갈리는 것 62건(=위 영화 무리).
//
//     song_album  김승진 → Kim Seung-jin   (Go player)
//     group       백다연 → Back Da-yeon    (tennis player)
//     show        차현승 → Cha Hyun-seung  (dancer)
//   ← 사람 이름이 앨범·그룹·방송으로 분류돼 있었다.
//
// ★대상은 유형만 바꾼다. ID 도 이름도 표기도 앵커도 그대로다(I01/I02: UUID 불변).
// ★기본 dry-run. 전건 kwave_kdb_recheck_log(verdict='retype', new_type) 로 되돌릴 수 있다.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
)

// personRomanizedName — `Kim Seung-jin` · `In-taek Yoo` · `Bae Il-ho` 꼴.
// 작품 제목(`The Yellow Sea`, `Anarchist from Colony`)을 걸러내는 것이 목적이므로
// **관사·전치사가 들어가면 인명으로 보지 않는다.**
var personRomanizedName = regexp.MustCompile(`^[A-Z][a-z]+(-[a-z]+)? [A-Z][a-z]+(-[a-z]+)?$`)

var titleWords = map[string]bool{
	"the": true, "of": true, "from": true, "in": true, "a": true, "an": true,
	"and": true, "for": true, "to": true, "my": true, "our": true, "with": true,
}

// IsKoreanPersonNameShape — 성씨로 시작하는 2~4자 한글. 표는 kanaSurnames 하나만 쓴다
// (사본을 두면 갈라진다 — 오판 29 가 그 계열이었다).
func IsKoreanPersonNameShape(ko string) bool {
	r := []rune(strings.TrimSpace(ko))
	if len(r) < 2 || len(r) > 4 {
		return false
	}
	for _, c := range r {
		if c < '가' || c > '힣' {
			return false
		}
	}
	if len(r) >= 3 && kanaSurnames[string(r[0:2])] { // 복성(남궁·황보 …)
		return true
	}
	return kanaSurnames[string(r[0])]
}

// IsPersonRomanizedShape — 인명 로마자꼴이고 제목어(관사·전치사)를 안 쓴다.
func IsPersonRomanizedShape(en string) bool {
	en = strings.TrimSpace(en)
	if !personRomanizedName.MatchString(en) {
		return false
	}
	for _, w := range strings.Fields(strings.ToLower(en)) {
		if titleWords[w] {
			return false
		}
	}
	return true
}

type RetypeResult struct {
	Checked, Retyped, Collided, Skipped int
	Review                              []PersonAnchorMismatch
}

func DrainRetypePersonMisfiled(ctx context.Context, pool *pgxpool.Pool, cl *wikidata.Client, limit int, dry bool) RetypeResult {
	var r RetypeResult
	if pool == nil || cl == nil {
		return r
	}
	if limit <= 0 {
		limit = 300
	}
	rows, err := pool.Query(ctx, `
SELECT e.id::text, e.canonical_ko, e.entity_type::text, COALESCE(e.canonical_en,''), x.external_id
  FROM kwave_entities e
  JOIN kwave_entity_external_refs x ON x.entity_id = e.id AND x.provider = 'wikidata'
 WHERE e.status = 'active' AND NOT e.operator_locked
   AND e.entity_type::text NOT IN ('person','character','unknown')
   AND x.external_id ~ '^Q[0-9]+$'
   AND COALESCE(e.canonical_en,'') <> ''
 ORDER BY e.canonical_ko
 LIMIT $1`, limit)
	if err != nil {
		log.Printf("kdb.retype: select: %v", err)
		return r
	}
	type row struct{ id, ko, typ, en, qid string }
	var items []row
	for rows.Next() {
		var it row
		if rows.Scan(&it.id, &it.ko, &it.typ, &it.en, &it.qid) == nil {
			items = append(items, it)
		}
	}
	rows.Close()

	for _, it := range items {
		// ②③ 을 먼저 본다 — 위키데이터를 부르기 전에 걸러야 호출이 준다.
		if !IsKoreanPersonNameShape(it.ko) || !IsPersonRomanizedShape(it.en) {
			continue
		}
		ent, ferr := cl.Fetch(ctx, it.qid)
		if ferr != nil || ent == nil {
			continue
		}
		r.Checked++
		// ① P31 에 Q5. 이름요소 항목은 근거가 아니다.
		if nameEl, _ := ent.IsNameElement(); nameEl || !containsString(ent.InstanceOf, "Q5") {
			r.Review = append(r.Review, PersonAnchorMismatch{
				ID: it.id, KO: it.ko, EntityType: it.typ, QID: it.qid,
				Verdict: "name-shape-but-not-human", Desc: ent.Descriptions["en"]})
			r.Skipped++
			continue
		}
		log.Printf("  %-16s %-12s → person   %-24s %s", it.ko, it.typ, it.en, ent.Descriptions["en"])
		if dry {
			r.Retyped++
			continue
		}
		if err := retypeOne(ctx, pool, it.id, it.ko, it.typ, it.qid, ent.Descriptions["en"]); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				// 같은 이름의 person 이 이미 있다. 같은 사람인지 동명이인인지는
				// 이름만으로 못 정한다(I05·M06). 합치지 않고 검수로 보낸다.
				r.Collided++
				log.Printf("  [충돌] %-16s 같은 이름의 person 이 이미 있다 — 검수", it.ko)
				continue
			}
			log.Printf("kdb.retype: %s: %v", it.ko, err)
			continue
		}
		r.Retyped++
	}
	return r
}

func retypeOne(ctx context.Context, pool *pgxpool.Pool, id, ko, oldType, qid, desc string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `
UPDATE kwave_entities SET entity_type = 'person'::kwave_entity_type, updated_at = now()
 WHERE id = $1 AND status = 'active' AND NOT operator_locked`, id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `
INSERT INTO kwave_kdb_recheck_log (entity_id, term_ko, verdict, new_type, models, agreed, evidence)
VALUES ($1, $2, 'retype', 'person', 'wikidata-p31+name-shape', true, $3)`,
		id, ko, fmt.Sprintf("%s → person · %s (%s) · 근거 셋 일치(P31=Q5 · 한국 인명꼴 · 인명 로마자꼴)", oldType, qid, desc)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
