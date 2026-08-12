// Command otosu is both the drop server and the client that collects drops on a
// dev box.
//
//	otosu serve -domain otosu.example.com     run the server
//	otosu pull B4K7QW2M#<key>                 collect a drop and hand it to the agent
//	otosu tmux-plugin                         print the tmux integration script
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rlongdragon/otosu/internal/client"
	"github.com/rlongdragon/otosu/internal/code"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "serve":
		err = runServe(os.Args[2:])
	case "pull":
		err = runPull(os.Args[2:])
	case "tmux-plugin":
		err = runTmuxPlugin(os.Args[2:])
	case "version", "-v", "--version":
		fmt.Println("otosu", version)
	case "help", "-h", "--help":
		usage()
	default:
		usage()
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "otosu:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `otosu - paste a screenshot or file into a remote agent CLI

Usage:
  otosu serve [flags]        run the drop server
  otosu pull <code#key>      collect a drop and type its path into the agent
  otosu tmux-plugin          print the tmux integration script
  otosu version

Run "otosu <command> -h" for the flags of a command.
`)
}

// permute moves flags ahead of positional arguments.
//
// The flag package stops parsing at the first non-flag argument, which would
// break the natural "otosu pull <code> -t <pane>" ordering the tmux plugin
// generates.
func permute(fs *flag.FlagSet, args []string) []string {
	var flags, positional []string

	for i := 0; i < len(args); i++ {
		arg := args[i]

		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if len(arg) < 2 || arg[0] != '-' {
			positional = append(positional, arg)
			continue
		}

		flags = append(flags, arg)
		name, hasValue := strings.CutPrefix(arg, "-")
		name = strings.TrimPrefix(name, "-")
		if name, _, hasValue = strings.Cut(name, "="); hasValue {
			continue
		}
		// A non-boolean flag consumes the next argument as its value.
		if f := fs.Lookup(name); f != nil && !isBoolFlag(f) && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return append(flags, positional...)
}

func isBoolFlag(f *flag.Flag) bool {
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}

// ------------------------------------------------------------------- config ---

// configPath returns the location of the file the installer writes.
func configPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "otosu", "config")
}

// serverURL resolves the server address from, in order: the flag, the
// environment, then the config file the installer wrote.
func serverURL(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if v := os.Getenv("OTOSU_SERVER"); v != "" {
		return v, nil
	}

	path := configPath()
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("no server configured: pass -server, set OTOSU_SERVER, or run the installer")
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, value, ok := strings.Cut(sc.Text(), "=")
		if ok && strings.TrimSpace(key) == "server" {
			if v := strings.TrimSpace(value); v != "" {
				return v, nil
			}
		}
	}
	return "", fmt.Errorf("no server= line in %s", path)
}

func dropDir(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if v := os.Getenv("OTOSU_DIR"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	return filepath.Join(home, ".otosu"), nil
}

// --------------------------------------------------------------------- pull ---

func runPull(args []string) error {
	fs := flag.NewFlagSet("pull", flag.ExitOnError)
	server := fs.String("server", "", "otosu server URL (default: from config)")
	dir := fs.String("dir", "", "directory for collected files (default: ~/.otosu)")
	pane := fs.String("t", "", "target tmux pane (default: auto-detect the agent pane)")
	noInject := fs.Bool("N", false, "just print the path, do not type it into a pane")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "Usage: otosu pull [flags] <code#key>\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(permute(fs, args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("expected exactly one code")
	}

	ref, err := code.ParseRef(fs.Arg(0))
	if err != nil {
		return err
	}
	url, err := serverURL(*server)
	if err != nil {
		return err
	}
	target, err := dropDir(*dir)
	if err != nil {
		return err
	}

	// Draw a progress bar in the tmux status line, but only when the result is
	// going into a pane: with -N the caller wants a plain path on stdout.
	var report client.Progress
	inTmux := client.InTmux()
	if inTmux && !*noInject {
		client.Status("otosu  fetching…", 4*time.Second)
		report = client.NewStatusProgress()
	}

	path, err := client.Pull(url, ref, target, report)
	if err != nil {
		if report != nil {
			client.Status("otosu  ✗ "+err.Error(), 5*time.Second)
		}
		return err
	}

	if *noInject {
		fmt.Println(path)
		return nil
	}

	if err := inject(*pane, path); err != nil {
		if inTmux {
			client.Status("otosu  ✗ "+err.Error(), 5*time.Second)
		}
		return err
	}

	if inTmux {
		info, statErr := os.Stat(path)
		size := ""
		if statErr == nil {
			size = " (" + client.HumanBytes(info.Size()) + ")"
		}
		// Stays briefly, then clears itself.
		client.Status("otosu  ✓ download done!  "+filepath.Base(path)+size, 2500*time.Millisecond)
	}
	return nil
}

// inject types the path into the agent pane, falling back to printing it when
// there is no tmux to work with.
func inject(pane, path string) error {
	if !client.InTmux() {
		fmt.Println(path)
		return nil
	}

	var tool client.Tool
	if pane == "" {
		found, err := client.FindPane()
		if err != nil {
			// Not a failure: the file is on disk, the user can still use it.
			fmt.Println(path)
			if errors.Is(err, client.ErrNoPane) {
				fmt.Fprintln(os.Stderr, "otosu: no agent pane found, printed the path instead")
				return nil
			}
			return err
		}
		pane, tool = found.ID, found.Tool
	} else {
		t, err := client.ToolForPane(pane)
		if err != nil {
			return err
		}
		tool = t
	}

	if err := client.Inject(pane, tool, path); err != nil {
		fmt.Println(path)
		return err
	}

	name := string(tool)
	if name == "" {
		name = "generic"
	}
	fmt.Fprintf(os.Stderr, "otosu: %s -> %s (%s)\n", path, pane, name)
	return nil
}

// -------------------------------------------------------------- tmux plugin ---

func runTmuxPlugin(args []string) error {
	fs := flag.NewFlagSet("tmux-plugin", flag.ExitOnError)
	// "v" is unbound in default tmux, and unlike Ctrl-based keys it is not
	// swallowed by terminal emulators before tmux ever sees it. Windows Terminal
	// binds Ctrl+V to paste, for instance.
	key := fs.String("key", "v", "prefix key that prompts for a code")
	if err := fs.Parse(args); err != nil {
		return err
	}

	bin, err := os.Executable()
	if err != nil {
		bin = "otosu"
	}

	// %% is where tmux substitutes what the user typed at the prompt.
	fmt.Printf(`#!/bin/sh
# otosu tmux integration, generated by "otosu tmux-plugin".

BIN=%q

# prefix + %s  ->  prompt for a code, then collect it into the agent pane.
tmux bind-key %s command-prompt -p "otosu code:" \
    "run-shell -b '$BIN pull \"%%%%\" -t \"#{pane_id}\"'"

# Also usable as a tmux command:  :otosu <code>
tmux set -s command-alias[100] \
    "otosu=command-prompt -p 'otosu code:' 'run-shell -b \"$BIN pull \\\"%%%%\\\" -t \\\"#{pane_id}\\\"\"'"
`, bin, *key, *key)
	return nil
}

// -------------------------------------------------------------------- serve ---

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	domain := fs.String("domain", "", "public hostname; enables automatic HTTPS via Let's Encrypt")
	addr := fs.String("addr", "", "plain HTTP listen address for local development, e.g. 127.0.0.1:8080")
	baseURL := fs.String("base-url", "", "public URL baked into the installer (default: derived from -domain or -addr)")
	certDir := fs.String("cert-dir", "", "certificate cache directory (default: ~/.local/share/otosu/certs)")
	tokenFlag := fs.String("token", "", "login token (default: $OTOSU_TOKEN, or generated and printed)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if (*domain == "") == (*addr == "") {
		return errors.New("pass exactly one of -domain (production) or -addr (development)")
	}

	token := *tokenFlag
	if token == "" {
		token = os.Getenv("OTOSU_TOKEN")
	}
	generated := token == ""
	if generated {
		t, err := newToken()
		if err != nil {
			return err
		}
		token = t
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg := serveConfig{
		domain:    *domain,
		addr:      *addr,
		baseURL:   *baseURL,
		certDir:   *certDir,
		token:     token,
		generated: generated,
		log:       log,
	}
	return serve(cfg)
}

// redirectToHTTPS sends plain HTTP callers to the TLS endpoint. autocert's own
// handler intercepts the ACME challenge path before this runs.
func redirectToHTTPS(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "https://"+r.Host+r.URL.RequestURI(), http.StatusMovedPermanently)
}
