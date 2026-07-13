# syntax=docker/dockerfile:1

# ---- build stage ----------------------------------------------------------
FROM golang:1.24-alpine AS build
WORKDIR /src

# Cache modules first.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Static binaries so the runtime image needs no Go toolchain.
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w" -o /out/arnosvpn-server ./cmd/arnosvpn-server \
 && go build -trimpath -ldflags="-s -w" -o /out/arnosvpnctl ./cmd/arnosvpnctl

# ---- runtime stage --------------------------------------------------------
FROM alpine:3.20
# iptables + iproute2 are required to program NAT and bring up the TUN device.
RUN apk add --no-cache iptables ip6tables iproute2 ca-certificates

COPY --from=build /out/arnosvpn-server /usr/local/bin/arnosvpn-server
COPY --from=build /out/arnosvpnctl /usr/local/bin/arnosvpnctl

# Persistent state (generated PSK, ACME cache) lives here; mount a volume.
VOLUME ["/data"]

# Default internal port (proxy mode). Reverse proxies detect this EXPOSE, so it
# must match ARNOS_LISTEN's default (:8443). For self mode on :443, override
# both ARNOS_LISTEN and the published port.
EXPOSE 8443

# Let the orchestrator know when the tunnel endpoint is actually serving, so it
# doesn't route to a not-yet-ready or crashed container (a common 502 cause).
HEALTHCHECK --interval=15s --timeout=3s --start-period=20s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8443/healthz >/dev/null 2>&1 || exit 1

# The container needs NET_ADMIN and /dev/net/tun at runtime:
#   docker run --cap-add NET_ADMIN --device /dev/net/tun ...
ENTRYPOINT ["/usr/local/bin/arnosvpn-server"]
