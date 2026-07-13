package client

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"strconv"
	"time"

	"golang.zx2c4.com/wireguard/tun/netstack"
)

// netDialer is satisfied by netstack's *Net: it dials TCP through the userspace
// stack, whose packets ride the ArnosVPN tunnel.
type netDialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// RunProxy runs the tunnel entirely in userspace and exposes it as local SOCKS5
// and/or HTTP CONNECT proxies. No TUN device, admin rights, or routing changes
// are needed — point your browser/system proxy at 127.0.0.1 and traffic exits
// from the server. This is the portable, privilege-free mode.
func RunProxy(ctx context.Context, t *Tunnel, socksAddr, httpAddr string) error {
	localIP, err := netip.ParseAddr(t.LocalIP)
	if err != nil {
		return fmt.Errorf("assigned IP %q: %w", t.LocalIP, err)
	}
	var dns []netip.Addr
	for _, d := range t.DNS {
		if a, err := netip.ParseAddr(d); err == nil {
			dns = append(dns, a)
		}
	}

	dev, tnet, err := netstack.CreateNetTUN([]netip.Addr{localIP}, dns, t.MTU)
	if err != nil {
		return fmt.Errorf("create netstack: %w", err)
	}

	done := make(chan struct{})
	go t.KeepAlive(20*time.Second, done)

	errc := make(chan error, 3)
	go func() { errc <- pump(dev, t) }()

	if socksAddr != "" {
		ln, err := net.Listen("tcp", socksAddr)
		if err != nil {
			return fmt.Errorf("listen socks %s: %w", socksAddr, err)
		}
		log.Printf("SOCKS5 proxy on %s -> %s", socksAddr, hostOf(t))
		go func() { errc <- acceptLoop(ctx, ln, tnet, handleSOCKS) }()
	}
	if httpAddr != "" {
		ln, err := net.Listen("tcp", httpAddr)
		if err != nil {
			return fmt.Errorf("listen http %s: %w", httpAddr, err)
		}
		log.Printf("HTTP proxy on %s -> %s", httpAddr, hostOf(t))
		go func() { errc <- acceptLoop(ctx, ln, tnet, handleHTTPConnect) }()
	}

	select {
	case <-ctx.Done():
		close(done)
		return nil
	case err := <-errc:
		close(done)
		return err
	}
}

func hostOf(t *Tunnel) string { return t.Gateway }

type connHandler func(ctx context.Context, c net.Conn, dialer netDialer)

func acceptLoop(ctx context.Context, ln net.Listener, dialer netDialer, h connHandler) error {
	defer ln.Close()
	for {
		c, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return err
			}
		}
		go h(ctx, c, dialer)
	}
}

// --- SOCKS5 (RFC 1928, no-auth, CONNECT) --------------------------------------

func handleSOCKS(ctx context.Context, c net.Conn, dialer netDialer) {
	defer c.Close()
	br := make([]byte, 262)

	// Greeting: VER, NMETHODS, METHODS...
	if _, err := io.ReadFull(c, br[:2]); err != nil || br[0] != 0x05 {
		return
	}
	n := int(br[1])
	if _, err := io.ReadFull(c, br[:n]); err != nil {
		return
	}
	// Reply: no authentication required.
	if _, err := c.Write([]byte{0x05, 0x00}); err != nil {
		return
	}

	// Request: VER, CMD, RSV, ATYP, ADDR, PORT
	if _, err := io.ReadFull(c, br[:4]); err != nil || br[0] != 0x05 {
		return
	}
	if br[1] != 0x01 { // only CONNECT
		_, _ = c.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	host, err := readSOCKSAddr(c, br[3], br)
	if err != nil {
		return
	}
	if _, err := io.ReadFull(c, br[:2]); err != nil {
		return
	}
	port := binary.BigEndian.Uint16(br[:2])

	target := net.JoinHostPort(host, strconv.Itoa(int(port)))
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	remote, err := dialer.DialContext(dctx, "tcp", target)
	cancel()
	if err != nil {
		_, _ = c.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0}) // conn refused
		return
	}
	defer remote.Close()
	// Success reply (bound addr 0.0.0.0:0).
	if _, err := c.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	relay(c, remote)
}

func readSOCKSAddr(c net.Conn, atyp byte, buf []byte) (string, error) {
	switch atyp {
	case 0x01: // IPv4
		if _, err := io.ReadFull(c, buf[:4]); err != nil {
			return "", err
		}
		return net.IP(buf[:4]).String(), nil
	case 0x04: // IPv6
		if _, err := io.ReadFull(c, buf[:16]); err != nil {
			return "", err
		}
		return net.IP(buf[:16]).String(), nil
	case 0x03: // domain
		if _, err := io.ReadFull(c, buf[:1]); err != nil {
			return "", err
		}
		l := int(buf[0])
		if _, err := io.ReadFull(c, buf[:l]); err != nil {
			return "", err
		}
		return string(buf[:l]), nil
	}
	return "", fmt.Errorf("unsupported atyp %d", atyp)
}

// relay copies bidirectionally until either side closes.
func relay(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(a, b); done <- struct{}{} }()
	go func() { _, _ = io.Copy(b, a); done <- struct{}{} }()
	<-done
}
