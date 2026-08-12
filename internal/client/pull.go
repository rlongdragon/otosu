// Package client collects a drop on a dev box and hands the resulting file to
// whichever agent CLI is running in tmux.
package client

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/rlongdragon/otosu/internal/code"
	"github.com/rlongdragon/otosu/internal/wire"
)

// DefaultMaxDownload bounds what we will read from the server, so a hostile or
// broken endpoint cannot exhaust memory on the dev box.
const DefaultMaxDownload = 64 << 20

// ErrNotFound means the code was unknown, already collected, or expired.
var ErrNotFound = errors.New("drop not found: the code may be wrong, already collected, or expired")

// Pull fetches the drop named by ref, decrypts it, and writes it into dir.
// It returns the path of the written file.
//
// report may be nil. When set it is called as bytes arrive, which the tmux
// integration uses to draw a progress bar in the status line.
func Pull(server string, ref code.Ref, dir string, report Progress) (string, error) {
	blob, err := download(server, ref.Code, report)
	if err != nil {
		return "", err
	}

	hdr, data, err := wire.Open(ref.Key, blob)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}

	path := uniquePath(dir, safeName(hdr.Name))
	// 0600: drops routinely contain screenshots of tokens and internal systems,
	// and dev boxes are not always single user.
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}

func download(server, c string, report Progress) ([]byte, error) {
	url := strings.TrimRight(server, "/") + "/d/" + c

	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("contact %s: %w", server, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, ErrNotFound
	case http.StatusTooManyRequests:
		return nil, errors.New("rate limited by the server, wait a moment and retry")
	default:
		return nil, fmt.Errorf("server returned %s", resp.Status)
	}

	body := io.Reader(io.LimitReader(resp.Body, DefaultMaxDownload+1))
	if report != nil {
		// ContentLength is -1 when the server did not send one; Bar copes.
		body = &progressReader{r: body, total: resp.ContentLength, report: report}
	}

	blob, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if len(blob) > DefaultMaxDownload {
		return nil, fmt.Errorf("drop exceeds the %d byte download limit", DefaultMaxDownload)
	}
	return blob, nil
}

// unsafeRunes matches anything we refuse to put in a file name: path separators
// and control characters.
var unsafeRunes = regexp.MustCompile(`[^\p{L}\p{N} ._@()+-]`)

// safeName reduces a name from the sealed header to something safe to create.
//
// The header is authenticated, so the name came from the user's own browser
// rather than an attacker. It is still sanitised, because a name is the kind of
// value that later ends up in a shell command or a path join.
func safeName(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	name = unsafeRunes.ReplaceAllString(name, "_")
	name = strings.TrimLeft(name, ".-")

	if len(name) > 100 {
		ext := filepath.Ext(name)
		if len(ext) > 16 {
			ext = ""
		}
		name = name[:100-len(ext)] + ext
	}
	if name == "" {
		name = "drop-" + time.Now().Format("20060102-150405")
	}
	return name
}

// uniquePath avoids clobbering an earlier drop that happened to share a name.
func uniquePath(dir, name string) string {
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return path
	}

	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 2; i < 1000; i++ {
		p := filepath.Join(dir, fmt.Sprintf("%s-%d%s", stem, i, ext))
		if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
			return p
		}
	}
	return filepath.Join(dir, fmt.Sprintf("%s-%d%s", stem, time.Now().UnixNano(), ext))
}
