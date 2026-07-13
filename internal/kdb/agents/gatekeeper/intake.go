package gatekeeper

// This file is the fail-closed admission gate for *new* research terms.  It
// deliberately has a different contract from PreGate: PreGate only finds
// obvious malformed candidates, while this gate must prove enough positive
// evidence before any provider investigation is allowed.

import (
	"net"
	"net/url"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const IntakeRuleVersion = "proper-noun-v1-20260710"

type IntakeVerdict string

const (
	IntakePass   IntakeVerdict = "pass"
	IntakeReview IntakeVerdict = "review"
	IntakeReject IntakeVerdict = "reject"
)

type IntakeInput struct {
	Term             string
	EntityType       string
	Context          string
	SourceURL        string
	HasSourceID      bool
	SourceTrusted    bool
	ExistingEntity   bool
	IdentityConflict bool
	TypeConflict     bool
	OperatorApproved bool
}

type IntakeDecision struct {
	Verdict       IntakeVerdict
	Normalized    string
	NormalizedKey string
	ReasonCode    string
	Flags         []string
	RuleVersion   string
}

// categoryOnlyTerms cannot identify one K-content entity.  Ambiguous common
// nouns such as "괴물" or "마더" are intentionally not listed because they can
// be legitimate work titles; those are held for review when proof is absent.
var categoryOnlyTerms = map[string]struct{}{
	"가수": {}, "배우": {}, "연예인": {}, "아이돌": {}, "그룹": {}, "걸그룹": {}, "보이그룹": {},
	"영화": {}, "드라마": {}, "예능": {}, "방송": {}, "프로그램": {}, "앨범": {}, "노래": {}, "음악": {},
	"뉴스": {}, "기사": {}, "인터뷰": {}, "검색": {}, "추천": {}, "공개": {}, "출연": {}, "컴백": {}, "데뷔": {},
	"오늘": {}, "어제": {}, "내일": {}, "지난": {}, "최근": {}, "이번": {}, "사진": {}, "영상": {}, "현장": {},
}

// These can be valid work titles, so they are never hard-rejected. They are,
// however, not sufficient person/group/agency/etc names even when a consumer
// supplied a mistaken type and a nearby article URL.
var ambiguousCommonTerms = map[string]struct{}{
	"건강": {}, "사랑": {}, "행복": {}, "인기": {}, "도움": {}, "친구": {}, "세계일주": {},
	"보자기": {}, "책": {}, "적": {}, "묘한": {}, "경박해": {}, "타이틀곡": {}, "트로트": {}, "수능": {},
}

var roleAffixes = []string{
	"배우 ", "가수 ", "감독 ", "작가 ", "모델 ", "개그맨 ", "개그우먼 ", "방송인 ",
	" 배우", " 가수", " 감독", " 작가", " 모델", " 멤버", " 대표", " 회장", " 기자",
}

var placeCues = []string{
	"올림픽공원", "올림픽홀", "컨벤션 센터", "컨벤션센터", "공연장", "체육관", "강변북로",
	"삼청동", "명동", "한남동",
}

var eventCues = []string{"국제영화제", "영화제", "패션위크", "시상식", "페스티벌", "축제"}

var typeCues = map[string][]string{
	"person":         {"배우", "가수", "감독", "작가", "모델", "개그맨", "개그우먼", "방송인", "멤버", "출연자"},
	"group":          {"그룹", "걸그룹", "보이그룹", "밴드", "멤버", "팀", "듀오"},
	"show":           {"예능", "프로그램", "방송", "쇼", "출연"},
	"drama":          {"드라마", "시리즈", "방영", "극본", "연출"},
	"movie":          {"영화", "개봉", "감독", "주연", "상영"},
	"song_album":     {"앨범", "싱글", "신곡", "노래", "음원", "발매"},
	"agency":         {"소속사", "기획사", "엔터테인먼트", "레이블"},
	"channel_outlet": {"방송사", "채널", "매체", "신문", "출판사"},
	"brand_place":    {"브랜드", "매장", "장소", "공연장", "호텔", "센터", "공원", "홀"},
	"event_tour":     {"공연", "투어", "콘서트", "영화제", "시상식", "페스티벌", "축제", "패션위크"},
	"character":      {"캐릭터", "역", "배역", "등장인물"},
}

func DecideIntake(in IntakeInput) IntakeDecision {
	t := normalizeIntakeTerm(in.Term)
	decision := IntakeDecision{Normalized: t, NormalizedKey: intakeNormalizedKey(t), RuleVersion: IntakeRuleVersion}
	if t == "" {
		return decision.with(IntakeReject, "empty", "empty")
	}
	if !utf8.ValidString(in.Term) || containsUnsafeFormat(t) {
		return decision.with(IntakeReject, "unsafe_unicode", "unsafe_unicode")
	}
	if !containsLetter(t) {
		return decision.with(IntakeReject, "no_letter", "no_letter")
	}

	pre := PreGate(t)
	decision.Flags = append(decision.Flags, pre.Flags...)
	if isFatalPreGate(pre) {
		return decision.with(IntakeReject, "fatal_"+preReasonCode(pre), pre.Flags...)
	}
	if _, generic := categoryOnlyTerms[strings.ToLower(t)]; generic {
		return decision.with(IntakeReject, "category_not_entity", "category_only")
	}
	if strings.EqualFold(strings.TrimSpace(in.EntityType), "term") {
		return decision.with(IntakeReject, "term_not_proper_noun", "term_type")
	}
	if in.ExistingEntity {
		return decision.with(IntakePass, "existing_entity", "existing_entity")
	}
	if in.IdentityConflict {
		return decision.with(IntakeReview, "normalized_identity_conflict", "identity_conflict")
	}
	if in.OperatorApproved {
		return decision.with(IntakePass, "operator_approved", "operator_approved")
	}
	if in.TypeConflict {
		return decision.with(IntakeReview, "conflicting_entity_types", "type_conflict")
	}

	et := strings.ToLower(strings.TrimSpace(in.EntityType))
	if !isConcreteIntakeType(et) {
		return decision.with(IntakeReview, "missing_or_unsupported_type", "type_unproven")
	}
	if _, generic := ambiguousCommonTerms[strings.ToLower(t)]; generic && !isWorkOrEventType(et) {
		return decision.with(IntakeReview, "ambiguous_common_for_type", "common_noun_risk")
	}
	if hasTypeShapeConflict(t, et) {
		return decision.with(IntakeReview, "type_shape_conflict", "type_shape_conflict")
	}

	hasSource := in.SourceTrusted && ValidIntakeSourceURL(in.SourceURL)
	if !hasSource {
		if in.HasSourceID {
			decision.Flags = append(decision.Flags, "source_id_unverified")
		}
		return decision.with(IntakeReview, "missing_source_evidence", "source_unproven")
	}
	context := normalizeContext(in.Context)
	if !exactContextMention(context, t) {
		return decision.with(IntakeReview, "missing_exact_context", "context_unproven")
	}
	if !hasTypeContextCue(context, t, et) {
		return decision.with(IntakeReview, "missing_type_context_cue", "context_cue_unproven")
	}

	// Non-fatal shape risks (long/numbered/spaced titles, punctuation, verb-like
	// title endings) may pass only after the full positive proof above.  This is
	// what admits real titles without turning PreGray into a blanket pass.
	return decision.with(IntakePass, "typed_context_evidence", "typed_context_evidence")
}

func (d IntakeDecision) with(v IntakeVerdict, reason string, flags ...string) IntakeDecision {
	d.Verdict = v
	d.ReasonCode = reason
	for _, flag := range flags {
		if flag == "" || containsString(d.Flags, flag) {
			continue
		}
		d.Flags = append(d.Flags, flag)
	}
	return d
}

func normalizeIntakeTerm(s string) string {
	return strings.Join(strings.Fields(norm.NFC.String(strings.TrimSpace(s))), " ")
}

// intakeNormalizedKey is used only for identity/dedup lookup. Raw canonical
// spelling remains untouched. Case, spacing and punctuation variants should
// not trigger a second provider investigation.
func intakeNormalizedKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(norm.NFC.String(strings.TrimSpace(s))) {
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func normalizeContext(s string) string {
	return strings.Join(strings.Fields(norm.NFC.String(strings.TrimSpace(s))), " ")
}

func containsUnsafeFormat(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Co, unicode.Cs) {
			return true
		}
	}
	return false
}

func containsLetter(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

func isFatalPreGate(pre PreResult) bool {
	if pre.Verdict != PreReject {
		return false
	}
	switch pre.Reason {
	case "empty", "lone jamo (broken)", "keyboard/random mash", "control/PUA char":
		return true
	default:
		return false
	}
}

func preReasonCode(pre PreResult) string {
	if len(pre.Flags) > 0 {
		return pre.Flags[0]
	}
	return "malformed"
}

func isConcreteIntakeType(t string) bool {
	_, ok := typeCues[t]
	return ok
}

// IsConcreteIntakeType reports whether t is a concrete (cue-verifiable) entity
// type for intake purposes. Exposed for the auto-evidence drain, which must
// decide whether a consumer-supplied type hint is worth prioritizing.
func IsConcreteIntakeType(t string) bool {
	return isConcreteIntakeType(strings.ToLower(strings.TrimSpace(t)))
}

func hasTypeShapeConflict(term, entityType string) bool {
	low := strings.ToLower(term)
	if entityType == "person" {
		for _, affix := range roleAffixes {
			if strings.HasPrefix(low, affix) || strings.HasSuffix(low, affix) {
				return true
			}
		}
		parts := strings.Fields(term)
		if len(parts) == 2 && utf8.RuneCountInString(parts[0]) >= 4 && utf8.RuneCountInString(parts[1]) <= 4 {
			return true // likely "group member" compound; canonical person is unresolved
		}
	}
	if entityType != "brand_place" {
		for _, cue := range placeCues {
			if strings.Contains(low, strings.ToLower(cue)) {
				return true
			}
		}
	}
	if entityType != "event_tour" {
		for _, cue := range eventCues {
			if strings.Contains(low, strings.ToLower(cue)) {
				return true
			}
		}
		if strings.HasPrefix(low, "제") && strings.Contains(low, "회 ") {
			return true
		}
	}
	if entityType != "channel_outlet" && (strings.HasPrefix(low, "출판사 ") || strings.HasPrefix(low, "방송사 ")) {
		return true
	}
	return false
}

func exactContextMention(context, term string) bool {
	return contextMentionIndex(context, term) >= 0
}

func hasTypeContextCue(context, term, entityType string) bool {
	low := contextMentionWindow(context, term, 32)
	for _, cue := range typeCues[entityType] {
		if strings.Contains(low, strings.ToLower(cue)) {
			return true
		}
	}
	if isWorkOrEventType(entityType) && quotedMention(context, term) {
		return true
	}
	return false
}

func contextMentionIndex(context, term string) int {
	low, needle := strings.ToLower(context), strings.ToLower(term)
	if low == "" || needle == "" {
		return -1
	}
	for offset := 0; offset <= len(low)-len(needle); {
		rel := strings.Index(low[offset:], needle)
		if rel < 0 {
			return -1
		}
		idx := offset + rel
		beforeOK := idx == 0
		if !beforeOK {
			r, _ := utf8.DecodeLastRuneInString(low[:idx])
			beforeOK = !unicode.IsLetter(r) && !unicode.IsDigit(r)
		}
		after := low[idx+len(needle):]
		afterOK := after == ""
		if !afterOK {
			r, _ := utf8.DecodeRuneInString(after)
			afterOK = (!unicode.IsLetter(r) && !unicode.IsDigit(r)) || startsWithKoreanBoundarySuffix(after)
		}
		if beforeOK && afterOK {
			return idx
		}
		_, size := utf8.DecodeRuneInString(low[idx:])
		if size <= 0 {
			return -1
		}
		offset = idx + size
	}
	return -1
}

func startsWithKoreanBoundarySuffix(s string) bool {
	for _, suffix := range []string{
		"이", "가", "은", "는", "을", "를", "과", "와", "의", "도", "만", "에", "에서", "로", "으로",
		"에게", "한테", "께서", "처럼", "보다", "부터", "까지", "씨", "님",
	} {
		if strings.HasPrefix(s, suffix) {
			return true
		}
	}
	return false
}

func contextMentionWindow(context, term string, radius int) string {
	low := strings.ToLower(context)
	idx := contextMentionIndex(low, term)
	if idx < 0 {
		return ""
	}
	startRune := utf8.RuneCountInString(low[:idx])
	termRunes := utf8.RuneCountInString(term)
	runes := []rune(low)
	start, end := startRune-radius, startRune+termRunes+radius
	if start < 0 {
		start = 0
	}
	if end > len(runes) {
		end = len(runes)
	}
	return string(runes[start:end])
}

func isWorkOrEventType(t string) bool {
	switch t {
	case "show", "drama", "movie", "song_album", "event_tour":
		return true
	default:
		return false
	}
}

func quotedMention(context, term string) bool {
	for _, pair := range [][2]string{{"'", "'"}, {"\"", "\""}, {"‘", "’"}, {"“", "”"}, {"《", "》"}, {"〈", "〉"}} {
		if strings.Contains(context, pair[0]+term+pair[1]) {
			return true
		}
	}
	return false
}

// ValidIntakeSourceURL validates provenance syntax.  It is not by itself a
// trust proof; DecideIntake also requires a concrete type and exact contextual
// mention/cue. Literal local/private destinations are rejected to prevent
// source metadata from becoming an SSRF-shaped bypass token.
func ValidIntakeSourceURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()) {
		return false
	}
	return true
}

func IntakeSourceHost(raw string) (string, bool) {
	if !ValidIntakeSourceURL(raw) {
		return "", false
	}
	u, _ := url.Parse(strings.TrimSpace(raw))
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	return strings.TrimPrefix(host, "www."), host != ""
}

// ConfiguredTrustedIntakeSourceURL recognizes exact server-owned ingress
// article hosts. Media domains stored in kwave_news_whitelist are added by the
// Store/worker using DB state; this helper never uses suffix matching, so
// kbs.co.kr.evil.tld cannot impersonate kbs.co.kr.
func ConfiguredTrustedIntakeSourceURL(raw string) bool {
	host, ok := IntakeSourceHost(raw)
	if !ok {
		return false
	}
	configured := strings.TrimSpace(os.Getenv("KDB_INTAKE_TRUSTED_SOURCE_HOSTS"))
	if configured == "" {
		configured = "kstory.aiinplanet.com"
	}
	for _, item := range strings.Split(configured, ",") {
		want := strings.TrimPrefix(strings.TrimSuffix(strings.ToLower(strings.TrimSpace(item)), "."), "www.")
		if want != "" && host == want {
			return true
		}
	}
	return false
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
