package cert

import (
	"crypto/tls"
	"fmt"
	"os"

	"arnosvpn/internal/config"
	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

// letsEncryptProvider obtains and renews a certificate directly from Let's
// Encrypt. It uses the TLS-ALPN-01 challenge, which is answered inline on the
// same :443 listener, so no extra ports or web root are required.
type letsEncryptProvider struct {
	mgr    *autocert.Manager
	domain string
}

func newLetsEncrypt(c *config.Config) (Provider, error) {
	if err := os.MkdirAll(c.ACMECacheDir, 0o700); err != nil {
		return nil, fmt.Errorf("acme cache dir: %w", err)
	}
	mgr := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(c.Domain),
		Cache:      autocert.DirCache(c.ACMECacheDir),
		Email:      c.ACMEEmail,
	}
	return &letsEncryptProvider{mgr: mgr, domain: c.Domain}, nil
}

func (p *letsEncryptProvider) Describe() string {
	return fmt.Sprintf("letsencrypt (domain=%s)", p.domain)
}

func (p *letsEncryptProvider) TLSConfig() (*tls.Config, error) {
	tc := baseTLS()
	tc.GetCertificate = p.mgr.GetCertificate
	// acme-tls/1 lets autocert answer the TLS-ALPN-01 challenge on this listener.
	tc.NextProtos = append([]string{acme.ALPNProto}, tc.NextProtos...)
	return tc, nil
}
