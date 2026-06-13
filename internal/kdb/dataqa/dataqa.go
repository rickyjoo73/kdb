// Package dataqa — gpt-5.5 기반 데이터 품질 검수 + 복구.
//
// 동명이인/멤버-그룹 혼동 등으로 canonical_<loc> 에 다른 대상의 표기가 섞이는
// 오염(예: 인물 '다니엘'의 id 칸에 'Kang Daniel')은 규칙으로 잡기 어렵다("IU=Lee
// Ji-eun" 같은 정상과 구분 불가). 따라서 LLM(gpt-5.5)으로 검수하고, 오염 locale 만
// 감사 로그(kwave_kdb_dataqa_log)에 원본을 남긴 뒤 비운다 — 되돌리기 가능.
//
// 운영자 진입점: `kdb-app dataqa`(기본 dry-run 리포트, --apply 시 복구).
package dataqa

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Schema — codex --output-schema 로 넘길 dataqa 응답 스키마(바이너리에 embed).
//
//go:embed schema.json
var Schema []byte

// pendingFilter — 검수 대상: active person/group 중 미검수(NULL)이거나 검수 후
// 변경(updated_at > last_dataqa_at)된 것. 이미 검수한 안정 entity 는 재검수 안 함
// (codex 낭비 방지). enrich 등으로 갱신되면 자동 재검수 대상이 된다.
const pendingFilter = `status='active' AND entity_type IN ('person','group')
  AND (last_dataqa_at IS NULL OR updated_at > last_dataqa_at)`

// normExpr — 로마자 locale 값들을 이름-비교 정규화한 distinct 집합. Go
// normalizeName(wikidata/musicbrainz)과 동일 문자셋을 제거(공백/._'"·・,-)해야
// "같은 이름 modulo 구두점" 판정이 SQL·Go 에서 일치한다(과거엔 SQL 이 ·・ 를 안 지워
// 같은 이름쌍을 불일치로 오플래그). SuspectSQL/CountSuspects 가 공유하는 단일 출처.
const normExpr = `ARRAY(SELECT DISTINCT lower(regexp_replace(v,'[ ._''"·・,-]','','g'))
          FROM unnest(ARRAY[canonical_en,canonical_vi,canonical_es,canonical_id,canonical_pt_br]) v
          WHERE v IS NOT NULL AND v <> '')`

// SuspectSQL — pending 중 로마자 locale 값이 서로 정규화-불일치하는(오염 의심) entity.
// 미디어 제목은 번역이 정당히 달라 제외(person/group 만).
const SuspectSQL = `
WITH lat AS (
  SELECT id, entity_type, canonical_ko, canonical_en, canonical_vi, canonical_es, canonical_id, canonical_pt_br,
         canonical_ja, canonical_zh, canonical_zh_hant,
    ` + normExpr + ` AS norms
  FROM kwave_entities WHERE ` + pendingFilter + `
)
SELECT id, entity_type, canonical_ko,
       COALESCE(canonical_en,''), COALESCE(canonical_vi,''), COALESCE(canonical_es,''),
       COALESCE(canonical_id,''), COALESCE(canonical_pt_br,''),
       COALESCE(canonical_ja,''), COALESCE(canonical_zh,''), COALESCE(canonical_zh_hant,'')
FROM lat WHERE array_length(norms,1) >= 2
ORDER BY canonical_ko LIMIT $1`

// Entity — 검수 대상 한 건. ja/zh/zh_hant 는 suspect 탐지(로마자 정규화 불일치)
// 에는 안 쓰이지만, 일단 의심된 entity 의 CJK locale 오염도 같은 검수에서 잡도록
// 프롬프트에 포함한다(박보검 ja 오염류 — 추가 codex 호출 비용 0).
type Entity struct {
	ID     uuid.UUID
	Type   string
	Ko     string
	En     string
	Vi     string
	Es     string
	Idn    string // 인도네시아 (DB canonical_id; 'id' 는 entity id 와 헷갈려 Idn)
	PtBr   string
	Ja     string
	Zh     string
	ZhHant string
}

// Verdict — gpt-5.5 판정 한 건 (kdb_dataqa.schema.json 과 1:1).
type Verdict struct {
	ID          string   `json:"id"`
	Verdict     string   `json:"verdict"` // ok | contaminated | duplicate | uncertain
	WrongFields []string `json:"wrong_fields"`
	Reason      string   `json:"reason"`
}

// Runner — codexcli.Runner 추상화 (테스트용 fake 주입 가능).
type Runner interface {
	Run(ctx context.Context, prompt string, schema []byte) (json.RawMessage, error)
}

// localeColumn — locale 코드 → canonical 컬럼. 미지원이면 "".
func localeColumn(loc string) (canon, source string) {
	switch loc {
	case "en":
		return "canonical_en", "canonical_en_source"
	case "vi":
		return "canonical_vi", "canonical_vi_source"
	case "es":
		return "canonical_es", "canonical_es_source"
	case "id":
		return "canonical_id", "canonical_id_source"
	case "pt_br":
		return "canonical_pt_br", "canonical_pt_br_source"
	case "ja":
		return "canonical_ja", "canonical_ja_source"
	case "zh":
		return "canonical_zh", "canonical_zh_source"
	case "zh_hant":
		return "canonical_zh_hant", "canonical_zh_hant_source"
	}
	return "", ""
}

// LoadSuspects — pending 의심 entity 를 limit 만큼 읽는다(검수 후 MarkReviewed 로 소진).
func LoadSuspects(ctx context.Context, pool *pgxpool.Pool, limit int) ([]Entity, error) {
	rows, err := pool.Query(ctx, SuspectSQL, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entity
	for rows.Next() {
		var e Entity
		if err := rows.Scan(&e.ID, &e.Type, &e.Ko, &e.En, &e.Vi, &e.Es, &e.Idn, &e.PtBr, &e.Ja, &e.Zh, &e.ZhHant); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CountSuspects — pending(미검수/변경) 의심 entity 건수.
func CountSuspects(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	var n int
	err := pool.QueryRow(ctx, `
WITH lat AS (
  SELECT id, `+normExpr+` AS norms
  FROM kwave_entities WHERE `+pendingFilter+`)
SELECT count(*) FROM lat WHERE array_length(norms,1) >= 2`).Scan(&n)
	return n, err
}

// MarkReviewed — 검수한 entity 들의 last_dataqa_at 을 지금으로(재검수 방지). Apply 후
// 호출해 last_dataqa_at >= updated_at 이 되도록 한다.
func MarkReviewed(ctx context.Context, pool *pgxpool.Pool, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := pool.Exec(ctx, `UPDATE kwave_entities SET last_dataqa_at=now() WHERE id = ANY($1)`, ids)
	return err
}

// FlagDuplicate — duplicate 판정 entity 를 needs_disambig 으로 표시해 운영자 충돌
// 리뷰 큐에 올린다(자동 병합은 위험 — 운영자 판단). operator_locked 는 보존.
func FlagDuplicate(ctx context.Context, pool *pgxpool.Pool, eid uuid.UUID, reason string) error {
	_, err := pool.Exec(ctx, `
UPDATE kwave_entities
   SET needs_disambig = true,
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || 'dataqa: duplicate? ' || $2,
       updated_at = now()
 WHERE id = $1 AND operator_locked = false`, eid, reason)
	return err
}

// Stats — 한 배치 처리 결과.
type Stats struct {
	Reviewed, OK, Contaminated, Duplicate, Uncertain, ClearedFields, FlaggedDup int
}

// RunBatch — pending 의심 entity 를 limit 만큼 gpt-5.5 로 검수하고, apply=true 면
// contaminated locale 정리(감사) + duplicate 플래그. 검수한 entity 는 모두 reviewed
// 표시. 워커 주기 호출과 dataqa 서브커맨드가 공유하는 단일 경로.
func RunBatch(ctx context.Context, pool *pgxpool.Pool, r Runner, schema []byte, limit int, apply bool) (Stats, []Verdict, error) {
	var st Stats
	ents, err := LoadSuspects(ctx, pool, limit)
	if err != nil || len(ents) == 0 {
		return st, nil, err
	}
	verds, err := Review(ctx, r, schema, ents)
	if err != nil {
		return st, nil, err
	}
	st.Reviewed = len(ents)
	for _, v := range verds {
		switch v.Verdict {
		case "contaminated":
			st.Contaminated++
			if apply {
				if n, e := Apply(ctx, pool, v); e == nil {
					st.ClearedFields += n
				}
			}
		case "duplicate":
			st.Duplicate++
			if apply {
				if eid, e := uuid.Parse(v.ID); e == nil {
					if FlagDuplicate(ctx, pool, eid, v.Reason) == nil {
						st.FlaggedDup++
					}
				}
			}
		case "uncertain":
			st.Uncertain++
		default:
			st.OK++
		}
	}
	// 검수한 모든 entity 를 reviewed 표시(변경 전까지 재검수 안 함). Apply 후라
	// last_dataqa_at >= updated_at 보장.
	ids := make([]uuid.UUID, 0, len(ents))
	for _, e := range ents {
		ids = append(ids, e.ID)
	}
	if e := MarkReviewed(ctx, pool, ids); e != nil {
		return st, verds, e
	}
	return st, verds, nil
}

const promptHeader = `너는 K-엔터테인먼트 고유명사 DB 의 데이터 품질 검수자다. 각 entity 는 한국어 정식명(ko)과
locale 표기(en/vi/es/idn=인도네시아/pt_br/ja=일본어/zh=중국어 간체/zh_hant=번체)를 가진다. type 은 person/group 이다.
모든 locale 값은 같은 인물/그룹의 로마자/현지 표기여야 한다. 어떤 값이 '다른 인물/그룹'의
것이면 contaminated 로 보고 그 locale 코드를 wrong_fields 에 넣어라(코드: en/vi/es/id/pt_br/ja/zh/zh_hant;
idn 은 id 로 적어라). 예: person 'LE' 인데 en='LE SSERAFIM'(그룹명) → contaminated, wrong_fields=['en'].
ja/zh/zh_hant 도 동일 기준 — 그 표기가 동명의 '다른' 인물/그룹의 현지 표기면 contaminated
(예: 배우 박보검의 ja 가 다른 인물의 가타카나 표기). 인명의 정당한 가타카나/한자 음역·간체↔번체 차이는 ok.
스타일화 표기(BLACKPINK=BLΛƆKPIИK), 예명 vs 본명(RM=Kim Nam-joon), 단순 로마자 표기차는 ok.
같은 대상의 중복으로 의심되면 duplicate. 확신 없으면 uncertain.
반드시 입력의 eid 를 'id' 필드에 그대로 echo 하고, JSON(reviews 배열)만 출력하라. reason 은 한국어.

검수 대상:
`

// BuildPrompt — entity 배치를 검수 프롬프트로.
func BuildPrompt(ents []Entity) string {
	var b strings.Builder
	b.WriteString(promptHeader)
	for _, e := range ents {
		row, _ := json.Marshal(map[string]string{
			"eid": e.ID.String(), "type": e.Type, "ko": e.Ko,
			"en": e.En, "vi": e.Vi, "es": e.Es, "idn": e.Idn, "pt_br": e.PtBr,
			"ja": e.Ja, "zh": e.Zh, "zh_hant": e.ZhHant,
		})
		b.Write(row)
		b.WriteByte('\n')
	}
	return b.String()
}

// Review — 한 배치를 gpt-5.5 로 검수해 verdict 목록을 반환.
func Review(ctx context.Context, r Runner, schema []byte, ents []Entity) ([]Verdict, error) {
	if len(ents) == 0 {
		return nil, nil
	}
	raw, err := r.Run(ctx, BuildPrompt(ents), schema)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Reviews []Verdict `json:"reviews"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("dataqa parse: %w", err)
	}
	return resp.Reviews, nil
}

// Apply — contaminated verdict 의 wrong_fields 를 감사 로그에 남기고 비운다.
// cleared = 실제로 비운 (entity,locale) 수. 빈 값/미지원 locale 은 건너뛴다.
func Apply(ctx context.Context, pool *pgxpool.Pool, v Verdict) (cleared int, err error) {
	if v.Verdict != "contaminated" {
		return 0, nil
	}
	eid, err := uuid.Parse(v.ID)
	if err != nil {
		return 0, fmt.Errorf("bad entity id %q: %w", v.ID, err)
	}
	// 감사 INSERT 와 clear UPDATE 를 한 트랜잭션으로 — 둘 사이 크래시 시 로그/실제
	// 불일치(로그는 비웠다는데 값 잔존, Revert 가 건너뜀) 방지. UPDATE...RETURNING +
	// CTE 로 old 값을 스냅샷해 별도 SELECT 제거(왕복 1회로 축소).
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	for _, loc := range v.WrongFields {
		canon, srcCol := localeColumn(loc)
		if canon == "" {
			continue
		}
		var oldVal, oldSrc string
		err := tx.QueryRow(ctx, fmt.Sprintf(`
WITH old AS (SELECT COALESCE(%[1]s,'') v, COALESCE(%[2]s,'') s FROM kwave_entities WHERE id=$1)
UPDATE kwave_entities SET %[1]s='', %[2]s='', updated_at=now()
 WHERE id=$1 AND COALESCE(%[1]s,'') <> ''
 RETURNING (SELECT v FROM old), (SELECT s FROM old)`, canon, srcCol),
			eid).Scan(&oldVal, &oldSrc)
		if errors.Is(err, pgx.ErrNoRows) {
			continue // 이미 비어있음 — 변경 없음
		}
		if err != nil {
			return cleared, err
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO kwave_kdb_dataqa_log (entity_id, locale, old_value, old_source, verdict, reason)
VALUES ($1,$2,$3,$4,$5,$6)`, eid, loc, oldVal, oldSrc, v.Verdict, v.Reason); err != nil {
			return cleared, err
		}
		cleared++
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return cleared, nil
}

// Revert — 감사 로그 한 건(또는 entity 전체)을 원복한다. 빈 칸일 때만 되돌려
// (그 사이 새 값이 들어왔으면 보존). 되돌린 건수 반환.
func Revert(ctx context.Context, pool *pgxpool.Pool, logID int64) (int, error) {
	rows, err := pool.Query(ctx,
		`SELECT id, entity_id, locale, old_value, old_source FROM kwave_kdb_dataqa_log
		  WHERE id=$1 AND reverted_at IS NULL`, logID)
	if err != nil {
		return 0, err
	}
	type rec struct {
		id          int64
		eid         uuid.UUID
		loc, val, src string
	}
	var recs []rec
	for rows.Next() {
		var r rec
		if err := rows.Scan(&r.id, &r.eid, &r.loc, &r.val, &r.src); err != nil {
			rows.Close()
			return 0, err
		}
		recs = append(recs, r)
	}
	rows.Close()
	n := 0
	for _, r := range recs {
		canon, srcCol := localeColumn(r.loc)
		if canon == "" {
			continue
		}
		tag, err := pool.Exec(ctx,
			fmt.Sprintf(`UPDATE kwave_entities SET %s=$2, %s=$3, updated_at=now()
			              WHERE id=$1 AND COALESCE(%s,'')=''`, canon, srcCol, canon),
			r.eid, r.val, r.src)
		if err != nil {
			return n, err
		}
		if tag.RowsAffected() > 0 {
			n++
		}
		_, _ = pool.Exec(ctx, `UPDATE kwave_kdb_dataqa_log SET reverted_at=now() WHERE id=$1`, r.id)
	}
	return n, nil
}
