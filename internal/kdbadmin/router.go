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
	r.Post("/admin/login", s.loginPost)
	r.Get("/admin/setup", s.setupGet)
	r.Post("/admin/setup", s.setupPost)

	// Authenticated admin tree.
	r.Group(func(r chi.Router) {
		r.Use(s.sessionAuth)
		r.Get("/", s.redirectToAdmin)
		r.Get("/admin", s.dashboard)
		r.Get("/admin/", s.dashboard)
		r.Post("/admin/logout", s.logout)
		r.Route("/admin/entities", func(r chi.Router) {
			r.Get("/", s.entitiesList)
			r.Get("/sources", s.entitySources)
			r.Get("/review", s.entityReview)
			r.Get("/conflicts", s.entityConflicts)
			r.Get("/whitelist", s.entityWhitelist)
			r.Get("/{id}", s.entityDetail)
		})
		r.Route("/admin/kdb", func(r chi.Router) {
			r.Get("/candidates", s.kdbCandidates)
			r.Get("/codex-runs", s.kdbCodexRuns)
			r.Get("/observations", s.kdbObservations)
		})
		r.Get("/admin/persons", s.personsList)
		r.Get("/admin/hermes", s.handleHermes)
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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("kdbadmin: render %s: %v", name, err)
	}
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
		{Title: "운영 개요", Path: "/admin", Action: "API"},

		{Title: "Data", Section: true},
		{Title: "고유명사 DB", Path: "/admin/entities", Action: "entity"},
		{Title: "인물 DB", Path: "/admin/persons", Action: "person"},
		{Title: "Entity 후보", Path: "/admin/kdb/candidates", Action: "review"},

		{Title: "Review", Section: true},
		{Title: "검토 큐", Path: "/admin/entities/review", Action: "pick"},
		{Title: "충돌 / 동명이인", Path: "/admin/entities/conflicts", Action: "merge"},
		{Title: "매체 표기 관측", Path: "/admin/kdb/observations", Action: "signals"},

		{Title: "Sources", Section: true},
		{Title: "다국어 DB 소스", Path: "/admin/entities/sources", Action: "cascade"},
		{Title: "RSS Whitelist", Path: "/admin/entities/whitelist", Action: "poll"},
		{Title: "Codex Audit", Path: "/admin/kdb/codex-runs", Action: "LLM"},

		{Title: "Agents", Section: true},
		{Title: "Hermes", Path: "/admin/hermes", Action: "supervise"},
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
