// Command arnosvpn-server runs the ArnosVPN tunnel server. It configures itself
// from the environment, generating a pre-shared key on first run, brings up the
// TUN device with NAT, and prints a connect URI + QR the moment it is ready.
//
// TLS is handled one of two ways (ARNOS_TLS_MODE):
//   - proxy (default): an upstream reverse proxy — Coolify's Traefik —
//     terminates TLS and forwards the WebSocket to ArnosVPN over an internal
//     port. Use this when 443 is already taken and you route by domain.
//   - self: ArnosVPN terminates TLS itself using Traefik's certificate or
//     Let's Encrypt.
package main

import (
	"context"
	"crypto/tls"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"arnosvpn/internal/cert"
	"arnosvpn/internal/config"
	"arnosvpn/internal/provision"
	"arnosvpn/internal/server"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[arnosvpn] ")

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	// Build the TLS config only when we terminate TLS ourselves. In proxy mode
	// the reverse proxy owns the certificate, so we never touch acme.json/ACME.
	var tlsConf *tls.Config
	description := "proxy mode — TLS terminated by upstream reverse proxy"
	if cfg.TLSMode == config.TLSSelf {
		provider, err := cert.New(cfg)
		if err != nil {
			log.Fatalf("cert provider: %v", err)
		}
		if tlsConf, err = provider.TLSConfig(); err != nil {
			log.Fatalf("tls config: %v", err)
		}
		description = "self mode — " + provider.Describe()
	}

	ln, err := cfg.ResolveListener()
	if err != nil {
		log.Fatalf("listen: %v", err)
	}

	srv, err := server.New(cfg)
	if err != nil {
		log.Fatalf("server: %v", err)
	}
	defer srv.Close()

	printProfile(cfg, ln.Addr())

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := srv.Serve(ctx, ln, tlsConf, description); err != nil {
		log.Fatalf("serve: %v", err)
	}
	log.Printf("shutdown complete")
	os.Exit(0)
}

// printProfile emits the one-scan connect profile so a client can be set up
// without touching any config files. The profile always points at the public
// endpoint (PublicHost:PublicPort) — what clients dial through the proxy — not
// the internal bind address.
func printProfile(cfg *config.Config, bind net.Addr) {
	host := cfg.PublicHost
	if host == "" {
		host = "<your-domain>"
	}
	p := provision.Profile{
		Host: host,
		Port: cfg.PublicPort,
		Path: cfg.WSPath,
		SNI:  host,
		PSK:  cfg.PSK,
		Name: "arnosvpn",
	}
	log.Printf("tls mode: %s", cfg.TLSMode)
	log.Printf("internal bind: %s (expose this port in Coolify)", bind)
	log.Printf("connect URI: %s", p.URI())
	if qr, err := p.QRString(); err == nil {
		log.Printf("scan to configure a client:\n%s", qr)
	}
	if host == "<your-domain>" {
		log.Printf("note: set ARNOS_DOMAIN (or ARNOS_PUBLIC_HOST) so the profile has a real host")
	}
}
