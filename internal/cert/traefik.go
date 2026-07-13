package cert

import (
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"arnosvpn/internal/config"
)

// traefikProvider serves the certificate Traefik manages. It supports two
// on-disk shapes, in priority order:
//
//  1. A mounted PEM cert/key pair (ARNOS_TLS_CERT / ARNOS_TLS_KEY). This is the
//     simplest Coolify setup: expose the proxy's cert files to the container.
//  2. Traefik's acme.json (ARNOS_TRAEFIK_ACME), the store Traefik writes when it
//     obtains certificates itself. We decode the base64 PEM blobs and pick the
//     certificate whose domain matches ARNOS_DOMAIN (or the only one present).
//
// The chosen source is watched by mtime and reloaded on change, so a Traefik
// renewal is served without restarting ArnosVPN.
type traefikProvider struct {
	cfg *config.Config

	mu       sync.RWMutex
	current  *tls.Certificate
	loadedAt time.Time
	source   string
	modTime  time.Time
}

func newTraefik(c *config.Config) (Provider, error) {
	p := &traefikProvider{cfg: c}
	if err := p.reload(true); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *traefikProvider) Describe() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return fmt.Sprintf("traefik (source=%s)", p.source)
}

func (p *traefikProvider) TLSConfig() (*tls.Config, error) {
	tc := baseTLS()
	tc.GetCertificate = func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		if err := p.reload(false); err != nil {
			// Serve the last good certificate rather than failing the handshake.
			p.mu.RLock()
			defer p.mu.RUnlock()
			if p.current != nil {
				return p.current, nil
			}
			return nil, err
		}
		p.mu.RLock()
		defer p.mu.RUnlock()
		return p.current, nil
	}
	return tc, nil
}

// activeSource returns the file we load from and its mtime.
func (p *traefikProvider) activeSource() (path string, mod time.Time, err error) {
	if p.cfg.TLSCertPath != "" && p.cfg.TLSKeyPath != "" {
		fi, err := os.Stat(p.cfg.TLSCertPath)
		if err != nil {
			return "", time.Time{}, err
		}
		return p.cfg.TLSCertPath, fi.ModTime(), nil
	}
	fi, err := os.Stat(p.cfg.TraefikACMEPath)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("no PEM pair and acme store unavailable: %w", err)
	}
	return p.cfg.TraefikACMEPath, fi.ModTime(), nil
}

func (p *traefikProvider) reload(force bool) error {
	path, mod, err := p.activeSource()
	if err != nil {
		return err
	}
	p.mu.RLock()
	unchanged := !force && p.current != nil && mod.Equal(p.modTime) && p.source == path
	p.mu.RUnlock()
	if unchanged {
		return nil
	}

	var certPEM, keyPEM []byte
	if path == p.cfg.TLSCertPath {
		if certPEM, err = os.ReadFile(p.cfg.TLSCertPath); err != nil {
			return err
		}
		if keyPEM, err = os.ReadFile(p.cfg.TLSKeyPath); err != nil {
			return err
		}
	} else {
		certPEM, keyPEM, err = certFromACME(path, p.cfg.Domain)
		if err != nil {
			return err
		}
	}

	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("build key pair from %s: %w", path, err)
	}

	p.mu.Lock()
	p.current = &pair
	p.loadedAt = time.Now()
	p.source = path
	p.modTime = mod
	p.mu.Unlock()
	return nil
}

// acmeStore models Traefik's acme.json. Field names vary a little across
// Traefik versions, so decode tolerantly with lowercased keys.
type acmeCertificate struct {
	Domain struct {
		Main string   `json:"main"`
		SANs []string `json:"sans"`
	} `json:"domain"`
	Certificate string `json:"certificate"`
	Key         string `json:"key"`
}

// certFromACME extracts a PEM cert chain and key from Traefik's acme.json,
// selecting the entry whose domain matches want (or the first entry if want is
// empty). Traefik stores the cert and key as base64-encoded PEM.
func certFromACME(path, want string) (certPEM, keyPEM []byte, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}

	// Top level is { "<resolver>": { "Certificates": [ ... ] } }, with mixed
	// capitalisation depending on version. Normalise by scanning generically.
	var resolvers map[string]json.RawMessage
	if err := json.Unmarshal(raw, &resolvers); err != nil {
		return nil, nil, fmt.Errorf("parse acme.json: %w", err)
	}

	want = strings.ToLower(strings.TrimSpace(want))
	var fallback *acmeCertificate

	for _, resolverRaw := range resolvers {
		var resolver map[string]json.RawMessage
		if json.Unmarshal(resolverRaw, &resolver) != nil {
			continue
		}
		for k, v := range resolver {
			if !strings.EqualFold(k, "certificates") {
				continue
			}
			var certs []acmeCertificate
			if json.Unmarshal(v, &certs) != nil {
				continue
			}
			for i := range certs {
				c := certs[i]
				if c.Certificate == "" || c.Key == "" {
					continue
				}
				if fallback == nil {
					fallback = &certs[i]
				}
				if want == "" || matchesDomain(c, want) {
					return decodeACMEPair(c)
				}
			}
		}
	}

	if fallback != nil {
		return decodeACMEPair(*fallback)
	}
	return nil, nil, fmt.Errorf("no usable certificate found in %s", path)
}

func matchesDomain(c acmeCertificate, want string) bool {
	if strings.EqualFold(c.Domain.Main, want) {
		return true
	}
	for _, s := range c.Domain.SANs {
		if strings.EqualFold(s, want) || wildcardMatch(s, want) {
			return true
		}
	}
	return wildcardMatch(c.Domain.Main, want)
}

// wildcardMatch handles a single leading "*." wildcard label.
func wildcardMatch(pattern, host string) bool {
	if !strings.HasPrefix(pattern, "*.") {
		return false
	}
	suffix := pattern[1:] // ".example.com"
	return strings.HasSuffix(host, suffix) &&
		strings.Count(host, ".") == strings.Count(suffix, ".")
}

func decodeACMEPair(c acmeCertificate) (certPEM, keyPEM []byte, err error) {
	certPEM, err = base64.StdEncoding.DecodeString(c.Certificate)
	if err != nil {
		return nil, nil, fmt.Errorf("decode acme certificate: %w", err)
	}
	keyPEM, err = base64.StdEncoding.DecodeString(c.Key)
	if err != nil {
		return nil, nil, fmt.Errorf("decode acme key: %w", err)
	}
	return certPEM, keyPEM, nil
}
