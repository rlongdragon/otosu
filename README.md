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

## Install on a dev box

Log in to your otosu page and copy the command it shows:

```sh
curl -fsSL https://otosu.example.com/install | sh
```

The server bakes its own URL into that script, so there is nothing to edit. It
installs the binary to `~/.local/bin`, writes the server address, and registers
the tmux integration. No token, no key, nothing secret ever lands on the box —
which is what makes the command safe to paste anywhere.

To deploy the server itself, see [deploy/README.md](deploy/README.md).

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
