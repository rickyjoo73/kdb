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
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb"
	"github.com/rickyjoo73/kdb/internal/kdb/aijudge"
	"github.com/rickyjoo73/kdb/internal/kdb/apikeys"
	"github.com/rickyjoo73/kdb/internal/kdb/homonym"
	"github.com/rickyjoo73/kdb/internal/kdb/kofic"
	"github.com/rickyjoo73/kdb/internal/kdb/musicbrainz"
	"github.com/rickyjoo73/kdb/internal/kdb/tmdb"
	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
)

// Orchestrator — entity 단위 enrich 실행자.
type Orchestrator struct {
	Pool *pgxpool.Pool

	Wikidata    *wikidata.Client
	MusicBrainz *musicbrainz.Client
	TMDb        *tmdb.Client
	KOFIC       *kofic.Client
	AIJudge     *aijudge.Client
}

func New(pool *pgxpool.Pool) *Orchestrator {
	return &Orchestrator{
		Pool:        pool,
		Wikidata:    wikidata.New(),
		MusicBrainz: musicbrainz.New(),
		TMDb:        tmdb.New(),
		KOFIC:       kofic.New(),
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
	// Suppressed — dataqa(gpt-5.5)가 오염으로 비운 적 있는(미복원) (locale → 정규화 값)
	// 집합. enrich 자동소스가 같은 값을 재주입하면 dataqa 가 또 비우는 무한 핑퐁이
	// 생긴다(나비→Ella Gross 433회). 자동 쓰기 전에 여기서 막아 수렴시킨다.
	Suppressed map[string]map[string]bool
}

// normForSuppress — 이름 비교용 정규화. dataqa 의 normExpr(SQL) 및
// wikidata/musicbrainz normalizeName 과 *동일한* 문자셋을 제거해야 suppression
// 매칭이 dataqa 가 비운 기준과 일치한다(공백/중점/하이픈/마침표/언더스코어/따옴표/쉼표).
func normForSuppress(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch r {
		case ' ', '\t', '·', '・', '-', '.', '_', '\'', '"', ',':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// isSuppressed — (loc,val)이 과거 dataqa 오염판정으로 비워진 값과 정규화 일치하면 true.
// operator 가 Revert 하면 reverted_at 이 채워져 로드 시 제외되므로 suppression 도 해제된다.
func (s *snapshot) isSuppressed(loc, val string) bool {
	set := s.Suppressed[loc]
	if set == nil {
		return false
	}
	// 핑퐁 수렴 가드(2026-06-20): 같은 locale 이 서로 다른 값으로 2회+ dataqa 오염
	// clear 됐으면(문자변종으로 per-value suppression 을 우회하는 핑퐁 — 동명이인 CJK
	// 표기, 나비 Ella Gross 433회 류), 값 무관하게 자동 enrich 쓰기를 막는다. 이 locale
	// 은 운영자/검수가 정값을 넣을 때까지 자동소스 재주입 금지(무한 핑퐁 종료).
	if len(set) >= 2 {
		return true
	}
	return set[normForSuppress(val)]
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

	// 수렴 가드 로드: dataqa 가 이 entity 에서 오염으로 비운(미복원) (locale,값) 들.
	// 자동소스 재주입을 차단해 dataqa↔enrich 무한 핑퐁을 끊는다.
	s.Suppressed = map[string]map[string]bool{}
	if rows, e := pool.Query(ctx, `
SELECT locale, old_value FROM kwave_kdb_dataqa_log
 WHERE entity_id=$1 AND verdict='contaminated' AND reverted_at IS NULL`, id); e == nil {
		defer rows.Close()
		for rows.Next() {
			var loc, ov string
			if rows.Scan(&loc, &ov) == nil && ov != "" {
				if s.Suppressed[loc] == nil {
					s.Suppressed[loc] = map[string]bool{}
				}
				s.Suppressed[loc][normForSuppress(ov)] = true
			}
		}
	}
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

	// L2b: TMDb / KOFIC — movie / drama / show 의 다국어 제목(권위 API).
	if (snap.EntityType == "movie" || snap.EntityType == "drama" || snap.EntityType == "show") && len(missingLocales(snap)) > 0 {
		if applied := o.runVideoAPIs(ctx, snap); len(applied) > 0 {
			rep.LayersRun = append(rep.LayersRun, "tmdb/kofic")
			for loc, v := range applied {
				rep.Filled[loc] = v
			}
			if snap2, _ := loadSnapshot(ctx, o.Pool, id); snap2 != nil {
				snap = snap2
			}
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

	// L3.5: 검색-그라운딩(SearXNG+gemma 다회투표) — codex 합성 *전에* 빈칸을 그라운딩값으로
	// (강증거→local-usage, 약증거→local-search). 신규 codex-fallback 민팅을 생산지점에서
	// 차단(누수차단: 별도 비동기 reground 가 못 따라잡던 firehose 를 source 에서 끔). flag
	// KDB_ENRICH_GROUND=1 게이트 — off 면 no-op → 아래 L4 codex 가 기존대로 폴백.
	groundHandled := false
	if len(missingLocales(snap)) > 0 {
		if n, handled, err := kdb.GroundEntity(ctx, o.Pool, id.String(), 4); err != nil {
			rep.Errors = append(rep.Errors, "ground: "+err.Error())
		} else {
			groundHandled = handled
			if n > 0 {
				rep.LayersRun = append(rep.LayersRun, "ground")
				if snap2, _ := loadSnapshot(ctx, o.Pool, id); snap2 != nil {
					snap = snap2
				}
			}
		}
	}

	// L4: Codex LLM fallback — 그래도 빈 칸 남으면. 단 strict(빈칸>틀린값) 모드에서
	// grounding 이 담당한 엔티티는 스킵 — 검색 무신호 locale 은 codex 추측값 대신 빈칸 유지
	// (오너 방침). reground 캠페인·쿨다운 만료 시 재방문. flag off 면 기존대로 codex 폴백.
	if len(missingLocales(snap)) > 0 && !(groundHandled && kdb.EnrichGroundStrict()) {
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

// RefillFromWikidata — 권위 refill(누락정보 빠른 확보): stored QID 의 Wikidata 라벨/langlink 로
// 빈칸을 채우고 codex-fallback 칸을 권위 공식표기로 업그레이드한다. Enrich() 와 달리 missingLocales
// 게이트가 없어 codex 로 채워진 칸도 권위값으로 교체한다(wikidata-label prio 5 > codex 8).
// QID-pin(runWikidata)이 동명이인 라벨복사를 차단하므로 안전. codex-fallback 층은 호출 안 함.
func (o *Orchestrator) RefillFromWikidata(ctx context.Context, id uuid.UUID) (*Report, error) {
	rep := &Report{EntityID: id, Filled: map[string]Fill{}, LayersRun: []string{}}
	snap, err := loadSnapshot(ctx, o.Pool, id)
	if err != nil {
		return rep, fmt.Errorf("load snapshot: %w", err)
	}
	rep.Ko = snap.Ko
	rep.EntityType = snap.EntityType
	applied, _, err := o.runWikidata(ctx, snap)
	if err != nil && !errors.Is(err, errNoMatch) {
		return rep, err
	}
	for loc, v := range applied {
		rep.Filled[loc] = v
	}
	if len(applied) > 0 {
		rep.LayersRun = append(rep.LayersRun, "wikidata-refill")
	}
	return rep, nil
}

// DrainAnchoredRefill — Wikidata QID 를 보유했지만 빈칸/codex locale 이 남은 active 엔티티 n건에
// 권위 refill 을 적용한다(누락정보 빠른 확보, 오너 지시). 권위 앵커가 있으니 추측 없이 공식
// 표기를 당겨온다. 매 호출 사이 가벼운 pacing(Wikidata 예의). 반환=(처리, 1개라도 채운 건수).
func (o *Orchestrator) DrainAnchoredRefill(ctx context.Context, n int) (processed, upgraded int) {
	if o.Pool == nil || n <= 0 {
		return 0, 0
	}
	// ★대상: 빈칸/codex locale 이 있고 + (QID 앵커 보유 OR 기본정보 보유)인 엔티티.
	// QID 보유 → 그 QID 직접 Fetch(공식표기). 앵커 없어도 매체언급(source_domains)·미디어
	// 관측 등 기본정보가 있으면 runWikidata 가 이름검색+이름검증+QID유일성/동명이인 가드로
	// 안전하게 발굴·부착. ★기본정보 전무한 bare string(typo 위험)은 제외(오너 원칙: 기본정보로 추적).
	rows, err := o.Pool.Query(ctx, `
SELECT e.id
  FROM kwave_entities e
 WHERE e.status='active'
   AND ( EXISTS(SELECT 1 FROM kwave_entity_external_refs x
                WHERE x.entity_id=e.id AND x.provider='wikidata' AND x.external_id<>'')
         OR COALESCE(array_length(e.source_domains,1),0) > 0
         OR EXISTS(SELECT 1 FROM kwave_media_observations m WHERE m.entity_id=e.id) )
   AND ( canonical_en=''OR canonical_ja=''OR canonical_vi=''OR canonical_id=''OR canonical_es=''
         OR canonical_pt_br=''OR canonical_zh=''OR canonical_zh_hant=''
         OR 'codex-fallback' IN (canonical_en_source,canonical_ja_source,canonical_vi_source,
              canonical_id_source,canonical_es_source,canonical_pt_br_source,canonical_zh_source,canonical_zh_hant_source) )
   AND NOT EXISTS(SELECT 1 FROM kwave_kdb_enrich_attempts a WHERE a.entity_id=e.id
                  AND a.field='wdrefill' AND a.last_attempt_at > now() - interval '14 days')
 ORDER BY e.updated_at DESC
 LIMIT $1`, n)
	if err != nil {
		return 0, 0
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()

	for _, id := range ids {
		processed++
		rep, _ := o.RefillFromWikidata(ctx, id)
		_, _ = o.Pool.Exec(ctx, `
INSERT INTO kwave_kdb_enrich_attempts (entity_id, field, attempts, last_attempt_at, last_source)
VALUES ($1,'wdrefill',1,now(),'wikidata')
ON CONFLICT (entity_id, field) DO UPDATE SET attempts=kwave_kdb_enrich_attempts.attempts+1, last_attempt_at=now()`, id)
		if rep != nil && len(rep.Filled) > 0 {
			upgraded++
		}
		time.Sleep(250 * time.Millisecond) // Wikidata 예의
	}
	return processed, upgraded
}

// DrainLanglinkUpgrade — QID 앵커 보유 codex 셀을 Wikipedia langlink(각 언어판 문서제목)로
// 업그레이드한다. 일반 enrich 의 langlink 적용은 applyEmptyOnly(빈칸 전용, 운영자 보수정책)라
// 이미 codex 가 박힌 셀은 못 건드렸다 — 그래서 QID 사이트링크가 있어도 codex 가 잔존(zh_hant
// 실측 ~50%가 zhwiki 보유인데 미적용). 이 drain 은 ★ko-label 매칭 게이트(QID ko==canonical_ko)로
// mislink(동명이인/오링크)를 먼저 차단한 뒤에만 codex 를 langlink 제목으로 교체한다(applyFromMap,
// prio6<codex8). 권위값(wikidata-label prio5 등)은 priority 룰이 보호. zhwiki(zh_hant)가 주수율,
// jawiki/viwiki 등 존재 시 동반. 14d 쿨다운. 반환=(처리수, 업그레이드 발생 엔티티수).
func (o *Orchestrator) DrainLanglinkUpgrade(ctx context.Context, n int) (processed, upgraded int) {
	if o.Pool == nil || n <= 0 {
		return 0, 0
	}
	rows, err := o.Pool.Query(ctx, `
SELECT e.id
  FROM kwave_entities e
 WHERE e.status='active'
   AND EXISTS(SELECT 1 FROM kwave_entity_external_refs x
              WHERE x.entity_id=e.id AND x.provider='wikidata' AND x.external_id<>'')
   AND 'codex-fallback' IN (canonical_en_source,canonical_ja_source,canonical_vi_source,
        canonical_id_source,canonical_es_source,canonical_pt_br_source,canonical_zh_source,canonical_zh_hant_source)
   AND COALESCE(e.notes,'') NOT LIKE '%[scope:review]%'
   AND NOT EXISTS(SELECT 1 FROM kwave_kdb_enrich_attempts a WHERE a.entity_id=e.id
                  AND a.field='langlinkupg' AND a.last_attempt_at > now() - interval '14 days')
 ORDER BY e.updated_at DESC
 LIMIT $1`, n)
	if err != nil {
		return 0, 0
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()

	for _, id := range ids {
		processed++
		if o.upgradeLanglinks(ctx, id) {
			upgraded++
		}
		_, _ = o.Pool.Exec(ctx, `
INSERT INTO kwave_kdb_enrich_attempts (entity_id, field, attempts, last_attempt_at, last_source)
VALUES ($1,'langlinkupg',1,now(),'wikipedia-langlinks')
ON CONFLICT (entity_id, field) DO UPDATE SET attempts=kwave_kdb_enrich_attempts.attempts+1, last_attempt_at=now()`, id)
		time.Sleep(250 * time.Millisecond) // Wikidata 예의
	}
	return processed, upgraded
}

// upgradeLanglinks — 단일 엔티티: stored QID Fetch → ko-label 매칭 게이트 → langlink 제목으로
// codex/빈칸 셀 교체(applyFromMap). ko 불일치·QID 부재·langlink 부재면 무변경(false).
func (o *Orchestrator) upgradeLanglinks(ctx context.Context, id uuid.UUID) bool {
	snap, err := loadSnapshot(ctx, o.Pool, id)
	if err != nil {
		return false
	}
	qid := o.storedWikidataQID(ctx, id)
	if qid == "" {
		return false
	}
	ent, err := o.Wikidata.Fetch(ctx, qid)
	if err != nil || ent == nil {
		return false
	}
	// ★ko-label 매칭 게이트(필수): QID 의 ko 라벨이 canonical_ko 와 불일치하거나 부재면
	// mislink 위험 — codex 교체를 하지 않는다(D2 안전규칙: ko 매칭 필수).
	koLab := ent.Labels["ko"]
	if koLab == "" || wikidata.NormalizeName(koLab) != wikidata.NormalizeName(snap.Ko) {
		return false
	}
	titles := ent.LanglinkTitles()
	if len(titles) == 0 {
		return false
	}
	applied, err := o.applyFromMap(ctx, snap, titles, kdb.SourceWikipediaLanglinks)
	if err != nil {
		return false
	}
	return len(applied) > 0
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

// loadPersonSignals — entity 의 저장된 동명이인 판별 신호를 읽는다. 신호가 모두
// 비어있으면 ok=false (비교 의미 없음 → 호출측이 conflict 검사를 건너뜀).
func (o *Orchestrator) loadPersonSignals(ctx context.Context, id uuid.UUID) (homonym.PersonSignals, bool) {
	var s homonym.PersonSignals
	var works []string
	err := o.Pool.QueryRow(ctx, `
SELECT COALESCE(agency,''), COALESCE(primary_role::text,''), COALESCE(birth_year,0),
       COALESCE(notable_works,'{}'::text[])
  FROM kwave_entity_person_details WHERE entity_id = $1`, id).
		Scan(&s.Agency, &s.PrimaryRole, &s.BirthYear, &works)
	if err != nil {
		return s, false
	}
	s.NotableWorks = works
	has := strings.TrimSpace(s.Agency) != "" || s.BirthYear != 0 ||
		(s.PrimaryRole != "" && s.PrimaryRole != "other") || len(works) > 0
	return s, has
}

// xrefClaimedByOther — 이 (provider, externalID) 외부ID 가 *다른 canonical_ko 의*
// active entity 에 이미 붙어있으면 그 entity 의 canonical_ko 를 돌려준다. 외부ID
// (Wikidata QID·TMDb id)는 실세계 1엔티티를 가리키므로, 서로 다른 이름의 두
// entity 가 같은 외부ID 를 갖는 것은 이름검색 false-positive(동명/유사명)로 인한
// mislink 다(예: '김하늘' 검색이 강하늘 QID Q12583151 을 반환 → 'Kang Ha-neul'
// 라벨이 김하늘에 복사). 이런 경우 라벨/ref 적용을 막아 교차인물 오데이터를 차단.
// 같은 canonical_ko(자기중복)는 무해하므로 제외 — 그건 dedup(WF-2) 영역.
func (o *Orchestrator) xrefClaimedByOther(ctx context.Context, id uuid.UUID, provider, externalID string) (string, bool) {
	if strings.TrimSpace(externalID) == "" {
		return "", false
	}
	var otherKo string
	// TMDb media 가드: movie id 와 tv id 는 별개 네임스페이스(movie 550 ≠ tv 550)인데
	// external_id 는 숫자만, provider 는 'tmdb' 로 동일 저장된다. 그래서 같은 숫자라도
	// 한쪽이 movie 타입이면 다른쪽도 movie 일 때만 충돌로 본다(entity_type='movie'
	// 동치). drama/show 는 tv 묶음. 비-tmdb(wikidata 등)는 이 조건이 항상 참(무영향).
	err := o.Pool.QueryRow(ctx, `
SELECT e.canonical_ko
  FROM kwave_entity_external_refs x
  JOIN kwave_entities e ON e.id = x.entity_id
 WHERE x.provider = $2 AND x.external_id = $3
   AND x.entity_id <> $1
   AND e.status = 'active'
   AND e.canonical_ko IS DISTINCT FROM (SELECT canonical_ko FROM kwave_entities WHERE id = $1)
   AND ($2 <> 'tmdb'
        OR (e.entity_type = 'movie') = ((SELECT entity_type FROM kwave_entities WHERE id = $1) = 'movie'))
 LIMIT 1`, id, provider, externalID).Scan(&otherKo)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false // 정상: 다른 보유자 없음 = 충돌 아님
		}
		// 연결오류/타임아웃/취소 등 — fail-closed. 가드 무력화로 교차오염이 들어가는
		// 것보다, 이번 enrich 를 보류(차단)하는 편이 안전하다(다음 사이클 재시도).
		log.Printf("kdb.enrich: xrefClaimedByOther 조회 실패 — 보수적 차단(provider=%s id=%s): %v", provider, externalID, err)
		return "(unknown)", true
	}
	return otherKo, true
}

// tmdbMislink — 이 TMDb id 가 다른 이름의 active entity 에 이미 붙어있으면 true
// (mislink). 영화/드라마 제목 검색 false-positive 로 두 작품이 한 TMDb id 에
// 묶여 같은 제목이 복사되는 것을 막는다(예: 그림자 아이/두 번째 아이 → tmdb:1367933).
func (o *Orchestrator) tmdbMislink(ctx context.Context, snap *snapshot, tmdbID int) bool {
	if other, clash := o.xrefClaimedByOther(ctx, snap.ID, "tmdb", fmt.Sprintf("%d", tmdbID)); clash {
		log.Printf("kdb.enrich: tmdb id %d mislink 차단 — %q (이미 %q 보유)", tmdbID, snap.Ko, other)
		return true
	}
	return false
}

// qidConfirmed — 이 entity 가 이미 이 Wikidata QID 를 external_ref 로 보유하면 true
// (= 과거에 이 인물=이 QID 로 확정됨). homonym 가드를 건너뛰는 데 쓴다.
func (o *Orchestrator) qidConfirmed(ctx context.Context, id uuid.UUID, qid string) bool {
	if strings.TrimSpace(qid) == "" {
		return false
	}
	var exists bool
	err := o.Pool.QueryRow(ctx, `
SELECT EXISTS(SELECT 1 FROM kwave_entity_external_refs
               WHERE entity_id=$1 AND provider='wikidata' AND external_id=$2)`, id, qid).Scan(&exists)
	return err == nil && exists
}

// storedWikidataQID — 이 entity 가 보유한 Wikidata QID(external_ref). 없으면 "".
// QID-pin(CR-4): 이름검색 대신 이 QID 를 직접 Fetch 해 동명이인 라벨 복사를 차단한다.
func (o *Orchestrator) storedWikidataQID(ctx context.Context, id uuid.UUID) string {
	var qid string
	_ = o.Pool.QueryRow(ctx,
		`SELECT external_id FROM kwave_entity_external_refs
		  WHERE entity_id=$1 AND provider='wikidata' AND external_id <> '' LIMIT 1`, id).Scan(&qid)
	return strings.TrimSpace(qid)
}

var errNoMatch = errors.New("no match")

// --- Layer 2: MusicBrainz ------------------------------------------------

func (o *Orchestrator) runMusicBrainz(ctx context.Context, snap *snapshot) (map[string]Fill, error) {
	// FindAliases 는 반환 artist 의 name/alias 가 snap.Ko 와 정규화 일치할 때만
	// alias 를 돌려준다 (동명이인 오매칭 → canonical 오염 가드).
	aliases, err := o.MusicBrainz.FindAliases(ctx, snap.Ko)
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

// applyAliases — 외부 source 의 locale→alias 후보들을 aliases_<locale> 에 append 한다.
// canonical 은 건드리지 않고 '변형표기'만 누적한다. Wikidata 가 fetch 만 하고 버리던
// aliases 를 영속화하는 데 사용(현지 변형표기 확보 — 운영자 정공법 §보강). char-set
// 가드·canonical중복제외·suppression·배열중복제외 적용. 반환=추가된 alias 수.
func (o *Orchestrator) applyAliases(ctx context.Context, snap *snapshot, m map[string][]string) int {
	added := 0
	for loc, vals := range m {
		_, aliasCol, _ := localeColumns(loc)
		if aliasCol == "" {
			continue
		}
		for _, v := range vals {
			v = strings.TrimSpace(v)
			if v == "" || !kdb.IsValidSpellingForLocale(loc, v) {
				continue
			}
			if normForSuppress(v) == normForSuppress(snap.Values[loc]) {
				continue // canonical 과 동일 표기 — alias 불필요
			}
			if snap.isSuppressed(loc, v) {
				continue // dataqa 가 오염으로 비운 값 — 재주입 금지
			}
			tag, err := o.Pool.Exec(ctx,
				`UPDATE kwave_entities
				    SET `+aliasCol+` = array_append(COALESCE(`+aliasCol+`,'{}'), $2), updated_at = now()
				  WHERE id = $1 AND $2 <> ALL(COALESCE(`+aliasCol+`,'{}'))`, snap.ID, v)
			if err == nil && tag.RowsAffected() > 0 {
				added++
			}
		}
	}
	return added
}

func (o *Orchestrator) runWikidata(ctx context.Context, snap *snapshot) (map[string]Fill, *wdInfo, error) {
	// ★QID-pin(CR-4, 2026-06-28): 이 entity 가 이미 Wikidata QID 를 확정 보유하면 이름검색
	// (SearchAndFetch) 대신 그 QID 를 직접 Fetch 한다. 이름검색은 매 cycle 동명이인 라벨을
	// 복사할 위험(박민영 es='Sam' 478회 핑퐁) — stored QID 직결로 그 인물의 라벨만 패치.
	// Fetch 실패 시에만 이름검색 폴백.
	var ent *wikidata.Entity
	var cand *wikidata.Candidate
	var err error
	if pinned := o.storedWikidataQID(ctx, snap.ID); pinned != "" {
		ent, err = o.Wikidata.Fetch(ctx, pinned)
		if err != nil || ent == nil {
			ent, cand, err = o.Wikidata.SearchAndFetch(ctx, snap.Ko)
		}
	} else {
		ent, cand, err = o.Wikidata.SearchAndFetch(ctx, snap.Ko)
	}
	if err != nil {
		return nil, nil, err
	}
	if ent == nil {
		return nil, nil, errNoMatch
	}
	// QID 전역 유일성 가드(2026-06-20): 이 QID 가 이미 *다른 이름의* active entity
	// 에 붙어있으면 이름검색 false-positive 로 인한 mislink 다(저장 신호가 비어
	// homonym 가드가 못 걸러도 이 가드는 작동). 라벨/QID 미적용 — 교차인물 오데이터
	// 방지(예: 김하늘→강하늘 QID, 유정→최유정 QID).
	// 단, 이 entity 가 이미 그 QID 를 확정 보유(qidConfirmed)면 *정당한 주인*이므로
	// 차단하지 않는다 — 충돌은 제3의 잘못 보유 entity 쪽이며, 그쪽이 enrich 될 때
	// (확정 미보유라) 가드에 걸린다. 이 가드로 정상 entity 의 보강이 막히면 안 됨.
	if !o.qidConfirmed(ctx, snap.ID, ent.QID) {
		if other, clash := o.xrefClaimedByOther(ctx, snap.ID, "wikidata", ent.QID); clash {
			log.Printf("kdb.enrich: wikidata QID %s mislink 차단 — %q (이미 %q 보유)", ent.QID, snap.Ko, other)
			return nil, nil, errNoMatch
		}
	}
	// 동명이인 가드(2026-06-03): SearchAndFetch 는 이름 일치를 보장하지만, 같은
	// 이름의 다른 인물(homonym)일 수 있다. 우리가 이미 보유한 신호(agency/
	// birth_year/작품)와 Wikidata claims 가 충돌하면 그 entity 의 외래 locale 라벨을
	// 적용하지 않는다(잘못된 인물 표기로 오염 방지). 저장 신호가 비어있으면 비교
	// 불가하므로 claims 조회조차 건너뛴다(불필요한 호출 회피).
	//
	// 단, 이 QID 가 *이미 이 entity 의 확정 external_ref* 이면(과거에 이 인물=이 QID 로
	// 확인됨) 신호 '충돌'은 동명이인이 아니라 우리 저장 신호가 stale 하다는 뜻이므로
	// 가드를 건너뛴다 — 정상 인물의 라벨이 오래된 birth_year 하나로 통째 누락되던
	// 과잉 차단(R3) 방지. 처음 보는 QID + 충돌일 때만 오염 가드로 skip.
	if snap.EntityType == "person" && !o.qidConfirmed(ctx, snap.ID, ent.QID) {
		if stored, ok := o.loadPersonSignals(ctx, snap.ID); ok {
			if wc, _ := o.Wikidata.LookupClaims(ctx, ent.QID); wc != nil {
				incoming := homonym.PersonSignals{
					Agency: wc.Agency, BirthYear: wc.BirthYear, NotableWorks: wc.NotableWorks,
				}
				if homonym.Conflict(stored, incoming) {
					return nil, nil, errNoMatch // 동명이인 — 라벨 미적용
				}
			}
		}
	}
	// ★ko-라벨 앵커 가드(2026-06-29, A2 권고): QID 의 자체 ko 라벨이 우리 canonical_ko 와
	// 양성 불일치하면 이 QID 는 mislink(다른 엔티티)다 — 어떤 locale 라벨·alias·langlink 도
	// 임포트하지 않는다(具俊曄 vs 具俊瞱, 李成延 동명이인 등 차단). QID-pin/xref/homonym 가드의
	// 방어심화. 면제: (1) qidConfirmed(이 엔티티의 확정 ref=정당한 주인), (2) QID 에 ko 라벨
	// 부재(다수 niche/외국 엔티티 — 판단불가이므로 기존 가드에 위임, 과잉차단 방지).
	if !o.qidConfirmed(ctx, snap.ID, ent.QID) {
		if koLab := ent.Labels["ko"]; koLab != "" &&
			wikidata.NormalizeName(koLab) != wikidata.NormalizeName(snap.Ko) {
			log.Printf("kdb.enrich: wikidata QID %s ko-label 불일치 차단 — 우리=%q QID-ko=%q", ent.QID, snap.Ko, koLab)
			return nil, nil, errNoMatch
		}
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
	// Wikidata aliases 영속화(2026-06-21): 그동안 fetch 만 하고 버리던 ent.Aliases 를
	// aliases_<locale> 에 누적한다(현지 변형표기). canonical 은 위 라벨이 채우고,
	// 여기선 변형만 append. snap 은 라벨 적용 후라 canonical중복제외가 정확.
	if len(ent.Aliases) > 0 {
		if snap2, _ := loadSnapshot(ctx, o.Pool, snap.ID); snap2 != nil {
			snap = snap2
		}
		o.applyAliases(ctx, snap, ent.Aliases)
	}
	// Wikipedia 각 언어판 문서 제목(langlink) 으로 빈 locale 보강 (2026-06-01).
	// ★기존값 보존(운영자 방침): applyEmptyOnly 로 "빈칸만" 채운다 — 이미 채워진
	// 값(LLM 합성 포함)은 덮어쓰지 않는다. 신규 발굴분·빈칸만 위키 실제 제목으로
	// 채워, 앞으로 들어오는 것부터 현지 통용 표기를 확보한다(예: ja パク・ボゴム).
	if titles := ent.LanglinkTitles(); len(titles) > 0 {
		if snap2, _ := loadSnapshot(ctx, o.Pool, snap.ID); snap2 != nil {
			snap = snap2
		}
		if ll, _ := o.applyEmptyOnly(ctx, snap, titles, kdb.SourceWikipediaLanglinks); len(ll) > 0 {
			for loc, v := range ll {
				applied[loc] = v
			}
		}
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
		if snap.isSuppressed(sp.Locale, sp.Value) {
			continue // dataqa 가 오염으로 비운 값 — 재주입 금지(수렴 가드)
		}
		// L4 는 최저신뢰(llm 합성) — dataqa 가 *한 번이라도* 오염으로 비운 locale 은
		// 다른 llm 추측으로 재합성하지 않는다(교차인물 변종 재유입 차단, 예: 김하늘 zh=
		// 姜河呢). 정값은 권위소스(L2/L3)나 운영자만. 빈칸으로 두는 게 오답보다 안전.
		if len(snap.Suppressed[sp.Locale]) > 0 {
			continue
		}
		// ★charset 가드(CR-4, 2026-06-28): zh/zh-hant 칸에 한자 없는 ASCII(영어 leak)·
		// 칸별 문자셋 불일치 합성을 생산 지점에서 차단. 빈칸이 오답(영어 노출)보다 안전.
		// (기존엔 가드 부재로 zh ASCII ~519·zh_hant ~585 codex 유입.)
		if !kdb.IsValidSpellingForLocale(sp.Locale, sp.Value) {
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
// runVideoAPIs — movie/drama/show 다국어 제목을 TMDb(+영화는 KOFIC)로 채운다.
// 키는 apikeys.Resolve(DB 우선, .env fallback). 키 없으면 해당 소스 skip.
func (o *Orchestrator) runVideoAPIs(ctx context.Context, snap *snapshot) map[string]Fill {
	out := map[string]Fill{}
	if token, _ := apikeys.Resolve(ctx, o.Pool, "KDB_TMDB_API_TOKEN"); token != "" {
		if m, tmdbID, err := o.TMDb.Enrich(ctx, token, snap.Ko, snap.EntityType); err == nil && tmdbID > 0 &&
			!o.tmdbMislink(ctx, snap, tmdbID) {
			// 매칭된 TMDb id 캐시 — 재검색 방지 + 향후 풍부한 활용(credits 등).
			media := "movie"
			if snap.EntityType == "drama" || snap.EntityType == "show" {
				media = "tv"
			}
			_, _ = o.Pool.Exec(ctx, `
INSERT INTO kwave_entity_external_refs (entity_id, provider, external_id, url, confidence, fetched_at)
VALUES ($1, 'tmdb', $2, $3, 0.800, now())
ON CONFLICT (entity_id, provider) DO UPDATE SET external_id=EXCLUDED.external_id, url=EXCLUDED.url, fetched_at=now()`,
				snap.ID, fmt.Sprintf("%d", tmdbID),
				fmt.Sprintf("https://www.themoviedb.org/%s/%d", media, tmdbID))
			if applied, _ := o.applyFromMap(ctx, snap, m, kdb.SourceTMDb); len(applied) > 0 {
				for k, v := range applied {
					out[k] = v
				}
				if snap2, _ := loadSnapshot(ctx, o.Pool, snap.ID); snap2 != nil {
					snap = snap2
				}
			}
		}
	}
	if snap.EntityType == "movie" {
		if key, _ := apikeys.Resolve(ctx, o.Pool, "KDB_KOFIC_API_KEY"); key != "" {
			if m, err := o.KOFIC.Enrich(ctx, key, snap.Ko); err == nil && len(m) > 0 {
				if applied, _ := o.applyFromMap(ctx, snap, m, kdb.SourceKOFIC); len(applied) > 0 {
					for k, v := range applied {
						out[k] = v
					}
				}
			}
		}
	}
	return out
}

// RefreshVideoTitles — 작품(movie/drama/show) batch 건의 TMDb 제목을 재적용한다.
//
// 기존 Enrich 는 "빈 locale 이 있을 때만" runVideoAPIs 를 호출하므로, 9칸이 (초기
// wikidata/codex 가 채운 잘못된 영어복사값으로라도) 다 차 있으면 TMDb 가 영영
// 안 돈다 → 오징어게임 pt_br=Squid Game 이 Round 6 로 교정되지 못한다. 이 패스는
// 빈칸 유무와 무관하게 runVideoAPIs 를 강제 호출해, applyFromMap 의 우선순위 룰
// (TMDb=4 < wikidata=5 < codex=7)로 하위소스 값을 TMDb 로 교체한다. operator-locked·
// 매체합의(prio 1·2)는 ShouldReplace 가 보존한다. updated_at 오래된 순으로 batch 건만.
// koFilter 가 비어있지 않으면 그 canonical_ko 작품 1건만 재적용한다(검증용).
func (o *Orchestrator) RefreshVideoTitles(ctx context.Context, batch int, koFilter string) (works, filled, reattributed int, err error) {
	q := `
SELECT id FROM kwave_entities
 WHERE status='active' AND operator_locked=false
   AND entity_type IN ('movie','drama','show')
 ORDER BY updated_at ASC
 LIMIT $1`
	args := []any{batch}
	if strings.TrimSpace(koFilter) != "" {
		q = `SELECT id FROM kwave_entities WHERE canonical_ko=$1 AND entity_type IN ('movie','drama','show')`
		args = []any{koFilter}
	}
	rows, err := o.Pool.Query(ctx, q, args...)
	if err != nil {
		return 0, 0, 0, err
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, 0, err
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		snap, e := loadSnapshot(ctx, o.Pool, id)
		if e != nil || snap == nil {
			continue
		}
		applied := o.runVideoAPIs(ctx, snap)
		works++
		filled += len(applied)
		if len(applied) > 0 {
			parts := make([]string, 0, len(applied))
			for loc, f := range applied {
				parts = append(parts, loc+"="+f.Value)
			}
			log.Printf("kdb.tmdb-refresh: %q ← %s", snap.Ko, strings.Join(parts, " "))
		}
		reattributed += o.reattributeTMDb(ctx, id)
	}
	return works, filled, reattributed, nil
}

// reattributeTMDb — TMDb 가 공식제목으로 확인한 codex-fallback locale 값(영문복사 포함)을
// source 만 codex-fallback→tmdb 로 승급한다(값 무변·고신뢰 라벨링). Enrich/runVideoAPIs 의
// 필링은 "빈칸>영어복사" 정책으로 영문복사를 *반환하지 않아* 정답인 codex-fallback 영문제목
// 이 영영 저신뢰로 남던 것을 정직화(오너 승인 2026-06-24). 반환=승급된 locale 수.
func (o *Orchestrator) reattributeTMDb(ctx context.Context, id uuid.UUID) int {
	token, _ := apikeys.Resolve(ctx, o.Pool, "KDB_TMDB_API_TOKEN")
	if token == "" {
		return 0
	}
	snap, _ := loadSnapshot(ctx, o.Pool, id) // 필링 반영된 최신 상태
	if snap == nil {
		return 0
	}
	// codex-fallback locale 이 하나도 없으면 TMDb 재조회 불필요(깨끗한 작품 호출 절약).
	hasCF := false
	for _, loc := range []string{"en", "ja", "vi", "id", "es", "pt_br", "zh", "zh_hant"} {
		if snap.Sources[loc] == string(kdb.SourceCodexFallback) {
			hasCF = true
			break
		}
	}
	if !hasCF {
		return 0
	}
	titles, tmdbID, err := o.TMDb.AllTitles(ctx, token, snap.Ko, snap.EntityType)
	if err != nil || tmdbID == 0 || o.tmdbMislink(ctx, snap, tmdbID) {
		return 0
	}
	n := 0
	for loc, ts := range titles {
		canonCol, _, srcCol := localeColumns(loc)
		if canonCol == "" || snap.Sources[loc] != string(kdb.SourceCodexFallback) {
			continue
		}
		if !tmdb.TitleMatches(snap.Values[loc], ts) {
			continue
		}
		// 값은 그대로, source 만 승급. 가드(srcCol='codex-fallback')로 멱등·안전.
		ct, e := o.Pool.Exec(ctx,
			`UPDATE kwave_entities SET `+srcCol+`='tmdb', updated_at=now() WHERE id=$1 AND `+srcCol+`='codex-fallback'`, id)
		if e == nil && ct.RowsAffected() > 0 {
			n++
		}
	}
	if n > 0 {
		log.Printf("kdb.tmdb-reattribute: %q → tmdb 승급 %d locale", snap.Ko, n)
	}
	return n
}

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
		// locale 문자셋 가드: 외부 소스가 영문 칸에 한글을 넣는 등(예: MusicBrainz
		// primary name=한국어) 오염을 차단. 부적합 값은 적용하지 않는다.
		if !kdb.IsValidSpellingForLocale(loc, newVal) {
			continue
		}
		// 수렴 가드: dataqa 가 동명이인 오염으로 비웠던 바로 그 값이면 재주입 금지
		// (안 막으면 dataqa 가 또 비우는 무한 핑퐁 — 나비 es='Ella Gross' 433회).
		if snap.isSuppressed(loc, newVal) {
			continue
		}
		// person 의 Latin locale 에 en 값이 그대로 복사되는 en-copy 방지.
		// person은 locale 별 음역이 달라야 함(BTS 같은 그룹은 en=locale 가 정상이라 person만 적용).
		if snap.EntityType == "person" && snap.Values["en"] != "" {
			latinLocs := map[string]bool{"vi": true, "es": true, "id": true, "pt_br": true}
			if latinLocs[loc] && normForSuppress(newVal) == normForSuppress(snap.Values["en"]) {
				continue // en-copy — 빈칸으로 두고 media consensus 로 채움
			}
		}
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

// applyEmptyOnly — 빈 locale 에만 값 적용하고, 이미 채워진 값은 source 무관 보존한다
// (운영자 방침 2026-06-01: "기존 데이터는 유지, 신규·빈칸부터만 변경"). langlink
// 보강 전용 — 기존 LLM(codex) 값을 덮어쓰지 않는다. WHERE 의 빈칸 조건으로 동시성 보호.
func (o *Orchestrator) applyEmptyOnly(ctx context.Context, snap *snapshot, m map[string][]string, src kdb.Source) (map[string]Fill, error) {
	out := map[string]Fill{}
	for loc, vals := range m {
		if len(vals) == 0 || vals[0] == "" {
			continue
		}
		if snap.Values[loc] != "" {
			continue // 기존값 유지
		}
		if !kdb.IsValidSpellingForLocale(loc, vals[0]) {
			continue // locale 문자셋 부적합(영문 칸 한글 등) 차단
		}
		if snap.isSuppressed(loc, vals[0]) {
			continue // dataqa 가 오염으로 비운 값 — 재주입 금지(수렴 가드)
		}
		// person 의 Latin locale 에 en 값이 그대로 복사되는 en-copy 방지.
		if snap.EntityType == "person" && snap.Values["en"] != "" {
			latinLocs := map[string]bool{"vi": true, "es": true, "id": true, "pt_br": true}
			if latinLocs[loc] && normForSuppress(vals[0]) == normForSuppress(snap.Values["en"]) {
				continue
			}
		}
		canonCol, _, srcCol := localeColumns(loc)
		if canonCol == "" {
			continue
		}
		if _, err := o.Pool.Exec(ctx,
			`UPDATE kwave_entities SET `+canonCol+` = $2, `+srcCol+` = $3, updated_at = now()
			   WHERE id = $1 AND COALESCE(`+canonCol+`,'') = ''`,
			snap.ID, vals[0], string(src)); err == nil {
			out[loc] = Fill{Value: vals[0], Source: string(src)}
		}
	}
	return out, nil
}
