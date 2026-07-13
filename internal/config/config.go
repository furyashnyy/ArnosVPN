// Package config loads ArnoVPN server settings from the environment and, where
// values are missing, generates them so the server runs with zero manual
// configuration. State that must persist across restarts (the PSK, mainly) is
// written to a small JSON state file next to the binary.
package config

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/furyashnyy/arnosvpn/internal/protocol"
)

// CertProvider selects where the TLS certificate comes from.
type CertProvider string

const (
	// CertTraefik reads certificates that Traefik (as run by Coolify) already
	// obtained and stored, either in acme.json or as a mounted cert/key pair.
	// This is the default: on a Coolify host the reverse proxy already manages
	// certificates, so ArnoVPN simply reuses them.
	CertTraefik CertProvider = "traefik"
	// CertLetsEncrypt makes ArnoVPN obtain its own certificate directly from
	// Let's Encrypt via the ACME TLS-ALPN/HTTP challenge (autocert).
	CertLetsEncrypt CertProvider = "letsencrypt"
)

// Config is the fully-resolved server configuration.
type Config struct {
	// Domain the certificate is valid for (also the SNI clients connect with).
	Domain string
	// ListenAddr for the HTTPS/WSS listener.
	ListenAddr string
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
		Domain:          env("ARNO_DOMAIN", ""),
		ListenAddr:      env("ARNO_LISTEN", ":443"),
		WSPath:          env("ARNO_WS_PATH", "/"),
		TunnelCIDR:      env("ARNO_TUNNEL_CIDR", protocol.DefaultTunnelCIDR),
		Gateway:         env("ARNO_GATEWAY", protocol.DefaultGateway),
		WANInterface:    env("ARNO_WAN_IFACE", ""),
		CertProvider:    CertProvider(strings.ToLower(env("CERT_PROVIDER", string(CertTraefik)))),
		TraefikACMEPath: env("ARNO_TRAEFIK_ACME", "/traefik/acme.json"),
		TLSCertPath:     env("ARNO_TLS_CERT", ""),
		TLSKeyPath:      env("ARNO_TLS_KEY", ""),
		ACMEEmail:       env("ARNO_ACME_EMAIL", ""),
		ACMECacheDir:    env("ARNO_ACME_CACHE", "/data/acme"),
		StateFile:       env("ARNO_STATE_FILE", "/data/arno-state.json"),
		MTU:             envInt("ARNO_MTU", protocol.DefaultMTU),
		DNS:             envList("ARNO_DNS", []string{"1.1.1.1", "1.0.0.1"}),
	}

	if c.CertProvider != CertTraefik && c.CertProvider != CertLetsEncrypt {
		return nil, fmt.Errorf("CERT_PROVIDER must be %q or %q, got %q",
			CertTraefik, CertLetsEncrypt, c.CertProvider)
	}
	if c.CertProvider == CertLetsEncrypt && c.Domain == "" {
		return nil, fmt.Errorf("ARNO_DOMAIN is required when CERT_PROVIDER=letsencrypt")
	}

	if err := c.resolvePSK(); err != nil {
		return nil, err
	}
	return c, nil
}

// resolvePSK uses ARNO_PSK if set, otherwise loads a persisted key, otherwise
// generates one and persists it. This is what lets the server "configure
// itself": the operator never has to invent or copy a key by hand.
func (c *Config) resolvePSK() error {
	if v := os.Getenv("ARNO_PSK"); v != "" {
		psk, err := decodeKey(v)
		if err != nil {
			return fmt.Errorf("ARNO_PSK: %w", err)
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
