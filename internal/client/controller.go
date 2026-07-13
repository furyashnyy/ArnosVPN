package client

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"
)

// Controller manages the tunnel lifecycle for the GUI: it holds the server
// list and can connect/disconnect on demand, exposing a snapshot of state. It
// is safe for concurrent use by the HTTP handlers.
type Controller struct {
	cfgPath string

	mu         sync.Mutex
	cfg        *Config
	cancel     context.CancelFunc
	tunnel     *Tunnel
	connected  bool
	connecting bool
	mode       string
	assignedIP string
	lastError  string
	socksAddr  string
	httpAddr   string
}

// NewController loads the server list from cfgPath.
func NewController(cfgPath string) (*Controller, error) {
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		return nil, err
	}
	return &Controller{
		cfgPath:   cfgPath,
		cfg:       cfg,
		socksAddr: "127.0.0.1:1080",
		httpAddr:  "127.0.0.1:8080",
	}, nil
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
		AssignedIP: c.assignedIP,
		Socks:      c.socksAddr,
		Http:       c.httpAddr,
		Active:     c.cfg.Active,
		Error:      c.lastError,
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

// AddServer validates and stores a server, then persists.
func (c *Controller) AddServer(uri string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.cfg.Add("", uri); err != nil {
		return err
	}
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

// Connect brings up the active server in the given mode ("proxy" or "tun").
func (c *Controller) Connect(mode string) error {
	if mode == "" {
		mode = "proxy"
	}
	c.mu.Lock()
	if c.connected || c.connecting {
		c.mu.Unlock()
		return fmt.Errorf("already connected")
	}
	profile, err := c.cfg.Profile("")
	if err != nil {
		c.mu.Unlock()
		return err
	}
	c.connecting = true
	c.lastError = ""
	c.mode = mode
	c.mu.Unlock()

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
		return err
	}

	c.mu.Lock()
	c.cancel = cancel
	c.tunnel = tunnel
	c.connected = true
	c.connecting = false
	c.assignedIP = tunnel.LocalIP
	socks, http := c.socksAddr, c.httpAddr
	c.mu.Unlock()

	go func() {
		var runErr error
		switch mode {
		case "tun":
			runErr = RunTUN(ctx, tunnel, resolveHost(profile.Host), "arnos0")
		default:
			runErr = RunProxy(ctx, tunnel, socks, http)
		}
		c.mu.Lock()
		c.connected = false
		c.assignedIP = ""
		if runErr != nil && ctx.Err() == nil {
			c.lastError = runErr.Error()
		}
		c.mu.Unlock()
		_ = tunnel.Close()
	}()
	return nil
}

// Disconnect tears down the active tunnel.
func (c *Controller) Disconnect() {
	c.mu.Lock()
	cancel := c.cancel
	tunnel := c.tunnel
	c.cancel = nil
	c.tunnel = nil
	c.connected = false
	c.assignedIP = ""
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if tunnel != nil {
		_ = tunnel.Close()
	}
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
