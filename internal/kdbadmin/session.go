package kdbadmin

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	sessionCookieName = "kdb_admin_session"
	sessionMaxAge     = 7 * 24 * time.Hour
)

// newSessionSecret returns a freshly generated 32-byte secret.
// Operators should set KDB_ADMIN_SESSION_SECRET to a stable value; otherwise
// every restart invalidates all sessions.
func newSessionSecret() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand only fails on catastrophic OS issues; panic surfaces it.
		panic("kdbadmin: crypto/rand: " + err.Error())
	}
	return b
}

func encodeSession(secret []byte, user string, ttl time.Duration) string {
	expires := time.Now().Add(ttl).Unix()
	payload := user + ":" + strconv.FormatInt(expires, 10)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	sig := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func decodeSession(secret []byte, token string) (user string, ok bool) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return "", false
	}
	bits := strings.SplitN(string(payload), ":", 2)
	if len(bits) != 2 {
		return "", false
	}
	exp, err := strconv.ParseInt(bits[1], 10, 64)
	if err != nil {
		return "", false
	}
	if time.Now().Unix() > exp {
		return "", false
	}
	return bits[0], true
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionMaxAge.Seconds()),
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
}

// isSecureRequest returns true when the request reached the server over HTTPS,
// either directly (r.TLS) or via a known reverse proxy (X-Forwarded-Proto).
func isSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		return true
	}
	return false
}
