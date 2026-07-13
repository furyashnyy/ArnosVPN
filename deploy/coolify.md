# Deploying ArnoVPN on Coolify (with Traefik)

Coolify runs Traefik as its ingress proxy and manages Let's Encrypt
certificates for you. ArnoVPN reuses those certificates so you never configure
TLS by hand.

## Prerequisites

- A Coolify server with the bundled Traefik proxy (the default).
- A DNS `A` record for your VPN domain (e.g. `vpn.example.com`) pointing at the
  Coolify host.
- The host kernel exposes `/dev/net/tun` (true on virtually all VPS providers).

## Option A — TLS passthrough (recommended, default `CERT_PROVIDER=traefik`)

ArnoVPN keeps its own certificate on the wire; Traefik only routes by SNI.

1. In Coolify, create a **Docker Compose** resource from this repository.
2. Set environment variables (Coolify UI or `.env`):
   - `ARNO_DOMAIN=vpn.example.com`
   - `CERT_PROVIDER=traefik`
   - `TRAEFIK_ACME_PATH=/data/coolify/proxy/acme.json` (Coolify's proxy store)
3. In `docker-compose.yml`, comment out the `ports:` block and uncomment the
   `labels:` and `networks:` blocks (the TLS passthrough router).
4. Make sure Traefik actually obtains a certificate for `ARNO_DOMAIN` so it
   lands in `acme.json`. The simplest way is to add the domain to any
   terminating HTTP router once, or list it under your cert resolver's domains.
5. Deploy. ArnoVPN reads the certificate from the mounted `acme.json` and
   presents it to clients.

## Option B — Standalone (`CERT_PROVIDER=letsencrypt`)

ArnoVPN obtains and renews its own certificate; Traefik is not involved.

1. Give ArnoVPN sole ownership of `:443` for its domain (a dedicated host or IP,
   or a `:443` entrypoint Traefik does not use for this domain).
2. Set:
   - `ARNO_DOMAIN=vpn.example.com`
   - `CERT_PROVIDER=letsencrypt`
   - `ARNO_ACME_EMAIL=you@example.com`
3. Keep the `ports: ["443:443"]` block in `docker-compose.yml`.
4. Deploy. ArnoVPN answers the ACME TLS-ALPN-01 challenge inline on `:443`.

## Getting a client online

After the first successful boot, read the connect profile straight from the
container logs — it prints a `arno://connect?...` URI and a scannable QR code:

```
docker logs <arnovpn-container> | tail -n 40
```

or regenerate it any time:

```
docker exec <arnovpn-container> arnovpnctl qr
docker exec <arnovpn-container> arnovpnctl png /data/connect.png
```

Open the Android app, tap **Scan QR**, point it at the code — done. No config
files, no keys to copy by hand.

## Required container capabilities

ArnoVPN must be able to create a TUN device and program NAT. The compose file
already sets these; if you run it by hand include:

```
--cap-add NET_ADMIN --device /dev/net/tun --sysctl net.ipv4.ip_forward=1
```
