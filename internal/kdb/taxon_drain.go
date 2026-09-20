package kdb

// taxon_drain.go — **요청이 들어온 낱말을 분류군으로 해소해 원장에 적는다.**
//
// ★왜 이 레인이 필요한가. 답을 못 준 낱말은 소비자가 스스로 만든다 — 그리고 그 값은
//	우리 원장에 안 남고, 소비자마다 다르고, 고칠 수도 없다. 전어→鲭鱼(고등어)는
//	그렇게 나갔다. 요청이 오면 **찾아서 우리 DB 에 적어야** 다음 요청부터 우리가 답한다.
//
// ★무엇을 고르나. 「최근 요청이 왔는데 지금도 못 답하는 낱말」이다. 보유량이 아니라
//	수요가 순서를 정한다 — 안 물어보는 것을 채우는 것은 창고만 늘린다.
//
// ★무엇을 적나.
//	· 새로 만들 때는 **candidate** 로 만든다. 범위가 넓어졌다는 것이 «근거 없이 서빙해도
//	  된다»는 뜻은 아니다 — 승급은 평소 경로가 근거를 보고 한다.
//	· 표준명을 canonical_ko 로, **요청어(통칭)를 별칭으로** 싣는다. 기사는 `광어`라 쓰고
//	  문서는 `넙치`에 있다. 이 연결이 없으면 원천을 붙여도 서로 만나지 못한다.
//	· 학명을 taxon_name 에 적는다(0150). 이것이 있으면 fill_hint 가
//	  use_standard_name 으로 바뀐다 — 소비자가 «뜻을 옮겨라»를 받지 않게 된다.
//	· locale 값은 빈칸과 **기계값**에만 쓴다. 권위 소스는 건드리지 않는다.
//
// ★분류군이 아니면 아무것도 안 적는다. 그리고 **안 적었다는 사실을 남긴다** — 그게
//	없으면 같은 낱말에 매 회차 외부 API 를 세 번씩 쓴다.
//
// ★기본 dry-run.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// taxonFlag — 이 낱말을 분류군으로 조회해 봤다는 표식. 재조회를 막는다.
const taxonFlag = "taxon_checked"

// taxonValueSource — 언어판 문서 제목에서 온 값의 출처(priority 6).
// 위키데이터 라벨(5)보다 아래지만 기계값(7~9)보다는 위다 — 그래서 기계값만 덮는다.
const taxonValueSource = SourceWikipediaLanglinks

// taxonLocaleColumns — KDB locale → (값 컬럼, 출처 컬럼).
//
// zh(간체)는 **여기서 만들지 않는다.** zhwiki 표제는 번체라 zh_hant 에 들어가고,
// 간체는 opencc 가 결정적으로 변환한다. 두 자리가 같은 칸을 서로 다른 규칙으로
// 채우면 한쪽으로 오염이 샌다(9/19 qaCharsetOK 와 같은 계열).
var taxonLocaleColumns = map[string][2]string{
	"en":      {"canonical_en", "canonical_en_source"},
	"ja":      {"canonical_ja", "canonical_ja_source"},
	"vi":      {"canonical_vi", "canonical_vi_source"},
	"zh_hant": {"canonical_zh_hant", "canonical_zh_hant_source"},
	"es":      {"canonical_es", "canonical_es_source"},
	"id":      {"canonical_id", "canonical_id_source"},
	"pt_br":   {"canonical_pt_br", "canonical_pt_br_source"},
}

// TaxonDrainResult — 한 회차의 결과.
type TaxonDrainResult struct {
	Checked   int // 조회한 낱말
	Created   int // 새로 만든 대상
	Updated   int // 기존 대상에 적은 것
	Cells     int // 채운 locale 칸
	NotTaxon  int // 분류군이 아니라 손대지 않은 것
	Failed    int // 조회 실패(네트워크 등) — 표식을 남기지 않는다. 다음에 다시 본다
	Anchored  int // 학명을 새로 적은 대상
	AliasAdds int // 통칭을 별칭으로 실은 것
}

// DrainTaxonRequests — 요청은 왔는데 못 답하는 낱말을 분류군으로 해소해 원장에 적는다.
//
// dry=true 면 아무것도 쓰지 않고 판정만 로그로 보여준다(기본값으로 두라).
func DrainTaxonRequests(ctx context.Context, pool *pgxpool.Pool, limit int, dry bool) TaxonDrainResult {
	var res TaxonDrainResult
	if pool == nil {
		return res
	}
	if limit <= 0 {
		limit = 20
	}
	terms, err := selectTaxonDemand(ctx, pool, limit)
	if err != nil {
		log.Printf("kdb.taxon: 선정 조회: %v", err)
		return res
	}
	tc := NewTaxonClient(&http.Client{Timeout: 15 * time.Second})

	for _, t := range terms {
		res.Checked++
		look, err := tc.Lookup(ctx, t.term)
		switch {
		case errors.Is(err, ErrNotTaxon), errors.Is(err, ErrNoKoWikiArticle):
			res.NotTaxon++
			if !dry {
				markTaxonChecked(ctx, pool, t.term)
			}
			continue
		case err != nil:
			// 네트워크·상대 장애는 **표식을 남기지 않는다.** 남기면 "확인했다"가 되어
			// 영영 다시 안 본다 — 못 한 것과 아닌 것은 다르다.
			res.Failed++
			log.Printf("kdb.taxon: %q 조회 실패: %v", t.term, err)
			continue
		}
		if len(look.ByLocale) == 0 {
			res.NotTaxon++
			if !dry {
				markTaxonChecked(ctx, pool, t.term)
			}
			continue
		}
		if dry {
			log.Printf("kdb.taxon: [dry] %s → %s (%s / %s) locale=%d 요청=%d",
				t.term, look.StandardKO, look.QID, look.Scientific, len(look.ByLocale), t.requests)
			for _, n := range look.Notes {
				log.Printf("kdb.taxon: [dry]   · %s", n)
			}
			continue
		}
		applyTaxonLookup(ctx, pool, look, &res)
		markTaxonChecked(ctx, pool, t.term)
	}
	log.Printf("kdb.taxon: DrainTaxonRequests checked=%d created=%d updated=%d cells=%d alias=%d anchored=%d not-taxon=%d failed=%d (dry=%v)",
		res.Checked, res.Created, res.Updated, res.Cells, res.AliasAdds, res.Anchored, res.NotTaxon, res.Failed, dry)
	return res
}

type taxonDemand struct {
	term     string
	requests int
}

// selectTaxonDemand — 최근 요청이 왔는데 **지금도** active 원장이 못 답하는 낱말.
//
// 이미 분류군으로 조회해 본 낱말(taxon_checked)은 뺀다. 같은 낱말에 매 회차 외부
// API 를 세 번씩 쓰는 것을 막는다.
func selectTaxonDemand(ctx context.Context, pool *pgxpool.Pool, limit int) ([]taxonDemand, error) {
	rows, err := pool.Query(ctx, `
SELECT r.term_ko, count(*)::int
  FROM kwave_kdb_request_terms r
 WHERE r.created_at > now() - interval '30 days'
   AND r.origin IN ('prepare','lookup')
   AND r.term_ko <> ''
   AND NOT EXISTS (
        SELECT 1 FROM kwave_entities e
         WHERE e.status = 'active'
           AND (e.canonical_ko = r.term_ko OR r.term_ko = ANY(e.aliases_ko)))
   AND NOT EXISTS (
        SELECT 1 FROM kwave_entity_research_queue q
         WHERE q.entity_ko = r.term_ko AND $2 = ANY(q.precheck_flags))
 GROUP BY r.term_ko
 ORDER BY count(*) DESC, max(r.created_at) DESC
 LIMIT $1`, limit, taxonFlag)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []taxonDemand
	for rows.Next() {
		var d taxonDemand
		if rows.Scan(&d.term, &d.requests) == nil {
			out = append(out, d)
		}
	}
	return out, rows.Err()
}

// markTaxonChecked — 이 낱말을 분류군으로 조회해 봤다고 큐에 남긴다.
// 큐 행이 없으면 아무 일도 하지 않는다(상태·값 불변 — 표식만 남기는 경로다).
func markTaxonChecked(ctx context.Context, pool *pgxpool.Pool, term string) {
	_, _ = pool.Exec(ctx, `
UPDATE kwave_entity_research_queue
   SET precheck_flags = array_append(precheck_flags, $2)
 WHERE entity_ko = $1 AND NOT ($2 = ANY(precheck_flags))`, term, taxonFlag)
}

// applyTaxonLookup — 조회 결과를 원장에 적는다. 없으면 만들고, 있으면 채운다.
func applyTaxonLookup(ctx context.Context, pool *pgxpool.Pool, look *TaxonLookup, res *TaxonDrainResult) {
	var (
		id     string
		locked bool
	)
	err := pool.QueryRow(ctx, `
SELECT id::text, operator_locked FROM kwave_entities
 WHERE canonical_ko = $1 OR canonical_ko = $2 OR $1 = ANY(aliases_ko) OR $2 = ANY(aliases_ko)
 ORDER BY (status='active') DESC, updated_at DESC
 LIMIT 1`, look.StandardKO, look.Term).Scan(&id, &locked)
	if err != nil {
		// 없다 → candidate 로 만든다. 승급은 평소 경로가 근거를 보고 한다.
		note := fmt.Sprintf("KDB taxon 레인 — 학명 %s (%s). 요청어 %q 로 들어왔다.",
			look.Scientific, look.QID, look.Term)
		if len(look.Notes) > 0 {
			note += " / " + strings.Join(look.Notes, " / ")
		}
		var newID string
		if err := pool.QueryRow(ctx, `
INSERT INTO kwave_entities (canonical_ko, entity_type, confidence, status, taxon_name, aliases_ko, source_urls, notes)
VALUES ($1, 'term', 0.400, 'candidate', $2, $3::text[], $4::text[], $5)
RETURNING id::text`,
			look.StandardKO, look.Scientific, taxonAliasArray(look),
			[]string{"https://www.wikidata.org/wiki/" + look.QID,
				"https://ko.wikipedia.org/wiki/" + strings.ReplaceAll(look.StandardKO, " ", "_")},
			note).Scan(&newID); err != nil {
			log.Printf("kdb.taxon: %q 생성 실패: %v", look.StandardKO, err)
			return
		}
		res.Created++
		res.Anchored++
		if look.AliasKO != "" {
			res.AliasAdds++
		}
		res.Cells += writeTaxonLocales(ctx, pool, newID, look)
		return
	}
	if locked {
		log.Printf("kdb.taxon: %q 는 운영자 잠금 — 손대지 않는다", look.StandardKO)
		return
	}

	// 있다 → 학명·별칭을 붙이고 빈칸/기계값만 채운다.
	var anchored bool
	if pool.QueryRow(ctx, `
UPDATE kwave_entities SET taxon_name=$2, updated_at=now()
 WHERE id=$1 AND COALESCE(taxon_name,'') = ''
 RETURNING true`, id, look.Scientific).Scan(&anchored) == nil && anchored {
		res.Anchored++
	}
	if look.AliasKO != "" {
		var added bool
		if pool.QueryRow(ctx, `
UPDATE kwave_entities
   SET aliases_ko = array_append(COALESCE(aliases_ko,'{}'::text[]), $2), updated_at=now()
 WHERE id=$1 AND NOT ($2 = ANY(COALESCE(aliases_ko,'{}'::text[]))) AND canonical_ko <> $2
 RETURNING true`, id, look.AliasKO).Scan(&added) == nil && added {
			res.AliasAdds++
		}
	}
	if n := writeTaxonLocales(ctx, pool, id, look); n > 0 {
		res.Cells += n
		res.Updated++
	}
}

// taxonAliasArray — 새로 만들 때 실을 별칭. 통칭이 표준명과 다를 때만.
func taxonAliasArray(look *TaxonLookup) []string {
	if look.AliasKO == "" || look.AliasKO == look.StandardKO {
		return []string{}
	}
	return []string{look.AliasKO}
}

// writeTaxonLocales — locale 값을 쓴다. **빈칸과 기계값에만** 쓴다.
//
// SELECT 과 UPDATE 가 같은 목록을 보게 한다 — 고르기만 넓히고 쓰기를 좁혀 뽑은 것을
// 그 자리에서 버린 전례가 있다(iTunes 1,025건).
func writeTaxonLocales(ctx context.Context, pool *pgxpool.Pool, id string, look *TaxonLookup) int {
	weaker := MachineFilledSourcesWeakerThan(taxonValueSource)
	filled := 0
	for locale, val := range look.ByLocale {
		cols, ok := taxonLocaleColumns[locale]
		if !ok || strings.TrimSpace(val) == "" {
			continue
		}
		var applied bool
		err := pool.QueryRow(ctx, `
UPDATE kwave_entities
   SET `+cols[0]+`=$2, `+cols[1]+`=$3, updated_at=now()
 WHERE id=$1
   AND operator_locked = false
   AND (COALESCE(`+cols[0]+`,'') = '' OR COALESCE(`+cols[1]+`,'') = ANY($4::text[]))
 RETURNING true`, id, val, string(taxonValueSource), weaker).Scan(&applied)
		if err == nil && applied {
			filled++
		}
	}
	return filled
}
