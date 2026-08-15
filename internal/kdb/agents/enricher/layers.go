package enricher

import (
	"context"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb"
	"github.com/rickyjoo73/kdb/internal/kdb/aijudge"
	"github.com/rickyjoo73/kdb/internal/kdb/apikeys"
	"github.com/rickyjoo73/kdb/internal/kdb/musicbrainz"
	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
	"github.com/rickyjoo73/kdb/internal/kdb/zhvariant"
)

// cascadeLocales fills empty locale canonicals + aliases_ko for the given
// missing columns via L2 MusicBrainz → L3 Wikidata → L4 gpt-5.5. Each layer
// only targets columns still empty; persistence never overwrites a non-empty
// value. filledFields/tried/failed/skipped are updated in place — failed 는 codex
// transport 실패(타임아웃/브레이커)로 이번 cycle 에 실제 시도가 일어나지 않은
// 필드(attempts 미소진, 다음 cycle 재시도), skipped 는 strict 정책이 L4 를 아예
// 부르지 않은 필드(마찬가지로 attempts 미소진 — 실패가 아니라 미질의).
func (a *Agent) cascadeLocales(ctx context.Context, pool *pgxpool.Pool, r *record, missing []string, filledFields, tried map[string]string, failed, skipped map[string]bool) {
	remaining := func() []string {
		var out []string
		for _, f := range missing {
			if _, done := filledFields[f]; !done {
				out = append(out, f)
			}
		}
		return out
	}

	// L1 TMDb — drama/movie/show 의 공식 현지 제목(작품 "번역"의 정답 소스). codex
	// 가 모르는 작품 제목을 영어로 복사하던 문제의 근본 수정: 권위 소스를 codex 보다
	// 먼저, 그리고 priority-aware 로 써서 기존 codex-fallback 영어복사도 교체한다.
	if a.src.tmdb != nil && len(remaining()) > 0 &&
		(r.entityType == "drama" || r.entityType == "movie" || r.entityType == "show") {
		if m := a.tmdbTitles(ctx, pool, r); len(m) > 0 {
			a.applyLocaleMap(ctx, pool, r, remaining(), m, kdb.SourceTMDb, filledFields, tried, "tmdb")
		}
	}

	// L2 MusicBrainz — group / singer-type artists. Provides locale aliases +
	// Korean aliases.
	if a.src.mb != nil && len(remaining()) > 0 && (r.entityType == "group" || r.entityType == "person") {
		if m := a.musicbrainzAliases(ctx, pool, r.id, r.ko); len(m) > 0 {
			a.applyLocaleMap(ctx, pool, r, remaining(), m, kdb.SourceMusicBrainz, filledFields, tried, "musicbrainz")
		}
	}

	// L3 Wikidata — labels for all locales + Korean aliases (from also-known-as).
	var wd *aijudge.ClassifyWikidata
	var sitelinks map[string]string
	var langlinks map[string][]string
	if a.src.wd != nil && len(remaining()) > 0 {
		ent, cand, err := a.wikidataEntity(ctx, pool, r)
		if err == nil && ent != nil {
			wd = &aijudge.ClassifyWikidata{QID: ent.QID}
			if cand != nil {
				wd.Description = cand.Description
			}
			sitelinks = ent.Sitelinks
			langlinks = ent.LanglinkTitles()
			m := map[string][]string{}
			for code, v := range ent.Labels {
				if strings.TrimSpace(v) != "" {
					m[code] = []string{v}
				}
			}
			a.applyLocaleMap(ctx, pool, r, remaining(), m, kdb.SourceWikidataLabel, filledFields, tried, "wikidata")
		}
	}

	// L3.2 Wikipedia 각 언어판 문서 제목(langlink) — 라벨이 비어 있어도 그 언어 위키에
	// 문서가 있으면 표제어는 존재한다. CJK 빈칸의 상당수가 정확히 이 상태다.
	//
	// ★2026-08-06 이전까지 이 cascade 는 그 값을 조회해 놓고 쓰지 않았다. sitelink 는
	// makeFillInput 을 통해 L4 프롬프트의 힌트로만 넘어갔는데, 그 L4 는 ground-strict 로
	// 사실상 상시 꺼져 있다(grounding 성공률 6.9%). 정답을 받아 놓고 버린 셈이다.
	//
	// ★규칙은 wikidata.LanglinkTitles() 하나만 쓴다. enrich/orchestrator 가 같은 변환을
	// 이미 이걸로 하고 있다(:560 langlink-upgrade, :865 enrich). 여기서 별도 규칙을 만들면
	// "어느 레인이 먼저 손댔는지"에 따라 같은 엔티티가 다른 칸에 다른 표기로 들어간다 —
	// 이번 작업이 없애려는 규칙 분화를 locale 채움 쪽에 새로 만드는 꼴이다. 초안에서
	// 실제로 그렇게 짰다가(zhwiki→zh vs zh_hant, 괄호 폐기 vs 절삭) 되돌린 자리다.
	// source 도 orchestrator 와 같은 wikipedia-langlinks(prio 6) 로 통일한다.
	if len(langlinks) > 0 {
		if want := fieldsCoveredBy(remaining(), langlinks); len(want) > 0 {
			a.applyLocaleMap(ctx, pool, r, want, langlinks, kdb.SourceWikipediaLanglinks,
				filledFields, tried, "wikidata-langlink")
		}
	}

	// L3.5 zh 간체↔번체 결정적 정규화 — canonical_zh=간체, canonical_zh_hant=번체로
	// 맞춘다. Wikidata 가 보통 한 변종(번체 邊佑錫)만 줘서 ① 다른 칸이 비거나
	// ② 간체 칸에 번체가 들어가던 문제를 MediaWiki LanguageConverter(규칙 기반,
	// 환각 없음)로 교정. 운영자·매체합의 값은 보존.
	a.fillZhVariants(ctx, pool, r, filledFields, tried)

	// L3.7 검색-그라운딩(SearXNG+gemma 다회투표) — L4 codex 합성 *전에* 빈칸을 그라운딩값으로
	// (강증거→local-usage, 약증거→local-search). 이 hermes Enricher 도 codex-fallback 생산자
	// 였음(enrich/orchestrator 의 L3.5 와 별개 cascade — 둘 다 막아야 누수 완전봉인). enrichground
	// 쿨다운은 orchestrator 와 공유 → 같은 엔티티 중복검색 없음. flag KDB_ENRICH_GROUND=1 게이트.
	// perEntity 상한은 localFillLocales 전체(8)를 덮어야 한다. 4 였을 때는 타깃이
	// en→ja→vi→id→es→pt_br→zh→zh_hant 순으로 생성되므로 앞 4개에서 예산이 끊겨
	// zh·zh_hant 는 검색조차 되지 않는데, handled=true 는 엔티티 단위로 반환되어
	// 아래 strict 게이트가 "검색해봤지만 무신호"로 오인해 L4 까지 막았다 — 검색도
	// 합성도 없는 칸이 CJK 에만 구조적으로 쌓인 경로(orchestrator 는 이미 8).
	groundHandled := false
	if len(remaining()) > 0 {
		if _, handled, err := kdb.GroundEntity(ctx, pool, r.id.String(), len(localeToCode)); err == nil {
			groundHandled = handled
		}
	}

	// L4 gpt-5.5 — synthesize the locale spellings still missing. aliases_ko is
	// not a locale code the fill prompt handles, so it is left to L2/L3 only.
	// strict(빈칸>틀린값): grounding 담당(실행 OR 7d쿨다운) 엔티티는 codex 스킵 → 검색 무신호
	// (동명이인·무명) locale 은 codex 추측값 대신 빈칸 유지. reground·쿨다운만료 시 재방문.
	rem := remaining()
	var missCodes []string
	for _, f := range rem {
		if code, ok := localeToCode[f]; ok {
			missCodes = append(missCodes, code)
		}
	}
	if len(missCodes) > 0 && a.localeBase != nil && groundHandled && kdb.EnrichGroundStrict() {
		// 정책 스킵 — L4 를 부르지 않았음을 호출측에 알린다. 이 표시가 없으면
		// enrichOne 이 "시도했으나 실패"로 집계해 attempts 를 소진시킨다.
		for _, f := range rem {
			if _, ok := localeToCode[f]; ok && skipped != nil {
				skipped[f] = true
			}
		}
	}
	if len(missCodes) > 0 && a.localeBase != nil && !(groundHandled && kdb.EnrichGroundStrict()) {
		in := makeFillInput(r, missCodes, wd, sitelinks)
		var res aijudge.FillResult
		if err := a.localeBase.CallJSON(ctx, in, &res); err == nil {
			// 호출 성공 시에만 "gpt-5.5 까지 시도함" 으로 기록 — transport
			// 실패(타임아웃/브레이커)가 attempts 를 소진해 채울 수 있는 필드가
			// 조기 exhausted 되는 것을 막는다(codex http_error 가 상시 수 % 존재).
			for _, f := range rem {
				if _, ok := localeToCode[f]; ok {
					tried[f] = "gpt-5.5"
				}
			}
			for _, sp := range res.Spellings {
				col := "canonical_" + sp.Locale
				if !contains(rem, col) || strings.TrimSpace(sp.Value) == "" {
					continue
				}
				if a.writeLocale(ctx, pool, r, col, sp.Value, string(kdb.SourceCodexFallback)) {
					filledFields[col] = "gpt-5.5"
				}
			}
		} else if failed != nil {
			for _, f := range rem {
				if _, ok := localeToCode[f]; ok {
					failed[f] = true
				}
			}
		}
	}
}

// zhConvertibleSources — zh 정규화로 덮어써도 되는 source(저신뢰/위키 계열). 운영자·
// 매체합의·현지매체관측(rss-observation)·권위API 는 보존(실제 통용 표기일 수 있음).
const zhConvertibleSources = `('', 'wikidata-label', 'wikipedia-langlinks', 'wikipedia-sitelink', 'wikipedia-zh-variant', 'codex-fallback')`

// fillZhVariants — canonical_zh=간체, canonical_zh_hant=번체로 정규화. zh/zh_hant 중
// 한자 값(아무 변종)을 기준으로 간체/번체를 각각 생성해, 빈 칸은 채우고 변종이
// 틀린 칸(예: 간체 칸의 번체)은 교정한다. 운영자·매체합의 등은 안 건드린다.
func (a *Agent) fillZhVariants(ctx context.Context, pool *pgxpool.Pool, r *record, filledFields, tried map[string]string) {
	han := strings.TrimSpace(r.localeVals["canonical_zh"])
	if han == "" {
		han = strings.TrimSpace(r.localeVals["canonical_zh_hant"])
	}
	if !zhvariant.HasHan(han) {
		return
	}
	a.normalizeZhCol(ctx, pool, r, "canonical_zh", zhvariant.ToSimplified(ctx, han), filledFields, tried)
	a.normalizeZhCol(ctx, pool, r, "canonical_zh_hant", zhvariant.ToTraditional(ctx, han), filledFields, tried)
}

// normalizeZhCol — col 을 val(간체/번체 변환 결과)로 맞춘다. 빈 칸이면 채우고,
// convertible source 의 다른 값이면 교정. 같으면 no-op. operator/매체합의 보존.
// val=="" 면 변환 fetch 실패라 쓰기를 건너뛴다(번체→간체칸 오염 방지).
func (a *Agent) normalizeZhCol(ctx context.Context, pool *pgxpool.Pool, r *record, col, val string, filledFields, tried map[string]string) {
	if pool == nil || strings.TrimSpace(val) == "" || r.localeVals[col] == val {
		return
	}
	// dataqa 수렴 가드(2026-06-20): 오염으로 비워진 값 재주입 금지 — writeLocale(202행)과
	// 대칭. 누락 시 zh-variant 정규화가 dataqa 가 비운 zh 오염값을 도로 채우는 핑퐁 발생.
	if r.isSuppressed(col, val) {
		return
	}
	tried[col] = "zh-variant"
	// operator_locked 플래그가 아니라 source 기준으로 보호한다 — 잠긴 유명인물의
	// 자동 채움값(wikidata-label)은 교정 대상, 운영자가 직접 넣은 값(source=operator)
	// 만 보존. convertible 집합이 operator/매체합의/현지매체관측을 이미 제외한다.
	tag, err := pool.Exec(ctx,
		`UPDATE kwave_entities SET `+col+`=$2, `+col+`_source='wikipedia-zh-variant', updated_at=now()
		  WHERE id=$1
		    AND COALESCE(`+col+`_source,'') IN `+zhConvertibleSources+`
		    AND COALESCE(`+col+`,'') <> $2`, r.id, val)
	if err == nil && tag.RowsAffected() > 0 {
		r.localeVals[col] = val
		filledFields[col] = "zh-variant"
	}
}

// applyLocaleMap writes external-source locale values (priority-aware, empty
// only) for the requested columns + aliases_ko, recording fills.
func (a *Agent) applyLocaleMap(ctx context.Context, pool *pgxpool.Pool, r *record, want []string, m map[string][]string, src kdb.Source, filledFields, tried map[string]string, label string) {
	wantSet := map[string]bool{}
	for _, f := range want {
		wantSet[f] = true
		tried[f] = label
	}
	for code, vals := range m {
		vals = trimNonEmpty(vals)
		if len(vals) == 0 {
			continue
		}
		if code == "ko" {
			if wantSet["aliases_ko"] {
				if a.appendAliasesKo(ctx, pool, r, vals) {
					filledFields["aliases_ko"] = label
				}
			}
			continue
		}
		col := "canonical_" + code
		if !wantSet[col] {
			continue
		}
		if a.writeLocale(ctx, pool, r, col, vals[0], string(src)) {
			filledFields[col] = label
		}
	}
}

// writeLocale sets a canonical_<loc> column ONLY when it is currently empty
// (no-overwrite). Returns true if a value was written.
func (a *Agent) writeLocale(ctx context.Context, pool *pgxpool.Pool, r *record, col, val, source string) bool {
	if pool == nil || strings.TrimSpace(val) == "" {
		return false
	}
	// locale 문자셋 가드: 영문 등 칸에 한글/한자 등 부적합 표기가 외부 소스로부터
	// 들어오는 오염을 차단 (col="canonical_en" → locale="en").
	if loc := strings.TrimPrefix(col, "canonical_"); !kdb.IsValidSpellingForLocale(loc, val) {
		return false
	}
	// dataqa 수렴 가드: 오염으로 비워진 값 재주입 금지.
	if r.isSuppressed(col, val) {
		return false
	}
	srcCol := col + "_source"
	tag, err := pool.Exec(ctx,
		`UPDATE kwave_entities SET `+col+`=$2, `+srcCol+`=$3, updated_at=now()
		  WHERE id=$1 AND (`+col+` IS NULL OR `+col+`='')`, r.id, val, source)
	if err == nil && tag.RowsAffected() > 0 {
		r.localeVals[col] = val
		return true
	}
	return false
}

// appendAliasesKo de-dupes new Korean aliases into aliases_ko, excluding the
// canonical itself. Returns true if at least one new alias was added.
func (a *Agent) appendAliasesKo(ctx context.Context, pool *pgxpool.Pool, r *record, vals []string) bool {
	if pool == nil {
		return false
	}
	add := make([]string, 0, len(vals))
	have := map[string]bool{r.ko: true}
	for _, x := range r.aliasesKo {
		have[strings.TrimSpace(x)] = true
	}
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if v != "" && !have[v] {
			add = append(add, v)
			have[v] = true
		}
	}
	if len(add) == 0 {
		return false
	}
	tag, err := pool.Exec(ctx, `
UPDATE kwave_entities
   SET aliases_ko = (SELECT ARRAY(SELECT DISTINCT x FROM unnest(COALESCE(aliases_ko,'{}'::text[]) || $2::text[]) x WHERE x <> '')),
       updated_at = now()
 WHERE id = $1`, r.id, add)
	if err == nil && tag.RowsAffected() > 0 {
		r.aliasesKo = append(r.aliasesKo, add...)
		return true
	}
	return false
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// wikidataEntity — 저장된 QID 가 있으면 그것으로 직접 Fetch 하고, 없을 때만 이름으로
// 검색한다.
//
// ★왜(2026-08-07 실측): 종전에는 앵커를 갖고 있어도 항상 SearchAndFetch(canonical_ko) 로
// **다시 검색**했다. 그런데 그 이름검색은 오매칭 방지 가드(박보검→허성진 사고로 도입)에
// 걸려 상당수가 거부된다 — 3시간 로그에서 wikidata 조회 18건이 **전부** "이름요소 후보
// 배제"였다. 즉 QID 를 이미 확정해 둔 엔티티조차 L3 라벨·L3.2 langlink 에 닿지 못했다.
//
// 같은 파일의 TMDb 계층은 이 교훈을 이미 반영해 id 를 external_refs 에 적재하고 재검색을
// 피한다(tmdbTitles 주석 "re-search 방지"). Wikidata 만 빠져 있었다.
//
// QID 직접 조회는 오매칭을 새로 만들지 않는다 — 그 QID 는 과거에 이름 일치 검증을 통과해
// 적재된 값이다. 반환하는 cand 는 QID 경로에선 nil 이다(설명문은 L4 힌트로만 쓰인다).
func (a *Agent) wikidataEntity(ctx context.Context, pool *pgxpool.Pool, r *record) (*wikidata.Entity, *wikidata.Candidate, error) {
	if pool != nil {
		var qid string
		if err := pool.QueryRow(ctx, `
SELECT external_id FROM kwave_entity_external_refs
 WHERE entity_id=$1 AND provider='wikidata' AND external_id <> '' LIMIT 1`, r.id).Scan(&qid); err == nil {
			if qid = strings.TrimSpace(qid); qid != "" {
				ent, err := a.src.wd.Fetch(ctx, qid)
				if err == nil && ent != nil {
					return ent, nil, nil
				}
				// QID 조회 실패(삭제된 항목·일시장애)는 이름검색으로 넘어간다.
			}
		}
	}
	return a.src.wd.SearchAndFetch(ctx, r.ko)
}

// tmdbTitles resolves the TMDb token (DB > .env) and fetches the work's official
// localized titles (KDB-locale → [title]). On a confident match it caches the
// tmdb id in external_refs (re-search 방지). Empty map if no token / no match.
func (a *Agent) tmdbTitles(ctx context.Context, pool *pgxpool.Pool, r *record) map[string][]string {
	if a.src.tmdb == nil || pool == nil {
		return nil
	}
	token, _ := apikeys.Resolve(ctx, pool, "KDB_TMDB_API_TOKEN")
	if strings.TrimSpace(token) == "" {
		return nil
	}
	m, id, err := a.src.tmdb.Enrich(ctx, token, r.ko, r.entityType)
	if err != nil || id == 0 || len(m) == 0 {
		return nil
	}
	media := "movie"
	if r.entityType == "drama" || r.entityType == "show" {
		media = "tv"
	}

	// ★식별자 기록은 **앵커 드레인과 같은 엄격도**로만 한다(2026-08-15 실측 사고).
	//
	// 종전에는 Enrich 의 느슨한 매치(정규화 제목 일치)로 곧장 ref 를 쓰고, ON CONFLICT 로
	// **기존 external_id 까지 덮었다**. 그 결과 두 가지가 실제로 일어났다:
	//
	//   ① tmdb-anchor 가 `foreign-original`(정확·유일 일치했으나 original_language≠ko —
	//      한국어 개봉제목 충돌)로 **거부한 앵커를 이 경로가 18분 뒤에 되살렸다**.
	//      실측: `범죄자` → /tv/322571 은 일본 드라마 `犯罪者`(2026)다. 앵커가 붙자
	//      로케일 칸까지 그 작품 값(ja `Hanzaisha`, zh `犯罪者`)으로 다시 채워졌다.
	//   ② 운영자가 오부착을 고쳐 재부착해 둔 id 도 다음 회차에 조용히 되돌아간다
	//      (`소울메이트`는 ko-KR 검색 1위가 동명의 영어권 영화 Soulm8te 다).
	//
	// 제목 회수(m)는 느슨한 매치로도 쓸모가 있으니 그대로 반환하고, **정체 단언(ref)만**
	// 엄격 매처로 가른다. 규칙 사본을 만들지 않으려고 앵커 드레인이 쓰는
	// SearchExactKoreanID 를 그대로 호출한다(정규화 정확일치 + 유일성 + 원작언어 ko).
	strictID, ambiguous, foreign, seasonOnly, serr := a.src.tmdb.SearchExactKoreanID(ctx, token, r.ko, r.entityType)
	if !shouldRecordTMDbRef(id, strictID, ambiguous, foreign, seasonOnly, serr) {
		// ★제목도 함께 버린다(2026-08-15b 정정). 처음엔 "정체 단언(ref)만 엄격하게,
		// 제목 회수(m)는 느슨해도 된다"고 했는데 **틀렸다** — 실측으로 되돌아왔다:
		// ref 는 막혔는데 `범죄자` 의 ja/zh 가 14분 만에 다시 `Hanzaisha`/`犯罪者` 가 됐다.
		// 느슨한 매치가 집은 건 *다른 작품*이고, 그 작품의 현지제목은 우리 엔티티의
		// 현지제목이 아니다. 앵커 드레인이 이 매치를 정체 근거로 못 쓴다고 판정했으면
		// 그 매치에서 나온 로케일 값도 쓸 수 없다 — 로케일은 정체의 하위 결과다.
		//
		// 정당한 외국작(`어느 가족`/万引き家族 등)이 손해 보지 않는 이유: 그쪽은 이미
		// 앵커를 갖고 있어 tmdb_locale_drain(확정 id 조회)이 제목을 채운다. 검색 기반
		// 제목 채움은 검색 기반 앵커링만큼만 믿을 수 있다는 게 이 레인의 규칙이다.
		return nil
	}
	// 기존 행이 있으면 external_id 는 건드리지 않는다 — 앞선 엄격한 판정(또는 운영자
	// 교정)을 뒤에 온 판정이 조용히 갈아치우면 앵커가 바뀐 사실 자체가 기록에 안 남는다.
	_, _ = pool.Exec(ctx, `
INSERT INTO kwave_entity_external_refs (entity_id, provider, external_id, url, confidence, fetched_at)
VALUES ($1, 'tmdb', $2, $3, 0.800, now())
ON CONFLICT (entity_id, provider) DO UPDATE SET fetched_at=now()`,
		r.id, strconv.Itoa(id), "https://www.themoviedb.org/"+media+"/"+strconv.Itoa(id))
	return m
}

// shouldRecordTMDbRef — 느슨한 매치(Enrich)로 얻은 id 를 **정체 단언(external_ref)** 으로
// 적어도 되는가. 앵커 드레인의 엄격 매처 결과와 전부 합치할 때만 true.
//
// looseID 는 제목 회수용 매치(정규화 제목 일치), strictID 는 유일성 + 원작언어 ko 까지
// 통과한 매치다. 둘이 다르면 느슨한 쪽이 다른 작품을 잡은 것이므로 적지 않는다.
// 전송실패(err)도 적지 않는다 — "물어보지 못했다"를 "맞다"로 바꾸면 안 된다.
func shouldRecordTMDbRef(looseID, strictID int, ambiguous, foreign, seasonOnly bool, err error) bool {
	if err != nil || ambiguous || foreign || seasonOnly {
		return false
	}
	return strictID != 0 && strictID == looseID
}

// musicbrainzAliases searches MusicBrainz for the artist and returns its
// locale-keyed aliases (the L2 source). Empty map on no match / error.
// 확정된 MBID 는 external_ref 로 적재한다 — 이 cascade 도 enrich/orchestrator 와
// 똑같이 MBID 를 버리던 누수원이었다(2026-08-03, api-source-no-ref).
func (a *Agent) musicbrainzAliases(ctx context.Context, pool *pgxpool.Pool, entityID uuid.UUID, ko string) map[string][]string {
	if a.src.mb == nil {
		return nil
	}
	// FindAliases: 반환 artist 가 ko 와 정규화 일치할 때만 alias 반환 (오매칭 가드).
	mbid, aliases, err := a.src.mb.FindAliases(ctx, ko)
	if err != nil || len(aliases) == 0 {
		return nil
	}
	kdb.RecordAPIRef(ctx, pool, entityID, "musicbrainz", mbid, musicbrainz.ArtistURL(mbid), 0.800)
	return map[string][]string(aliases)
}
