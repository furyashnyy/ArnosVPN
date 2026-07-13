// Package protocol implements the ArnoVPN wire protocol.
//
// ArnoVPN ("Adaptive Reliable Network Obfuscation") tunnels IP packets inside
// an ordinary-looking HTTPS/WebSocket connection on port 443. To a passive
// observer the traffic is indistinguishable from a browser talking to a web
// app behind Traefik: a real TLS handshake, a real WebSocket upgrade, and
// binary WebSocket frames afterwards.
//
// Layers, outermost first:
//
//  1. TLS 1.2+ on :443 using the site certificate (Traefik dynamic config or
//     Let's Encrypt). Provides transport confidentiality and forward secrecy.
//  2. HTTP/WebSocket. The client performs a normal `GET` upgrade to a benign
//     path. Auth data rides in the first control frame, never in headers, so
//     the request looks like any other WebSocket app.
//  3. A pre-shared-key (PSK) handshake that authenticates the client and
//     derives per-direction ChaCha20-Poly1305 keys (defence in depth on top
//     of TLS, and the mechanism that binds a session to a tunnel IP).
//  4. IP packets from the TUN device, one AEAD-sealed packet per binary frame.
//
// The same protocol is implemented by the Go server (internal/server) and the
// Android client (apps/android). Keep both sides in sync with this file.
package protocol

const (
	// Version is the protocol version negotiated in the hello frame.
	Version = 1

	// DefaultTunnelCIDR is the address pool handed out to clients.
	DefaultTunnelCIDR = "10.66.0.0/24"
	// DefaultGateway is the server side (TUN) address of the tunnel.
	DefaultGateway = "10.66.0.1"
	// DefaultMTU keeps room for TLS + WebSocket + AEAD overhead under a
	// typical 1500-byte path so tunnelled packets never fragment.
	DefaultMTU = 1400

	// PSKLen is the length in bytes of the pre-shared key.
	PSKLen = 32
	// SaltLen is the length of each side's handshake salt.
	SaltLen = 16
	// KeyLen is the ChaCha20-Poly1305 key length.
	KeyLen = 32
	// AuthWindow is how far apart (seconds) client and server clocks may be
	// for a handshake to be accepted; bounds replay of captured hellos.
	AuthWindow = 90
)

// Control-frame types (sent as WebSocket text frames, JSON-encoded).
const (
	MsgHello   = "hello"   // client -> server
	MsgWelcome = "welcome" // server -> client
	MsgError   = "error"   // server -> client
	MsgPing    = "ping"    // either direction, keepalive
	MsgPong    = "pong"
)

// Hello is the first frame a client sends after the WebSocket upgrade. It
// authenticates the client via an HMAC over the PSK and pins the handshake to
// a timestamp to bound replay.
type Hello struct {
	Type    string `json:"type"`           // MsgHello
	Version int    `json:"v"`              // Version
	Salt    string `json:"salt"`           // base64, SaltLen bytes of client entropy
	TS      int64  `json:"ts"`             // client unix time
	Auth    string `json:"auth"`           // base64 HMAC-SHA256(PSK, authContext(salt, ts))
	Name    string `json:"name,omitempty"` // free-form device label
}

// Welcome is the server's response to a valid Hello. It carries the assigned
// tunnel address and everything the client needs to configure its TUN device
// with zero manual input.
type Welcome struct {
	Type    string   `json:"type"` // MsgWelcome
	Salt    string   `json:"salt"` // base64, SaltLen bytes of server entropy
	IP      string   `json:"ip"`   // assigned tunnel address, e.g. 10.66.0.5
	Mask    int      `json:"mask"` // prefix length of the tunnel network
	Gateway string   `json:"gw"`
	DNS     []string `json:"dns"`
	MTU     int      `json:"mtu"`
}

// ErrorMsg is returned instead of Welcome when a handshake is rejected.
type ErrorMsg struct {
	Type string `json:"type"` // MsgError
	Msg  string `json:"msg"`
}

// Control is used to sniff the "type" field before full decoding.
type Control struct {
	Type string `json:"type"`
}
