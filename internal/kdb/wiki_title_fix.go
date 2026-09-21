package kdb

// wiki_title_fix — **KDB 자신이 단 출처와 저장값이 어긋나면 스스로 고친다.**
//
// ★계기 (2026-09-21, 소비자 피드백). 번역 쪽 에이전트가 이렇게 알려 왔다:
//
//	"배윤규 K0004162 는 canonical_en = "Bae Youn-kyu"(tier authoritative)를 갖고 있는데,
//	 KDB 자신이 근거로 단 source_urls 가 https://en.wikipedia.org/wiki/Bae_Yoon-gyu 다.
//	 출처와 저장값이 어긋난다."
//
//	오너: "이렇게 피드백을 받았는데 왜 제대로 처리를 못해? 근거가 확실하면 업데이트해줘야지."
//
//	맞는 말이다. 근거는 **우리 원장 안에** 있었다. 소비자가 신고할 필요도 없이 우리가
//	잡았어야 한다.
//
// ★왜 안 고쳐졌나. wikidata-label(5) 이 wikipedia-sitelink(6) 보다 우선순위가 높아서
//
//	위키데이터 «라벨»이 위키백과 «문서 제목»을 이겼다. 둘 다 위키미디어인데 서로 다르다.
//	그런데 영어 위키백과 문서 제목은 «영어 출처에서 가장 흔한 이름» 규칙으로 정해진다.
//	en 칸에 대해서는 라벨보다 나은 증거다(mig 0153 에서 wikipedia-title=4 로 올렸다).
//
// ★실측 683건(동음이의 괄호를 뗀 정직한 수). 그런데 **일괄로 바꾸면 사고가 난다**:
//
//	✓ 강부자 Kang Bu-ja → Kang Boo-ja · 김수미 Kim Su-mi → Kim Soo-mi
//	✗ 이수   → "MC the Max"  — 출처 링크가 그의 **밴드** 문서를 가리킨다
//	✗ DK     → "Dplus KIA"   — 출처 링크가 **e스포츠 팀** 문서를 가리킨다
//
//	출처 URL 은 사람이나 레인이 단 것이라 그 자체가 틀릴 수 있다. 그래서 이 레인은
//	**우리 앵커 QID 의 enwiki 사이트링크 제목이 그 URL 제목과 같을 때만** 고친다 —
//	「그 문서가 이 대상의 것」이 위키데이터 쪽에서 확인된 경우다. 이수·DK 의 앵커는
//	그 문서를 가리키지 않으므로 거기서 멈춘다.
//
// ★덮는 범위는 우선순위가 정한다(can_replace_canonical). wikipedia-title(4) 은
// wikidata-label(5) 만 이긴다. musicbrainz·교정검증(4)·rss(3)·운영자(1)는 못 덮는다.
//
// ★되돌릴 수 있게 남긴다(dataqa_log). 기본 dry-run — 그리고 dry 도 쓰기를 **실제로**
// 해 보고 되돌린다(18건을 찾아 놓고 컬럼명 오타로 전부 못 넣은 일을 반복하지 않는다).

import (
	"context"
	"log"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
)

// WikiTitleFixResult — 한 번 돈 결과.
type WikiTitleFixResult struct {
	Checked, Fixed int
	// 왜 안 고쳤는지 칸을 나눈다 — 한 칸으로 세면 원인이 안 보인다.
	NotSameEntity, NoSitelink, Protected, FetchFailed, AlreadySame int
	Samples                                                        []string
}

// DrainWikiTitleMismatch — en 칸이 우리 출처(enwiki 문서)와 어긋나는 행을 고친다.
func DrainWikiTitleMismatch(ctx context.Context, pool *pgxpool.Pool, cl *wikidata.Client, limit int, dry bool) WikiTitleFixResult {
	var r WikiTitleFixResult
	if pool == nil || cl == nil || limit <= 0 {
		return r
	}
	rows, err := pool.Query(ctx, `
SELECT e.id::text, e.canonical_ko, e.canonical_en, COALESCE(e.canonical_en_source,''),
       x.external_id, u
  FROM kwave_entities e
  JOIN kwave_entity_external_refs x ON x.entity_id = e.id AND x.provider = 'wikidata'
  CROSS JOIN LATERAL unnest(e.source_urls) u
 WHERE e.status = 'active' AND e.operator_locked = false
   -- ★사람만 (2026-09-21 dry 실측). 사람의 영어 위키 문서 제목은 «영어 출처의 통용명»
   --   규칙대로라 저장값보다 낫다(정해균 Jung Hae-gyun → Jung Hae-kyun). 그런데 작품·기업은
   --   편집상 이유로 제목이 달라진다 — 그대로 덮으면 이렇게 된다:
   --     약한영웅 Class 1  "Weak Hero Class 1" → "Weak Hero"   (시즌을 시리즈로)
   --     LG생활건강        "LG Household & Health Care" → "LG H&H"  (정식명을 약칭으로)
   --     감기              "The Flu" → "Flu"                    (영화 제목이 "The Flu")
   --   근거가 «확실한» 것은 사람뿐이다. 나머지는 손대지 않는다.
   AND e.entity_type::text = 'person'
   AND u LIKE '%en.wikipedia.org/wiki/%'
   AND COALESCE(e.canonical_en,'') <> ''
   -- 덮을 수 있는 출처만. 판단은 아래 UPDATE 의 can_replace_canonical 이 한 번 더 한다.
   AND can_replace_canonical(false, COALESCE(e.canonical_en_source,''), 'wikipedia-title')
   AND NOT EXISTS (SELECT 1 FROM kwave_kdb_enrich_attempts g
                    WHERE g.entity_id = e.id AND g.field = 'wiki-title'
                      AND g.last_attempt_at > now() - interval '30 days')
 ORDER BY e.updated_at ASC
 LIMIT $1`, limit)
	if err != nil {
		log.Printf("kdb.wiki-title: select: %v", err)
		return r
	}
	type row struct{ id, ko, en, src, qid, u string }
	var items []row
	for rows.Next() {
		var it row
		if rows.Scan(&it.id, &it.ko, &it.en, &it.src, &it.qid, &it.u) == nil {
			items = append(items, it)
		}
	}
	rows.Close()

	for _, it := range items {
		urlTitle := enwikiTitleFromURL(it.u)
		if urlTitle == "" {
			continue
		}
		if wikidata.NormalizeName(urlTitle) == wikidata.NormalizeName(it.en) {
			r.AlreadySame++
			continue
		}
		r.Checked++
		if !dry {
			_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_enrich_attempts (entity_id, field, attempts, last_attempt_at, last_source)
VALUES ($1::uuid,'wiki-title',1,now(),'wikipedia')
ON CONFLICT (entity_id, field) DO UPDATE
   SET attempts = kwave_kdb_enrich_attempts.attempts + 1, last_attempt_at = now()`, it.id)
		}
		ent, ferr := cl.Fetch(ctx, it.qid)
		if ferr != nil || ent == nil {
			r.FetchFailed++
			continue
		}
		sitelink := stripParenSuffix(strings.TrimSpace(ent.SiteTitles["enwiki"]))
		if sitelink == "" {
			r.NoSitelink++
			continue
		}
		// ★「그 문서가 이 대상의 것인가」 — 우리 앵커의 enwiki 문서가 곧 그 URL 문서여야 한다.
		//   이수(→MC the Max)·DK(→Dplus KIA) 는 여기서 멈춘다.
		if wikidata.NormalizeName(sitelink) != wikidata.NormalizeName(urlTitle) {
			r.NotSameEntity++
			if len(r.Samples) < 40 {
				r.Samples = append(r.Samples, "✗ "+it.ko+" 출처="+urlTitle+" ≠ 앵커문서="+sitelink+" (다른 대상의 문서)")
			}
			continue
		}
		// ★두 번째 대조 — **앵커의 한국어 문서 제목이 우리 한국어 이름과 같은가** (2026-09-21).
		//
		//   위 대조(앵커 enwiki = 출처 URL)는 «둘이 같은 문서를 가리킨다»만 보장한다.
		//   **둘이 같이 틀리면** 통과한다. 사람 한정 dry 에서 실제로 나왔다:
		//
		//     하정   Hajeong      → Ha Jung-woo            앵커·출처 둘 다 **하정우**
		//     김선일 Kim Sun-il   → Killing of Kim Sun-il  인물이 아니라 **사건** 문서
		//     김남준 Kim Nam Joon → RM                     본명 행에 **예명** 문서
		//     김민정 Kim Min-jung → Winter                 (같음)
		//     송민   Song Min-ho  → Mino                   앵커가 **송민호**
		//
		//   앵커의 kowiki 제목은 각각 「하정우」「김선일 피살 사건」「RM (가수)」「윈터 (가수)」
		//   「송민호」다 — 우리 이름과 다르다. 한국어 쪽에서 «이 문서가 이 이름의 사람»임이
		//   맞아야 그 영어 제목을 이 이름의 영어로 쓸 수 있다.
		koTitle := stripParenSuffix(strings.TrimSpace(ent.SiteTitles["kowiki"]))
		if koTitle == "" || wikidata.NormalizeName(koTitle) != wikidata.NormalizeName(it.ko) {
			r.NotSameEntity++
			if len(r.Samples) < 40 {
				r.Samples = append(r.Samples, "✗ "+it.ko+" → "+sitelink+" — 앵커 한국어 문서가 «"+koTitle+"» (이 이름의 사람이 아니다)")
			}
			continue
		}
		// ★학술 표기(매큔-라이샤워의 ŏ·ŭ)는 번역 소비자가 쓰는 표기가 아니다.
		//   영어 위키는 역사 인물을 그렇게 적지만(장덕수 → Chang Tŏksu) 기사·자막은 안 쓴다.
		if strings.ContainsAny(sitelink, "ŏŭŎŬ") {
			r.Protected++
			if len(r.Samples) < 40 {
				r.Samples = append(r.Samples, "✗ "+it.ko+" → "+sitelink+" — 학술 표기(ŏ·ŭ). 매체가 쓰는 표기가 아니다")
			}
			continue
		}
		if !IsValidSpellingForLocale("en", sitelink) {
			r.Protected++
			continue
		}
		if len(r.Samples) < 40 {
			r.Samples = append(r.Samples, "✓ "+it.ko+"  "+it.en+" ("+it.src+") → "+sitelink)
		}
		if dry {
			// ★예행도 실제로 써 보고 되돌린다.
			if tx, terr := pool.Begin(ctx); terr == nil {
				_, werr := tx.Exec(ctx, wikiTitleUpdateSQL, it.id, sitelink)
				_ = tx.Rollback(ctx)
				if werr != nil {
					log.Printf("kdb.wiki-title: ★쓰기 예행 실패 — 본 실행도 실패한다: %v", werr)
					r.FetchFailed++
					continue
				}
			}
			r.Fixed++
			continue
		}
		_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_dataqa_log (entity_id, locale, old_value, old_source, verdict, reason, model)
VALUES ($1::uuid, 'en', $2, $3, 'wiki-title-fix', $4, 'rule')`,
			it.id, it.en, it.src, "앵커 "+it.qid+" 의 enwiki 문서 제목 = 원장 출처 URL 제목: "+sitelink)
		tag, uerr := pool.Exec(ctx, wikiTitleUpdateSQL, it.id, sitelink)
		if uerr != nil {
			log.Printf("kdb.wiki-title: %s 쓰기 실패: %v", it.ko, uerr)
			continue
		}
		if tag.RowsAffected() > 0 {
			r.Fixed++
			log.Printf("kdb.wiki-title: %s  %q (%s) → %q", it.ko, it.en, it.src, sitelink)
		} else {
			r.Protected++
		}
	}
	return r
}

// wikiTitleUpdateSQL — 예행과 본 실행이 같은 문장을 쓴다(한 자리).
const wikiTitleUpdateSQL = `
UPDATE kwave_entities
   SET canonical_en = $2, canonical_en_source = 'wikipedia-title', updated_at = now()
 WHERE id = $1::uuid AND operator_locked = false
   AND can_replace_canonical(operator_locked, COALESCE(canonical_en_source,''), 'wikipedia-title')`

// enwikiTitleFromURL — "https://en.wikipedia.org/wiki/Bae_Yoon-gyu" → "Bae Yoon-gyu".
// 퍼센트 인코딩을 풀고 밑줄을 공백으로, 끝의 동음이의 괄호를 뗀다. 앵커(#)는 버린다.
func enwikiTitleFromURL(u string) string {
	i := strings.Index(u, "en.wikipedia.org/wiki/")
	if i < 0 {
		return ""
	}
	t := u[i+len("en.wikipedia.org/wiki/"):]
	if j := strings.IndexAny(t, "#?"); j >= 0 {
		t = t[:j]
	}
	if dec, err := url.PathUnescape(t); err == nil {
		t = dec
	}
	t = strings.ReplaceAll(t, "_", " ")
	return stripParenSuffix(strings.TrimSpace(t))
}
