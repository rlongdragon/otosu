package client

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestHumanBytes(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{999, "999 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{10 * 1024, "10 KB"},
		{131944, "129 KB"},
		{1024 * 1024, "1.0 MB"},
		{5*1024*1024 + 512*1024, "5.5 MB"},
		{3 * 1024 * 1024 * 1024, "3.0 GB"},
	} {
		if got := HumanBytes(tc.in); got != tc.want {
			t.Errorf("HumanBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBar(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		got := Bar(0, 1000)
		if !strings.Contains(got, "  0%") {
			t.Errorf("Bar(0, 1000) = %q, want 0%%", got)
		}
		if strings.Count(got, "▓") != 0 {
			t.Errorf("Bar(0, 1000) = %q, want no filled cells", got)
		}
	})

	t.Run("half", func(t *testing.T) {
		got := Bar(500, 1000)
		if !strings.Contains(got, " 50%") {
			t.Errorf("Bar(500, 1000) = %q, want 50%%", got)
		}
		if n := strings.Count(got, "▓"); n != barWidth/2 {
			t.Errorf("filled cells = %d, want %d", n, barWidth/2)
		}
	})

	t.Run("complete", func(t *testing.T) {
		got := Bar(1000, 1000)
		if !strings.Contains(got, "100%") {
			t.Errorf("Bar(1000, 1000) = %q, want 100%%", got)
		}
		if n := strings.Count(got, "▓"); n != barWidth {
			t.Errorf("filled cells = %d, want %d", n, barWidth)
		}
		if strings.Contains(got, "░") {
			t.Errorf("Bar at 100%% still shows empty cells: %q", got)
		}
	})

	t.Run("overshoot is clamped", func(t *testing.T) {
		got := Bar(2000, 1000)
		if !strings.Contains(got, "100%") {
			t.Errorf("Bar(2000, 1000) = %q, want it clamped to 100%%", got)
		}
	})

	t.Run("unknown total", func(t *testing.T) {
		// A server without Content-Length gives -1; a percentage would be a lie.
		got := Bar(4096, -1)
		if strings.Contains(got, "%") {
			t.Errorf("Bar(4096, -1) = %q, want no percentage", got)
		}
		if !strings.Contains(got, "4.0 KB") {
			t.Errorf("Bar(4096, -1) = %q, want the byte count", got)
		}
	})
}

func TestProgressReader(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 5000)

	var calls int
	var lastDone, lastTotal int64
	pr := &progressReader{
		r:     bytes.NewReader(data),
		total: int64(len(data)),
		report: func(done, total int64) {
			calls++
			lastDone, lastTotal = done, total
		},
	}

	got, err := io.ReadAll(pr)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Error("progressReader altered the stream")
	}
	if calls == 0 {
		t.Error("report was never called")
	}
	if lastDone != int64(len(data)) {
		t.Errorf("final done = %d, want %d", lastDone, len(data))
	}
	if lastTotal != int64(len(data)) {
		t.Errorf("total = %d, want %d", lastTotal, len(data))
	}
}

func TestProgressReaderNilReport(t *testing.T) {
	pr := &progressReader{r: strings.NewReader("hello"), total: 5}
	got, err := io.ReadAll(pr)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("got %q, want hello", got)
	}
}
