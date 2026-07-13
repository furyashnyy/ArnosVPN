// Command arnosvpnctl is a small helper for operators: it prints the connect
// profile (URI, QR, or PNG) for the current server configuration and can
// generate a fresh pre-shared key. It reads the same environment/state as the
// server, so "arnosvpnctl uri" always matches what the running server accepts.
package main

import (
	"encoding/base64"
	"fmt"
	"os"

	"arnosvpn/internal/config"
	"arnosvpn/internal/protocol"
	"arnosvpn/internal/provision"
)

func usage() {
	fmt.Fprintf(os.Stderr, `arnosvpnctl — ArnosVPN operator helper

Usage:
  arnosvpnctl uri            print the connect URI
  arnosvpnctl qr             print a scannable QR code
  arnosvpnctl png <file>     write the connect QR to a PNG file
  arnosvpnctl genpsk         print a fresh base64 pre-shared key
  arnosvpnctl show           print the resolved configuration

Configuration is read from the same environment variables as the server
(ARNOS_DOMAIN, ARNOS_WS_PATH, ARNOS_PSK / state file, ...).
`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "genpsk":
		psk, err := protocol.RandBytes(protocol.PSKLen)
		must(err)
		fmt.Println(base64.StdEncoding.EncodeToString(psk))
		return
	case "-h", "--help", "help":
		usage()
		return
	}

	cfg, err := config.Load()
	must(err)
	host := cfg.PublicHost
	if host == "" {
		host = "<your-domain>"
	}
	profile := provision.Profile{
		Host: host,
		Port: cfg.PublicPort,
		Path: cfg.WSPath,
		SNI:  host,
		PSK:  cfg.PSK,
		Name: "arnosvpn",
	}

	switch os.Args[1] {
	case "uri":
		fmt.Println(profile.URI())
	case "qr":
		qr, err := profile.QRString()
		must(err)
		fmt.Println(qr)
	case "png":
		if len(os.Args) < 3 {
			usage()
			os.Exit(2)
		}
		png, err := profile.QRPNG(512)
		must(err)
		must(os.WriteFile(os.Args[2], png, 0o644))
		fmt.Printf("wrote %s\n", os.Args[2])
	case "show":
		fmt.Printf("domain:        %s\n", cfg.Domain)
		fmt.Printf("public:        %s:%d\n", cfg.PublicHost, cfg.PublicPort)
		fmt.Printf("tls mode:      %s\n", cfg.TLSMode)
		fmt.Printf("listen:        %s\n", cfg.ListenAddr)
		fmt.Printf("ws path:       %s\n", cfg.WSPath)
		fmt.Printf("cert provider: %s\n", cfg.CertProvider)
		fmt.Printf("tunnel cidr:   %s\n", cfg.TunnelCIDR)
		fmt.Printf("gateway:       %s\n", cfg.Gateway)
		fmt.Printf("mtu:           %d\n", cfg.MTU)
		fmt.Printf("psk (base64):  %s\n", cfg.PSKBase64())
	default:
		usage()
		os.Exit(2)
	}
}

func must(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
