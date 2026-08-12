package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
)

const (
	sessionCookie = "otosu_session"
	sessionLabel  = "otosu-session-v1"
)

// NewToken returns a fresh login token, printed on first run so the operator
// can paste it into the browser once.
func NewToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("server: generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// sessionValue derives the cookie payload from the login token.
//
// Deriving rather than storing keeps the server stateless, so sessions survive
// a restart, which is what the user asked for. The raw token never sits in the
// cookie, and the cookie is worth exactly as much as the token it came from.
func sessionValue(token string) string {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte(sessionLabel))
	return hex.EncodeToString(mac.Sum(nil))
}

// checkToken compares a submitted token against the configured one in constant
// time, so a timing side channel cannot reveal it byte by byte.
func (s *Server) checkToken(got string) bool {
	return hmac.Equal([]byte(got), []byte(s.token))
}

func (s *Server) authed(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	return hmac.Equal([]byte(c.Value), []byte(sessionValue(s.token)))
}

func (s *Server) setSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:  sessionCookie,
		Value: sessionValue(s.token),
		Path:  "/",
		// HttpOnly keeps the session out of reach of any script on the page, so
		// an XSS bug cannot walk off with it.
		HttpOnly: true,
		// Secure is dropped in dev mode, where the server listens on plain HTTP
		// on localhost and the browser would otherwise discard the cookie.
		Secure:   s.secureCookies,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int((10 * 365 * 24 * 60 * 60)), // effectively no expiry
	})
}

func (s *Server) clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secureCookies,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

// requireAuth guards the endpoints that create drops. Reading a drop is not
// guarded: there, the code itself is the capability.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authed(r) {
			writeJSONError(w, http.StatusUnauthorized, "not logged in")
			return
		}
		next(w, r)
	}
}
