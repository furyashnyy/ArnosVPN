package client

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

func newHTTPGet(rawURL string) (*http.Request, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	// Only fetch subscriptions over HTTP(S). Blocks file://, gopher://, etc.,
	// which http.NewRequest would otherwise accept for some schemes.
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return nil, fmt.Errorf("subscription URL must be http(s), got %q", req.URL.Scheme)
	}
	req.Header.Set("User-Agent", "ArnosVPN/desktop")
	return req, nil
}

func fetchBody(req *http.Request) ([]byte, error) {
	client := &http.Client{
		Timeout:   15 * time.Second,
		Transport: safeTransport(),
		// Re-validate the scheme and destination on every redirect hop, so a
		// crafted redirect cannot bounce the fetch to an internal address.
		CheckRedirect: func(r *http.Request, _ []*http.Request) error {
			if r.URL.Scheme != "http" && r.URL.Scheme != "https" {
				return fmt.Errorf("refusing redirect to %q", r.URL.Scheme)
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20)) // 4 MiB cap
}

// safeTransport is an http.Transport whose dialer refuses to connect to
// loopback, private, link-local or otherwise non-public addresses. This turns
// the subscription fetch (which takes a user-supplied URL) into a poor SSRF
// primitive: it can only reach public hosts.
func safeTransport() *http.Transport {
	d := &net.Dialer{Timeout: 10 * time.Second}
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				if !isPublicIP(ip.IP) {
					return nil, fmt.Errorf("refusing to connect to non-public address %s", ip.IP)
				}
			}
			// Dial the first vetted IP directly so the connection cannot race a
			// re-resolution to a different (internal) address.
			return d.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
		},
	}
}

// isPublicIP reports whether ip is a routable public address (not loopback,
// private, link-local, multicast or unspecified).
func isPublicIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	return true
}

// maybeBase64 decodes a whole-body base64 blob (common for subscription feeds).
func maybeBase64(s string) (string, bool) {
	t := strings.TrimSpace(s)
	if strings.Contains(t, "arnos://") {
		return "", false // already plain text
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding} {
		if b, err := enc.DecodeString(t); err == nil && strings.Contains(string(b), "arnos://") {
			return string(b), true
		}
	}
	return "", false
}
