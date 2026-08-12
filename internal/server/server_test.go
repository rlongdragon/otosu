package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rlongdragon/otosu/internal/client"
	"github.com/rlongdragon/otosu/internal/code"
	"github.com/rlongdragon/otosu/internal/store"
	"github.com/rlongdragon/otosu/internal/wire"
)

const testToken = "test-token-do-not-use"

func newTestServer(t *testing.T, opt store.Options) (*httptest.Server, *http.Client) {
	t.Helper()

	srv, err := New(Options{
		Token:   testToken,
		BaseURL: "https://otosu.example.com",
		Store:   store.New(opt),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	return ts, &http.Client{Jar: jar}
}

func login(t *testing.T, ts *httptest.Server, c *http.Client, token string) int {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"token": token})
	resp, err := c.Post(ts.URL+"/api/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func createDrop(t *testing.T, ts *httptest.Server, c *http.Client, blob []byte) (string, int) {
	t.Helper()
	resp, err := c.Post(ts.URL+"/api/drop", "application/octet-stream", bytes.NewReader(blob))
	if err != nil {
		t.Fatalf("create drop: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode
	}
	var out struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode drop response: %v", err)
	}
	return out.Code, resp.StatusCode
}

func TestHealth(t *testing.T) {
	ts, c := newTestServer(t, store.Options{})
	resp, err := c.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestLogin(t *testing.T) {
	ts, c := newTestServer(t, store.Options{})

	if got := login(t, ts, c, "wrong"); got != http.StatusUnauthorized {
		t.Fatalf("login with wrong token: status = %d, want 401", got)
	}
	if got := login(t, ts, c, testToken); got != http.StatusOK {
		t.Fatalf("login with right token: status = %d, want 200", got)
	}

	resp, err := c.Get(ts.URL + "/api/session")
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("session after login: status = %d, want 200", resp.StatusCode)
	}
}

func TestCreateDropRequiresAuth(t *testing.T) {
	ts, c := newTestServer(t, store.Options{})
	if _, status := createDrop(t, ts, c, []byte("sealed")); status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", status)
	}
}

func TestFetchDropNeedsNoAuth(t *testing.T) {
	ts, uploader := newTestServer(t, store.Options{})
	login(t, ts, uploader, testToken)

	blob := []byte("sealed bytes")
	dropCode, status := createDrop(t, ts, uploader, blob)
	if status != http.StatusOK {
		t.Fatalf("create: status = %d", status)
	}

	// A fresh client with no cookie stands in for a dev box, which by design
	// stores no credentials.
	anon := &http.Client{}
	resp, err := anon.Get(ts.URL + "/d/" + dropCode)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !bytes.Equal(got, blob) {
		t.Errorf("blob = %q, want %q", got, blob)
	}
}

func TestFetchLimitAndMissesAreIndistinguishable(t *testing.T) {
	ts, c := newTestServer(t, store.Options{MaxFetches: 1})
	login(t, ts, c, testToken)

	dropCode, _ := createDrop(t, ts, c, []byte("x"))
	anon := &http.Client{}

	if resp, err := anon.Get(ts.URL + "/d/" + dropCode); err != nil {
		t.Fatalf("first fetch: %v", err)
	} else {
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("first fetch: status = %d, want 200", resp.StatusCode)
		}
	}

	// Collected, unknown and syntactically invalid codes must all look the same,
	// so probing tells an attacker nothing.
	for _, tc := range []struct{ name, path string }{
		{"already collected", "/d/" + dropCode},
		{"unknown code", "/d/ZZZZZZZZ"},
		{"invalid characters", "/d/!!!!!!!!"},
		{"wrong length", "/d/ABC"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := anon.Get(ts.URL + tc.path)
			if err != nil {
				t.Fatalf("fetch: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("status = %d, want 404", resp.StatusCode)
			}
		})
	}
}

func TestEmptyDropRejected(t *testing.T) {
	ts, c := newTestServer(t, store.Options{})
	login(t, ts, c, testToken)
	if _, status := createDrop(t, ts, c, nil); status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
}

func TestOversizedDropRejected(t *testing.T) {
	ts, c := newTestServer(t, store.Options{MaxBlob: 64})
	login(t, ts, c, testToken)
	if _, status := createDrop(t, ts, c, make([]byte, 65)); status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", status)
	}
}

func TestInstallScriptCarriesBaseURL(t *testing.T) {
	ts, c := newTestServer(t, store.Options{})
	resp, err := c.Get(ts.URL + "/install")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if !strings.Contains(string(body), "https://otosu.example.com") {
		t.Error("installer does not mention the configured base URL")
	}
	if !strings.HasPrefix(string(body), "#!/bin/sh") {
		t.Error("installer is not a shell script")
	}
}

func TestSecurityHeaders(t *testing.T) {
	ts, c := newTestServer(t, store.Options{})
	resp, err := c.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	defer resp.Body.Close()

	for header, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "no-referrer",
		"X-Frame-Options":        "DENY",
	} {
		if got := resp.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Errorf("CSP = %q, want it to restrict default-src to self", csp)
	}
}

// TestEndToEnd walks the whole path a real drop takes: the browser seals a
// file, the server stores ciphertext it cannot read, and the dev box collects
// and decrypts it.
func TestEndToEnd(t *testing.T) {
	ts, browser := newTestServer(t, store.Options{})
	login(t, ts, browser, testToken)

	key, err := wire.NewKey()
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	original := []byte("\x89PNG\r\n\x1a\n fake screenshot bytes")
	blob, err := wire.Seal(key, wire.Header{Name: "shot.png", Mime: "image/png"}, original)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	dropCode, status := createDrop(t, ts, browser, blob)
	if status != http.StatusOK {
		t.Fatalf("create drop: status = %d", status)
	}

	dir := t.TempDir()
	path, err := client.Pull(ts.URL, code.Ref{Code: dropCode, Key: key}, dir, nil)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}

	if got := filepath.Base(path); got != "shot.png" {
		t.Errorf("file name = %q, want shot.png", got)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read collected file: %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Errorf("collected content does not match the original")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("permissions = %o, want 600", perm)
	}
}

func TestPullWrongKeyFails(t *testing.T) {
	ts, browser := newTestServer(t, store.Options{})
	login(t, ts, browser, testToken)

	key, _ := wire.NewKey()
	blob, _ := wire.Seal(key, wire.Header{Name: "x.png"}, []byte("secret"))
	dropCode, _ := createDrop(t, ts, browser, blob)

	wrong, _ := wire.NewKey()
	if _, err := client.Pull(ts.URL, code.Ref{Code: dropCode, Key: wrong}, t.TempDir(), nil); err == nil {
		t.Fatal("Pull succeeded with the wrong key")
	}
}
