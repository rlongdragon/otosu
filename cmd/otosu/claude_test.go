package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rlongdragon/otosu/internal/code"
	"github.com/rlongdragon/otosu/internal/server"
	"github.com/rlongdragon/otosu/internal/store"
	"github.com/rlongdragon/otosu/internal/wire"
)

// dropServer runs a real otosu server and returns its URL plus a function that
// seals data into a new drop and returns the reference the browser would copy.
func dropServer(t *testing.T) (string, func(name string, data []byte) string) {
	t.Helper()
	srv, err := server.New(server.Options{
		Token:   "test-token",
		BaseURL: "http://otosu.example.com",
		Store:   store.New(store.Options{}),
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar}
	resp, err := browser.Post(ts.URL+"/api/login", "application/json", strings.NewReader(`{"token":"test-token"}`))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	resp.Body.Close()

	drop := func(name string, data []byte) string {
		key, err := wire.NewKey()
		if err != nil {
			t.Fatalf("NewKey: %v", err)
		}
		blob, err := wire.Seal(key, wire.Header{Name: name}, data)
		if err != nil {
			t.Fatalf("Seal: %v", err)
		}
		resp, err := browser.Post(ts.URL+"/api/drop", "application/octet-stream", bytes.NewReader(blob))
		if err != nil {
			t.Fatalf("create drop: %v", err)
		}
		defer resp.Body.Close()
		var out struct {
			Code string `json:"code"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Code == "" {
			t.Fatalf("create drop: status %s, %v", resp.Status, err)
		}
		return code.Ref{Code: out.Code, Key: key}.String()
	}
	return ts.URL, drop
}

func hookInput(prompt string) *bytes.Reader {
	b, _ := json.Marshal(map[string]string{"prompt": prompt, "hook_event_name": "UserPromptSubmit"})
	return bytes.NewReader(b)
}

func TestClaudeHookCollectsPastedDrop(t *testing.T) {
	url, drop := dropServer(t)
	dir := t.TempDir()
	ref := drop("shot.png", []byte("fake png"))

	var out bytes.Buffer
	err := claudeHook(hookInput("看這張 "+ref+" 哪裡錯了"), &out, []string{"-server", url, "-dir", dir})
	if err != nil {
		t.Fatalf("claudeHook: %v", err)
	}

	var resp struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("hook output is not JSON: %v\n%s", err, out.String())
	}
	if resp.HookSpecificOutput.HookEventName != "UserPromptSubmit" {
		t.Errorf("hookEventName = %q", resp.HookSpecificOutput.HookEventName)
	}
	ctx := resp.HookSpecificOutput.AdditionalContext
	path := filepath.Join(dir, "shot.png")
	if !strings.Contains(ctx, path) {
		t.Errorf("context does not name %s:\n%s", path, ctx)
	}
	if strings.Contains(ctx, ref[9:]) {
		t.Errorf("context repeats the key:\n%s", ctx)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "fake png" {
		t.Errorf("collected file = %q, %v", got, err)
	}
}

func TestClaudeHookStaysQuiet(t *testing.T) {
	url, drop := dropServer(t)
	ref := drop("x.txt", []byte("x"))

	for _, tc := range []struct {
		name  string
		input *bytes.Reader
	}{
		{"no code", hookInput("refactor the parser please")},
		{"left to the /otosu:otosu command", hookInput("/otosu:otosu " + ref)},
		{"not JSON", bytes.NewReader([]byte("garbage"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			dir := t.TempDir()
			if err := claudeHook(tc.input, &out, []string{"-server", url, "-dir", dir}); err != nil {
				t.Fatalf("claudeHook: %v", err)
			}
			if out.Len() != 0 {
				t.Errorf("hook printed %q, want nothing", out.String())
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Errorf("hook collected %d files, want none", len(entries))
			}
		})
	}
}

func TestClaudeHookApprovesOnlyDropReads(t *testing.T) {
	dir := t.TempDir()
	drop := filepath.Join(dir, "shot.png")
	outside := filepath.Join(t.TempDir(), "secret.txt")
	link := filepath.Join(dir, "escape.txt")
	for _, p := range []string{drop, outside} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name  string
		tool  string
		path  string
		allow bool
	}{
		{"collected file", "Read", drop, true},
		{"dotdot back in", "Read", filepath.Join(dir, "..", filepath.Base(dir), "shot.png"), true},
		{"outside the drop dir", "Read", outside, false},
		{"symlink out of it", "Read", link, false},
		{"the directory itself", "Read", dir, false},
		{"missing file", "Read", filepath.Join(dir, "nope.png"), false},
		{"another tool", "Write", drop, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, _ := json.Marshal(map[string]any{
				"hook_event_name": "PreToolUse",
				"tool_name":       tc.tool,
				"tool_input":      map[string]string{"file_path": tc.path},
			})
			var out bytes.Buffer
			if err := claudeHook(bytes.NewReader(in), &out, []string{"-dir", dir}); err != nil {
				t.Fatalf("claudeHook: %v", err)
			}
			allowed := strings.Contains(out.String(), `"permissionDecision":"allow"`)
			if allowed != tc.allow {
				t.Errorf("allowed = %v, want %v (output %q)", allowed, tc.allow, out.String())
			}
		})
	}
}

func TestClaudeCommandReport(t *testing.T) {
	url, drop := dropServer(t)
	dir := t.TempDir()
	good := drop("notes.txt", []byte("hello"))
	gone := code.Ref{Code: "B4K7QW2M", Key: make([]byte, wire.KeySize)}.String()

	var out bytes.Buffer
	text := good + " " + gone + " summarise these"
	if err := claudeHook(strings.NewReader(""), &out, []string{"-server", url, "-dir", dir, "-text", text}); err != nil {
		t.Fatalf("claudeHook: %v", err)
	}
	report := out.String()
	if !strings.Contains(report, filepath.Join(dir, "notes.txt")) {
		t.Errorf("report does not name the collected file:\n%s", report)
	}
	if !strings.Contains(report, "B4K7QW2M: "+"drop not found") {
		t.Errorf("report does not explain the failed code:\n%s", report)
	}

	out.Reset()
	if err := claudeHook(strings.NewReader(""), &out, []string{"-text", ""}); err != nil {
		t.Fatalf("claudeHook: %v", err)
	}
	if !strings.Contains(out.String(), "Usage: /otosu") {
		t.Errorf("empty /otosu printed %q, want usage", out.String())
	}
}
