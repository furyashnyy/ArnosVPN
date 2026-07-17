package client

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"arnosvpn/internal/provision"
)

// Controller manages the tunnel lifecycle for the GUI: it holds the server
// list and settings, and can connect/disconnect on demand, exposing a snapshot
// of state. It is safe for concurrent use by the HTTP handlers.
type Controller struct {
	cfgPath string

	mu         sync.Mutex
	cfg        *Config
	cancel     context.CancelFunc
	tunnel     *Tunnel
	connected  bool
	connecting bool
	wantConn   bool
	mode       string
	lastError  string
	sysProxyOn bool // we set the OS proxy and must clear it on disconnect
}

// NewController loads the server list and settings from cfgPath.
func NewController(cfgPath string) (*Controller, error) {
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		return nil, err
	}
	return &Controller{cfgPath: cfgPath, cfg: cfg}, nil
}

// StateServer is one server in the state snapshot.
type StateServer struct {
	Name   string `json:"name"`
	Host   string `json:"host"`
	Port   int    `json:"port"`
	Active bool   `json:"active"`
}

// State is the JSON snapshot the GUI renders.
type State struct {
	Connected  bool          `json:"connected"`
	Connecting bool          `json:"connecting"`
	Mode       string        `json:"mode"`
	AssignedIP string        `json:"assignedIP"`
	Socks      string        `json:"socks"`
	Http       string        `json:"http"`
	Active     string        `json:"active"`
	Error      string        `json:"error"`
	Rx         uint64        `json:"rx"`
	Tx         uint64        `json:"tx"`
	Since      int64         `json:"since"`
	Servers    []StateServer `json:"servers"`
}

// State returns a snapshot for the UI.
func (c *Controller) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := State{
		Connected:  c.connected,
		Connecting: c.connecting,
		Mode:       c.mode,
		Socks:      c.cfg.Settings.Socks,
		Http:       c.cfg.Settings.Http,
		Active:     c.cfg.Active,
		Error:      c.lastError,
	}
	if c.tunnel != nil {
		s.AssignedIP = c.tunnel.LocalIP
		s.Rx, s.Tx = c.tunnel.Stats()
		s.Since = c.tunnel.Since.Unix()
	}
	for _, srv := range c.cfg.Servers {
		item := StateServer{Name: srv.Name, Active: srv.Name == c.cfg.Active}
		if p, err := c.cfg.Profile(srv.Name); err == nil {
			item.Host, item.Port = p.Host, p.Port
		}
		s.Servers = append(s.Servers, item)
	}
	return s
}

// Settings returns the current settings.
func (c *Controller) Settings() *Settings {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg.Settings
}

// SetSettings replaces settings and persists.
func (c *Controller) SetSettings(s *Settings) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s.Socks == "" {
		s.Socks = "127.0.0.1:1080"
	}
	if s.Http == "" {
		s.Http = "127.0.0.1:8080"
	}
	if s.Mode == "" {
		s.Mode = "proxy"
	}
	c.cfg.Settings = s
	return c.cfg.Save(c.cfgPath)
}

// AddServer validates and stores a server, then persists.
func (c *Controller) AddServer(uri string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.cfg.Add("", uri); err != nil {
		return err
	}
	guiLog.add("added server")
	return c.cfg.Save(c.cfgPath)
}

// SetActive switches the active server.
func (c *Controller) SetActive(name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.cfg.Profile(name); err != nil {
		return err
	}
	c.cfg.Active = name
	return c.cfg.Save(c.cfgPath)
}

// RemoveServer deletes a server.
func (c *Controller) RemoveServer(name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cfg.Remove(name)
	return c.cfg.Save(c.cfgPath)
}

// Subscribe fetches a URL whose body is a list of arnos:// URIs (one per line,
// or base64 of the same) and adds each as a server. Returns how many were added.
func (c *Controller) Subscribe(rawURL string) (int, error) {
	req, err := newHTTPGet(rawURL)
	if err != nil {
		return 0, err
	}
	body, err := fetchBody(req)
	if err != nil {
		return 0, err
	}
	text := string(body)
	if decoded, ok := maybeBase64(text); ok {
		text = decoded
	}
	added := 0
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "arnos://") {
			continue
		}
		c.mu.Lock()
		err := c.cfg.Add("", line)
		c.mu.Unlock()
		if err == nil {
			added++
		}
	}
	if added == 0 {
		return 0, fmt.Errorf("no arnos:// entries found at that URL")
	}
	c.mu.Lock()
	err = c.cfg.Save(c.cfgPath)
	c.mu.Unlock()
	guiLog.add(fmt.Sprintf("subscription: added %d server(s)", added))
	return added, err
}

// Ping measures the TCP connect latency to the active server, in milliseconds.
func (c *Controller) Ping(name string) (int64, error) {
	c.mu.Lock()
	profile, err := c.cfg.Profile(name)
	c.mu.Unlock()
	if err != nil {
		return 0, err
	}
	// Open a real tunnel (TLS + WebSocket upgrade + PSK auth), then measure the
	// in-tunnel ping/pong round-trip — the true link latency. This avoids
	// reporting the one-off DNS/handshake setup cost, which on a machine with
	// slow DNS can be seconds even when the link itself is ~tens of ms.
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	start := time.Now()
	tun, err := Connect(ctx, profile)
	if err != nil {
		return 0, err
	}
	defer tun.Close()
	if rtt, err := tun.RTT(5 * time.Second); err == nil {
		return rtt.Milliseconds(), nil
	}
	// Pong didn't arrive but the handshake did — fall back to setup time.
	return time.Since(start).Milliseconds(), nil
}

// Connect brings up the active server. mode overrides the saved setting when
// non-empty.
func (c *Controller) Connect(mode string) error {
	c.mu.Lock()
	if c.connected || c.connecting {
		c.mu.Unlock()
		return fmt.Errorf("already connected")
	}
	if mode == "" {
		mode = c.cfg.Settings.Mode
	}
	if mode == "" {
		mode = "proxy"
	}
	profile, err := c.cfg.Profile("")
	if err != nil {
		c.mu.Unlock()
		return err
	}
	socks, httpAddr := lanAdjust(c.cfg.Settings.Socks, c.cfg.Settings.AllowLAN),
		lanAdjust(c.cfg.Settings.Http, c.cfg.Settings.AllowLAN)
	c.connecting = true
	c.lastError = ""
	c.mode = mode
	c.mu.Unlock()

	guiLog.add(fmt.Sprintf("connecting to %s:%d (%s mode)", profile.Host, profile.Port, mode))

	ctx, cancel := context.WithCancel(context.Background())
	dialCtx, dialCancel := context.WithTimeout(ctx, 20*time.Second)
	tunnel, err := Connect(dialCtx, profile)
	dialCancel()
	if err != nil {
		cancel()
		c.mu.Lock()
		c.connecting = false
		c.lastError = err.Error()
		c.mu.Unlock()
		guiLog.add("connect failed: " + err.Error())
		return err
	}

	c.mu.Lock()
	c.cancel = cancel
	c.tunnel = tunnel
	c.connected = true
	c.connecting = false
	c.wantConn = true
	c.mu.Unlock()
	guiLog.add("connected, assigned " + tunnel.LocalIP)

	// In proxy mode, optionally point the OS proxy at our local HTTP proxy so
	// browsers/apps route through the tunnel with no per-app setup (TUN mode
	// already routes everything system-wide, so it needs no OS proxy).
	if mode == "proxy" && c.cfg.Settings.SystemProxy && c.cfg.Settings.Http != "" {
		if err := setSystemProxy(c.cfg.Settings.Http); err != nil {
			guiLog.add("system proxy not set: " + err.Error())
		} else {
			c.mu.Lock()
			c.sysProxyOn = true
			c.mu.Unlock()
			guiLog.add("system proxy → " + c.cfg.Settings.Http)
		}
	}

	// Supervisor: run the tunnel and, on any unexpected drop, reconnect with
	// backoff until the user disconnects. This is what keeps the VPN up instead
	// of dying after a few minutes.
	go c.supervise(ctx, profile, mode, socks, httpAddr, tunnel)
	return nil
}

func (c *Controller) supervise(ctx context.Context, profile provision.Profile, mode, socks, httpAddr string, tunnel *Tunnel) {
	for {
		runErr := runMode(ctx, tunnel, mode, socks, httpAddr)
		_ = tunnel.Close()

		c.mu.Lock()
		c.connected = false
		c.tunnel = nil
		still := c.wantConn && ctx.Err() == nil
		c.mu.Unlock()
		if !still {
			guiLog.add("disconnected")
			return
		}
		if runErr != nil {
			guiLog.add("connection lost: " + runErr.Error() + " — reconnecting")
		} else {
			guiLog.add("connection lost — reconnecting")
		}

		// Reconnect with exponential backoff.
		delay := 2 * time.Second
		var nt *Tunnel
		for {
			c.mu.Lock()
			c.connecting = true
			ok := c.wantConn
			c.mu.Unlock()
			if !ok || ctx.Err() != nil {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			dctx, dcancel := context.WithTimeout(ctx, 20*time.Second)
			t, err := Connect(dctx, profile)
			dcancel()
			if err == nil {
				nt = t
				break
			}
			delay *= 2
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
		}

		c.mu.Lock()
		if !c.wantConn {
			c.mu.Unlock()
			_ = nt.Close()
			return
		}
		c.tunnel = nt
		c.connected = true
		c.connecting = false
		c.lastError = ""
		c.mu.Unlock()
		guiLog.add("reconnected, assigned " + nt.LocalIP)
		tunnel = nt
	}
}

func runMode(ctx context.Context, tunnel *Tunnel, mode, socks, httpAddr string) error {
	if mode == "tun" {
		return RunTUN(ctx, tunnel, resolveHost(tunnelHost(tunnel)), "arnos0")
	}
	return RunProxy(ctx, tunnel, socks, httpAddr)
}

// tunnelHost returns the gateway; TUN mode resolves the server host separately
// via the profile at dial time, so the gateway is a safe placeholder here.
func tunnelHost(t *Tunnel) string { return t.Gateway }

// Disconnect tears down the active tunnel.
func (c *Controller) Disconnect() {
	c.mu.Lock()
	cancel := c.cancel
	tunnel := c.tunnel
	sysOn := c.sysProxyOn
	c.cancel = nil
	c.tunnel = nil
	c.connected = false
	c.wantConn = false
	c.sysProxyOn = false
	c.mu.Unlock()
	if sysOn {
		if err := clearSystemProxy(); err != nil {
			guiLog.add("system proxy not cleared: " + err.Error())
		} else {
			guiLog.add("system proxy cleared")
		}
	}
	if cancel != nil {
		cancel()
	}
	if tunnel != nil {
		_ = tunnel.Close()
	}
}

// lanAdjust rebinds a 127.0.0.1 listen address to 0.0.0.0 when LAN sharing is on.
func lanAdjust(addr string, allowLAN bool) string {
	if !allowLAN {
		return addr
	}
	if h, p, err := net.SplitHostPort(addr); err == nil && (h == "127.0.0.1" || h == "localhost") {
		return net.JoinHostPort("0.0.0.0", p)
	}
	return addr
}

// resolveHost returns the first IP for host (or host if already an IP).
func resolveHost(host string) string {
	if net.ParseIP(host) != nil {
		return host
	}
	if ips, err := net.LookupHost(host); err == nil && len(ips) > 0 {
		return ips[0]
	}
	return host
}

// splitDNS splits a comma-separated DNS list (used by callers wiring settings).
func splitDNS(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
