// Package server implements the ArnosVPN tunnel server: an HTTPS/WebSocket
// endpoint that authenticates clients, hands each a tunnel address, and bridges
// their traffic through a TUN device that is NATed onto the host's WAN
// interface so the client's exit IP becomes the server's.
package server

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"arnosvpn/internal/config"
	"arnosvpn/internal/protocol"
	"github.com/gorilla/websocket"
)

// Server owns the TUN device, address pool and connected clients.
type Server struct {
	cfg     *config.Config
	pool    *ipPool
	tun     *tunDevice
	netcfg  *netConfig
	upgrade websocket.Upgrader

	mu      sync.RWMutex
	clients map[uint32]*client // keyed by tunnel IPv4 as uint32
}

// client is one connected tunnel session.
type client struct {
	conn    *websocket.Conn
	session *protocol.Session
	ip      net.IP
	ipKey   uint32
	name    string
	send    chan []byte // outbound encrypted frames, serialized by writeLoop
	closed  chan struct{}
	once    sync.Once
}

// New sets up the TUN device and host networking. It requires CAP_NET_ADMIN
// (run the container with NET_ADMIN and /dev/net/tun).
func New(cfg *config.Config) (*Server, error) {
	pool, err := newIPPool(cfg.TunnelCIDR, cfg.Gateway)
	if err != nil {
		return nil, err
	}
	tun, err := openTUN("arnos0", cfg.MTU)
	if err != nil {
		return nil, err
	}
	netcfg, err := configureNetwork(tun.Name(), cfg.Gateway, cfg.TunnelCIDR, pool.Ones(), cfg.WANInterface)
	if err != nil {
		tun.Close()
		return nil, err
	}
	log.Printf("tunnel up: dev=%s gw=%s cidr=%s wan=%s", tun.Name(), cfg.Gateway, cfg.TunnelCIDR, netcfg.WANInterface())

	s := &Server{
		cfg:     cfg,
		pool:    pool,
		tun:     tun,
		netcfg:  netcfg,
		clients: make(map[uint32]*client),
		upgrade: websocket.Upgrader{
			ReadBufferSize:  64 * 1024,
			WriteBufferSize: 64 * 1024,
			// Any Origin is fine; auth is by PSK, not by browser same-origin.
			CheckOrigin: func(*http.Request) bool { return true },
		},
	}
	go s.tunReadLoop()
	return s, nil
}

// Close tears down host networking and the TUN device.
func (s *Server) Close() {
	if s.netcfg != nil {
		s.netcfg.Teardown()
	}
	if s.tun != nil {
		s.tun.Close()
	}
}

// Handler returns the HTTP handler: WebSocket tunnel on the configured path,
// an innocuous landing page everywhere else so scanners see a plain web app.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Plain health endpoint for Coolify/Traefik/load-balancer checks. A
		// failing health check is a common reason a proxy starts returning
		// 5xx/403 for the service, so keep this always-200 and unauthenticated.
		if r.URL.Path == "/healthz" {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("ok"))
			return
		}

		isWS := websocket.IsWebSocketUpgrade(r)
		// One concise access log line per request. Invaluable for diagnosing
		// "403 upstream" reports: if these lines appear, the request reached
		// ArnosVPN and the proxy is not the problem; if they don't, it is.
		log.Printf("request %s %s ws=%v ua=%q from=%s xff=%q",
			r.Method, r.URL.Path, isWS, r.UserAgent(), r.RemoteAddr,
			r.Header.Get("X-Forwarded-For"))

		// Accept the tunnel upgrade on ANY path. Some proxies rewrite or strip
		// the path; as long as it's a WebSocket upgrade that passes the PSK
		// handshake, serve it. Everything else gets the innocuous decoy page.
		if isWS {
			s.handleTunnel(w, r)
			return
		}
		s.decoy(w, r)
	})
}

// decoy serves a boring page so the endpoint looks like an ordinary site.
func (s *Server) decoy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Server", "nginx")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<!doctype html><html><head><title>Welcome</title></head><body><h1>It works!</h1></body></html>`))
}

func (s *Server) handleTunnel(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrade.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade already wrote an error
	}

	c, err := s.handshake(conn)
	if err != nil {
		_ = conn.WriteJSON(protocol.ErrorMsg{Type: protocol.MsgError, Msg: err.Error()})
		_ = conn.Close()
		log.Printf("handshake rejected from %s: %v", r.RemoteAddr, err)
		return
	}
	log.Printf("client connected: %s ip=%s name=%q", r.RemoteAddr, c.ip, c.name)

	s.register(c)
	defer s.deregister(c)

	go c.writeLoop()
	c.readLoop(s) // blocks until the connection ends
}

// handshake performs the PSK-authenticated exchange and returns a ready client.
func (s *Server) handshake(conn *websocket.Conn) (*client, error) {
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	mt, data, err := conn.ReadMessage()
	if err != nil {
		return nil, err
	}
	if mt != websocket.TextMessage {
		return nil, errors.New("expected hello control frame")
	}
	var hello protocol.Hello
	if err := json.Unmarshal(data, &hello); err != nil {
		return nil, errors.New("malformed hello")
	}
	if hello.Type != protocol.MsgHello || hello.Version != protocol.Version {
		return nil, errors.New("unsupported protocol version")
	}

	clientSalt, err := base64.StdEncoding.DecodeString(hello.Salt)
	if err != nil || len(clientSalt) != protocol.SaltLen {
		return nil, errors.New("bad client salt")
	}
	if skew := time.Now().Unix() - hello.TS; skew > protocol.AuthWindow || skew < -protocol.AuthWindow {
		return nil, errors.New("stale handshake")
	}
	if !protocol.VerifyAuth(s.cfg.PSK, clientSalt, hello.TS, hello.Auth) {
		return nil, errors.New("authentication failed")
	}

	serverSalt, err := protocol.RandBytes(protocol.SaltLen)
	if err != nil {
		return nil, err
	}
	session, err := protocol.DeriveSession(s.cfg.PSK, clientSalt, serverSalt, true)
	if err != nil {
		return nil, err
	}

	ip, err := s.pool.Allocate()
	if err != nil {
		return nil, err
	}

	welcome := protocol.Welcome{
		Type:    protocol.MsgWelcome,
		Salt:    base64.StdEncoding.EncodeToString(serverSalt),
		IP:      ip.String(),
		Mask:    s.pool.Ones(),
		Gateway: s.cfg.Gateway,
		DNS:     s.cfg.DNS,
		MTU:     s.cfg.MTU,
	}
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := conn.WriteJSON(welcome); err != nil {
		s.pool.Release(ip)
		return nil, err
	}
	_ = conn.SetReadDeadline(time.Time{})
	_ = conn.SetWriteDeadline(time.Time{})

	return &client{
		conn:    conn,
		session: session,
		ip:      ip,
		ipKey:   ipToUint32(ip),
		name:    hello.Name,
		send:    make(chan []byte, 256),
		closed:  make(chan struct{}),
	}, nil
}

func (s *Server) register(c *client) {
	s.mu.Lock()
	if old, ok := s.clients[c.ipKey]; ok {
		old.close()
	}
	s.clients[c.ipKey] = c
	s.mu.Unlock()
}

func (s *Server) deregister(c *client) {
	s.mu.Lock()
	if cur, ok := s.clients[c.ipKey]; ok && cur == c {
		delete(s.clients, c.ipKey)
	}
	s.mu.Unlock()
	s.pool.Release(c.ip)
	c.close()
	log.Printf("client disconnected: ip=%s name=%q", c.ip, c.name)
}

// tunReadLoop reads packets arriving on the TUN device (replies from the
// internet) and routes each to the owning client by destination address.
func (s *Server) tunReadLoop() {
	buf := make([]byte, s.cfg.MTU+64)
	for {
		n, err := s.tun.Read(buf)
		if err != nil {
			log.Printf("tun read ended: %v", err)
			return
		}
		if n < 20 {
			continue
		}
		pkt := buf[:n]
		if pkt[0]>>4 != 4 {
			continue // only IPv4 is routed through the pool
		}
		dst := binary.BigEndian.Uint32(pkt[16:20])

		s.mu.RLock()
		c := s.clients[dst]
		s.mu.RUnlock()
		if c == nil {
			continue
		}

		frame := c.session.Seal(pkt)
		select {
		case c.send <- frame:
		default:
			// Client is not draining; drop rather than block the whole tunnel.
		}
	}
}

// readLoop reads encrypted frames from one client and writes decrypted packets
// to the TUN device. Control frames (ping/pong) are handled inline.
func (c *client) readLoop(s *Server) {
	for {
		mt, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		switch mt {
		case websocket.BinaryMessage:
			pkt, err := c.session.Open(data)
			if err != nil {
				log.Printf("decrypt error from %s: %v", c.ip, err)
				return
			}
			if len(pkt) < 20 || pkt[0]>>4 != 4 {
				continue
			}
			// Anti-spoofing: only forward packets whose source is the address
			// we assigned this client.
			if binary.BigEndian.Uint32(pkt[12:16]) != c.ipKey {
				continue
			}
			if _, err := s.tun.Write(pkt); err != nil {
				log.Printf("tun write error: %v", err)
			}
		case websocket.TextMessage:
			var ctrl protocol.Control
			if json.Unmarshal(data, &ctrl) == nil && ctrl.Type == protocol.MsgPing {
				select {
				case c.send <- nil: // nil frame signals writeLoop to send a pong
				default:
				}
			}
		}
	}
}

// writeLoop is the single writer for a client's WebSocket connection.
func (c *client) writeLoop() {
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-c.closed:
			return
		case frame := <-c.send:
			if frame == nil {
				_ = c.conn.WriteJSON(protocol.Control{Type: protocol.MsgPong})
				continue
			}
			if err := c.conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
				c.close()
				return
			}
		case <-ping.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)); err != nil {
				c.close()
				return
			}
			_ = c.conn.SetWriteDeadline(time.Time{})
		}
	}
}

func (c *client) close() {
	c.once.Do(func() {
		close(c.closed)
		_ = c.conn.Close()
	})
}

// Serve runs the HTTP(S) server on the given listener until the context is
// cancelled. When tlsConf is non-nil ArnosVPN terminates TLS itself (self
// mode); when nil it speaks plain HTTP/WS and an upstream proxy (Coolify's
// Traefik) provides TLS (proxy mode). description is only used for logging.
func (s *Server) Serve(ctx context.Context, ln net.Listener, tlsConf *tls.Config, description string) error {
	srv := &http.Server{
		Handler:   s.Handler(),
		TLSConfig: tlsConf,
	}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	log.Printf("listening on %s (%s)", ln.Addr(), description)
	if tlsConf != nil {
		ln = tls.NewListener(ln, tlsConf)
	}
	err := srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func ipToUint32(ip net.IP) uint32 {
	return binary.BigEndian.Uint32(ip.To4())
}
