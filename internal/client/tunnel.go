// Package client implements the ArnosVPN protocol client shared by the desktop
// apps. It opens a browser-shaped WSS tunnel, performs the PSK handshake, and
// exposes a simple ReadPacket/WritePacket interface over the encrypted session.
package client

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"arnosvpn/internal/protocol"
	"arnosvpn/internal/provision"
	"github.com/gorilla/websocket"
)

// Tunnel is an established ArnosVPN connection. It is safe for one reader
// goroutine and one writer goroutine (the usual TUN pump layout).
type Tunnel struct {
	conn    *websocket.Conn
	session *protocol.Session
	writeMu sync.Mutex

	// Traffic counters (payload bytes), for the stats page.
	rxBytes atomic.Uint64
	txBytes atomic.Uint64
	Since   time.Time

	// Network parameters assigned by the server.
	LocalIP string
	Mask    int
	Gateway string
	DNS     []string
	MTU     int
}

// Stats returns received/sent payload byte totals.
func (t *Tunnel) Stats() (rx, tx uint64) {
	return t.rxBytes.Load(), t.txBytes.Load()
}

// Connect dials the server described by profile and completes the handshake.
// Every call randomizes the request path, User-Agent and (via the protocol)
// per-frame padding, so no two connections share a fingerprint.
func Connect(ctx context.Context, p provision.Profile) (*Tunnel, error) {
	sni := p.SNI
	if sni == "" {
		sni = p.Host
	}
	dialer := &websocket.Dialer{
		HandshakeTimeout: 15 * time.Second,
		TLSClientConfig: &tls.Config{
			ServerName: sni,
			MinVersion: tls.VersionTLS12,
		},
	}
	header := http.Header{}
	header.Set("Origin", "https://"+p.Host)
	header.Set("User-Agent", randomUserAgent())
	header.Set("Accept-Language", "en-US,en;q=0.9")

	url := fmt.Sprintf("wss://%s:%d%s", p.Host, p.Port, randomPath())
	conn, resp, err := dialer.DialContext(ctx, url, header)
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("connect: %s (HTTP %d)", err, resp.StatusCode)
		}
		return nil, fmt.Errorf("connect: %w", err)
	}

	t, err := handshake(conn, p.PSK)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return t, nil
}

func handshake(conn *websocket.Conn, psk []byte) (*Tunnel, error) {
	clientSalt, err := protocol.RandBytes(protocol.SaltLen)
	if err != nil {
		return nil, err
	}
	ts := time.Now().Unix()
	hello := protocol.Hello{
		Type:    protocol.MsgHello,
		Version: protocol.Version,
		Salt:    base64.StdEncoding.EncodeToString(clientSalt),
		TS:      ts,
		Auth:    protocol.ComputeAuth(psk, clientSalt, ts),
		Name:    "arnosvpn-desktop",
	}
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := conn.WriteJSON(hello); err != nil {
		return nil, fmt.Errorf("send hello: %w", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	var welcome protocol.Welcome
	if err := conn.ReadJSON(&welcome); err != nil {
		return nil, fmt.Errorf("read welcome: %w", err)
	}
	if welcome.Type == protocol.MsgError {
		return nil, fmt.Errorf("server rejected connection")
	}
	if welcome.Type != protocol.MsgWelcome || welcome.IP == "" {
		return nil, fmt.Errorf("unexpected server response %q", welcome.Type)
	}

	serverSalt, err := base64.StdEncoding.DecodeString(welcome.Salt)
	if err != nil || len(serverSalt) != protocol.SaltLen {
		return nil, fmt.Errorf("bad server salt")
	}
	session, err := protocol.DeriveSession(psk, clientSalt, serverSalt, false)
	if err != nil {
		return nil, err
	}

	_ = conn.SetReadDeadline(time.Time{})
	_ = conn.SetWriteDeadline(time.Time{})
	return &Tunnel{
		conn:    conn,
		session: session,
		Since:   time.Now(),
		LocalIP: welcome.IP,
		Mask:    welcome.Mask,
		Gateway: welcome.Gateway,
		DNS:     welcome.DNS,
		MTU:     welcome.MTU,
	}, nil
}

// ReadPacket blocks for the next decrypted IP packet from the server. Control
// frames (pong) are skipped transparently.
func (t *Tunnel) ReadPacket() ([]byte, error) {
	for {
		mt, data, err := t.conn.ReadMessage()
		if err != nil {
			return nil, err
		}
		if mt != websocket.BinaryMessage {
			continue // control JSON (pong/keepalive)
		}
		pkt, err := t.session.Open(data)
		if err != nil {
			return nil, fmt.Errorf("decrypt: %w", err)
		}
		t.rxBytes.Add(uint64(len(pkt)))
		return pkt, nil
	}
}

// WritePacket encrypts and sends one IP packet to the server.
func (t *Tunnel) WritePacket(pkt []byte) error {
	frame := t.session.Seal(pkt)
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if err := t.conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
		return err
	}
	t.txBytes.Add(uint64(len(pkt)))
	return nil
}

// KeepAlive periodically sends a ping so idle tunnels and NAT mappings survive.
// Run it in its own goroutine; it returns when the tunnel closes.
func (t *Tunnel) KeepAlive(interval time.Duration, done <-chan struct{}) {
	tk := time.NewTicker(interval)
	defer tk.Stop()
	for {
		select {
		case <-done:
			return
		case <-tk.C:
			t.writeMu.Lock()
			err := t.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))
			t.writeMu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

// Close terminates the tunnel.
func (t *Tunnel) Close() error {
	t.writeMu.Lock()
	_ = t.conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, "bye"),
		time.Now().Add(2*time.Second))
	t.writeMu.Unlock()
	return t.conn.Close()
}
