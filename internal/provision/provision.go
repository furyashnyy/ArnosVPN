// Package provision turns server configuration into a single connect string
// (and QR code) that fully configures a client. This is the "no fiddling with
// configs" path: the operator copies one URL or scans one QR, and the client
// has everything it needs — endpoint, SNI, path and key.
package provision

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

// Profile is the full set of parameters a client needs to connect.
type Profile struct {
	Host string // domain the client dials and validates the cert against
	Port int
	Path string // WebSocket path
	SNI  string // TLS SNI (defaults to Host)
	PSK  []byte // 32-byte pre-shared key
	Name string // optional label
}

// Scheme is the custom URI scheme the Android app registers for deep links.
const Scheme = "arno"

// URI encodes the profile as arno://connect?host=...&port=...&path=...&psk=...
// The PSK is base64url without padding so it is URL- and QR-safe.
func (p Profile) URI() string {
	sni := p.SNI
	if sni == "" {
		sni = p.Host
	}
	q := url.Values{}
	q.Set("host", p.Host)
	q.Set("port", strconv.Itoa(p.Port))
	q.Set("path", p.Path)
	q.Set("sni", sni)
	q.Set("psk", base64.RawURLEncoding.EncodeToString(p.PSK))
	if p.Name != "" {
		q.Set("name", p.Name)
	}
	return fmt.Sprintf("%s://connect?%s", Scheme, q.Encode())
}

// ParseURI decodes a profile produced by URI.
func ParseURI(s string) (Profile, error) {
	var p Profile
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil {
		return p, err
	}
	if u.Scheme != Scheme {
		return p, fmt.Errorf("not an %s:// URI", Scheme)
	}
	q := u.Query()
	p.Host = q.Get("host")
	p.Path = q.Get("path")
	p.SNI = q.Get("sni")
	p.Name = q.Get("name")
	if p.Host == "" {
		return p, fmt.Errorf("missing host")
	}
	if p.Path == "" {
		p.Path = "/"
	}
	if p.SNI == "" {
		p.SNI = p.Host
	}
	p.Port = 443
	if v := q.Get("port"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			p.Port = n
		}
	}
	psk, err := base64.RawURLEncoding.DecodeString(q.Get("psk"))
	if err != nil {
		return p, fmt.Errorf("bad psk: %w", err)
	}
	p.PSK = psk
	return p, nil
}

// QRString renders the connect URI as a QR code drawn with block characters,
// suitable for scanning straight from a terminal.
func (p Profile) QRString() (string, error) {
	q, err := qrcode.New(p.URI(), qrcode.Medium)
	if err != nil {
		return "", err
	}
	return q.ToSmallString(false), nil
}

// QRPNG renders the connect URI to a PNG of the given pixel size.
func (p Profile) QRPNG(size int) ([]byte, error) {
	return qrcode.Encode(p.URI(), qrcode.Medium, size)
}
