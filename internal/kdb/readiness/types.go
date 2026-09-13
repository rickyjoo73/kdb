// Package readiness owns the durable, tenant-scoped requested-locale ledger.
// It does not equate an English fallback or an ended research task with a
// verified native-language name. Legacy /prepare serving semantics stay intact.
package readiness

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/kdb"
)

const PolicyVersion = "native-evidence-v1"
const CommonPolicyVersion = "common-reviewed-names-v1"

var validQID = regexp.MustCompile(`^Q[1-9][0-9]*$`)

var (
	ErrConflict = errors.New("idempotency key reused with a different request")
	ErrNotFound = errors.New("preparation not found")
	ErrRevision = errors.New("preparation revision changed")
)

type Term struct {
	KO       string `json:"ko"`
	Type     string `json:"type,omitempty"`
	EntityID string `json:"entity_id,omitempty"`
	Context  string `json:"context,omitempty"`
}

type Input struct {
	Terms          []Term   `json:"terms"`
	Locales        []string `json:"locales"`
	SourceURL      string   `json:"source_url,omitempty"`
	ArticleID      string   `json:"article_id,omitempty"`
	ArticleVersion string   `json:"article_version,omitempty"`
	Catalog        string   `json:"catalog,omitempty"`
}

type Locale struct {
	Locale        string          `json:"locale"`
	State         string          `json:"state"`
	Value         string          `json:"value"`
	Source        string          `json:"source"`
	FallbackValue string          `json:"fallback_value"`
	Reason        string          `json:"reason"`
	Fingerprint   string          `json:"input_fingerprint"`
	ObservedAt    time.Time       `json:"observed_at"`
	FirstReadyAt  *time.Time      `json:"first_ready_at,omitempty"`
	ReadyAt       *time.Time      `json:"ready_at"`
	Proof         json.RawMessage `json:"proof,omitempty"`
	// PolicyProof — S03. ready/no_form 은 사용한 근거와 필수 의존의 모든 정책
	// (policy_id, revision) 을 중복 없이 고정한 비어 있지 않은 배열이어야 한다.
	PolicyProof json.RawMessage `json:"policy_proof,omitempty"`
}

type Item struct {
	Ordinal          int         `json:"ordinal"`
	Term             string      `json:"term"`
	Type             string      `json:"type,omitempty"`
	Context          string      `json:"context,omitempty"`
	SuppliedEntityID *uuid.UUID  `json:"supplied_entity_id,omitempty"`
	EntityID         *uuid.UUID  `json:"resolved_entity_id,omitempty"`
	BoundEntityID    *uuid.UUID  `json:"bound_entity_id,omitempty"`
	CandidateIDs     []uuid.UUID `json:"candidate_ids"`
	IdentityState    string      `json:"identity_state"`
	Locales          []Locale    `json:"locales"`
}

type Preparation struct {
	ID               uuid.UUID `json:"id"`
	Owner            string    `json:"-"`
	PolicyVersion    string    `json:"policy_version"`
	Status           string    `json:"status"`
	Revision         int64     `json:"revision"`
	RequestedLocales []string  `json:"requested_locales"`
	SourceURL        string    `json:"source_url,omitempty"`
	ArticleID        string    `json:"article_id,omitempty"`
	ArticleVersion   string    `json:"article_version,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	Items            []Item    `json:"items"`
}

var locales = map[string]string{
	"en": "en", "ja": "ja", "zh": "zh", "zh-cn": "zh", "zh-hans": "zh",
	"zh-tw": "zh_hant", "zh-hant": "zh_hant", "vi": "vi", "es": "es", "id": "id", "pt-br": "pt_br",
}

func Normalize(in Input) (Input, error) {
	if in.Catalog != "" && in.Catalog != "common" {
		return in, errors.New("catalog must be common or omitted for legacy")
	}
	in.Terms = append([]Term(nil), in.Terms...)
	if len(in.Terms) < 1 || len(in.Terms) > 200 {
		return in, errors.New("terms must contain 1 to 200 items")
	}
	if len(in.Locales) == 0 {
		in.Locales = []string{"en"}
	}
	set := map[string]bool{}
	var ls []string
	for _, l := range in.Locales {
		key := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(l)), "_", "-")
		n, ok := locales[key]
		if in.Catalog == "common" {
			n, ok = commonLocales[key]
		}
		if !ok {
			return in, fmt.Errorf("unsupported locale %q", l)
		}
		if !set[n] {
			set[n] = true
			ls = append(ls, n)
		}
	}
	sort.Strings(ls)
	in.Locales = ls
	for i, t := range in.Terms {
		t.KO = strings.TrimSpace(t.KO)
		t.Type = strings.TrimSpace(t.Type)
		t.EntityID = strings.TrimSpace(t.EntityID)
		if t.KO == "" || len([]rune(t.KO)) > 200 || len(t.Type) > 50 || len([]rune(t.Context)) > 2000 {
			return in, fmt.Errorf("invalid term %d", i)
		}
		if t.EntityID != "" {
			id, e := uuid.Parse(t.EntityID)
			if e != nil || id == uuid.Nil {
				return in, fmt.Errorf("invalid entity_id at term %d", i)
			}
			t.EntityID = id.String()
		}
		in.Terms[i] = t
	}
	if len(in.SourceURL) > 2048 || len(in.ArticleID) > 200 || len(in.ArticleVersion) > 200 {
		return in, errors.New("article metadata too long")
	}
	return in, nil
}

func hash(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Source must belong to the actual locale, not inferred from another locale or
// the entity's general URL list. Operator locking alone does not approve a value.
func qualifiedSource(source string) bool {
	switch source {
	case "operator", "operator-locked", "wikidata-label", "tmdb", "musicbrainz", "kofic", "kmdb", "naver-people", "correction-verified", "netflix", "disney", "itunes", "discogs", "media-consensus", "local-usage":
		return true
	}
	return false
}

type Snapshot struct {
	ID                                 uuid.UUID
	KO, Type, Status, QID, Fingerprint string
	Locked, Ambiguous                  bool
	Aliases                            []string
	Values, Sources                    map[string]string
	// Policies — provider 별 **현재 승인된** 정책. legacy source_code 는 ProviderForSourceCode 로
	// provider namespace 를 얻은 뒤 여기서 찾는다. 없으면 그 값은 공급하지 않는다(S03).
	Policies map[string]PolicyRef
}

// PolicyRef — 정책 증명 한 항목.
type PolicyRef struct {
	PolicyID string `json:"policy_id"`
	Revision int64  `json:"revision"`
}

// ProviderForSourceCode — legacy canonical_<loc>_source 값을 목표 provider namespace 로 옮긴다.
// 표의 근거는 KDB_SCHEMA_DIFF_0115_0122.md §9(source_code → 정책 namespace 8단계)다.
// 코드 문자열 자체는 names.source_code 에 그대로 보존하고, 정책 조회만 이 매핑으로 한다.
func ProviderForSourceCode(code string) string {
	switch {
	case code == "":
		return ""
	case strings.HasPrefix(code, "rss-observation"):
		return "rss-observation"
	case code == "operator" || code == "operator-locked" || code == "local-usage":
		return "operator"
	case code == "correction-verified":
		return "correction"
	case code == "wikidata-label":
		return "wikidata"
	case strings.HasPrefix(code, "wikipedia-"):
		return "wikipedia"
	case code == "gtranslate":
		return "mt:gtranslate"
	case code == "codex-fallback":
		return "mt:codex"
	case code == "romanization" || code == "opencc" || code == "kana-rule":
		return "generated:" + code
	case code == "local-search" || code == "naver-search" || code == "kakao-search" || code == "gemini-search":
		return "search:" + code
	default:
		return code
	}
}

func evaluate(s Snapshot, locale string) Locale {
	l := Locale{Locale: locale, State: "pending", Reason: "requested locale has no evidenced value", Fingerprint: hash([]string{s.Fingerprint, locale, s.Values[locale], s.Sources[locale], PolicyVersion})}
	if s.Status != "active" {
		l.State = "policy_blocked"
		l.Reason = "entity is not active"
		return l
	}
	if s.Ambiguous {
		l.State = "ambiguous"
		l.Reason = "entity identity requires review"
		return l
	}
	v, source := strings.TrimSpace(s.Values[locale]), s.Sources[locale]
	if v != "" && qualifiedSource(source) && kdb.IsValidSpellingForLocale(locale, v) {
		// S03: 출처 등급을 통과해도 그 provider 의 원천 정책이 지금 승인돼 있어야 공급한다.
		// 승인 전에는 값을 지어내지 않고 검수로 보낸다(P0.09 §5.5 기본 차단).
		ref, ok := s.Policies[ProviderForSourceCode(source)]
		if !ok {
			l.State = "policy_blocked"
			l.Reason = "source policy for " + source + " is not approved for name export"
			return l
		}
		l.State = "ready"
		l.Value = v
		l.Source = source
		l.Reason = "native locale value with recorded evidence source"
		l.PolicyProof, _ = json.Marshal([]PolicyRef{ref})
		return l
	}
	if locale != "en" && qualifiedSource(s.Sources["en"]) {
		l.FallbackValue = strings.TrimSpace(s.Values["en"])
	}
	if s.Locked {
		l.State = "policy_blocked"
		l.Reason = "operator lock prevents automatic fill"
		return l
	}
	if v != "" {
		l.State = "unverified"
		l.Source = source
		l.Reason = "stored value lacks qualified locale evidence; not overwritten automatically"
		return l
	}
	if !validQID.MatchString(s.QID) {
		l.State = "no_evidence"
		l.Reason = "no resolved stable identity anchor for automatic fill"
	}
	return l
}
