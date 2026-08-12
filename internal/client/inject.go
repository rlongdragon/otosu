package client

import (
	"bufio"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Tool identifies an agent CLI, which decides how a file path must be written
// for that tool to pick it up.
type Tool string

const (
	ToolClaude   Tool = "claude"
	ToolCodex    Tool = "codex"
	ToolOpenCode Tool = "opencode"
	ToolAider    Tool = "aider"
	ToolUnknown  Tool = ""
)

// ErrNoPane means no tmux pane running a known agent CLI was found.
var ErrNoPane = errors.New("no agent pane found in tmux")

// Reference renders path the way tool expects a file to be mentioned.
//
// This is the only tool-specific part of otosu. Everything below it works at
// the PTY level and is therefore agnostic to which CLI is running.
func Reference(tool Tool, path string) string {
	switch tool {
	case ToolClaude:
		return "@" + path + " "
	case ToolOpenCode:
		// OpenCode resolves '@' mentions against the client's working directory,
		// which is wrong for a remote session, so use the file URL form.
		return "file://" + path + " "
	case ToolAider:
		return "/add " + path + "\n"
	default:
		// Codex and anything unrecognised accept a bare path, which is also what
		// dragging a file into the terminal produces.
		return path + " "
	}
}

// Inject types the reference into a tmux pane.
//
// It goes through a tmux paste buffer rather than send-keys so the text is
// delivered as a single paste and never reinterpreted as key sequences.
func Inject(pane string, tool Tool, path string) error {
	ref := Reference(tool, path)

	load := exec.Command("tmux", "load-buffer", "-b", "otosu", "-")
	load.Stdin = strings.NewReader(ref)
	if out, err := load.CombinedOutput(); err != nil {
		return fmt.Errorf("tmux load-buffer: %w: %s", err, strings.TrimSpace(string(out)))
	}

	// -p wraps the text in bracketed paste so a TUI treats it as pasted input.
	// -d drops the buffer afterwards so nothing lingers in the tmux server.
	paste := exec.Command("tmux", "paste-buffer", "-b", "otosu", "-p", "-d", "-t", pane)
	if out, err := paste.CombinedOutput(); err != nil {
		return fmt.Errorf("tmux paste-buffer: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// InTmux reports whether a tmux server is reachable.
func InTmux() bool {
	return exec.Command("tmux", "list-sessions").Run() == nil
}

// Status writes a message into the tmux status line, where it stays for hold
// and then disappears on its own. A later message replaces an earlier one.
func Status(msg string, hold time.Duration) {
	ms := strconv.FormatInt(hold.Milliseconds(), 10)
	// Failure is not interesting: the status line is a convenience, and the
	// caller still reports the outcome on stderr.
	_ = exec.Command("tmux", "display-message", "-d", ms, msg).Run()
}

// statusHold is how long a progress frame lingers if nothing replaces it. It
// only matters if a transfer stalls, so it is comfortably longer than the
// update interval.
const statusHold = 4 * time.Second

// NewStatusProgress returns a Progress that draws a bar in the tmux status
// line, throttled so a fast transfer does not spawn a tmux process per read.
func NewStatusProgress() Progress {
	const minInterval = 100 * time.Millisecond

	var last time.Time
	return func(done, total int64) {
		// Always draw the final frame, so the bar visibly reaches 100%.
		complete := total > 0 && done >= total
		if !complete && time.Since(last) < minInterval {
			return
		}
		last = time.Now()

		Status("otosu  "+Bar(done, total), statusHold)
	}
}

// process is one row of the system process table.
type process struct {
	pid  int
	ppid int
	args string
}

// processes snapshots the process table once, so detecting the tool across many
// panes costs a single exec rather than one per pane.
func processes() (map[int]process, map[int][]int, error) {
	out, err := exec.Command("ps", "-o", "pid=,ppid=,args=", "-A").Output()
	if err != nil {
		return nil, nil, fmt.Errorf("ps: %w", err)
	}

	byPID := make(map[int]process)
	children := make(map[int][]int)

	sc := bufio.NewScanner(strings.NewReader(string(out)))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		ppid, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil {
			continue
		}
		byPID[pid] = process{pid: pid, ppid: ppid, args: strings.Join(fields[2:], " ")}
		children[ppid] = append(children[ppid], pid)
	}
	return byPID, children, sc.Err()
}

// DetectTool walks the descendants of pid looking for a known agent CLI.
//
// It matches on the executable name, and for node/bun/deno wrappers on the
// script path. Matching the whole argument string instead produces false
// positives whenever an unrelated plugin directory mentions a tool's name.
func DetectTool(pid int, byPID map[int]process, children map[int][]int) Tool {
	var walk func(int, int) Tool
	walk = func(p int, depth int) Tool {
		if depth > 12 {
			return ToolUnknown
		}
		if proc, ok := byPID[p]; ok {
			if t := classify(proc.args); t != ToolUnknown {
				return t
			}
		}
		for _, kid := range children[p] {
			if t := walk(kid, depth+1); t != ToolUnknown {
				return t
			}
		}
		return ToolUnknown
	}
	return walk(pid, 0)
}

func classify(args string) Tool {
	fields := strings.Fields(args)
	if len(fields) == 0 {
		return ToolUnknown
	}

	switch base := strings.ToLower(filepath.Base(fields[0])); {
	case base == "opencode" || strings.HasPrefix(base, "opencode."):
		return ToolOpenCode
	case base == "codex" || strings.HasPrefix(base, "codex."):
		return ToolCodex
	case base == "claude" || strings.HasPrefix(base, "claude."):
		return ToolClaude
	case base == "aider" || strings.HasPrefix(base, "aider."):
		return ToolAider
	case base == "node", base == "nodejs", base == "bun", base == "deno":
		if len(fields) < 2 {
			return ToolUnknown
		}
		script := strings.ToLower(fields[1])
		switch {
		case strings.Contains(script, "/opencode"):
			return ToolOpenCode
		case strings.Contains(script, "/codex"):
			return ToolCodex
		case strings.Contains(script, "/claude"):
			return ToolClaude
		}
	}
	return ToolUnknown
}

// Pane is a tmux pane that is running an agent CLI.
type Pane struct {
	ID   string
	Tool Tool
}

// FindPane returns the most recently active pane running a known agent CLI.
func FindPane() (Pane, error) {
	out, err := exec.Command("tmux", "list-panes", "-a", "-F",
		"#{pane_id} #{pane_pid} #{window_activity}").Output()
	if err != nil {
		return Pane{}, fmt.Errorf("tmux list-panes: %w", err)
	}

	byPID, children, err := processes()
	if err != nil {
		return Pane{}, err
	}

	best := Pane{}
	bestActivity := -1

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		tool := DetectTool(pid, byPID, children)
		if tool == ToolUnknown {
			continue
		}
		activity, err := strconv.Atoi(fields[2])
		if err != nil {
			activity = 0
		}
		if activity > bestActivity {
			best = Pane{ID: fields[0], Tool: tool}
			bestActivity = activity
		}
	}

	if best.ID == "" {
		return Pane{}, ErrNoPane
	}
	return best, nil
}

// ToolForPane identifies the agent running in a specific pane.
func ToolForPane(pane string) (Tool, error) {
	out, err := exec.Command("tmux", "display-message", "-p", "-t", pane, "#{pane_pid}").Output()
	if err != nil {
		return ToolUnknown, fmt.Errorf("tmux display-message: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return ToolUnknown, fmt.Errorf("parse pane pid: %w", err)
	}
	byPID, children, err := processes()
	if err != nil {
		return ToolUnknown, err
	}
	return DetectTool(pid, byPID, children), nil
}
