# Deploy with plain Docker

For a standalone VPS where nothing else owns port 443. ArnosVPN terminates TLS
itself and gets its own Let's Encrypt certificate for your domain.

Prerequisites: DNS for `vpn.example.com` points at this host, port 443 is free
and reachable, and `/dev/net/tun` is available (load it with `modprobe tun`).

```bash
# 1. Build the image (private — never pushed to a registry).
docker build -t arnosvpn .

# 2. Run it. NET_ADMIN + /dev/net/tun + ip_forward are required for the tunnel.
docker run -d --name arnosvpn --restart unless-stopped \
  --cap-add NET_ADMIN \
  --device /dev/net/tun \
  --sysctl net.ipv4.ip_forward=1 \
  -p 443:443 \
  -e ARNOS_DOMAIN=vpn.example.com \
  -e ARNOS_TLS_MODE=self \
  -e CERT_PROVIDER=letsencrypt \
  -e ARNOS_ACME_EMAIL=you@example.com \
  -e ARNOS_LISTEN=:443 \
  -e ARNOS_PUBLIC_PORT=443 \
  -v arnos-data:/data \
  arnosvpn

# 3. Read the connect URI + QR from the logs.
docker logs -f arnosvpn
```

The server generates and persists a pre-shared key in the `arnos-data` volume on
first run, so restarts keep the same client profile. Pin your own with
`-e ARNOS_PSK=$(docker run --rm arnosvpn arnosvpnctl genpsk)`.

The image's built-in health check targets the proxy-mode port (8443); in this
self-TLS-on-443 setup Docker will show the container as "unhealthy" even though
it works. That is cosmetic — disable it with `--no-healthcheck` on `docker run`
if it bothers you, or use `docker-compose.yml`, which already disables it.

Reissue / inspect the profile any time:

```bash
docker exec arnosvpn arnosvpnctl uri
docker exec arnosvpn arnosvpnctl qr
```

Behind Coolify or another Traefik proxy instead of standalone? See
[`coolify.md`](coolify.md) and use `docker-compose-coolify.yaml`.
