package client

import (
	"fmt"
	"io"
	"strings"
)

// Progress reports how much of a drop has been transferred. total is zero when
// the server did not send a Content-Length.
type Progress func(done, total int64)

// progressReader counts bytes as they are read and forwards the running total.
type progressReader struct {
	r      io.Reader
	total  int64
	done   int64
	report Progress
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.done += int64(n)
		if p.report != nil {
			p.report(p.done, p.total)
		}
	}
	return n, err
}

// barWidth is the number of cells in the rendered bar. It is kept modest so the
// whole line fits a narrow tmux status bar.
const barWidth = 20

// Bar renders a single line progress indicator.
//
//	▓▓▓▓▓▓▓▓▓▓▓▓░░░░░░░░  61%  ·  800 KB / 1.3 MB
//
// When total is unknown it degrades to a byte count, since a percentage would
// be a lie.
func Bar(done, total int64) string {
	if total <= 0 {
		return fmt.Sprintf("%s downloaded", HumanBytes(done))
	}
	if done > total {
		done = total
	}

	filled := int(float64(barWidth) * float64(done) / float64(total))
	pct := int(100 * float64(done) / float64(total))

	return fmt.Sprintf("%s%s  %3d%%  ·  %s / %s",
		strings.Repeat("▓", filled),
		strings.Repeat("░", barWidth-filled),
		pct,
		HumanBytes(done),
		HumanBytes(total),
	)
}

// HumanBytes formats a byte count for display.
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 3; v /= unit {
		div *= unit
		exp++
	}
	value := float64(n) / float64(div)
	format := "%.1f %cB"
	if value >= 10 {
		format = "%.0f %cB"
	}
	return fmt.Sprintf(format, value, "KMGT"[exp])
}
