# Deploying ArnosVPN on Coolify (with Traefik)

Coolify runs Traefik on ports 80/443 and manages Let's Encrypt certificates for
your domains. ArnosVPN slots in behind it using the normal **expose** workflow,
so it never fights Coolify for port 443 and reuses the certificate Coolify
already has.

## Prerequisites

- A Coolify server with the bundled Traefik proxy (the default).
- A DNS `A` record for your VPN domain (e.g. `vpn.example.com`) pointing at the
  Coolify host.
- The host exposes `/dev/net/tun` (true on virtually all VPS providers).

## Recommended — proxy mode + expose (default)

Port 443 stays with Coolify's Traefik; ArnosVPN listens on an internal port and
Traefik forwards the WebSocket to it. SSL is Coolify's existing certificate.

1. In Coolify, create a **Docker Compose** resource from this repository.
2. Attach your domain (`vpn.example.com`) to the `arnosvpn` service. Coolify
   obtains/renews the certificate automatically.
3. Set the service's **Ports Exposes** to `8443` (the internal port in
   `docker-compose.yml`). This is the "expose" you already use — no host port is
   published, so nothing collides with 443.
4. Set env: `ARNOS_DOMAIN=vpn.example.com` (proxy mode and `ARNOS_PUBLIC_PORT=443`
   are already the defaults).
5. Deploy.

Clients dial `wss://vpn.example.com` (443, Coolify's Traefik), Traefik
terminates TLS and forwards the WebSocket to ArnosVPN on `:8443`. To an
observer it is ordinary HTTPS to your domain.

> Why not a random host port here? With Coolify's expose model the internal port
> just needs to be *known* so Traefik can route to it — 8443 is fine and stable.
> ArnosVPN still logs its bind port at startup, and `ARNOS_LISTEN=auto` will pick
> a free one if you ever need it.

## Alternative — self mode on a random published port, with SSL

If you would rather ArnosVPN terminate TLS itself (keeping its certificate
end-to-end) on a Docker-assigned random host port:

1. In `docker-compose.yml`, uncomment the `ports: ["8443"]` block (the short
   form publishes the container port to a **random host port**) and the
   self-mode env/volume lines.
2. Set env:
   - `ARNOS_TLS_MODE=self`
   - `CERT_PROVIDER=traefik` (reuse Coolify's acme.json — no ACME challenge, so
     any port works), and mount `acme.json` read-only.
   - `ARNOS_PUBLIC_PORT=<the random host port>` — look it up after deploy with
     `docker compose port arnosvpn 8443`, then redeploy so the connect URI is
     correct. (This manual step is why proxy mode is recommended.)
3. Deploy. ArnosVPN presents Coolify's certificate directly on that port.

## Getting a client online

After the first boot, the connect profile is printed in the container logs as a
`arnos://connect?...` URI and a scannable QR code:

```
docker logs <arnosvpn-container> | tail -n 40
```

or regenerate it any time:

```
docker exec <arnosvpn-container> arnosvpnctl qr
docker exec <arnosvpn-container> arnosvpnctl png /data/connect.png
```

Open the Android app, tap **Scan QR**, point it at the code — done. No config
files, no keys copied by hand.

## Required container capabilities

ArnosVPN must create a TUN device and program NAT. The compose file sets these;
if you run it by hand include:

```
--cap-add NET_ADMIN --device /dev/net/tun --sysctl net.ipv4.ip_forward=1
```
