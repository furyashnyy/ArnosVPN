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

// Config is the desktop client's persisted list of servers.
type Config struct {
	Active  string   `json:"active"`
	Servers []Server `json:"servers"`
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
		return &Config{}, nil
	}
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
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
	if _, err := provision.ParseURI(uri); err != nil {
		return err
	}
	if name == "" {
		p, _ := provision.ParseURI(uri)
		name = p.Host
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
