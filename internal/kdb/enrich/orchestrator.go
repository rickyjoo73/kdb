// Package enrich — 다국어 cascade orchestrator.
//
// 운영자 정공법 (사용자 지시):
//  1. 로컬 (L)   — KDB 자체 매체 합의 + alias 매칭 (이미 RSS poll/SweepPromote 가 처리)
//  2. 권위 API (O) — entity_type 별 (TMDb/KOFIC/KMDb 미발급, MusicBrainz 무인증 OK)
//  3. Wikidata (W) — 9 locale + sitelinks
//  4. LLM (codex-fallback) — 빈 칸만 합성, conf 0.5 default
//
// 각 layer 는 비어있는 locale 만 채움. ShouldReplace 룰 적용 — 우선순위
// 높은 source (예: L) 가 이미 있으면 덮지 않음. 결과 EnrichReport.
package enrich

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb"
	"github.com/rickyjoo73/kdb/internal/kdb/aijudge"
	"github.com/rickyjoo73/kdb/internal/kdb/musicbrainz"
	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
)

// Orchestrator — entity 단위 enrich 실행자.
type Orchestrator struct {
	Pool *pgxpool.Pool

	Wikidata    *wikidata.Client
	MusicBrainz *musicbrainz.Client
	AIJudge     *aijudge.Client
}

func New(pool *pgxpool.Pool) *Orchestrator {
	return &Orchestrator{
		Pool:        pool,
		Wikidata:    wikidata.New(),
		MusicBrainz: musicbrainz.New(),
		AIJudge:     aijudge.New(),
	}
}

// Report — Enrich 결과 요약 (운영자 flash 메시지용).
type Report struct {
	EntityID   uuid.UUID
	Ko         string
	EntityType string

	LayersRun  []string        // ["wikidata", "musicbrainz", "codex-fallback"]
	Filled     map[string]Fill // locale → 채운 값/source
	StillEmpty []string        // 끝까지 못 채운 locale
	Duration   time.Duration
	Errors     []string
}

type Fill struct {
	Value  string
	Source string // wikidata-label / musicbrainz / codex-fallback ...
}

// All KDB locale columns (canonical_X).
var allLocales = []string{"en", "ja", "vi", "id", "es", "pt_br", "zh_hant", "zh"}

// localeColumns — canonical_X / aliases_X / canonical_X_source 컬럼명.
func localeColumns(loc string) (canonCol, aliasCol, srcCol string) {
	switch loc {
	case "en":
		return "canonical_en", "aliases_en", "canonical_en_source"
	case "ja":
		return "canonical_ja", "aliases_ja", "canonical_ja_source"
	case "vi":
		return "canonical_vi", "aliases_vi", "canonical_vi_source"
	case "id":
		return "canonical_id", "aliases_id", "canonical_id_source"
	case "es":
		return "canonical_es", "aliases_es", "canonical_es_source"
	case "pt_br":
		return "canonical_pt_br", "aliases_pt_br", "canonical_pt_br_source"
	case "zh_hant":
		return "canonical_zh_hant", "aliases_zh_hant", "canonical_zh_hant_source"
	case "zh":
		return "canonical_zh", "aliases_zh", "canonical_zh_source"
	}
	return "", "", ""
}

// snapshot — 현재 entity 의 9 locale value + source.
type snapshot struct {
	ID         uuid.UUID
	Ko         string
	EntityType string
	AliasesKo  []string
	Values     map[string]string
	Sources    map[string]string
}

func loadSnapshot(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (*snapshot, error) {
	s := &snapshot{
		ID:      id,
		Values:  map[string]string{},
		Sources: map[string]string{},
	}
	var en, enS, ja, jaS, vi, viS, id_, idS, es, esS, pt, ptS, zhH, zhHS, zh, zhS string
	err := pool.QueryRow(ctx, `
SELECT canonical_ko, entity_type::text, aliases_ko,
       COALESCE(canonical_en,''),    COALESCE(canonical_en_source,''),
       COALESCE(canonical_ja,''),    COALESCE(canonical_ja_source,''),
       COALESCE(canonical_vi,''),    COALESCE(canonical_vi_source,''),
       COALESCE(canonical_id,''),    COALESCE(canonical_id_source,''),
       COALESCE(canonical_es,''),    COALESCE(canonical_es_source,''),
       COALESCE(canonical_pt_br,''), COALESCE(canonical_pt_br_source,''),
       COALESCE(canonical_zh_hant,''), COALESCE(canonical_zh_hant_source,''),
       COALESCE(canonical_zh,''),    COALESCE(canonical_zh_source,'')
FROM kwave_entities WHERE id = $1`, id).Scan(
		&s.Ko, &s.EntityType, &s.AliasesKo,
		&en, &enS, &ja, &jaS, &vi, &viS, &id_, &idS,
		&es, &esS, &pt, &ptS, &zhH, &zhHS, &zh, &zhS)
	if err != nil {
		return nil, err
	}
	s.Values["en"] = en
	s.Sources["en"] = enS
	s.Values["ja"] = ja
	s.Sources["ja"] = jaS
	s.Values["vi"] = vi
	s.Sources["vi"] = viS
	s.Values["id"] = id_
	s.Sources["id"] = idS
	s.Values["es"] = es
	s.Sources["es"] = esS
	s.Values["pt_br"] = pt
	s.Sources["pt_br"] = ptS
	s.Values["zh_hant"] = zhH
	s.Sources["zh_hant"] = zhHS
	s.Values["zh"] = zh
	s.Sources["zh"] = zhS
	return s, nil
}

func missingLocales(s *snapshot) []string {
	out := []string{}
	for _, loc := range allLocales {
		if s.Values[loc] == "" {
			out = append(out, loc)
		}
	}
	return out
}

// Enrich — 단일 entity 의 빈 locale 을 4-layer cascade 로 채움.
func (o *Orchestrator) Enrich(ctx context.Context, id uuid.UUID) (*Report, error) {
	start := time.Now()
	rep := &Report{EntityID: id, Filled: map[string]Fill{}, LayersRun: []string{}}

	snap, err := loadSnapshot(ctx, o.Pool, id)
	if err != nil {
		return rep, fmt.Errorf("load snapshot: %w", err)
	}
	rep.Ko = snap.Ko
	rep.EntityType = snap.EntityType

	// L1 (로컬 매체 합의) 는 RSS poll/SweepPromote 가 이미 누적. 별도 호출 없음.

	// L2: MusicBrainz — group / singer (person + role=singer/idol/rapper) 일 때만.
	if (snap.EntityType == "group" || snap.EntityType == "person") && len(missingLocales(snap)) > 0 {
		if applied, err := o.runMusicBrainz(ctx, snap); err == nil {
			rep.LayersRun = append(rep.LayersRun, "musicbrainz")
			for loc, v := range applied {
				rep.Filled[loc] = v
			}
		} else if !errors.Is(err, errNoMatch) {
			rep.Errors = append(rep.Errors, "mb: "+err.Error())
		}
		// L2 가 채운 값 반영.
		if snap2, _ := loadSnapshot(ctx, o.Pool, id); snap2 != nil {
			snap = snap2
		}
	}

	// L3: Wikidata — 모든 entity 공통.
	var wikidataInfo *wdInfo
	if len(missingLocales(snap)) > 0 {
		applied, info, err := o.runWikidata(ctx, snap)
		if err == nil {
			rep.LayersRun = append(rep.LayersRun, "wikidata")
			wikidataInfo = info
			for loc, v := range applied {
				rep.Filled[loc] = v
			}
			if snap2, _ := loadSnapshot(ctx, o.Pool, id); snap2 != nil {
				snap = snap2
			}
		} else if !errors.Is(err, errNoMatch) {
			rep.Errors = append(rep.Errors, "wd: "+err.Error())
		}
	}

	// L4: Codex LLM fallback — 그래도 빈 칸 남으면.
	if len(missingLocales(snap)) > 0 {
		if applied, err := o.runCodexFallback(ctx, snap, wikidataInfo); err == nil {
			rep.LayersRun = append(rep.LayersRun, "codex-fallback")
			for loc, v := range applied {
				rep.Filled[loc] = v
			}
			if snap2, _ := loadSnapshot(ctx, o.Pool, id); snap2 != nil {
				snap = snap2
			}
		} else {
			rep.Errors = append(rep.Errors, "codex: "+err.Error())
		}
	}

	// 동명이인 구분 신호 (2026-05-29): person entity 면 Wikidata claims 에서
	// agency(P264/P463/P108) / birth_year(P569) / notable_works(P800) 를 추출해
	// person_details 에 채운다. global presence (foreign-locale 표기) 있는 인물만 —
	// KDB 가치는 global localization 이므로 Korean-only 인물은 저우선 skip.
	if snap.EntityType == "person" && snapHasGlobalPresence(snap) {
		qid := ""
		if wikidataInfo != nil {
			qid = wikidataInfo.QID
		}
		// L3 가 missing locale 없어 skip 됐을 수 있으니, QID 없으면 외부 ref 에서 조회.
		if qid == "" {
			_ = o.Pool.QueryRow(ctx,
				`SELECT external_id FROM kwave_entity_external_refs
				  WHERE entity_id=$1 AND provider='wikidata' LIMIT 1`, id).Scan(&qid)
		}
		if qid != "" {
			if claims, cErr := o.Wikidata.LookupClaims(ctx, qid); cErr == nil && claims != nil {
				o.persistPersonClaims(ctx, id, claims)
			}
		}
		// TODO(tmdb/kofic): filmography(작품/필모) 보강 — API key 확보 후 여기서
		// notable_works 를 TMDb credits / KOFIC 작품목록으로 확장한다.
	}

	rep.StillEmpty = missingLocales(snap)
	rep.Duration = time.Since(start)
	return rep, nil
}

// snapHasGlobalPresence — snapshot 의 9 locale value 중 하나라도 비어있지 않으면 global.
func snapHasGlobalPresence(s *snapshot) bool {
	for _, loc := range allLocales {
		if strings.TrimSpace(s.Values[loc]) != "" {
			return true
		}
	}
	return false
}

// persistPersonClaims — agency/birth_year/notable_works 를 person_details 에
// 채운다. 빈 값/0 은 기존 값 보호 (덮어쓰지 않음). 행 없으면 먼저 생성.
func (o *Orchestrator) persistPersonClaims(ctx context.Context, entityID uuid.UUID, c *wikidata.PersonClaims) {
	if c == nil {
		return
	}
	works := make([]string, 0, len(c.NotableWorks))
	for _, w := range c.NotableWorks {
		if w = strings.TrimSpace(w); w != "" {
			works = append(works, w)
		}
	}
	if strings.TrimSpace(c.Agency) == "" && c.BirthYear == 0 && len(works) == 0 {
		return
	}
	_, _ = o.Pool.Exec(ctx, `
INSERT INTO kwave_entity_person_details (entity_id, primary_role)
VALUES ($1, 'other'::person_role)
ON CONFLICT (entity_id) DO NOTHING`, entityID)
	_, _ = o.Pool.Exec(ctx, `
UPDATE kwave_entity_person_details
   SET agency        = COALESCE(NULLIF($2,''), agency),
       birth_year    = COALESCE(NULLIF($3,0), birth_year),
       notable_works = CASE
                         WHEN array_length(notable_works,1) IS NULL AND $4::text[] <> '{}'::text[]
                           THEN $4::text[]
                         ELSE notable_works
                       END
 WHERE entity_id = $1`, entityID, strings.TrimSpace(c.Agency), c.BirthYear, works)
}

var errNoMatch = errors.New("no match")

// --- Layer 2: MusicBrainz ------------------------------------------------

func (o *Orchestrator) runMusicBrainz(ctx context.Context, snap *snapshot) (map[string]Fill, error) {
	artists, err := o.MusicBrainz.Search(ctx, snap.Ko)
	if err != nil {
		return nil, err
	}
	if len(artists) == 0 {
		return nil, errNoMatch
	}
	// 첫 매칭 (KR + Score 우선) MBID 로 alias 가져옴.
	mbid := artists[0].ID
	aliases, err := o.MusicBrainz.FetchAliases(ctx, mbid)
	if err != nil {
		return nil, err
	}
	if len(aliases) == 0 {
		return nil, errNoMatch
	}
	return o.applyFromMap(ctx, snap, aliases, kdb.SourceMusicBrainz)
}

// --- Layer 3: Wikidata ----------------------------------------------------

type wdInfo struct {
	QID         string
	Description string
	Sitelinks   map[string]string
}

func (o *Orchestrator) runWikidata(ctx context.Context, snap *snapshot) (map[string]Fill, *wdInfo, error) {
	ent, cand, err := o.Wikidata.SearchAndFetch(ctx, snap.Ko)
	if err != nil {
		return nil, nil, err
	}
	if ent == nil {
		return nil, nil, errNoMatch
	}
	info := &wdInfo{QID: ent.QID, Sitelinks: ent.Sitelinks}
	if cand != nil {
		info.Description = cand.Description
	}
	// Wikidata Labels: 단일 string per locale → map[locale][]string 로 변환 후 apply.
	asMap := map[string][]string{}
	for loc, v := range ent.Labels {
		if v != "" {
			asMap[loc] = []string{v}
		}
	}
	applied, err := o.applyFromMap(ctx, snap, asMap, kdb.SourceWikidataLabel)
	if err != nil {
		return applied, info, err
	}
	// external_refs 에 Q-ID 매핑 기록 (이미 있으면 skip).
	_, _ = o.Pool.Exec(ctx, `
INSERT INTO kwave_entity_external_refs (entity_id, provider, external_id, url, confidence, fetched_at)
VALUES ($1, 'wikidata', $2, $3, 0.85, now())
ON CONFLICT DO NOTHING`,
		snap.ID, ent.QID, "https://www.wikidata.org/wiki/"+ent.QID)
	// sitelinks 를 source_urls 에 추가.
	if len(ent.Sitelinks) > 0 {
		urls := make([]string, 0, len(ent.Sitelinks))
		for _, u := range ent.Sitelinks {
			urls = append(urls, u)
		}
		_, _ = o.Pool.Exec(ctx, `
UPDATE kwave_entities
   SET source_urls = (SELECT ARRAY(SELECT DISTINCT x FROM unnest(source_urls || $2::text[]) x WHERE x <> ''))
 WHERE id = $1`, snap.ID, urls)
	}
	return applied, info, nil
}

// --- Layer 4: Codex LLM fallback -----------------------------------------

func (o *Orchestrator) runCodexFallback(ctx context.Context, snap *snapshot, wd *wdInfo) (map[string]Fill, error) {
	miss := missingLocales(snap)
	if len(miss) == 0 {
		return nil, nil
	}
	known := map[string]string{}
	for _, loc := range allLocales {
		if v := snap.Values[loc]; v != "" {
			known[loc] = v
		}
	}
	in := &aijudge.FillInput{
		Ko:         snap.Ko,
		EntityType: snap.EntityType,
		AliasesKo:  snap.AliasesKo,
		Known:      known,
		Missing:    miss,
	}
	if wd != nil {
		in.Wikidata = &aijudge.ClassifyWikidata{QID: wd.QID, Description: wd.Description}
		in.Sitelinks = wd.Sitelinks
	}
	r, err := o.AIJudge.FillLocale(ctx, in)
	if err != nil {
		return nil, err
	}
	if r == nil || len(r.Spellings) == 0 {
		return nil, nil
	}
	out := map[string]Fill{}
	for _, sp := range r.Spellings {
		canonCol, _, srcCol := localeColumns(sp.Locale)
		if canonCol == "" {
			continue
		}
		// 빈 칸만 채움 + source=codex-fallback (priority 7).
		_, err := o.Pool.Exec(ctx, `
UPDATE kwave_entities SET `+canonCol+` = $2, `+srcCol+` = 'codex-fallback', updated_at = now()
WHERE id = $1 AND (`+canonCol+` IS NULL OR `+canonCol+` = '')`, snap.ID, sp.Value)
		if err == nil {
			out[sp.Locale] = Fill{Value: sp.Value, Source: string(kdb.SourceCodexFallback)}
		}
	}
	return out, nil
}

// --- 공용: priority-aware UPDATE -----------------------------------------

// applyFromMap — 외부 source 의 locale → spellings 후보들 → DB 업데이트.
// ShouldReplace 룰 적용 — 빈 칸만 채움, 또는 priority 가 더 높을 때만 덮음.
func (o *Orchestrator) applyFromMap(ctx context.Context, snap *snapshot, m map[string][]string, src kdb.Source) (map[string]Fill, error) {
	out := map[string]Fill{}
	for loc, vals := range m {
		if len(vals) == 0 {
			continue
		}
		canonCol, _, srcCol := localeColumns(loc)
		if canonCol == "" {
			continue
		}
		newVal := vals[0]
		curVal := snap.Values[loc]
		curSrc := kdb.Source(snap.Sources[loc])
		replace, _ := kdb.ShouldReplace(curSrc, curVal, src, newVal)
		if !replace {
			// 빈 칸이라면 ShouldReplace 가 cur priority=99 vs new < 99 → replace=true 일 것이지만 안전하게.
			if curVal != "" {
				continue
			}
		}
		if _, err := o.Pool.Exec(ctx,
			`UPDATE kwave_entities SET `+canonCol+` = $2, `+srcCol+` = $3, updated_at = now() WHERE id = $1`,
			snap.ID, newVal, string(src)); err == nil {
			out[loc] = Fill{Value: newVal, Source: string(src)}
		}
	}
	return out, nil
}
