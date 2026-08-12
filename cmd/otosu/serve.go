package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/crypto/acme/autocert"

	"github.com/rlongdragon/otosu/internal/server"
	"github.com/rlongdragon/otosu/internal/store"
)

type serveConfig struct {
	domain    string // production: hostname for automatic HTTPS
	addr      string // development: plain HTTP listen address
	baseURL   string
	certDir   string
	token     string
	generated bool
	log       *slog.Logger
}

func newToken() (string, error) { return server.NewToken() }

func serve(cfg serveConfig) error {
	prod := cfg.domain != ""

	base := cfg.baseURL
	if base == "" {
		if prod {
			base = "https://" + cfg.domain
		} else {
			base = "http://" + cfg.addr
		}
	}

	st := store.New(store.Options{})
	stop := make(chan struct{})
	defer close(stop)
	go st.RunJanitor(time.Minute, stop)

	srv, err := server.New(server.Options{
		Token:   cfg.token,
		BaseURL: base,
		Store:   st,
		// A Secure cookie is dropped by browsers over plain HTTP, so it can only
		// be set in production.
		SecureCookies: prod,
		Logger:        cfg.log,
	})
	if err != nil {
		return err
	}

	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: server.ReadHeaderTimeout,
		ReadTimeout:       server.ReadTimeout,
		WriteTimeout:      server.WriteTimeout,
		IdleTimeout:       server.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(cfg.log.Handler(), slog.LevelWarn),
	}

	if cfg.generated {
		// Printed rather than logged so it is easy to copy on first run.
		fmt.Fprintf(os.Stderr, "\n  otosu login token: %s\n\n", cfg.token)
	}
	cfg.log.Info("starting", "version", version, "base_url", base)

	errc := make(chan error, 1)
	if prod {
		go func() { errc <- serveTLS(httpSrv, cfg) }()
	} else {
		httpSrv.Addr = cfg.addr
		go func() {
			if err := httpSrv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
				errc <- err
			}
		}()
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errc:
		return err
	case <-sig:
		cfg.log.Info("shutting down")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpSrv.Shutdown(ctx)
	}
}

// serveTLS runs the production listeners: HTTPS on 443 with certificates
// obtained automatically, and HTTP on 80 for the ACME challenge and redirects.
func serveTLS(httpSrv *http.Server, cfg serveConfig) error {
	certDir := cfg.certDir
	if certDir == "" {
		dir, err := os.UserCacheDir()
		if err != nil {
			return fmt.Errorf("locate cache directory: %w", err)
		}
		certDir = filepath.Join(dir, "otosu", "certs")
	}
	// Let's Encrypt account keys and certificates live here; keep them private.
	if err := os.MkdirAll(certDir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", certDir, err)
	}

	m := &autocert.Manager{
		Cache:      autocert.DirCache(certDir),
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(cfg.domain),
	}

	go func() {
		redirect := &http.Server{
			Addr:              ":80",
			Handler:           m.HTTPHandler(http.HandlerFunc(redirectToHTTPS)),
			ReadHeaderTimeout: server.ReadHeaderTimeout,
		}
		if err := redirect.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			cfg.log.Error("http listener", "err", err)
		}
	}()

	httpSrv.Addr = ":443"
	httpSrv.TLSConfig = m.TLSConfig()
	cfg.log.Info("listening", "https", ":443", "domain", cfg.domain, "certs", certDir)

	if err := httpSrv.ListenAndServeTLS("", ""); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
