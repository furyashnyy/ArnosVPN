# ArnosVPN

A self-hosted VPN that makes your device browse from your server's IP, over a
connection that looks like ordinary HTTPS. It configures itself, encrypts
everything, and reuses the TLS certificate your reverse proxy already manages.

- **Your IP becomes the server's.** All traffic is routed through a TUN device
  on the server and NATed onto its WAN interface, so your apparent public IP is
  the server's.
- **Looks like normal HTTPS.** The tunnel is a real TLS connection on `:443`
  carrying a real WebSocket upgrade. To a passive observer it is a browser
  talking to a web app behind Traefik.
- **Zero config.** The server generates and persists its own key on first run
  and prints a one-scan `arnos://` URI + QR. The Android app scans it and
  connects — no config files, no keys copied by hand.
- **Encrypted end to end.** TLS on the wire, plus an inner ChaCha20-Poly1305
  layer keyed per-connection and per-direction.
- **Fits Coolify without fighting for port 443.** Default **proxy mode** lets
  Coolify's Traefik keep 443 and terminate TLS (the certificate it already has
  for your domain); ArnosVPN listens on an internal port you `expose`. Optional
  **self mode** makes ArnosVPN terminate TLS itself (Traefik `acme.json` or
  Let's Encrypt), e.g. on a random published port.

> Private, single-user, self-hosted. The compose file builds locally and is
> never pushed to a registry. It is a privacy/self-hosting tool for your **own**
> infrastructure, not a tool for evading detection on networks you don't control.

## Repository layout

```
cmd/arnosvpn-server/     server entrypoint (self-configuring)
cmd/arnosvpn-client/     desktop client (Windows/Linux): TUN + local proxy
cmd/arnosvpnctl/         operator CLI: connect URI, QR, PSK, config dump
internal/protocol/      wire protocol: auth, HKDF, padded ChaCha20-Poly1305
internal/client/        desktop tunnel, fingerprint, multi-server, SOCKS/HTTP
internal/server/        TUN device, NAT, IP pool, WebSocket tunnel
internal/cert/          TLS providers: traefik (default) + letsencrypt
internal/config/        env-driven config with self-generated PSK
internal/provision/     arnos:// connect URI + QR generation
apps/android/           Android VpnService client (Kotlin, Gradle)
apps/android/build/     CI-built release APK lands here
deploy/                 plain-Docker + Coolify guides, Traefik dynamic config
docs/PROTOCOL.md        normative wire-protocol spec
setup.sh                interactive bare-metal installer (systemd, self-TLS)
Dockerfile              static server build (Alpine + iptables)
docker-compose.yml      standalone compose (self-TLS on 443, Let's Encrypt)
docker-compose-coolify.yaml  behind Coolify/Traefik (proxy mode, built locally)
```

## How it works

```
 Android device                Coolify host
 +--------------+     :443     +-----------------------------------------+
 |   apps       |   TLS/WSS    |  Traefik  --WS-->  arnosvpn-server      |
 |    |         |  (HTTPS to   |  (proxy mode)       | TUN (10.66.0.1)   |
 |  VpnService  |===your domain=>  terminates TLS    | NAT / MASQUERADE  |
 |  TUN 10.66.x |               |                     v                  |
 |  ChaCha20    |               |             eth0 --> internet          |
 +--------------+               +-----------------------------------------+
        exits from the server's public IP  ^
```

In **proxy mode** (default) Coolify's Traefik owns 443 and forwards the
WebSocket to ArnosVPN on an internal port. In **self mode** ArnosVPN terminates
TLS itself on its own port. Either way the wire is ordinary HTTPS to your domain.

The full packet path and crypto are specified in
[`docs/PROTOCOL.md`](docs/PROTOCOL.md).

## Quick start (server)

Pick the deployment that fits. All of them self-configure: the server generates
and persists its pre-shared key on first run and prints an `arnos://` connect URI
and a QR code you scan into the app.

Requirements are the same everywhere: `NET_ADMIN`, the `/dev/net/tun` device, and
`net.ipv4.ip_forward=1` (the installer and the compose/run commands set these).

### 1. Bare metal — `setup.sh` (default)

An interactive question-and-answer wizard for a plain VPS: it asks for the
domain, e-mail, port and DNS, then builds the server, installs a systemd
service, and terminates TLS itself (default **:443**) with a Let's Encrypt
certificate for your domain.

```bash
git clone https://…/ArnosVPN && cd ArnosVPN
sudo ./setup.sh
# or pre-fill answers: sudo ./setup.sh vpn.example.com you@example.com
# manage:    systemctl status|restart arnosvpn · journalctl -u arnosvpn -f
# uninstall: sudo ./setup.sh uninstall   (asks you to type I UNDERSTAND + the path)
```

Point your domain's DNS at the host and make sure the chosen port is free and
open.

### 2. Docker

Build the image and run it standalone (self-TLS on 443, Let's Encrypt). See
[`deploy/docker.md`](deploy/docker.md) for the full command; in short:

```bash
docker build -t arnosvpn .
docker run -d --name arnosvpn --restart unless-stopped \
  --cap-add NET_ADMIN --device /dev/net/tun --sysctl net.ipv4.ip_forward=1 \
  -p 443:443 -e ARNOS_DOMAIN=vpn.example.com -e ARNOS_TLS_MODE=self \
  -e CERT_PROVIDER=letsencrypt -e ARNOS_ACME_EMAIL=you@example.com \
  -e ARNOS_LISTEN=:443 -e ARNOS_PUBLIC_PORT=443 -v arnos-data:/data arnosvpn
docker logs -f arnosvpn        # prints the arnos:// connect URI and a QR code
```

### 3. Docker Compose

Standalone compose (self-TLS on 443):

```bash
cp .env.example .env         # set ARNOS_DOMAIN (+ ARNOS_ACME_EMAIL)
docker compose up -d --build
docker compose logs -f       # prints the arnos:// connect URI and a QR code
```

Behind **Coolify** / an existing Traefik instead, use the proxy-mode compose and
attach your domain to the service with **Ports Exposes** = `8443`:

```bash
docker compose -f docker-compose-coolify.yaml up -d --build
```

See [`deploy/coolify.md`](deploy/coolify.md) for the full Coolify walkthrough
(proxy mode vs. self mode on a random published port).

### TLS modes

| `ARNOS_TLS_MODE` | Who terminates TLS | When to use |
|------------------|--------------------|-------------|
| `proxy` (default) | Coolify's Traefik, on 443, using its existing cert | 443 is already taken; you route by domain (`expose`). ArnosVPN speaks plain WS internally. |
| `self` | ArnosVPN, using `CERT_PROVIDER` | Standalone / random published port. Cert from Traefik `acme.json` (`traefik`) or Let's Encrypt (`letsencrypt`). |

### Configuration reference

| Variable | Default | Meaning |
|----------|---------|---------|
| `ARNOS_DOMAIN` | – | Public domain clients connect to. |
| `ARNOS_TLS_MODE` | `proxy` | `proxy` (upstream Traefik terminates TLS) or `self`. |
| `ARNOS_LISTEN` | `:8443` | Internal bind port. Must match the port the proxy routes to (Coolify *Ports Exposes* / Dockerfile `EXPOSE`). `auto`/`:0` picks a random free port — only for non-proxied setups. |
| `ARNOS_PUBLIC_HOST` | `ARNOS_DOMAIN` | Host clients dial (in the connect URI). |
| `ARNOS_PUBLIC_PORT` | `443` | Port clients dial (in the connect URI). |
| `ARNOS_PSK` | generated | Base64 32-byte pre-shared key; auto-generated + persisted if unset. |
| `ARNOS_WS_PATH` | `/` | WebSocket upgrade path. |
| `ARNOS_TUNNEL_CIDR` | `10.66.0.0/24` | Client address pool. |
| `ARNOS_DNS` | `1.1.1.1,1.0.0.1` | DNS pushed to clients. |
| `ARNOS_MTU` | `1400` | Tunnel MTU. |
| `ARNOS_WAN_IFACE` | auto | Interface to masquerade onto. |
| `CERT_PROVIDER` | `traefik` | *(self mode)* `traefik` or `letsencrypt`. |
| `ARNOS_TRAEFIK_ACME` | `/traefik/acme.json` | *(self mode)* Traefik cert store. |
| `ARNOS_TLS_CERT` / `ARNOS_TLS_KEY` | – | *(self mode)* Mounted PEM pair. |
| `ARNOS_ACME_EMAIL` | – | *(self + letsencrypt)* ACME contact. |

### Operator CLI

```bash
arnosvpnctl uri            # print the arnos:// connect URI
arnosvpnctl qr             # print a scannable QR code
arnosvpnctl png out.png    # write the connect QR to a PNG
arnosvpnctl genpsk         # generate a fresh base64 pre-shared key
arnosvpnctl show           # dump the resolved configuration
```

## Android app

`apps/android` is a complete Kotlin `VpnService` client. Provisioning is one of:

1. **Scan QR** — point the camera at the QR printed by the server.
2. **Deep link** — open an `arnos://connect?...` link.
3. **Paste URI** — paste the connect URI.

Then tap **Connect**. The app builds the TUN interface from the server's
`welcome`, routes all traffic through the tunnel, and encrypts every packet with
the same protocol as the server.

The app manages **multiple servers** (tap **Servers** to view, switch, or
remove) and provisions each by QR/URI. The release APK is published at
[`apps/android/build/arnosvpn-release.apk`](apps/android/build/) by CI.

## Desktop client (Windows / Linux)

`cmd/arnosvpn-client` is a cross-platform desktop client. It manages multiple
servers and connects in one of two modes:

| Mode | Flag | What it does |
|------|------|--------------|
| **Proxy** (default) | `--mode proxy` | Runs the tunnel in userspace and exposes a local **SOCKS5** (`127.0.0.1:1080`) and **HTTP** (`127.0.0.1:8080`) proxy. No admin rights, no drivers — point your system/browser proxy at it. |
| **TUN** | `--mode tun` | Creates a system-wide TUN adapter (wintun on Windows) and routes **all** traffic through the server. Requires Administrator/root. |

```bash
arnosvpn-client gui                     # graphical control panel in your browser
arnosvpn-client add "arnos://connect?host=…&psk=…" home   # save a server
arnosvpn-client list                                       # view your servers
arnosvpn-client use home                                   # pick the active one
arnosvpn-client connect                 # proxy mode on 127.0.0.1:1080 / :8080
arnosvpn-client connect --mode tun      # system-wide (admin/root)
```

`arnosvpn-client gui` opens the full **graphical app** (a local web app on
`127.0.0.1`) — the easiest way to use the desktop client. It has a sidebar
(Servers, Settings, Statistics, Logs, About), a big connect button, a
Proxy/TUN switch, per-server ping, an Add dialog (single `arnos://` config or a
subscription URL), live traffic stats, a log viewer, and light/dark themes —
all custom-styled. The CLI subcommands do the same headlessly.

Prebuilt apps (APK + Windows/Linux) are attached to each
[GitHub Release](../../releases); see [RELEASING.md](RELEASING.md).

Every connection uses a fresh fingerprint (random path, rotating User-Agent,
random per-frame padding), so no two sessions look alike on the wire.

Binaries for `windows/amd64` and `linux/amd64` are built by the `desktop` CI
workflow. TUN mode on Windows needs `wintun.dll` next to the exe (bundled in the
Windows artifact); proxy mode needs nothing extra.

## Troubleshooting

**`Expected HTTP 101 response but was '403 Forbidden'`** — the WebSocket
upgrade is being blocked *before* it reaches ArnosVPN, almost always by a CDN/WAF
in front of the domain (common on `cdn.*` hosts behind Cloudflare):

- The client already sends a full browser-like header set (`Origin`, a Chrome
  `User-Agent`, `Sec-Fetch-*`), which clears most WAFs.
- If it persists on **Cloudflare**: turn off *Bot Fight Mode* (Security → Bots)
  for the domain, or add a WAF skip rule for the VPN path, or use a **DNS-only
  (grey-cloud)** record for the VPN subdomain so traffic goes straight to your
  origin. Cloudflare proxying does support WebSockets, but its bot rules can 403
  a non-browser client.

Diagnose *where* the block is: ArnosVPN logs one line per request and serves an
unauthenticated health endpoint. From your machine:

```bash
curl -sSI https://<your-domain>/healthz    # expect: HTTP/2 200, body "ok"
```

If that 403s too, the proxy/CDN is blocking everything before ArnosVPN. If it
returns 200 but the tunnel still 403s, check the container logs — every request
is logged (`request GET / ws=true ...`); no line means the upgrade never
reached ArnosVPN.

**`404`** — the domain isn't routing to ArnosVPN. On Coolify, attach the domain
to the service and set *Ports Exposes* to the internal port (`8443`).

**`502/503/504`** — the proxy has a route but can't reach the container. Almost
always a **port mismatch** or a **crashed container**:

- Make the proxy target port, the Dockerfile `EXPOSE`, and `ARNOS_LISTEN` all
  agree. They all default to **`8443`** now; in Coolify set *Ports Exposes* to
  `8443` and leave `ARNOS_LISTEN` unset (or `:8443`).
- If the container is restarting, check its logs — it needs `NET_ADMIN` and
  `/dev/net/tun`; without them TUN/NAT setup fails and it exits. The Docker
  `HEALTHCHECK` hits `/healthz` so an unhealthy container is visible.

**Server crash-loops on `/proc/sys/net/ipv4/ip_forward: read-only file system`**
— enable forwarding via the container sysctl (`--sysctl net.ipv4.ip_forward=1`,
already in `docker-compose.yml`) or on the host. Recent builds detect an
already-enabled value and don't fail.

## Development

```bash
go test ./...          # protocol + cross-language vectors
go vet ./...
go build ./...

cd apps/android && ./gradlew :app:testDebugUnitTest   # same vectors, Kotlin side
```

The Go and Kotlin implementations are locked together by identical pinned wire
vectors, so a change to one that breaks interop fails CI on both sides.

## Security notes

- Authentication is a PSK-keyed HMAC with a ±90 s timestamp window; the server
  also remembers accepted handshakes in that window so a captured `hello` cannot
  be replayed. The inner AEAD is keyed per connection and per direction.
- Data frames carry a per-direction counter; the receiver rejects any frame
  whose counter does not strictly advance (anti-replay), after the AEAD tag
  verifies.
- The server only forwards client packets whose source is the address it
  assigned (anti-spoofing) and NATs everything else.
- Inbound WebSocket messages are size-bounded on both the server and the desktop
  client so a peer cannot force an oversized allocation.
- Subscription fetches accept only `http(s)` URLs and refuse to connect to
  loopback/private addresses (SSRF hardening).
- Treat the PSK as a shared secret: anyone holding it can connect. Rotate by
  setting a new `ARNOS_PSK` (or deleting the state file) and re-provisioning
  clients.

## License

MIT — see [LICENSE](LICENSE).
