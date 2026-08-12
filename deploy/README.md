# Deploying otosu

The server is one static binary with no runtime dependencies. It can sit behind
an existing reverse proxy, or terminate TLS itself with no proxy at all.

## Two ways to run it

**Behind an existing reverse proxy** (nginx, Caddy, Nginx Proxy Manager). The
shipped unit file assumes this: otosu listens on `127.0.0.1:8099` and the proxy
terminates TLS. Pass the public origin as `-base-url` so the session cookie is
marked `Secure` and the installer advertises the right address, and pass
`-trusted-proxy` so rate limiting sees real client addresses instead of the
proxy.

**Terminating TLS in otosu itself**, with no proxy at all. Swap the `ExecStart`
flags for `-domain <host> -cert-dir /var/cache/otosu/certs` and restore
`AmbientCapabilities=CAP_NET_BIND_SERVICE` and `CacheDirectory=otosu` in the
unit. This needs ports 80 and 443 free: Let's Encrypt will not issue a
certificate for a bare IP, and port 80 must be reachable for the ACME
challenge even though real traffic is on 443.

Either way you need a DNS `A` record pointing at the machine.

## Install

```sh
# 1. Binary
VERSION=v0.1.0
curl -fsSL "https://github.com/rlongdragon/otosu/releases/download/${VERSION}/otosu_linux_amd64.tar.gz" \
  | sudo tar -xz -C /usr/local/bin otosu
sudo chmod 0755 /usr/local/bin/otosu

# 2. Unprivileged service account
sudo useradd --system --no-create-home --shell /usr/sbin/nologin otosu

# 3. Login token
sudo mkdir -p /etc/otosu
sudo sh -c 'printf "OTOSU_TOKEN=%s\n" "$(head -c 24 /dev/urandom | base64 | tr "+/" "-_" | tr -d "=")" > /etc/otosu/env'
sudo chmod 0600 /etc/otosu/env
sudo cat /etc/otosu/env          # copy the token into the browser once

# 4. Service
sudo cp deploy/otosu.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now otosu
```

Edit the `ExecStart` flags if your hostname or port differ from the defaults.

## Verify

```sh
systemctl status otosu
journalctl -u otosu -f
curl -fsS http://127.0.0.1:8099/healthz          # the service itself
curl -fsS https://otosu.rlongdragon.com/healthz  # through the proxy
```

`systemd-analyze security otosu` should report an exposure level around 1.5.

### Nginx Proxy Manager

Add a proxy host with:

| Field | Value |
| --- | --- |
| Domain Names | `otosu.rlongdragon.com` |
| Scheme | `http` |
| Forward Hostname / IP | `127.0.0.1` |
| Forward Port | `8099` |
| Block Common Exploits | on |
| Websockets Support | off |

On the SSL tab request a new certificate and enable Force SSL and HTTP/2.

In the Advanced tab, raise the upload limit. NPM defaults to nginx's 1 MB body
limit, which rejects every screenshot larger than that with a 413 before the
request ever reaches otosu:

```nginx
client_max_body_size 64m;
```

`127.0.0.1` works because the NPM container runs with host networking. On a
bridge network use the gateway address, `172.17.0.1` for the default bridge,
and change `-addr` to match since loopback would not be reachable.

## Why the unit is locked down

The service is reachable from the public internet and shares a machine with
development work, so a compromise must not become a foothold. `ProtectHome`
hides every home directory including SSH keys, `ProtectSystem=strict` makes the
whole filesystem read-only, and the capability set is emptied entirely: behind a
proxy the listener is an unprivileged port, so the process needs no capability
at all.

Drops are held in memory and never written to disk, so there is no state
directory to protect and nothing to clean up after a restart. That is also why
`ProtectSystem=strict` needs no `ReadWritePaths` exception. Terminating TLS in
otosu instead is the one case that does: the certificate cache must be
writable, and binding 443 needs `CAP_NET_BIND_SERVICE` back.

## Optional: fail2ban

The in-process rate limiter is the real defence, because it reacts immediately
and a drop code only stays live for minutes. fail2ban is a useful second layer
that moves repeat offenders to the firewall so they stop consuming resources.

`/etc/fail2ban/filter.d/otosu.conf`:

```ini
[Definition]
failregex = ^.*msg="drop miss" ip=<HOST>.*$
            ^.*msg="login failed" ip=<HOST>.*$
# Requires -trusted-proxy, otherwise every line shows the proxy address and the
# jail would ban the proxy itself, taking the whole site down.
ignoreregex =
```

`/etc/fail2ban/jail.d/otosu.conf`:

```ini
[otosu]
enabled  = true
port     = http,https
backend  = systemd
journalmatch = _SYSTEMD_UNIT=otosu.service
filter   = otosu
findtime = 120
maxretry = 20
bantime  = 3600
```

## Upgrading

```sh
sudo systemctl stop otosu
# install the new binary as above
sudo systemctl start otosu
```

Live drops are lost on restart by design. They expire within minutes anyway.
