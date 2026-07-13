package kdbadmin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 외부 매체(소비자)용 KDB 검색 API 키 발급/회수. /admin/settings 하단에 표시한다.
// 키는 kwave_kdb_api_consumers(migration 0067)에 저장되고, 런타임 인증(kdbapi)이
// .env KDB_API_KEYS 와 합집합으로 허용한다. 0066(아웃바운드 키)과 역할이 다르다.

// consumerRow — 소비자 키 한 행(표시용). 평문 키는 DB 에 저장하지 않으며(해시만),
// 발급 직후 1회만 newKey 로 화면에 노출한다.
type consumerRow struct {
	ID         string
	Label      string
	Masked     string // DB 의 key_prefix (kdb_xxxx••••yyyy). 평문 복원 불가.
	Active     bool
	CreatedAt  time.Time
	CreatedBy  string
	LastUsedAt *time.Time
}

func (s *Server) listConsumers(ctx context.Context) ([]consumerRow, error) {
	rows, err := s.pool.Query(ctx, `
SELECT id::text, label, COALESCE(key_prefix,''), active, created_at, created_by, last_used_at
  FROM kwave_kdb_api_consumers
 ORDER BY active DESC, created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]consumerRow, 0, 8)
	for rows.Next() {
		var c consumerRow
		if err := rows.Scan(&c.ID, &c.Label, &c.Masked, &c.Active, &c.CreatedAt, &c.CreatedBy, &c.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// generateConsumerKey — "kdb_" + 32 hex(16 random bytes). 충돌 확률 무시 가능.
func generateConsumerKey() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "kdb_" + hex.EncodeToString(b), nil
}

// hashConsumerKey — 저장/조회용 SHA-256 hex. 평문은 절대 영속화하지 않는다.
func hashConsumerKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// insertConsumer — 평문 키의 해시 + 표시용 마스킹 prefix 만 저장 (평문 미저장).
func insertConsumer(ctx context.Context, pool *pgxpool.Pool, label, key, by string) (string, error) {
	id := uuid.New()
	_, err := pool.Exec(ctx, `
INSERT INTO kwave_kdb_api_consumers (id, label, key_hash, key_prefix, active, created_by)
VALUES ($1, $2, $3, $4, true, $5)`, id, label, hashConsumerKey(key), maskKey(key), by)
	return id.String(), err
}

func revokeConsumer(ctx context.Context, pool *pgxpool.Pool, id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return err
	}
	_, err := pool.Exec(ctx,
		`UPDATE kwave_kdb_api_consumers SET active = false WHERE id = $1::uuid`, id)
	return err
}

// maskKey — "kdb_" prefix + 끝 4자만 노출. apikeys.Mask 와 동일 정책이되 prefix 보존.
func maskKey(v string) string {
	r := []rune(v)
	if len(r) <= 8 {
		return "••••"
	}
	return string(r[:4]) + "••••" + string(r[len(r)-4:])
}

// consumerActor — 세션 쿠키에서 발급자(admin user) 식별. 없으면 "admin".
func (s *Server) consumerActor(r *http.Request) string {
	if c, err := r.Cookie(sessionCookieName); err == nil {
		if u, ok := decodeSession(s.opts.SessionSecret, c.Value); ok {
			return u
		}
	}
	return "admin"
}

// consumersIssue — POST /admin/settings/consumers. 라벨로 새 키 발급.
// 발급된 평문 키를 1회 노출(?newkey=...)하고 설정 페이지로 redirect.
func (s *Server) consumersIssue(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, "consumer parse", err)
		return
	}
	label := strings.TrimSpace(r.PostFormValue("label"))
	if label == "" {
		http.Redirect(w, r, "/admin/settings?cerr=label+required", http.StatusSeeOther)
		return
	}
	key, err := generateConsumerKey()
	if err != nil {
		s.renderError(w, r, "consumer keygen", err)
		return
	}
	if _, err := insertConsumer(r.Context(), s.pool, label, key, s.consumerActor(r)); err != nil {
		s.renderError(w, r, "consumer insert", err)
		return
	}
	// 평문 키는 1회만 화면에 노출한다. redirect 쿼리에 실으면 브라우저 히스토리·
	// 액세스 로그·프록시에 평문이 남으므로(H4), redirect 대신 설정 페이지를 직접
	// 렌더하면서 newKey 를 data 로 전달한다 (URL/로그에 평문 미노출).
	consumers, cerr := s.listConsumers(r.Context())
	data := map[string]any{
		"title":           "API 설정",
		"page":            "/admin/settings",
		"rows":            s.settingRows(r),
		"consumers":       consumers,
		"sources":         sourceInventory(),
		"sourcePipelines": sourcePipelines(),
		"newKey":          key,
		"newLabel":        label,
	}
	if cerr != nil {
		data["cerr"] = "소비자 목록 조회 실패: " + cerr.Error()
	}
	s.render(w, r, "api_settings.html", data)
}

// consumersRevoke — POST /admin/settings/consumers/revoke. active=false 로 비활성.
func (s *Server) consumersRevoke(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, "consumer parse", err)
		return
	}
	id := strings.TrimSpace(r.PostFormValue("id"))
	if id == "" {
		http.Redirect(w, r, "/admin/settings?cerr=id+required", http.StatusSeeOther)
		return
	}
	if err := revokeConsumer(r.Context(), s.pool, id); err != nil {
		http.Redirect(w, r, "/admin/settings?cerr=revoke+failed", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/settings?crevoked=1", http.StatusSeeOther)
}
