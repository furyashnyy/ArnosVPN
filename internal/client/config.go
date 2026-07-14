package client

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"arnosvpn/internal/provision"
)

// Server is one saved endpoint, stored as its canonical arnos:// URI so it
// round-trips through exactly the same parser as QR codes and deep links.
type Server struct {
	Name string `json:"name"`
	URI  string `json:"uri"`
}

// Settings holds the desktop client's preferences (the Settings page). Some are
// wired to behaviour (mode, ports, DNS, allowLan, preferredIP); the rest are
// persisted for the UI and reserved for future protocol features.
type Settings struct {
	Mode        string `json:"mode"`        // proxy | tun
	Socks       string `json:"socks"`       // SOCKS5 listen address
	Http        string `json:"http"`        // HTTP proxy listen address
	DNS         string `json:"dns"`         // comma-separated
	PreferredIP string `json:"preferredIP"` // auto | 4 | 6
	AllowLAN    bool   `json:"allowLan"`    // bind proxies on 0.0.0.0
	SystemProxy bool   `json:"systemProxy"`
	Autostart   bool   `json:"autostart"`
	Fragment    bool   `json:"fragment"`
	Mux         bool   `json:"mux"`
	Theme       string `json:"theme"` // system | light | dark
	TunDNS      string `json:"tunDns"`
}

// DefaultSettings returns sensible defaults.
func DefaultSettings() *Settings {
	return &Settings{
		Mode: "proxy", Socks: "127.0.0.1:1080", Http: "127.0.0.1:8080",
		DNS: "1.1.1.1,1.0.0.1", PreferredIP: "auto", Theme: "light", TunDNS: "1.1.1.1",
	}
}

// Config is the desktop client's persisted list of servers and settings.
type Config struct {
	Active   string    `json:"active"`
	Servers  []Server  `json:"servers"`
	Settings *Settings `json:"settings,omitempty"`
}

// DefaultConfigPath returns the per-user config file location:
// %APPDATA%\ArnosVPN\servers.json on Windows, ~/.config/arnosvpn/servers.json
// elsewhere.
func DefaultConfigPath() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "ArnosVPN", "servers.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".arnosvpn", "servers.json")
}

// LoadConfig reads the config, returning an empty one if the file is absent.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Config{Settings: DefaultSettings()}, nil
	}
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if c.Settings == nil {
		c.Settings = DefaultSettings()
	}
	return &c, nil
}

// Save writes the config, creating the directory if needed.
func (c *Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	blob, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, blob, 0o600)
}

// Add validates and stores a server by arnos:// URI. If a server with the same
// name exists it is replaced. The first server added becomes active.
func (c *Config) Add(name, uri string) error {
	p, err := provision.ParseURI(uri)
	if err != nil {
		return err
	}
	if name == "" {
		name = p.Name
		if name == "" {
			name = p.Host
		}
	}
	for i := range c.Servers {
		if strings.EqualFold(c.Servers[i].Name, name) {
			c.Servers[i].URI = uri
			return nil
		}
	}
	c.Servers = append(c.Servers, Server{Name: name, URI: uri})
	if c.Active == "" {
		c.Active = name
	}
	return nil
}

// Remove deletes a server by name.
func (c *Config) Remove(name string) {
	out := c.Servers[:0]
	for _, s := range c.Servers {
		if !strings.EqualFold(s.Name, name) {
			out = append(out, s)
		}
	}
	c.Servers = out
	if strings.EqualFold(c.Active, name) {
		c.Active = ""
		if len(c.Servers) > 0 {
			c.Active = c.Servers[0].Name
		}
	}
}

// Profile resolves a server name (empty = the active one) to a Profile.
func (c *Config) Profile(name string) (provision.Profile, error) {
	if name == "" {
		name = c.Active
	}
	if name == "" {
		return provision.Profile{}, fmt.Errorf("no server selected; add one with `arnosvpn-client add <arnos://...>`")
	}
	for _, s := range c.Servers {
		if strings.EqualFold(s.Name, name) {
			return provision.ParseURI(s.URI)
		}
	}
	return provision.Profile{}, fmt.Errorf("server %q not found", name)
}
