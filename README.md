# otosu 落とす

Paste a screenshot into an agent CLI running over SSH.

You cannot paste an image into a terminal. Claude Code, Codex and OpenCode read
the *operating system* clipboard directly, so the moment the agent runs on a
remote box the paste has nothing to read. otosu moves the bytes out of band and
puts only a short code through the terminal, where plain text has always worked.

```
① Win+Shift+S            screenshot to the clipboard
② Ctrl+V on the page     encrypted in the browser, uploaded, code copied
③ prefix + v in tmux     paste the code
④                        the file path appears in the agent's input
```

With the [Claude Code plugin](#claude-code-plugin) step ③ is just pasting the
code into your prompt, no tmux needed.

Everything is encrypted before it leaves the browser. The server stores
ciphertext it cannot read, and dev boxes store no credentials at all.

## Why not the existing tools

[clipaste], [clipssh], [cc-clip] and [claude-ssh-image-skill] all solve this,
and all of them require the machine holding the clipboard to reach the dev box
over SSH. otosu does not: a code works from a phone, from a locked-down work
laptop, from anywhere with a browser. It also handles arbitrary files rather
than images only, and one code works on every dev box you own rather than a
configured host.

[clipaste]: https://github.com/hqhq1025/clipaste
[clipssh]: https://github.com/samuellawrentz/clipssh
[cc-clip]: https://github.com/ShunmeiCho/cc-clip
[claude-ssh-image-skill]: https://github.com/AlexZeitler/claude-ssh-image-skill

## Self-hosting

There is no central service: you run the server, and everything else talks to
yours. In order:

1. **Server**, once. Deploy it under your own domain, passed as `-base-url`. See
   [deploy/README.md](deploy/README.md).
2. **CLI**, on each dev box. Run the install command your server's page shows,
   [below](#install-on-a-dev-box). It points the box at your server.
3. **Claude Code plugin**, optional. The [plugin](#claude-code-plugin) is the
   same for everyone and carries no address: it runs the CLI, which talks to
   whichever server its installer came from. No fork needed.

Install the CLI before the plugin. The other way round works too; the plugin
just does nothing until the CLI is there.

## Install on a dev box

Log in to your otosu page and copy the command it shows:

```sh
curl -fsSL https://otosu.example.com/install | sh
```

The server bakes its own URL into that script, so there is nothing to edit. It
installs the binary to `~/.local/bin`, writes the server address, and registers
the tmux integration. No token, no key, nothing secret ever lands on the box —
which is what makes the command safe to paste anywhere.

Using Claude Code? Add the [plugin](#claude-code-plugin) as well.

The server address lives in `~/.config/otosu/config` as a `server=` line. To
point a box elsewhere, edit that line or run the other server's installer;
`OTOSU_SERVER` or `-server` override it for a single run.

To upgrade, run the install command again. It replaces the CLI and regenerates
the tmux integration, which some fixes need.

The installer always downloads the CLI from this repository's GitHub releases;
only the server address comes from your server. To ship your own build, fork
and change `REPO` in
[internal/server/install.sh.tmpl](internal/server/install.sh.tmpl).

## Usage

In tmux, `prefix + v` prompts for a code. Paste and press enter: a progress bar
runs in the status line and the file path is typed into whichever pane is
running an agent.

Outside tmux, or to script it:

```sh
otosu pull B4K7QW2M#3sK9...        # collect and type into the agent pane
otosu pull B4K7QW2M#3sK9... -N     # just print the path
otosu pull B4K7QW2M#3sK9... -t %5  # target a specific pane
```

The agent is detected automatically and the path is written the way that tool
expects it:

| Tool | Reference |
| --- | --- |
| Claude Code | `@/path/to/file` |
| Codex | `/path/to/file` |
| OpenCode | `file:///path/to/file` |
| Aider | `/add /path/to/file` |
| anything else | `/path/to/file` |

### Claude Code plugin

With the plugin, Claude Code collects drops itself, so tmux is not needed and
it works in any terminal. It drives the `otosu` CLI from the installer above,
v0.3.0 or later; with an older one the plugin quietly does nothing.

Inside Claude Code:

```
/plugin install otosu --marketplace rlongdragon/otosu
```

Then use either form:

```
B4K7QW2M#3sK9... what is wrong in this screenshot?   # a code anywhere in a prompt
/otosu:otosu B4K7QW2M#3sK9... what is wrong here?    # the explicit command
```

Claude reads the file as if it had been attached; several codes in one prompt
are all collected. Behind it, a `UserPromptSubmit` hook runs `otosu claude-hook`
to collect the drops into `~/.otosu`, and a `PreToolUse` hook lets Claude read
files inside that directory without a permission prompt. Every other read keeps
its usual permissions.

To update, rerun the installer for the CLI and `/plugin update otosu` for the
plugin.

Unlike with the tmux binding, the code and key end up in the conversation. A
drop only lives until its short TTL runs out or it has been fetched a few times,
so they are soon useless; if even that matters, use tmux.

## How it works

```
browser                     server                    dev box
───────                     ──────                    ───────
AES-256-GCM seal
  name + mime + bytes
      │
      ├── ciphertext ─────▶ hold in memory
      │                     10 min, 3 fetches
      ◀── code ────────────┘
      │
   CODE#KEY to clipboard
      │
      └─── you paste the code, over SSH, as text ───▶ GET /d/CODE
                                                     decrypt with KEY
                                                     write 0600
                                                     tmux paste-buffer
                                                       into the agent pane
```

Three properties fall out of that shape:

**The server cannot read your drops.** The key is generated in the browser and
travels only in the fragment after `#`, which is never sent to the server. Even
the file name is inside the ciphertext. A compromised server leaks nothing but
sizes and timings.

**Dev boxes hold no secrets.** Uploading needs the login session; collecting
needs only the code. The code *is* the capability, so the client has nothing to
protect and nothing to leak.

**Injection is tool-agnostic.** The path is delivered with `tmux paste-buffer`,
which operates below any TUI. Anything that accepts typed text works, including
tools that do not exist yet. Only the table above is tool-specific.

## Security

- 40 bit codes, live for 10 minutes or 3 fetches, whichever comes first
- Unknown, expired, collected and malformed codes all return an identical 404,
  so the endpoint cannot be used to probe which codes exist
- Per-IP rate limiting in process, tightest on code lookups and login
- Session cookie is `HttpOnly`, `SameSite=Strict`, and `Secure` whenever the
  public URL is https, including behind a TLS terminating proxy
- CSP restricts `connect-src` to `'self'`, so a script on the page cannot ship
  the AES key anywhere
- Drops are held in memory and never written to disk
- Collected files are written `0600`

Behind a reverse proxy, pass `-trusted-proxy` so `X-Forwarded-For` is believed
from that proxy and nowhere else. Without it every request shares one rate limit
bucket and a single prober locks out everyone; trusting the header blindly is
worse, since anyone could then forge an address.

## Development

```sh
go test ./...                                   # 85 tests
otosu serve -addr 127.0.0.1:8099                # dev server, prints a token
```

The browser and the client are separate implementations of the same wire
format, so `TestBrowserInterop` seals a payload with the page's own WebCrypto
module under Node and checks Go can open it. Without that test a format
mismatch would only surface when a real drop failed to decrypt.

WebCrypto is only available in a secure context. Reach a development server
through `ssh -L 8099:127.0.0.1:8099 devbox` and open `http://localhost:8099`,
which counts as secure; a plain HTTP origin will not.

## Licence

MIT
