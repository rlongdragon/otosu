package main

import (
	"bytes"
	"flag"
	"reflect"
	"strings"
	"testing"
)

func newPullFlags() (*flag.FlagSet, *string, *bool) {
	fs := flag.NewFlagSet("pull", flag.ContinueOnError)
	pane := fs.String("t", "", "target pane")
	noInject := fs.Bool("N", false, "print only")
	fs.String("server", "", "server URL")
	return fs, pane, noInject
}

// TestPermute covers the ordering the tmux plugin produces, where the code
// comes before the flags. Without permutation the flag package would treat
// "-t" as a second positional argument and the plugin would never work.
func TestPermute(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "flags after positional",
			args: []string{"CODE#KEY", "-t", "%3"},
			want: []string{"-t", "%3", "CODE#KEY"},
		},
		{
			name: "flags before positional",
			args: []string{"-t", "%3", "CODE#KEY"},
			want: []string{"-t", "%3", "CODE#KEY"},
		},
		{
			name: "bool flag does not eat the code",
			args: []string{"CODE#KEY", "-N"},
			want: []string{"-N", "CODE#KEY"},
		},
		{
			name: "equals form",
			args: []string{"CODE#KEY", "-t=%3"},
			want: []string{"-t=%3", "CODE#KEY"},
		},
		{
			name: "mixed",
			args: []string{"-N", "CODE#KEY", "-server", "https://x"},
			want: []string{"-N", "-server", "https://x", "CODE#KEY"},
		},
		{
			name: "double dash stops parsing",
			args: []string{"--", "-looks-like-a-flag"},
			want: []string{"-looks-like-a-flag"},
		},
		{
			name: "only positional",
			args: []string{"CODE#KEY"},
			want: []string{"CODE#KEY"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs, _, _ := newPullFlags()
			if got := permute(fs, tc.args); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("permute(%q) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

// TestPermuteParses checks the permuted arguments actually bind the flags.
func TestPermuteParses(t *testing.T) {
	fs, pane, noInject := newPullFlags()
	if err := fs.Parse(permute(fs, []string{"B4K7QW2M#key", "-t", "%3", "-N"})); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if *pane != "%3" {
		t.Errorf("-t = %q, want %%3", *pane)
	}
	if !*noInject {
		t.Error("-N was not set")
	}
	if fs.NArg() != 1 || fs.Arg(0) != "B4K7QW2M#key" {
		t.Errorf("positional args = %q, want one code", fs.Args())
	}
}

// TestTmuxPluginKeepsInputOutOfRunShell guards the prompt's input from
// run-shell's format expansion. When the reference was spliced straight into
// run-shell, a key starting with D, F, H, I, P, S, T, W or h lost its '#' to
// one of tmux's short aliases, "#W" becoming the window name for instance.
func TestTmuxPluginKeepsInputOutOfRunShell(t *testing.T) {
	var buf bytes.Buffer
	writeTmuxPlugin(&buf, "/usr/local/bin/otosu", "v")
	script := buf.String()

	stashed := strings.Count(script, "set -s @otosu_ref '%%'")
	if all := strings.Count(script, "%%"); stashed == 0 || all != stashed {
		t.Errorf("input substituted %d times, only %d of them into @otosu_ref:\n%s", all, stashed, script)
	}
	// Once for the key binding, once for the :otosu alias.
	if n := strings.Count(script, `pull \"#{@otosu_ref}\"`) + strings.Count(script, `pull \\\"#{@otosu_ref}\\\"`); n != 2 {
		t.Errorf("pull reads #{@otosu_ref} %d times, want 2:\n%s", n, script)
	}
}
