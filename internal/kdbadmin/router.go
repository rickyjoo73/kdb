// Package kdbadmin implements the KDB administrative web UI.
//
// All pages are protected by HTTP Basic Auth using KDB_ADMIN_USER /
// KDB_ADMIN_PASS. Pages are server-rendered html/template; no JS framework
// is required. Static assets are inlined from CDN (tailwind) so this
// binary is self-contained.
package kdbadmin

import (
	"embed"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb/ratelimit"
)

//go:embed templates
var templatesFS embed.FS

// Options carries server configuration.
type Options struct {
	SessionSecret []byte
	LogRequests   bool
}

// Server holds shared dependencies for handlers.
type Server struct {
	pool *pgxpool.Pool
	opts Options
	tmpl *template.Template
}

// NewRouter wires the admin router and returns it as an http.Handler.
func NewRouter(pool *pgxpool.Pool, opts Options) http.Handler {
	if len(opts.SessionSecret) == 0 {
		opts.SessionSecret = newSessionSecret()
		log.Printf("kdbadmin: KDB_ADMIN_SESSION_SECRET not provided — using ephemeral secret (sessions reset on restart)")
	}
	s := &Server{pool: pool, opts: opts}
	s.loadTemplates()

	r := chi.NewRouter()
	if opts.LogRequests {
		r.Use(requestLogger)
	}

	// Unauthenticated.
	r.Get("/healthz", s.health)
	r.Get("/admin/login", s.loginGet)
	r.Get("/admin/setup", s.setupGet)
	// 로그인/셋업 POST 는 IP 당 rate limit(브루트포스/스프레이 완화). 계정별
	// lockout(users.go)과 별개로 IP 단위 방어를 더한다.
	loginLimit := ratelimit.New(10, time.Minute).Middleware
	r.With(loginLimit).Post("/admin/login", s.loginPost)
	r.With(loginLimit).Post("/admin/setup", s.setupPost)

	// Authenticated admin tree.
	r.Group(func(r chi.Router) {
		r.Use(s.sessionAuth)
		r.Use(s.csrfProtect) // POST 메서드에만 작용 (GET 무영향)
		r.Get("/", s.redirectToAdmin)
		r.Get("/admin", s.dashboard)
		r.Get("/admin/", s.dashboard)
		r.Post("/admin/logout", s.logout)
		r.Route("/admin/entities", func(r chi.Router) {
			r.Get("/", s.entitiesList)
			r.Get("/sources", s.entitySources)
			r.Get("/review", s.entityReview)            // WF-4 품질 검토
			r.Get("/conflicts", s.entityConflicts)      // WF-2 충돌
			r.Post("/conflicts/mark-review", s.conflictMarkReview)
			r.Post("/conflicts/set-distinct", s.conflictSetDistinct)
			r.Get("/unclassified", s.entitiesUnclassified) // WF-1b 미분류
			r.Get("/locale-gaps", s.entitiesLocaleGaps) // WF-3 누락 locale
			r.Get("/whitelist", s.entityWhitelist)
			r.Get("/trust", s.entityTrust) // 검증 커버리지 대시보드
			r.Get("/{id}", s.entityDetail)
			// 운영자 액션: 분류/강제enrich/기각/잠금/Wikidata 채택.
			r.Post("/{id}/classify", s.entityClassify)
			r.Post("/{id}/enrich", s.entityEnrich)
			r.Post("/{id}/reject", s.entityReject)
			r.Post("/{id}/lock", s.entityLockToggle)
			r.Post("/{id}/wikidata-adopt", s.entityWikidataAdopt)
			// RSS Whitelist 운영자 액션 (도메인 추가/RSS 저장/토글/삭제).
			r.Post("/whitelist/add", s.whitelistAdd)
			r.Post("/whitelist/{domain}/{locale}/rss", s.whitelistSaveRSS)
			r.Post("/whitelist/{domain}/{locale}/toggle", s.whitelistToggle)
			r.Post("/whitelist/{domain}/{locale}/delete", s.whitelistDelete)
		})
		r.Route("/admin/kdb", func(r chi.Router) {
			r.Get("/candidates", s.kdbCandidates)
			r.Get("/codex-runs", s.kdbCodexRuns)
			r.Get("/observations", s.kdbObservations)
			r.Get("/requests", s.apiRequests)           // 클라이언트 API 요청 로그
			r.Get("/inbox", s.kdbInbox)                 // WF-1 신규 후보
			r.Post("/inbox/{id}/promote", s.inboxPromote)
			r.Post("/inbox/{id}/reject", s.inboxReject)
			r.Post("/inbox/{id}/defer", s.inboxDefer)
		})
		// 온디맨드(kstory) — 요청→발굴→응답 흐름 가시화.
		r.Route("/admin/ondemand", func(r chi.Router) {
			r.Get("/queue", s.ondemandQueue)
			r.Get("/consumers", s.ondemandConsumers)
		})
		// 품질 — 엔티티-레벨 정체성 검증 tier(서빙 정렬 SSOT).
		r.Route("/admin/quality", func(r chi.Router) {
			r.Get("/verification", s.qualityVerification)
		})
		r.Get("/admin/persons", s.personsList)
		r.Get("/admin/persons/{id}", s.personDetail) // 인물 세부정보(9개 언어 표기 전체)
		r.Get("/admin/corrections", s.correctionsList) // 외부 교정요청 심사
		r.Post("/admin/corrections/{id}/approve", s.correctionApprove)
		r.Post("/admin/corrections/{id}/reject", s.correctionReject)
		r.Get("/admin/hermes", s.handleHermes)
		r.Get("/admin/settings", s.apiSettings)
		r.Post("/admin/settings", s.apiSettingsSave)
		r.Post("/admin/settings/test", s.apiSettingsTest)
		r.Post("/admin/settings/consumers", s.consumersIssue)
		r.Post("/admin/settings/consumers/revoke", s.consumersRevoke)
	})

	return r
}

func (s *Server) loadTemplates() {
	sub, err := fs.Sub(templatesFS, "templates")
	if err != nil {
		log.Fatalf("kdbadmin: templates sub: %v", err)
	}
	t := template.New("").Funcs(funcMap())
	t, err = t.ParseFS(sub, "*.html")
	if err != nil {
		log.Fatalf("kdbadmin: parse templates: %v", err)
	}
	s.tmpl = t
}

func funcMap() template.FuncMap {
	return template.FuncMap{
		"trunc": func(n int, in string) string {
			if len(in) <= n {
				return in
			}
			return in[:n] + "…"
		},
		"fmtTime": func(v any) string {
			var t time.Time
			switch x := v.(type) {
			case time.Time:
				t = x
			case *time.Time:
				if x == nil {
					return "—"
				}
				t = *x
			case nil:
				return "—"
			default:
				return "?"
			}
			if t.IsZero() {
				return "—"
			}
			return t.In(time.Local).Format("2006-01-02 15:04")
		},
		"deref": func(v any) any {
			if v == nil {
				return ""
			}
			rv := reflect.ValueOf(v)
			if rv.Kind() == reflect.Pointer {
				if rv.IsNil() {
					return ""
				}
				return rv.Elem().Interface()
			}
			return v
		},
		"percent": func(part, total int64) int {
			if total == 0 {
				return 0
			}
			return int(part * 100 / total)
		},
		"addInt": func(a, b int) int { return a + b },
		"subInt": func(a, b int) int {
			c := a - b
			if c < 0 {
				return 0
			}
			return c
		},
		// dict builds a map[string]any from alternating key/value pairs, for
		// passing multiple named args to a sub-template, e.g.
		// {{template "localeCell" dict "Label" "English" "M" .En}}.
		"dict": func(kv ...any) (map[string]any, error) {
			if len(kv)%2 != 0 {
				return nil, errOddDict
			}
			m := make(map[string]any, len(kv)/2)
			for i := 0; i < len(kv); i += 2 {
				k, ok := kv[i].(string)
				if !ok {
					return nil, errDictKey
				}
				m[k] = kv[i+1]
			}
			return m, nil
		},
		// join concatenates a []string with sep. Tolerates a nil/empty slice.
		"join": func(sep string, items []string) string {
			return strings.Join(items, sep)
		},
	}
}

var (
	errOddDict = errDict("dict: odd number of arguments")
	errDictKey = errDict("dict: keys must be strings")
)

type errDict string

func (e errDict) Error() string { return string(e) }

func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	if _, ok := data["nav"]; !ok {
		data["nav"] = navItems()
	}
	if _, ok := data["page"]; !ok {
		data["page"] = name
	}
	if _, ok := data["user"]; !ok {
		if c, err := r.Cookie(sessionCookieName); err == nil {
			if u, ok := decodeSession(s.opts.SessionSecret, c.Value); ok {
				data["user"] = u
			}
		}
	}
	// CSRF 토큰 — 모든 인증 페이지 폼이 hidden field 로 싣도록 제공.
	if _, ok := data["csrf"]; !ok {
		if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
			data["csrf"] = csrfToken(s.opts.SessionSecret, c.Value)
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("kdbadmin: render %s: %v", name, err)
	}
}

// csrfProtect — 쿠키 인증 상태변경(POST 등) 요청에 세션-파생 CSRF 토큰을 요구한다.
// SameSite=Lax 가 이미 cross-site POST 쿠키를 막지만, 토큰으로 심층방어를 더한다.
// 안전 메서드(GET/HEAD)는 통과.
func (s *Server) csrfProtect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		c, err := r.Cookie(sessionCookieName)
		if err != nil || !validCSRF(s.opts.SessionSecret, c.Value, r.PostFormValue("_csrf")) {
			http.Error(w, "CSRF token invalid", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) sessionAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookieName)
		if err != nil || c.Value == "" {
			s.redirectToLogin(w, r)
			return
		}
		if _, ok := decodeSession(s.opts.SessionSecret, c.Value); !ok {
			clearSessionCookie(w)
			s.redirectToLogin(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) redirectToLogin(w http.ResponseWriter, r *http.Request) {
	next := r.URL.RequestURI()
	if next == "" || next == "/admin/login" {
		next = "/admin"
	}
	http.Redirect(w, r, "/admin/login?next="+url.QueryEscape(next), http.StatusFound)
}

func (s *Server) redirectToAdmin(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/admin", http.StatusFound)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte(`{"ok":true,"service":"kdb-admin"}`))
}

func (s *Server) notYet(label string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.render(w, r, "stub.html", map[string]any{
			"title":   label,
			"current": r.URL.Path,
		})
	}
}

// NavItem represents one sidebar link (or a section header when Section==true).
// Mirrors mediafine's kdb_base.html structure so operators see the same menu.
type NavItem struct {
	Title      string
	Path       string
	Action     string // action label echoed from the mediafine admin (review/pick/merge/…)
	Section    bool   // true → render as section header
	BadgeCount int    // optional pending-count badge (header partial renders it when > 0)
}

func navItems() []NavItem {
	return []NavItem{
		{Title: "운영 개요", Path: "/admin", Action: "home"},

		{Title: "DB", Section: true},
		{Title: "고유명사 DB", Path: "/admin/entities", Action: "전체"},
		{Title: "인물 DB", Path: "/admin/persons", Action: "person"},
		{Title: "locale 커버리지", Path: "/admin/entities/locale-gaps", Action: "coverage"},
		{Title: "검증 tier · 정체성", Path: "/admin/quality/verification", Action: "verify"},

		{Title: "온디맨드 · kstory", Section: true},
		{Title: "발굴 큐", Path: "/admin/ondemand/queue", Action: "queue"},
		{Title: "소비자 대시보드", Path: "/admin/ondemand/consumers", Action: "consumer"},
		{Title: "클라이언트 요청 로그", Path: "/admin/kdb/requests", Action: "API"},
		{Title: "신규 후보 (Inbox)", Path: "/admin/kdb/inbox", Action: "promote"},

		{Title: "검토 · 품질", Section: true},
		{Title: "검토 큐", Path: "/admin/entities/review", Action: "pick"},
		{Title: "충돌 · 동명이인", Path: "/admin/entities/conflicts", Action: "merge"},
		{Title: "교정요청 심사", Path: "/admin/corrections", Action: "review"},

		{Title: "운영 (Ops)", Section: true},
		{Title: "Hermes 파이프라인", Path: "/admin/hermes", Action: "supervise"},
		{Title: "해소 진단", Path: "/admin/entities/sources", Action: "diag"},
		{Title: "API · 키 설정", Path: "/admin/settings", Action: "key"},
	}
}

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		// 401s pre-auth are noisy and routine; quiet them.
		if sw.status == 401 && strings.HasPrefix(r.URL.Path, "/admin") {
			return
		}
		log.Printf("%-6s %3d %s (%s)", r.Method, sw.status, r.URL.Path, time.Since(start))
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
