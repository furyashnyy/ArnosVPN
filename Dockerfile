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
RUN go build -trimpath -ldflags="-s -w" -o /out/arnovpn-server ./cmd/arnovpn-server \
 && go build -trimpath -ldflags="-s -w" -o /out/arnovpnctl ./cmd/arnovpnctl

# ---- runtime stage --------------------------------------------------------
FROM alpine:3.20
# iptables + iproute2 are required to program NAT and bring up the TUN device.
RUN apk add --no-cache iptables ip6tables iproute2 ca-certificates

COPY --from=build /out/arnovpn-server /usr/local/bin/arnovpn-server
COPY --from=build /out/arnovpnctl /usr/local/bin/arnovpnctl

# Persistent state (generated PSK, ACME cache) lives here; mount a volume.
VOLUME ["/data"]
EXPOSE 443

# The container needs NET_ADMIN and /dev/net/tun at runtime:
#   docker run --cap-add NET_ADMIN --device /dev/net/tun ...
ENTRYPOINT ["/usr/local/bin/arnovpn-server"]
