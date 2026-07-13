// Package config loads ArnosVPN server settings from the environment and, where
// values are missing, generates them so the server runs with zero manual
// configuration. State that must persist across restarts (the PSK, mainly) is
// written to a small JSON state file next to the binary.
package config

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"arnosvpn/internal/protocol"
)

// CertProvider selects where the TLS certificate comes from.
type CertProvider string

const (
	// CertTraefik reads certificates that Traefik (as run by Coolify) already
	// obtained and stored, either in acme.json or as a mounted cert/key pair.
	// This is the default: on a Coolify host the reverse proxy already manages
	// certificates, so ArnosVPN simply reuses them.
	CertTraefik CertProvider = "traefik"
	// CertLetsEncrypt makes ArnosVPN obtain its own certificate directly from
	// Let's Encrypt via the ACME TLS-ALPN/HTTP challenge (autocert).
	CertLetsEncrypt CertProvider = "letsencrypt"
)

// TLSMode selects who terminates TLS.
type TLSMode string

const (
	// TLSProxy (default) means an upstream reverse proxy — Coolify's Traefik —
	// terminates TLS and forwards the (still HTTPS-looking) WebSocket to
	// ArnosVPN over plain HTTP on an internal port. This is the right mode when
	// port 443 is already owned by Coolify/Traefik and you route by domain
	// ("expose"): ArnosVPN never binds 443, and the certificate is the one
	// Traefik/Let's Encrypt already manages for your domain.
	TLSProxy TLSMode = "proxy"
	// TLSSelf means ArnosVPN terminates TLS itself using the cert provider
	// (Traefik acme.json / mounted PEM, or Let's Encrypt). Use this for a
	// standalone deployment that owns its listen port.
	TLSSelf TLSMode = "self"

	// ListenAuto asks the OS for a free port instead of pinning one.
	ListenAuto = "auto"
)

// Config is the fully-resolved server configuration.
type Config struct {
	// Domain the certificate is valid for (also the SNI clients connect with).
	Domain string
	// TLSMode selects who terminates TLS (proxy = upstream Traefik, self = us).
	TLSMode TLSMode
	// ListenAddr for the listener. "auto" (or a :0 port) picks a free port,
	// which is handy behind Coolify's proxy where the exact internal port does
	// not matter — the chosen port is logged at startup.
	ListenAddr string
	// PublicHost / PublicPort are what clients actually dial (the reverse
	// proxy's public endpoint). They appear in the connect URI. They are
	// independent of ListenAddr, which is the internal bind.
	PublicHost string
	PublicPort int
	// WSPath is the benign-looking WebSocket path clients upgrade on.
	WSPath string
	// PSK is the 32-byte pre-shared key shared with clients.
	PSK []byte
	// TunnelCIDR is the client address pool.
	TunnelCIDR string
	// Gateway is the server-side tunnel address.
	Gateway string
	// MTU for the TUN device.
	MTU int
	// DNS servers pushed to clients.
	DNS []string
	// WANInterface is masqueraded for outbound tunnel traffic; empty means
	// auto-detect the default-route interface.
	WANInterface string

	// Certificate provider and its inputs.
	CertProvider CertProvider
	// TraefikACMEPath is the acme.json file written by Traefik (default
	// provider). Optional if TLSCertPath/TLSKeyPath are given instead.
	TraefikACMEPath string
	// TLSCertPath / TLSKeyPath point at a mounted PEM cert+key (also used by
	// the traefik provider when certs are exposed as files rather than acme.json).
	TLSCertPath string
	TLSKeyPath  string
	// ACMEEmail and ACMECacheDir are used by the letsencrypt provider.
	ACMEEmail    string
	ACMECacheDir string

	// StateFile is where the generated PSK is persisted.
	StateFile string
}

type persisted struct {
	PSK string `json:"psk"`
}

// Load resolves configuration from the environment, generating and persisting
// anything that is missing.
func Load() (*Config, error) {
	c := &Config{
		Domain:          env("ARNOS_DOMAIN", ""),
		TLSMode:         TLSMode(strings.ToLower(env("ARNOS_TLS_MODE", string(TLSProxy)))),
		// Default to a fixed, well-known internal port so a reverse proxy
		// (Coolify/Traefik) that routes by the container's exposed port always
		// finds the backend. "auto" (a random free port) is available but must
		// not be the default — a random port behind a proxy causes 502s.
		ListenAddr:      env("ARNOS_LISTEN", ":8443"),
		PublicHost:      env("ARNOS_PUBLIC_HOST", ""),
		PublicPort:      envInt("ARNOS_PUBLIC_PORT", 443),
		WSPath:          env("ARNOS_WS_PATH", "/"),
		TunnelCIDR:      env("ARNOS_TUNNEL_CIDR", protocol.DefaultTunnelCIDR),
		Gateway:         env("ARNOS_GATEWAY", protocol.DefaultGateway),
		WANInterface:    env("ARNOS_WAN_IFACE", ""),
		CertProvider:    CertProvider(strings.ToLower(env("CERT_PROVIDER", string(CertTraefik)))),
		TraefikACMEPath: env("ARNOS_TRAEFIK_ACME", "/traefik/acme.json"),
		TLSCertPath:     env("ARNOS_TLS_CERT", ""),
		TLSKeyPath:      env("ARNOS_TLS_KEY", ""),
		ACMEEmail:       env("ARNOS_ACME_EMAIL", ""),
		ACMECacheDir:    env("ARNOS_ACME_CACHE", "/data/acme"),
		StateFile:       env("ARNOS_STATE_FILE", "/data/arnos-state.json"),
		MTU:             envInt("ARNOS_MTU", protocol.DefaultMTU),
		DNS:             envList("ARNOS_DNS", []string{"1.1.1.1", "1.0.0.1"}),
	}

	if c.TLSMode != TLSProxy && c.TLSMode != TLSSelf {
		return nil, fmt.Errorf("ARNOS_TLS_MODE must be %q or %q, got %q",
			TLSProxy, TLSSelf, c.TLSMode)
	}
	if c.PublicHost == "" {
		c.PublicHost = c.Domain
	}

	// Certificate settings only matter when ArnosVPN terminates TLS itself.
	if c.TLSMode == TLSSelf {
		if c.CertProvider != CertTraefik && c.CertProvider != CertLetsEncrypt {
			return nil, fmt.Errorf("CERT_PROVIDER must be %q or %q, got %q",
				CertTraefik, CertLetsEncrypt, c.CertProvider)
		}
		if c.CertProvider == CertLetsEncrypt && c.Domain == "" {
			return nil, fmt.Errorf("ARNOS_DOMAIN is required when CERT_PROVIDER=letsencrypt")
		}
	}

	if err := c.resolvePSK(); err != nil {
		return nil, err
	}
	return c, nil
}

// ResolveListener opens the TCP listener for ListenAddr, resolving "auto" (or a
// :0 port) to an OS-assigned free port. The returned listener's address
// reports the actual port chosen.
func (c *Config) ResolveListener() (net.Listener, error) {
	addr := c.ListenAddr
	if addr == "" || strings.EqualFold(addr, ListenAuto) {
		addr = ":0"
	}
	return net.Listen("tcp", addr)
}

// resolvePSK uses ARNOS_PSK if set, otherwise loads a persisted key, otherwise
// generates one and persists it. This is what lets the server "configure
// itself": the operator never has to invent or copy a key by hand.
func (c *Config) resolvePSK() error {
	if v := os.Getenv("ARNOS_PSK"); v != "" {
		psk, err := decodeKey(v)
		if err != nil {
			return fmt.Errorf("ARNOS_PSK: %w", err)
		}
		c.PSK = psk
		return nil
	}

	if data, err := os.ReadFile(c.StateFile); err == nil {
		var p persisted
		if json.Unmarshal(data, &p) == nil && p.PSK != "" {
			if psk, err := decodeKey(p.PSK); err == nil {
				c.PSK = psk
				return nil
			}
		}
	}

	psk := make([]byte, protocol.PSKLen)
	if _, err := rand.Read(psk); err != nil {
		return err
	}
	c.PSK = psk
	if err := os.MkdirAll(filepath.Dir(c.StateFile), 0o700); err != nil {
		return err
	}
	blob, _ := json.MarshalIndent(persisted{PSK: base64.StdEncoding.EncodeToString(psk)}, "", "  ")
	return os.WriteFile(c.StateFile, blob, 0o600)
}

// PSKBase64 returns the PSK in the base64 form used by client provisioning.
func (c *Config) PSKBase64() string {
	return base64.StdEncoding.EncodeToString(c.PSK)
}

// decodeKey accepts base64 (standard or URL) or hex-ish raw and validates length.
func decodeKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if b, err := base64.StdEncoding.DecodeString(s); err == nil && len(b) == protocol.PSKLen {
		return b, nil
	}
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil && len(b) == protocol.PSKLen {
		return b, nil
	}
	return nil, fmt.Errorf("key must decode to %d bytes", protocol.PSKLen)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envList(key string, def []string) []string {
	if v := os.Getenv(key); v != "" {
		parts := strings.Split(v, ",")
		out := parts[:0]
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return def
}
