package codexcli

import (
	"fmt"
	"strings"
)

// ExtractHint mirrors server.mjs buildPrompt hint entries.
type ExtractHint struct {
	CanonicalKo string
	Matched     string
	EntityID    string
}

// Wikidata mirrors the {qid,label,description} object passed to the JS prompt
// builders. A nil pointer means "(none)" / "(not queried)".
type Wikidata struct {
	QID         string
	Label       string
	Description string
}

// extract 프롬프트 입력 캡 — RSS description 은 본문 전체가 실려 올 수 있는데
// 표기 추출엔 앞부분이면 충분하다(일 수백 회 호출되는 최대 볼륨 경로의 토큰 절감).
const (
	extractTitleCap = 300
	extractDescCap  = 600
)

func capRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// BuildExtractPrompt ports server.mjs buildPrompt verbatim.
func BuildExtractPrompt(locale, title, description string, hints []ExtractHint) string {
	title = capRunes(title, extractTitleCap)
	description = capRunes(description, extractDescCap)
	hintParts := make([]string, 0, len(hints))
	for _, h := range hints {
		hintParts = append(hintParts, fmt.Sprintf(
			`  - canonical_ko="%s" matched_text="%s" entity_id=%s`,
			h.CanonicalKo, h.Matched, h.EntityID))
	}
	hintLines := strings.Join(hintParts, "\n")
	if hintLines == "" {
		hintLines = "  (none — discover from scratch)"
	}
	lines := []string{
		"You extract foreign-language spellings of Korean K-content entities (people, groups, works) from a single news headline + description.",
		"Output JSON only — an output schema is enforced by the runtime; emit nothing outside the JSON object.",
		"",
		fmt.Sprintf("Target locale (output language code): %s", locale),
		fmt.Sprintf("Title: %s", title),
		fmt.Sprintf("Description: %s", description),
		"Pre-matched entity hints (cheap-gate found these Korean entities in the source text):",
		hintLines,
		"",
		"Rules:",
		fmt.Sprintf(`- For each Korean entity that appears in the source, emit one "spellings" entry per %s-language form actually used in the source text.`, locale),
		`- ko_hint = canonical Korean form. Use the hint's canonical_ko when applicable; otherwise your best canonical guess in Korean (Hangul).`,
		fmt.Sprintf(`- locale = "%s" (echo).`, locale),
		"- spelling = the foreign-language form exactly as it appears in the source (preserve casing/diacritics).",
		"- confidence: 0.95+ when the source text explicitly contains the foreign spelling and the Korean mapping is unambiguous; 0.7~0.9 when likely but inferred; <0.7 = omit.",
		`- Do not put Korean text into "spelling". Do not invent entities that are not in the source.`,
		`- If nothing qualifies, return {"spellings": []}.`,
	}
	return strings.Join(lines, "\n")
}

// BuildClassifyPrompt ports server.mjs buildClassifyPrompt verbatim.
//
// requestedType — 소비자가 research 큐에 명시한 구체 타입 힌트(person/group/…).
// 비거나 unknown/term 이면 무시. 일반어와 철자 충돌하는 고유명사(예 "펜트하우스"=
// 드라마)를 type 맥락으로 분류하도록 강한 참고로 준다(비엔티티/K범위 필터가 오염방어).
func BuildClassifyPrompt(ko string, spellings map[string]string, sourceDomains []string, notes string, wikidata *Wikidata, searchHits []string, requestedType string) string {
	spParts := make([]string, 0, len(spellings))
	for _, k := range orderedKeys(spellings) {
		v := spellings[k]
		if strings.TrimSpace(v) == "" {
			continue
		}
		spParts = append(spParts, fmt.Sprintf(`  - %s: "%s"`, k, v))
	}
	spLines := strings.Join(spParts, "\n")
	if spLines == "" {
		spLines = "  (none)"
	}

	sd := sourceDomains
	sdJoined := strings.Join(sd, ", ")
	if sdJoined == "" {
		sdJoined = "(none)"
	}

	notesLine := notes
	if notesLine == "" {
		notesLine = "(none)"
	}

	var wdLine string
	if wikidata != nil {
		qid := wikidata.QID
		if qid == "" {
			qid = "?"
		}
		wdLine = fmt.Sprintf(`Wikidata candidate: Q=%s label="%s" description="%s"`, qid, wikidata.Label, wikidata.Description)
	} else {
		wdLine = "Wikidata: (not queried)"
	}

	hits := searchHits
	if len(hits) > 8 {
		hits = hits[:8]
	}
	var hitsLine string
	if len(hits) > 0 {
		hitParts := make([]string, 0, len(hits))
		for _, h := range hits {
			hitParts = append(hitParts, "  - "+h)
		}
		hitsLine = "External search hits (titles from local news): \n" + strings.Join(hitParts, "\n")
	} else {
		hitsLine = "External search hits: (none)"
	}

	// 소비자 typed 힌트 — 비/unknown/term 이면 미표시.
	var reqLine string
	rt := strings.TrimSpace(requestedType)
	if rt != "" && rt != "unknown" && rt != "term" {
		reqLine = fmt.Sprintf(`Requested entity_type hint (소비자가 이 type 으로 질의함, 강한 참고): "%s". 검색 hit 이 이 타입과 모순되지 않으면 이 타입을 우선 고려한다. 단, ko 가 명백한 일반어/비-K 면 힌트를 무시하고 term 으로(틀린 힌트가 오염을 통과시키지 않게).`, rt)
	} else {
		reqLine = "Requested entity_type hint: (none)"
	}

	lines := []string{
		"You classify a Korean K-content entity into one of 13 entity_type enum values.",
		"Output JSON only — an output schema is enforced. Emit nothing outside the JSON object.",
		"",
		fmt.Sprintf(`Korean canonical name: "%s"`, ko),
		"Multilingual spellings observed in Korean / global media:",
		spLines,
		fmt.Sprintf("Source media domains (%d): %s", len(sd), sdJoined),
		fmt.Sprintf("Existing notes: %s", notesLine),
		wdLine,
		hitsLine,
		reqLine,
		"",
		"INPUT INTEGRITY CHECK (do this FIRST):",
		`- If ko contains lone Hangul jamo (e.g., "ㄱ" "ㅁ" "ㅇ" "ㅏ") embedded between syllables — this is a typo from broken RSS/OCR.`,
		`- In that case set entity_type="unknown", confidence ≤ 0.30, needs_search=true, and reason="자소 분리 오타: <ko 정정안>".`,
		"",
		"NON-ENTITY FILTER (most important — do this SECOND):",
		"- Reject Korean general words / verbs / adverbs / phrases that are NOT proper nouns.",
		`- Examples that MUST be classified as entity_type="term" with confidence ≤ 0.30:`,
		`    "건강하게" (adverb "in good health"), "세계일주" (common noun "world tour"),`,
		`    "춤" ("dance"), "파일럿" ("pilot" as common loanword), "훌리건" ("hooligan"),`,
		`    "오십견" (medical term "frozen shoulder"), "마녀" ("witch" when not a known work title),`,
		`    "신사" ("gentleman" when context is general), "상도" ("business ethics" when ambiguous).`,
		`- If ko looks like a complete sentence, slogan, or descriptive phrase (조사 included, e.g. "~하게", "~이다", "~었다") → entity_type="term", confidence ≤ 0.30.`,
		`- A proper noun typically has NO Korean particle ("이/가/을/를/에/와/하게/하다") attached.`,
		`- If unsure but ko is a single common Korean word with NO supporting media evidence → entity_type="term", confidence ≤ 0.40, needs_search=true.`,
		"",
		"SEARCH-HIT PRIORITY (일반어 철자충돌 고유명사 구제 — NON-ENTITY 보다 우선):",
		"- 검색 hit 에 작품/활동 맥락(방영·출연·시청률·멤버·데뷔·발매·컴백·콘서트·예능·드라마·영화·앨범)이",
		`  보이면, ko 가 일반어와 철자가 같아도 그 고유명사 type 으로 분류한다(term 으로 떨구지 말 것).`,
		`  예: "펜트하우스"(drama), "마녀"(movie), "마크"(NCT person), "라이즈"(group).`,
		"- 일반어/고유명사 판단이 애매하고 hit 이 결정적이지 않으면 term 으로 단정하지 말고,",
		"  needs_search=true + best-guess type(저신뢰) 로 둔다 — 자율 루프가 재검색·운영자 검토하도록.",
		"",
		"",
		"K-SCOPE FILTER (do this THIRD — KDB 는 K-엔터테인먼트 전용 DB):",
		"- KDB 는 *한국 엔터테인먼트* 고유명사만 다룬다. 비-K 인물/일반 기업/외국 정치인·",
		"  기업인·헐리우드 배우·외국 스포츠선수 등 K-엔터 연관 없는 대상은 범위 밖이다.",
		`- 범위 밖이면 entity_type="term", confidence ≤ 0.30, needs_search=false,`,
		`  reason="비-K(범위밖): <간단설명>" 로 분류한다(다운스트림이 reject).`,
		`- 범위 밖 예: "젠슨 황"(NVIDIA CEO), "팀 쿡", "일론 머스크"(기업인); "안젤리나 졸리"·`,
		`  "브래들리 쿠퍼"·"숀 펜"(헐리우드 배우); "Bad Bunny"(외국 가수); 외국 정치인/운동선수.`,
		"- 예외(범위 안 = person 정상): ①K-pop 그룹의 외국국적 멤버(예: NewJeans 하니/다니엘,",
		"  (G)I-DLE 우기·민니·슈화, aespa 닝닝, 트와이스 모모/사나/미나), ②한국에서 K-엔터로",
		"  활동하는 외국인(가수·배우·방송인). 이들은 K-엔터 본업이므로 person 으로 분류한다.",
		"- 한국계라도 본업이 K-엔터가 아니면(예: 외국 IT기업인) 범위 밖. K-드라마/영화에 1회성",
		"  출연한 외국 배우도 본업이 K-엔터 아니면 범위 밖.",
		"",
		"- Otherwise proceed with classification below.",
		"",
		"Classification rules:",
		`- person: 한국 인물 (배우/가수/idol/감독/MC/스포츠 등). 2-4 자 한글 + 외래어 음역(예: ja "アン・ウンジン", en "Surname-Given") = 강한 단서.`,
		"- group: K-pop 그룹 / 보이밴드 / 걸그룹 / 듀오 (예: BTS, BLACKPINK, NewJeans). 멤버 인물 X.",
		"- drama: 한국 드라마 시리즈 (TV/Netflix/OTT 드라마 시리즈).",
		"- movie: 한국 영화 (극장 공개작).",
		"- show: 한국 예능 / 버라이어티 / 토크쇼 (드라마 X).",
		"- song_album: 곡명 / 앨범명 (그룹/가수가 아닌 작품).",
		"- agency: 연예 기획사 (HYBE/JYP/SM/YG 등).",
		"- channel_outlet: 방송사 / 뉴스 매체 / 채널 (KBS/SBS/Mnet/티빙).",
		"- brand_place: 브랜드 / 장소 / 회사 (K-content 관련).",
		"- event_tour: 콘서트 / 투어 / 시상식 / 페스티벌.",
		"- character: 작품 속 캐릭터 (실존 인물 X).",
		"- term: 일반 명사 / 개념 / 한국 문화 용어 (예: 한복, 김치).",
		"- unknown: 정보 부족 시에만 (가능하면 다른 type 선택).",
		"",
		"Confidence guide:",
		"- 0.95+ : 외래어 음역 / persons-sync 명시 / Wikidata K-content label 일치 등 1 의미 단서.",
		"- 0.8~0.95 : 강한 패턴 (인명형/제목구조) + 매체 수 ≥ 2.",
		"- 0.6~0.8 : 일반 추정 (단일 단서, K-content RSS 매체).",
		"- <0.6 : 동음이의어 가능 / 정보 부족. needs_search=true 권장.",
		"",
		"If entity_type=person, also fill primary_role (배우 → actor / 가수 → singer / idol → idol / 코미디언 → comedian / 감독 → director / etc).",
		"If entity_type=person AND you are confident, also fill these (else leave empty/null — NEVER guess):",
		`  - gender: "M" or "F" (null if unknown).`,
		`  - groups: array of group names the person belongs to / is active in (e.g. an idol's group). [] if none/unknown.`,
		`  - secondary_roles: array of additional person_role enum values beyond primary_role (e.g. an actor who is also a singer). [] if none.`,
		"For non-person entity_type leave gender=null, groups=[], secondary_roles=[].",
		"If you genuinely cannot decide above 0.6 confidence, set needs_search=true and pick best-guess type with low conf.",
	}
	return strings.Join(lines, "\n")
}

// BuildGatekeeperPrompt renders the CandidateGatekeeper gray-band prompt
// (docs/KDB_HERMES_AGENTS_DESIGN.md §B.2). The deterministic pre-gate
// (internal/kdb/agents/gatekeeper) already hard-rejects obvious junk
// (spaced/long/jamo/mixed-script/josa-tail) and hard-keeps obviously clean
// short Korean names; only the AMBIGUOUS remainder reaches this prompt.
//
// The contract is a strict gatekeeper: KEEP only a real proper noun; REJECT
// sentences/fragments/common nouns; if a proper noun is buried in extra text,
// return the cleaned form in canonical_suggestion; uncertain → human review.
func BuildGatekeeperPrompt(term string, heuristicFlags, sourceDomains []string) string {
	flags := strings.Join(heuristicFlags, ", ")
	if flags == "" {
		flags = "(none)"
	}
	sd := strings.Join(sourceDomains, ", ")
	if sd == "" {
		sd = "(none)"
	}
	lines := []string{
		"You are a STRICT gatekeeper for a proper-noun K-content entity database.",
		"Decide whether one candidate string is a real proper noun worth keeping, or junk to reject.",
		"Output JSON only — an output schema is enforced. Emit nothing outside the JSON object.",
		"",
		fmt.Sprintf(`Candidate term: "%s"`, term),
		fmt.Sprintf("Deterministic heuristic flags: %s", flags),
		fmt.Sprintf("Source media domains: %s", sd),
		"",
		"KEEP (verdict=proper_noun, keep=true) ONLY when the term names a real, specific:",
		"  person, group, drama, movie, show, song/album, agency, channel/outlet,",
		"  brand, place, organization, event, tour, or character of K-content.",
		"",
		"REJECT (keep=false) when the term is:",
		`  - a full sentence, clause, or slogan (verdict=sentence),`,
		`  - a phrase / particle-or-verb fragment with Korean 조사/어미 (이/가/을/를/에/와/하게/하다/이다/였다/었다) (verdict=fragment),`,
		`  - a common noun / generic descriptor / number-measure string (verdict=common_noun),`,
		`    e.g. "건강하게", "세계일주", "춤", "오십견" → common_noun.`,
		"",
		"misclassified: a real proper noun is buried in extra text — set keep=true and put the",
		`  cleaned Korean proper-noun form in canonical_suggestion (e.g. "배우 이정재" → "이정재").`,
		"",
		"uncertain: you genuinely cannot decide (thin evidence) — set verdict=uncertain, keep=false,",
		"  confidence < 0.6. Do NOT guess; uncertain routes to a human, it is NOT a silent drop.",
		"",
		"Be conservative about REJECT: a plausible 2-4 syllable Korean personal name, a known",
		"title, or a romanized brand should be KEPT even with little evidence. When the term is a",
		"clean single proper noun, prefer proper_noun over uncertain.",
	}
	return strings.Join(lines, "\n")
}

// BuildPersonReconcilePrompt renders the PersonExtractor orphan-reconciliation
// prompt (docs/KDB_HERMES_AGENTS_DESIGN.md §B.3): decide whether a legacy
// kwave_persons row (orphan) is the SAME real person as a candidate entity
// sharing its name, so the entity can be promoted to entity_type='person'.
// Pure judgement only — no field invention.
func BuildPersonReconcilePrompt(nameKo, legacyRole, legacyAgency string, legacyWorks []string, entityNotes string, entitySources []string) string {
	works := strings.Join(legacyWorks, ", ")
	if works == "" {
		works = "(none)"
	}
	if legacyRole == "" {
		legacyRole = "(unknown)"
	}
	if legacyAgency == "" {
		legacyAgency = "(unknown)"
	}
	if entityNotes == "" {
		entityNotes = "(none)"
	}
	sd := strings.Join(entitySources, ", ")
	if sd == "" {
		sd = "(none)"
	}
	lines := []string{
		"You reconcile a legacy person record against a database candidate entity that shares its Korean name.",
		"Decide if they refer to the SAME real K-content person, so the candidate can become entity_type='person'.",
		"Output JSON only — an output schema is enforced. Emit nothing outside the JSON object.",
		"",
		fmt.Sprintf(`Korean name (both share this): "%s"`, nameKo),
		"Legacy person record (operator-curated person DB):",
		fmt.Sprintf("  - primary_role: %s", legacyRole),
		fmt.Sprintf("  - agency: %s", legacyAgency),
		fmt.Sprintf("  - notable_works: %s", works),
		"Candidate entity (discovered from RSS):",
		fmt.Sprintf("  - notes: %s", entityNotes),
		fmt.Sprintf("  - source media domains: %s", sd),
		"",
		"Decide:",
		"  same=true  → the candidate entity is the same real person as the legacy record.",
		"  same=false → they are different (homonym) OR the legacy record looks like junk/non-person.",
		"  If you cannot tell, set same=false and is_junk=false (it will be flagged for a human, never deleted).",
		"  Set is_junk=true ONLY if the legacy name is clearly NOT a real person (a phrase/typo/common noun).",
		"Never invent agency/role/works; this decision is about identity only.",
	}
	return strings.Join(lines, "\n")
}

// BuildFillPersonPrompt renders the Enricher's L4 person-detail fill prompt
// (docs/KDB_HERMES_AGENTS_DESIGN.md §B.4). Only the requested missing_fields are
// asked for; the model fills ONLY fields it is confident about (well-known
// facts), and lists the rest under "skipped" so the Enricher can mark them
// source-exhausted and terminate the loop. Confident-only — never invent.
func BuildFillPersonPrompt(ko, primaryRole, agency string, groups, works []string, missing []string, wikidata *Wikidata) string {
	missJoined := strings.Join(missing, ", ")
	if missJoined == "" {
		missJoined = "(none)"
	}
	role := primaryRole
	if role == "" {
		role = "(unknown)"
	}
	ag := agency
	if ag == "" {
		ag = "(unknown)"
	}
	grp := strings.Join(groups, ", ")
	if grp == "" {
		grp = "(none/unknown)"
	}
	wk := strings.Join(works, ", ")
	if wk == "" {
		wk = "(none/unknown)"
	}
	var wdLine string
	if wikidata != nil {
		qid := wikidata.QID
		if qid == "" {
			qid = "?"
		}
		wdLine = fmt.Sprintf(`Wikidata: Q=%s description="%s"`, qid, wikidata.Description)
	} else {
		wdLine = "Wikidata: (none)"
	}
	lines := []string{
		"You supply MISSING structured details for a known Korean K-content person.",
		"Output JSON only — an output schema is enforced. Emit nothing outside the JSON object.",
		"",
		fmt.Sprintf(`Korean canonical name: "%s"`, ko),
		"Current (often partial) details — do NOT regenerate these, fill only the gaps:",
		fmt.Sprintf("  - primary_role: %s", role),
		fmt.Sprintf("  - agency: %s", ag),
		fmt.Sprintf("  - groups: %s", grp),
		fmt.Sprintf("  - notable_works: %s", wk),
		wdLine,
		fmt.Sprintf("Missing fields to fill (only these): %s", missJoined),
		"",
		"Rules:",
		"- Put a value in \"fields\" ONLY for a missing field you are confident about from well-known facts.",
		"- A field you cannot fill confidently MUST go in \"skipped\" — never guess, never invent an agency/birth_year/group.",
		"- gender: 'M' or 'F' only when certain.",
		"- birth_year: a 4-digit year 1900–2025 only when certain.",
		"- groups: real group(s) the person is/was a member of (e.g. an idol's group). [] if solo/none.",
		"- secondary_roles / primary_role: pick from the person_role enum; primary_role only if currently 'other'.",
		"- notable_works: well-known titles/films/albums credited to THIS person.",
		"- Every requested missing field must appear in EITHER fields OR skipped — do not omit a requested field from both.",
	}
	return strings.Join(lines, "\n")
}

// BuildDisambiguatePrompt renders the Disambiguator's cluster decision prompt
// (docs/KDB_HERMES_AGENTS_DESIGN.md §B.5). Members of a same/near-name cluster
// are judged per item: a misspelling / jamo error / nickname / alternate
// spelling of another member is the SAME person (merge into the canonical, the
// well-formed highest-evidence form); genuinely different real people get a
// disambiguator; thin evidence → uncertain (human review).
func BuildDisambiguatePrompt(name string, members []DisambigMember) string {
	parts := make([]string, 0, len(members))
	for _, m := range members {
		works := strings.Join(m.Works, ", ")
		if works == "" {
			works = "(none)"
		}
		role := m.Role
		if role == "" {
			role = "(unknown)"
		}
		agency := m.Agency
		if agency == "" {
			agency = "(unknown)"
		}
		parts = append(parts, fmt.Sprintf(
			`  - id=%s name="%s" role=%s agency=%s works=[%s] well_formed=%v alias_score=%.2f`,
			m.ID, m.Name, role, agency, works, m.WellFormed, m.AliasScore))
	}
	memberLines := strings.Join(parts, "\n")
	lines := []string{
		"You disambiguate a cluster of K-content entities that share the same or a very similar Korean name.",
		"Output JSON only — an output schema is enforced. Emit nothing outside the JSON object.",
		"",
		fmt.Sprintf(`Cluster name (normalized): "%s"`, name),
		"Members:",
		memberLines,
		"",
		"For EACH member decide one of:",
		"  merge    → this member is a misspelling (same_typo), jamo-broken form, nickname (same_nickname),",
		"             or alternate spelling (same_alias) of ANOTHER member that is the SAME real person.",
		"             Set same_as = the id of the surviving CANONICAL member (the well-formed,",
		"             highest-evidence form — never pick a malformed/typo form as the survivor).",
		"  distinct → a genuinely DIFFERENT real person who merely shares the name. Set a short",
		"             disambig label, e.g. '(배우)' '(가수)' '(YG)' '(1990)'.",
		"  uncertain→ thin/ambiguous evidence — do NOT guess; route to a human.",
		"",
		"HARD RULE (evidence gate): NEVER merge two members whose agency, primary_role, or notable_works",
		"clearly CONFLICT (different non-empty agency, different meaningful role, or disjoint works) —",
		"those are different people → distinct (or uncertain). Only merge when evidence is compatible AND",
		"one form is plainly a variant of the other.",
		"confidence: merge needs ≥0.70; below 0.60 use uncertain.",
	}
	return strings.Join(lines, "\n")
}

// DisambigMember is one cluster member passed to BuildDisambiguatePrompt.
type DisambigMember struct {
	ID         string
	Name       string
	Role       string
	Agency     string
	Works      []string
	WellFormed bool
	AliasScore float64
}

// relevantWiki — FillLocale 프롬프트에 포함할 위키 sitelink. 서비스 locale 의
// 위키만 남겨 프롬프트 토큰을 줄인다(Wikidata sitelinks 는 수십 개 언어판을
// 반환하지만 비서비스 언어판 URL 은 합성에 도움이 안 됨).
var relevantWiki = map[string]bool{
	"kowiki": true, "enwiki": true, "jawiki": true, "viwiki": true,
	"zhwiki": true, "eswiki": true, "idwiki": true, "ptwiki": true,
}

// BuildFillLocalePrompt ports server.mjs buildFillLocalePrompt verbatim.
func BuildFillLocalePrompt(ko, entityType, primaryRole string, aliasesKo []string, known map[string]string, missing []string, wikidata *Wikidata, sitelinks map[string]string) string {
	knParts := make([]string, 0, len(known))
	for _, k := range orderedKeys(known) {
		v := known[k]
		if strings.TrimSpace(v) == "" {
			continue
		}
		knParts = append(knParts, fmt.Sprintf(`  - %s: "%s"`, k, v))
	}
	knLines := strings.Join(knParts, "\n")
	if knLines == "" {
		knLines = "  (none)"
	}

	slParts := make([]string, 0, len(sitelinks))
	for _, k := range orderedKeys(sitelinks) {
		v := sitelinks[k]
		if strings.TrimSpace(v) == "" || !relevantWiki[k] {
			continue
		}
		slParts = append(slParts, fmt.Sprintf("  - %s: %s", k, v))
	}
	slLines := strings.Join(slParts, "\n")

	missJoined := strings.Join(missing, ", ")
	if missJoined == "" {
		missJoined = "(none)"
	}

	entityTypeStr := entityType
	if entityTypeStr == "" {
		entityTypeStr = "?"
	}
	entityTypeLine := fmt.Sprintf("entity_type: %s", entityTypeStr)
	if primaryRole != "" {
		entityTypeLine += fmt.Sprintf(" (primary_role=%s)", primaryRole)
	}

	var aliasLine string
	if len(aliasesKo) > 0 {
		aliasLine = "Korean aliases: " + strings.Join(aliasesKo, ", ")
	} else {
		aliasLine = "Korean aliases: (none)"
	}

	var wdLine string
	if wikidata != nil {
		qid := wikidata.QID
		if qid == "" {
			qid = "?"
		}
		wdLine = fmt.Sprintf(`Wikidata: Q=%s description="%s"`, qid, wikidata.Description)
	} else {
		wdLine = "Wikidata: (none)"
	}

	var slBlock string
	if slLines != "" {
		slBlock = "Wikipedia URLs by locale:\n" + slLines
	} else {
		slBlock = "Wikipedia URLs: (none)"
	}

	lines := []string{
		"You synthesize missing foreign-language spellings for a known Korean K-content entity.",
		"Output JSON only — an output schema is enforced.",
		"",
		fmt.Sprintf(`Korean canonical name: "%s"`, ko),
		entityTypeLine,
		aliasLine,
		"Known spellings (do NOT regenerate these — use as romanization basis):",
		knLines,
		fmt.Sprintf("Missing locales (synthesize these): %s", missJoined),
		wdLine,
		slBlock,
		"",
		"Rules:",
		"- Each output spelling must be how local media in that locale would actually print the name (not literal translation).",
		"- Script per locale (HARD requirement — violating output is discarded):",
		"  * en/vi/es/id/pt-br → Latin script (revised Romanization for Korean person names).",
		`  * ja → katakana/kanji (katakana for Korean person names, "・" between surname-given). Latin allowed only when Japanese media prints the Latin form as-is (e.g. group names like BTS).`,
		"  * zh → Simplified Chinese Han characters. zh-hant → Traditional Chinese Han characters.",
		`- Korean person names in zh/zh-hant: use the established Chinese rendering (hanja-based, e.g. 박보검 → zh 朴宝剑 / zh-hant 朴寶劍) or the phonetic form Chinese media actually uses. NEVER output a Latin romanization for zh/zh-hant — if you only know the Latin form, put the locale in "skipped".`,
		`- For drama/movie/show/song_album (WORKS): each locale must be the OFFICIAL TITLE that media in THAT locale actually use — this is a TRANSLATION/localization task, not romanization. e.g. "오징어 게임" → en "Squid Game", es "El juego del calamar", pt-br "Round 6", vi "Trò chơi con mực", ja "イカゲーム".`,
		`  * NEVER copy the English title (or any other locale's value) into a non-English locale. A Spanish/Vietnamese/Portuguese/Japanese field equal to the English title is a TRANSLATION FAILURE.`,
		`  * If you do NOT know the official localized title for a locale, put that locale in "skipped" — do NOT guess, do NOT copy English, do NOT romanize. (blank is better than a wrong/English value.)`,
		`  * EXCEPTION: if the work's official title is itself English/Latin even in Korea (the Korean name is just that Latin string, e.g. "Dear.M", "HUMINT"), then that same form is correct for every locale.`,
		"- For brand/agency/term: prefer the form used by domestic press in that language.",
		`- If you don't know for a locale and cannot derive confidently, put it in "skipped" — do NOT guess.`,
		"- confidence: 0.7-0.9 when supported by Wikidata sitelinks (clear evidence); 0.5 default for romanization from known en/ja; <0.5 → skip instead.",
		"- Do NOT include locales that already have a known spelling.",
	}
	return strings.Join(lines, "\n")
}
