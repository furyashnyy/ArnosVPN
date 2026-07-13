# ArnosVPN wire protocol

ArnosVPN ("Adaptive Reliable Network Obfuscation") tunnels IP packets inside an
ordinary-looking HTTPS/WebSocket connection. This document is the normative
reference; the Go server (`internal/protocol`) and the Android client
(`apps/android/.../protocol`) both implement it and are pinned together by the
cross-language test vectors in `internal/protocol/vectors_test.go` and
`CryptoTest.kt`.

## Layering

```
+-------------------------------------------------------------+
| IP packets from the TUN device (one per WebSocket frame)    |  L4
+-------------------------------------------------------------+
| ChaCha20-Poly1305 AEAD, per-direction key + counter nonce   |  L3
+-------------------------------------------------------------+
| WebSocket (text = control JSON, binary = data frames)       |  L2
+-------------------------------------------------------------+
| TLS 1.2+ on :443, site certificate (Traefik / Let's Encrypt)|  L1
+-------------------------------------------------------------+
```

To a passive observer this is a browser talking to a web app: a real TLS
handshake, a normal `GET` WebSocket upgrade, then binary frames.

## Handshake

All handshake fields are carried in WebSocket **text** frames as JSON.

### 1. Client → Server: `hello`

```json
{ "type":"hello", "v":1, "salt":"<b64 16B>", "ts":<unix>, "auth":"<b64>", "name":"<label>" }
```

- `salt`: 16 random bytes (standard base64).
- `ts`: client Unix time (seconds). The server rejects a skew greater than
  ±90 s to bound replay.
- `auth`: `base64( HMAC-SHA256(PSK, "arnos-auth-v1" || salt || ascii(ts)) )`.
  Verified in constant time.

### 2. Server → Client: `welcome` (or `error`)

```json
{ "type":"welcome", "salt":"<b64 16B>", "ip":"10.66.0.5", "mask":24,
  "gw":"10.66.0.1", "dns":["1.1.1.1"], "mtu":1400 }
```

The server allocates a tunnel address from its pool and returns everything the
client needs to configure its TUN device. On failure it sends
`{ "type":"error", "msg":"..." }` and closes.

## Key derivation

Both sides compute, with HKDF-SHA256 (RFC 5869):

```
salt = clientSalt || serverSalt
PRK  = HKDF-Extract(salt, PSK)
Kc2s = HKDF-Expand(PRK, "arnos-c2s-v1", 32)   # client -> server
Ks2c = HKDF-Expand(PRK, "arnos-s2c-v1", 32)   # server -> client
```

The client encrypts with `Kc2s` and decrypts with `Ks2c`; the server does the
reverse. Distinct keys per direction mean the two counters never share a
keystream.

## Data frames

Each tunnelled IP packet is one WebSocket **binary** frame:

```
+-----------------------------+------------------------------+
| counter (8 bytes, BE)       | ChaCha20-Poly1305( packet )  |
+-----------------------------+------------------------------+
```

- The nonce is `4 zero bytes || counter(8, big-endian)`.
- `counter` increments per packet, per direction, starting at 0.
- The AEAD output is `ciphertext || 16-byte tag` (no additional data).
- The 8-byte prefix lets the peer reconstruct the nonce without assuming
  in-order delivery.

The server drops any decrypted packet whose IPv4 source address is not the
address it assigned that client (anti-spoofing).

## Keepalive

Either side may send WebSocket `ping` control frames; the transport also
carries JSON `{"type":"ping"}` / `{"type":"pong"}` as an application-level
keepalive. Idle tunnels are kept warm with a ~20–25 s interval.

## Provisioning URI

A complete client profile is a single URI (also rendered as a QR code):

```
arnos://connect?host=<domain>&port=443&path=<ws-path>&sni=<domain>&psk=<b64url>&name=<label>
```

`psk` is base64url without padding. Parsing/encoding lives in
`internal/provision` (server) and `Profile.kt` (client).
