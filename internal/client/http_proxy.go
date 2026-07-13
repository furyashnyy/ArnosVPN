package client

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"time"
)

// handleHTTPConnect is a minimal forward proxy: CONNECT for HTTPS (the common
// case) and absolute-URI forwarding for plain HTTP, all dialed through the
// tunnel via the netstack dialer.
func handleHTTPConnect(ctx context.Context, c net.Conn, dialer netDialer) {
	defer c.Close()
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}

	if req.Method == http.MethodConnect {
		dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		remote, err := dialer.DialContext(dctx, "tcp", withPort(req.Host, "443"))
		cancel()
		if err != nil {
			_, _ = c.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
			return
		}
		defer remote.Close()
		if _, err := c.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
			return
		}
		relay(c, remote)
		return
	}

	// Plain HTTP: forward to the origin server.
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	remote, err := dialer.DialContext(dctx, "tcp", withPort(req.Host, "80"))
	cancel()
	if err != nil {
		_, _ = c.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		return
	}
	defer remote.Close()
	req.RequestURI = ""
	if err := req.Write(remote); err != nil {
		return
	}
	relay(c, remote)
}

// withPort ensures host has a port, appending def if it lacks one.
func withPort(host, def string) string {
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}
	return net.JoinHostPort(host, def)
}
