// Package server implements the otosu drop service.
//
// Two endpoints matter. Creating a drop requires the login session, because
// otherwise the service would be an open file host. Reading a drop requires
// only the code: the code *is* the capability, which is what lets dev boxes run
// the client without storing any secret.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/rlongdragon/otosu/internal/code"
	"github.com/rlongdragon/otosu/internal/store"
	"github.com/rlongdragon/otosu/web"
)

// Options configures a Server.
type Options struct {
	// Token is the login token for the browser. Required.
	Token string
	// BaseURL is the public origin, used to bake the right URL into the
	// installer script. Required.
	BaseURL string
	// Store holds live drops. Required.
	Store *store.Store
	// SecureCookies marks the session cookie Secure. Derived from BaseURL when
	// the server is constructed, so a TLS terminating proxy still gets it right.
	SecureCookies bool
	// TrustedProxies lists the networks whose X-Forwarded-For header may be
	// believed. Leave empty when otosu faces clients directly.
	TrustedProxies []netip.Prefix
	// Logger receives request and error logs. Defaults to slog.Default().
	Logger *slog.Logger
}

// Server routes HTTP requests for the drop service.
type Server struct {
	store          *store.Store
	token          string
	baseURL        string
	secureCookies  bool
	trustedProxies []netip.Prefix
	log            *slog.Logger

	// tight guards the endpoints an attacker would hammer: code lookups and
	// login attempts. loose guards everything else.
	tight *limiter
	loose *limiter
}

// New validates opt and returns a Server.
func New(opt Options) (*Server, error) {
	if opt.Token == "" {
		return nil, errors.New("server: token is required")
	}
	if opt.Store == nil {
		return nil, errors.New("server: store is required")
	}
	if opt.Logger == nil {
		opt.Logger = slog.Default()
	}
	base := strings.TrimRight(opt.BaseURL, "/")

	// A Secure cookie over plain HTTP is silently dropped by the browser, and a
	// non-Secure cookie behind an HTTPS proxy leaks on any downgrade. The public
	// scheme is the only thing that decides this correctly.
	secure := opt.SecureCookies || strings.HasPrefix(base, "https://")

	return &Server{
		store:          opt.Store,
		token:          opt.Token,
		baseURL:        base,
		secureCookies:  secure,
		trustedProxies: opt.TrustedProxies,
		log:            opt.Logger,
		tight:          newLimiter(5, 10),
		loose:          newLimiter(20, 40),
	}, nil
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	tight := func(h http.HandlerFunc) http.Handler { return s.tight.middleware(s.clientIP, h) }
	loose := func(h http.HandlerFunc) http.Handler { return s.loose.middleware(s.clientIP, h) }

	mux.Handle("GET /{$}", loose(s.handleIndex))
	mux.Handle("GET /app.js", loose(s.handleAsset))
	mux.Handle("GET /seal.js", loose(s.handleAsset))
	mux.Handle("GET /healthz", loose(s.handleHealth))
	mux.Handle("GET /install", loose(s.handleInstall))

	mux.Handle("POST /api/login", tight(s.handleLogin))
	mux.Handle("POST /api/logout", loose(s.handleLogout))
	mux.Handle("GET /api/session", loose(s.handleSession))
	mux.Handle("POST /api/drop", loose(s.requireAuth(s.handleCreateDrop)))

	// The code guessing surface. Rate limited hardest.
	mux.Handle("GET /d/{code}", tight(s.handleFetchDrop))

	return securityHeaders(mux)
}

// securityHeaders applies defaults that matter for a page holding key material.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		// The page must not be able to reach any origin but its own; a leak of
		// the AES key to a third party would defeat the whole design.
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; "+
				"connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// --------------------------------------------------------------- responses ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// ---------------------------------------------------------------- handlers ---

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	body, err := web.FS.ReadFile("index.html")
	if err != nil {
		http.Error(w, "missing index", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}

// handleAsset serves the embedded JavaScript modules. Only the exact routes
// registered on the mux reach it, so the path cannot be used to traverse.
func (s *Server) handleAsset(w http.ResponseWriter, r *http.Request) {
	body, err := web.FS.ReadFile(strings.TrimPrefix(r.URL.Path, "/"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	count, bytes := s.store.Stats()
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"drops":  count,
		"bytes":  bytes,
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "malformed request")
		return
	}
	if !s.checkToken(body.Token) {
		s.log.Warn("login failed", "ip", s.clientIP(r))
		writeJSONError(w, http.StatusUnauthorized, "wrong token")
		return
	}
	s.setSession(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.clearSession(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if !s.authed(r) {
		writeJSONError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleCreateDrop(w http.ResponseWriter, r *http.Request) {
	// Cap the body a little above the per-drop limit so an oversized upload
	// fails with a clear message rather than a truncated read.
	r.Body = http.MaxBytesReader(w, r.Body, store.DefaultMaxBlob+1<<20)
	blob, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSONError(w, http.StatusRequestEntityTooLarge, "file too large")
		return
	}
	if len(blob) == 0 {
		writeJSONError(w, http.StatusBadRequest, "empty body")
		return
	}

	// Retry on the astronomically unlikely code collision rather than failing.
	for attempt := 0; attempt < 8; attempt++ {
		c, err := code.New()
		if err != nil {
			s.log.Error("generate code", "err", err)
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}
		switch err := s.store.Put(c, blob); {
		case err == nil:
			s.log.Info("drop created", "code", c, "bytes", len(blob))
			// expires_in lets the page show the drop's remaining life without
			// hardcoding a TTL the operator may have changed.
			writeJSON(w, http.StatusOK, map[string]any{
				"code":       c,
				"expires_in": int(s.store.TTL().Seconds()),
			})
			return
		case errors.Is(err, store.ErrDuplicate):
			continue
		case errors.Is(err, store.ErrTooLarge):
			writeJSONError(w, http.StatusRequestEntityTooLarge, "file too large")
			return
		case errors.Is(err, store.ErrFull):
			writeJSONError(w, http.StatusServiceUnavailable, "server is full, try again shortly")
			return
		default:
			s.log.Error("store drop", "err", err)
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}
	writeJSONError(w, http.StatusInternalServerError, "could not allocate a code")
}

func (s *Server) handleFetchDrop(w http.ResponseWriter, r *http.Request) {
	c := code.Normalize(r.PathValue("code"))

	// Invalid syntax and unknown codes must be indistinguishable, so a prober
	// learns nothing from the response.
	if !code.Valid(c) {
		http.NotFound(w, r)
		return
	}
	blob, err := s.store.Get(c)
	if err != nil {
		s.log.Warn("drop miss", "ip", s.clientIP(r), "code", c)
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", fmt.Sprint(len(blob)))
	_, _ = w.Write(blob)
}

func (s *Server) handleInstall(w http.ResponseWriter, r *http.Request) {
	script, err := renderInstaller(s.baseURL)
	if err != nil {
		s.log.Error("render installer", "err", err)
		http.Error(w, "installer unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(script)
}

// Timeouts for the outer http.Server. Uploads can be tens of megabytes over a
// slow uplink, so the write and read budgets are generous.
const (
	ReadHeaderTimeout = 10 * time.Second
	ReadTimeout       = 5 * time.Minute
	WriteTimeout      = 5 * time.Minute
	IdleTimeout       = 2 * time.Minute
)
