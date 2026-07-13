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
cmd/arnosvpnctl/         operator CLI: connect URI, QR, PSK, config dump
internal/protocol/      wire protocol: auth, HKDF, ChaCha20-Poly1305 framing
internal/server/        TUN device, NAT, IP pool, WebSocket tunnel
internal/cert/          TLS providers: traefik (default) + letsencrypt
internal/config/        env-driven config with self-generated PSK
internal/provision/     arnos:// connect URI + QR generation
apps/android/           Android VpnService client (Kotlin, Gradle)
apps/android/build/     CI-built release APK lands here
deploy/                 Traefik dynamic config + Coolify guide
docs/PROTOCOL.md        normative wire-protocol spec
Dockerfile              static server build (Alpine + iptables)
docker-compose.yml      Coolify/Traefik-ready deployment (built locally)
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

With Docker:

```bash
cp .env.example .env         # set ARNOS_DOMAIN
docker compose up -d --build
docker compose logs -f       # prints the arnos:// connect URI and a QR code
```

On Coolify: attach your domain to the service and set its **Ports Exposes** to
`8443` — that's it. See [`deploy/coolify.md`](deploy/coolify.md) for the full
walkthrough (proxy mode vs. self mode on a random published port).

Requirements: the container runs with `--cap-add NET_ADMIN`, the
`/dev/net/tun` device, and `net.ipv4.ip_forward=1` (all set in the compose
file).

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
| `ARNOS_LISTEN` | `auto` | Internal bind. `auto`/`:0` picks a free port (logged at startup); or e.g. `:8443`. |
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

The release APK is published at
[`apps/android/build/arnosvpn-release.apk`](apps/android/build/) by CI. See that
directory's README for why it is built in CI rather than committed by hand, and
how to build it locally.

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

- Authentication is a PSK-keyed HMAC with a ±90 s timestamp window; the inner
  AEAD is keyed per connection and per direction.
- The server only forwards client packets whose source is the address it
  assigned (anti-spoofing) and NATs everything else.
- Treat the PSK as a shared secret: anyone holding it can connect. Rotate by
  setting a new `ARNOS_PSK` (or deleting the state file) and re-provisioning
  clients.

## License

MIT — see [LICENSE](LICENSE).
