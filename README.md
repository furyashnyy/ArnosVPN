# ArnoVPN

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
  and prints a one-scan `arno://` URI + QR. The Android app scans it and
  connects — no config files, no keys copied by hand.
- **Encrypted end to end.** TLS on the wire, plus an inner ChaCha20-Poly1305
  layer keyed per-connection and per-direction.
- **Certificates from Traefik or Let's Encrypt.** `CERT_PROVIDER=traefik`
  (default) reuses the certificate Coolify/Traefik already obtained;
  `CERT_PROVIDER=letsencrypt` makes ArnoVPN obtain its own.

> Intended for VPN operators running it on their **own** infrastructure (the
> Coolify/Traefik workflow it targets). It is a privacy/self-hosting tool, not a
> tool for evading detection on networks you do not control.

## Repository layout

```
cmd/arnovpn-server/     server entrypoint (self-configuring)
cmd/arnovpnctl/         operator CLI: connect URI, QR, PSK, config dump
internal/protocol/      wire protocol: auth, HKDF, ChaCha20-Poly1305 framing
internal/server/        TUN device, NAT, IP pool, WebSocket tunnel
internal/cert/          TLS providers: traefik (default) + letsencrypt
internal/config/        env-driven config with self-generated PSK
internal/provision/     arno:// connect URI + QR generation
apps/android/           Android VpnService client (Kotlin, Gradle)
apps/android/build/     CI-built release APK lands here
deploy/                 Traefik dynamic config + Coolify guide
docs/PROTOCOL.md        normative wire-protocol spec
Dockerfile              static server image (Alpine + iptables)
docker-compose.yml      Coolify/Traefik-ready deployment
```

## How it works

```
 Android device                          Your server (Coolify host)
 +--------------+                        +------------------------------+
 |   apps       |                        |  arnovpn-server              |
 |    |         |                        |    | TUN (10.66.0.1)         |
 |  VpnService  |   TLS/WSS on :443      |    | NAT / MASQUERADE        |
 |  TUN 10.66.x |=======================>|    v                         |
 |  ChaCha20    |   (looks like HTTPS)   |  eth0  --> internet          |
 +--------------+                        +------------------------------+
        exits from the server's public IP  ^
```

The full packet path and crypto are specified in
[`docs/PROTOCOL.md`](docs/PROTOCOL.md).

## Quick start (server)

With Docker:

```bash
cp .env.example .env         # set ARNO_DOMAIN (and CERT_PROVIDER if not traefik)
docker compose up -d --build
docker compose logs -f       # prints the arno:// connect URI and a QR code
```

Requirements: the container runs with `--cap-add NET_ADMIN`, the
`/dev/net/tun` device, and `net.ipv4.ip_forward=1` (all set in the compose
file).

### Certificate providers

| `CERT_PROVIDER` | Source | Notes |
|-----------------|--------|-------|
| `traefik` (default) | Traefik's `acme.json` or a mounted PEM pair | Reuses Coolify/Traefik certs; renewals are picked up live. |
| `letsencrypt` | ACME TLS-ALPN-01 on `:443` | ArnoVPN obtains and renews its own certificate. Requires `ARNO_DOMAIN` + `ARNO_ACME_EMAIL`. |

See [`deploy/coolify.md`](deploy/coolify.md) for the full Coolify + Traefik
walkthrough (TLS passthrough vs. standalone).

### Configuration reference

| Variable | Default | Meaning |
|----------|---------|---------|
| `ARNO_DOMAIN` | – | Domain clients connect to / certificate host. |
| `CERT_PROVIDER` | `traefik` | `traefik` or `letsencrypt`. |
| `ARNO_TRAEFIK_ACME` | `/traefik/acme.json` | Traefik cert store (traefik provider). |
| `ARNO_TLS_CERT` / `ARNO_TLS_KEY` | – | Mounted PEM pair (alternative to acme.json). |
| `ARNO_ACME_EMAIL` | – | ACME contact (letsencrypt provider). |
| `ARNO_PSK` | generated | Base64 32-byte pre-shared key; auto-generated + persisted if unset. |
| `ARNO_LISTEN` | `:443` | HTTPS listen address. |
| `ARNO_WS_PATH` | `/` | WebSocket upgrade path. |
| `ARNO_TUNNEL_CIDR` | `10.66.0.0/24` | Client address pool. |
| `ARNO_DNS` | `1.1.1.1,1.0.0.1` | DNS pushed to clients. |
| `ARNO_MTU` | `1400` | Tunnel MTU. |
| `ARNO_WAN_IFACE` | auto | Interface to masquerade onto. |

### Operator CLI

```bash
arnovpnctl uri            # print the arno:// connect URI
arnovpnctl qr             # print a scannable QR code
arnovpnctl png out.png    # write the connect QR to a PNG
arnovpnctl genpsk         # generate a fresh base64 pre-shared key
arnovpnctl show           # dump the resolved configuration
```

## Android app

`apps/android` is a complete Kotlin `VpnService` client. Provisioning is one of:

1. **Scan QR** — point the camera at the QR printed by the server.
2. **Deep link** — open an `arno://connect?...` link.
3. **Paste URI** — paste the connect URI.

Then tap **Connect**. The app builds the TUN interface from the server's
`welcome`, routes all traffic through the tunnel, and encrypts every packet with
the same protocol as the server.

The release APK is published at
[`apps/android/build/arnovpn-release.apk`](apps/android/build/) by CI. See that
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
  setting a new `ARNO_PSK` (or deleting the state file) and re-provisioning
  clients.

## License

MIT — see [LICENSE](LICENSE).
