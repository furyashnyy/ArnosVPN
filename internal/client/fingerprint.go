package client

import (
	"crypto/rand"
	"encoding/hex"
	"math/big"
)

// The connection fingerprint is randomized every time a tunnel is opened so no
// two connections look alike on the wire:
//
//   - a rotating, realistic browser User-Agent,
//   - a random request path (the server accepts the WebSocket upgrade on any
//     path), and
//   - random per-frame padding (in the protocol layer).
//
// Combined with TLS (real cert, real SNI) this makes each session an
// unrepeatable, browser-shaped HTTPS flow.

var userAgents = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36 Edg/124.0.0.0",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Safari/605.1.15",
	"Mozilla/5.0 (X11; Linux x86_64; rv:126.0) Gecko/20100101 Firefox/126.0",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:125.0) Gecko/20100101 Firefox/125.0",
}

// randomUserAgent returns a browser UA, chosen uniformly at random.
func randomUserAgent() string {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(userAgents))))
	if err != nil {
		return userAgents[0]
	}
	return userAgents[n.Int64()]
}

// randomPath returns a plausible, unique request path. The server accepts the
// tunnel upgrade on any path, so each connection can use a fresh one.
func randomPath() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "/"
	}
	// Shapes like "/assets/3f9c1a…" — indistinguishable from a static asset.
	prefixes := []string{"/assets/", "/static/", "/cdn/", "/img/", "/v1/ws/", "/live/"}
	pn, _ := rand.Int(rand.Reader, big.NewInt(int64(len(prefixes))))
	return prefixes[pn.Int64()] + hex.EncodeToString(buf)
}
