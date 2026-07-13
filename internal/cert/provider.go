// Package cert supplies the TLS certificate for the HTTPS listener. Two
// providers are supported, selected by the CERT_PROVIDER environment variable:
//
//   - "traefik" (default): reuse the certificate Traefik already manages on a
//     Coolify host, read either from Traefik's acme.json or from a mounted
//     PEM cert/key pair. ArnosVPN never touches ACME in this mode.
//   - "letsencrypt": obtain and renew a certificate directly from Let's
//     Encrypt using the ACME TLS-ALPN-01 challenge on the same :443 listener.
//
// Both providers return a *tls.Config whose GetCertificate hook always serves
// the freshest certificate, so renewals (by Traefik or by autocert) are picked
// up without a restart.
package cert

import (
	"crypto/tls"
	"fmt"

	"arnosvpn/internal/config"
)

// Provider builds a tls.Config for the listener.
type Provider interface {
	TLSConfig() (*tls.Config, error)
	// Describe returns a human-readable summary for startup logs.
	Describe() string
}

// New selects a provider from the resolved configuration.
func New(c *config.Config) (Provider, error) {
	switch c.CertProvider {
	case config.CertTraefik:
		return newTraefik(c)
	case config.CertLetsEncrypt:
		return newLetsEncrypt(c)
	default:
		return nil, fmt.Errorf("unknown cert provider %q", c.CertProvider)
	}
}

// baseTLS returns hardened defaults shared by every provider. ALPN advertises
// only HTTP/1.1: the tunnel relies on a WebSocket Upgrade, which requires a
// hijackable HTTP/1.1 connection (HTTP/2 cannot be hijacked). HTTP/1.1-only is
// still a completely ordinary HTTPS fingerprint.
func baseTLS() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"http/1.1"},
	}
}
