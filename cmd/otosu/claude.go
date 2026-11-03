package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/rlongdragon/otosu/internal/client"
	"github.com/rlongdragon/otosu/internal/code"
)

// ------------------------------------------------------------- claude-hook ---

// runClaudeHook backs the Claude Code plugin.
//
// As a UserPromptSubmit hook it reads the prompt as JSON on stdin, collects
// every drop referenced in it and hands Claude the file paths as extra context.
// As a PreToolUse hook it approves reading those files. With -text it serves
// the /otosu command and prints a plain report. No tmux is involved: Claude
// reads the files itself.
func runClaudeHook(args []string) error {
	return claudeHook(os.Stdin, os.Stdout, args)
}

func claudeHook(stdin io.Reader, stdout io.Writer, args []string) error {
	fs := flag.NewFlagSet("claude-hook", flag.ExitOnError)
	text := fs.String("text", "", "collect the references in this text and print a plain report (the /otosu command)")
	server := fs.String("server", "", "otosu server URL (default: from config)")
	dir := fs.String("dir", "", "directory for collected files (default: ~/.otosu)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	command := false
	fs.Visit(func(f *flag.Flag) { command = command || f.Name == "text" })
	if command {
		refs := code.FindRefs(*text)
		if len(refs) == 0 {
			fmt.Fprintln(stdout, "otosu: no drop code found. Usage: /otosu <code#key> [what to do with it]")
			return nil
		}
		fmt.Fprint(stdout, claudeReport(refs, *server, *dir))
		return nil
	}

	// From here on any failure stays silent: a hook that errors would get in
	// the way of every prompt, and most prompts have nothing to do with otosu.
	var in struct {
		Event     string `json:"hook_event_name"`
		Prompt    string `json:"prompt"`
		ToolName  string `json:"tool_name"`
		ToolInput struct {
			FilePath string `json:"file_path"`
		} `json:"tool_input"`
	}
	if err := json.NewDecoder(stdin).Decode(&in); err != nil {
		return nil
	}
	if in.Event == "PreToolUse" {
		return approveDropRead(stdout, in.ToolName, in.ToolInput.FilePath, *dir)
	}
	// /otosu:otosu collects its own argument. Pulling here as well would write
	// the file twice and spend one of the drop's few fetches for nothing. A bare
	// "/otosu" is not always resolved to the command and can reach Claude as
	// plain text, so that one is still collected here.
	if strings.HasPrefix(strings.TrimSpace(in.Prompt), "/otosu:") {
		return nil
	}
	refs := code.FindRefs(in.Prompt)
	if len(refs) == 0 {
		return nil
	}
	return writeHookContext(stdout, claudeReport(refs, *server, *dir))
}

// approveDropRead lets Claude read a file otosu collected without stopping to
// ask. The drop directory sits outside any project, so otherwise every pasted
// screenshot would raise a permission prompt. Every other read is left to
// Claude Code's own rules.
func approveDropRead(w io.Writer, tool, path, dirFlag string) error {
	if tool != "Read" || path == "" {
		return nil
	}
	dir, err := dropDir(dirFlag)
	if err != nil || !within(dir, path) {
		return nil
	}
	return json.NewEncoder(w).Encode(map[string]any{
		"hookSpecificOutput": map[string]string{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "allow",
			"permissionDecisionReason": "file collected by otosu",
		},
	})
}

// within reports whether path is a file inside dir once symlinks are resolved,
// so neither "../" nor a link can lead out of it.
func within(dir, path string) bool {
	d, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	p, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(d, p)
	if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// writeHookContext wraps text in the JSON a UserPromptSubmit hook answers with.
func writeHookContext(w io.Writer, text string) error {
	return json.NewEncoder(w).Encode(map[string]any{
		"hookSpecificOutput": map[string]string{
			"hookEventName":     "UserPromptSubmit",
			"additionalContext": text,
		},
	})
}

// claudeReport collects each drop and tells Claude, in its own words, where the
// files landed and which codes failed. It never repeats a key.
func claudeReport(refs []code.Ref, serverFlag, dirFlag string) string {
	url, err := serverURL(serverFlag)
	var target string
	if err == nil {
		target, err = dropDir(dirFlag)
	}
	if err != nil {
		return fmt.Sprintf("otosu could not collect the drop codes in this message: %v. Tell the user.\n", err)
	}

	var got, failed []string
	for _, ref := range refs {
		path, err := client.Pull(url, ref, target, nil)
		if err != nil {
			failed = append(failed, fmt.Sprintf("- %s: %v", ref.Code, err))
			continue
		}
		size := ""
		if info, err := os.Stat(path); err == nil {
			size = " (" + client.HumanBytes(info.Size()) + ")"
		}
		got = append(got, "- "+path+size)
	}

	var b strings.Builder
	if len(got) > 0 {
		b.WriteString("The user attached these files through otosu drop codes. They are decrypted on disk:\n")
		b.WriteString(strings.Join(got, "\n"))
		b.WriteString("\nRead each one with the Read tool before answering, as if it had been attached to this message. " +
			"The code#key strings in the message are spent and need no further action.\n")
	}
	if len(failed) > 0 {
		b.WriteString("otosu could not collect these drop codes. Tell the user, who can upload the file again:\n")
		b.WriteString(strings.Join(failed, "\n"))
		b.WriteString("\n")
	}
	return b.String()
}
