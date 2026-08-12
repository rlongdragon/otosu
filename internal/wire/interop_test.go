package wire

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os/exec"
	"testing"
)

// TestBrowserInterop runs the browser's own sealing module under Node and
// checks that Go can open what it produces.
//
// The browser and the client are separate implementations of the same wire
// format. Without this test a mismatch would stay invisible until a real drop
// failed to decrypt on a dev box.
func TestBrowserInterop(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed; skipping browser interop check")
	}

	out, err := exec.Command("node", "testdata/seal_interop.mjs").Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("run seal_interop.mjs: %v\n%s", err, ee.Stderr)
		}
		t.Fatalf("run seal_interop.mjs: %v", err)
	}

	var cases []struct {
		Name string `json:"name"`
		Mime string `json:"mime"`
		Key  string `json:"key"`
		Blob string `json:"blob"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal(out, &cases); err != nil {
		t.Fatalf("decode interop output: %v\nraw: %s", err, out)
	}
	if len(cases) == 0 {
		t.Fatal("interop script produced no cases")
	}

	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			key, err := base64.RawURLEncoding.DecodeString(tc.Key)
			if err != nil {
				t.Fatalf("decode key: %v", err)
			}
			blob, err := base64.StdEncoding.DecodeString(tc.Blob)
			if err != nil {
				t.Fatalf("decode blob: %v", err)
			}
			want, err := base64.StdEncoding.DecodeString(tc.Data)
			if err != nil {
				t.Fatalf("decode expected data: %v", err)
			}

			hdr, got, err := Open(key, blob)
			if err != nil {
				t.Fatalf("Open on a browser sealed blob: %v", err)
			}
			if hdr.Name != tc.Name {
				t.Errorf("Name = %q, want %q", hdr.Name, tc.Name)
			}
			if hdr.Mime != tc.Mime {
				t.Errorf("Mime = %q, want %q", hdr.Mime, tc.Mime)
			}
			if hdr.Size != int64(len(want)) {
				t.Errorf("Size = %d, want %d", hdr.Size, len(want))
			}
			if !bytes.Equal(got, want) {
				t.Errorf("payload mismatch: got %d bytes, want %d", len(got), len(want))
			}
		})
	}
}
