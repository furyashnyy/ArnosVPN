// Command arnovpn-server runs the ArnoVPN tunnel server. It configures itself
// from the environment, generating a pre-shared key on first run, obtains its
// TLS certificate from Traefik (default) or Let's Encrypt, brings up the TUN
// device with NAT, and prints a connect URI + QR the moment it is ready.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/furyashnyy/arnosvpn/internal/cert"
	"github.com/furyashnyy/arnosvpn/internal/config"
	"github.com/furyashnyy/arnosvpn/internal/provision"
	"github.com/furyashnyy/arnosvpn/internal/server"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[arnovpn] ")

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	provider, err := cert.New(cfg)
	if err != nil {
		log.Fatalf("cert provider: %v", err)
	}
	tlsConf, err := provider.TLSConfig()
	if err != nil {
		log.Fatalf("tls config: %v", err)
	}

	srv, err := server.New(cfg)
	if err != nil {
		log.Fatalf("server: %v", err)
	}
	defer srv.Close()

	printProfile(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := srv.Run(ctx, tlsConf, provider.Describe()); err != nil {
		log.Fatalf("serve: %v", err)
	}
	log.Printf("shutdown complete")
	os.Exit(0)
}

// printProfile emits the one-scan connect profile so a client can be set up
// without touching any config files.
func printProfile(cfg *config.Config) {
	host := cfg.Domain
	if host == "" {
		host = "<your-domain>"
	}
	p := provision.Profile{
		Host: host,
		Port: 443,
		Path: cfg.WSPath,
		SNI:  host,
		PSK:  cfg.PSK,
		Name: "arnovpn",
	}
	log.Printf("cert provider: %s", cfg.CertProvider)
	log.Printf("connect URI: %s", p.URI())
	if qr, err := p.QRString(); err == nil {
		log.Printf("scan to configure a client:\n%s", qr)
	}
	if host == "<your-domain>" {
		log.Printf("note: set ARNO_DOMAIN so the printed profile has a real host")
	}
}
