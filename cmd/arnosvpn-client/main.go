// Command arnosvpn-client is the cross-platform (Windows/Linux) ArnosVPN
// desktop client. It manages multiple servers and connects in one of two modes:
//
//	--mode proxy  local SOCKS5 + HTTP proxy on 127.0.0.1 (no admin, portable)
//	--mode tun    system-wide TUN adapter, all traffic exits via the server
//
// Every connection uses a fresh, browser-shaped fingerprint (random path,
// rotating User-Agent, random per-frame padding), so no two sessions look alike.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"

	"arnosvpn/internal/client"
)

func main() {
	cfgPath := client.DefaultConfigPath()

	// No arguments (e.g. a double-click on the executable) opens the graphical
	// control panel — the desktop "app" — rather than printing CLI usage to a
	// console window. The full CLI stays available for `arnosvpn-client <cmd>`;
	// on Windows the GUI-linked binary reattaches to the parent console so those
	// commands still print.
	if len(os.Args) < 2 {
		cmdGUI(cfgPath, nil)
		return
	}
	attachParentConsole()

	switch os.Args[1] {
	case "list", "ls":
		cmdList(cfgPath)
	case "add":
		cmdAdd(cfgPath, os.Args[2:])
	case "rm", "remove":
		cmdRemove(cfgPath, os.Args[2:])
	case "use":
		cmdUse(cfgPath, os.Args[2:])
	case "connect", "up":
		cmdConnect(cfgPath, os.Args[2:])
	case "gui":
		cmdGUI(cfgPath, os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `arnosvpn-client — ArnosVPN desktop client (Windows/Linux)

Usage:
  arnosvpn-client add <arnos://connect?...> [name]   save a server
  arnosvpn-client list                               list saved servers
  arnosvpn-client use <name>                         set the active server
  arnosvpn-client rm <name>                          remove a server
  arnosvpn-client connect [name] [flags]             connect
  arnosvpn-client gui [--addr 127.0.0.1:7654]        open the graphical panel

Connect flags:
  --mode  proxy|tun   proxy = local SOCKS5+HTTP (default); tun = system-wide
  --socks addr        SOCKS5 listen address (default 127.0.0.1:1080)
  --http  addr        HTTP proxy listen address (default 127.0.0.1:8080)
  --iface name        TUN adapter name (tun mode; default "arnos0")

Servers are stored at:
  `+client.DefaultConfigPath()+`
`)
}

func load(path string) *client.Config {
	c, err := client.LoadConfig(path)
	if err != nil {
		fatal(err)
	}
	return c
}

func cmdList(path string) {
	c := load(path)
	if len(c.Servers) == 0 {
		fmt.Println("no servers yet — add one with: arnosvpn-client add <arnos://...>")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "ACTIVE\tNAME\tENDPOINT")
	for _, s := range c.Servers {
		active := " "
		if s.Name == c.Active {
			active = "*"
		}
		endpoint := s.Name
		if p, err := c.Profile(s.Name); err == nil {
			endpoint = fmt.Sprintf("%s:%d", p.Host, p.Port)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", active, s.Name, endpoint)
	}
	_ = w.Flush()
}

func cmdAdd(path string, args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: add <arnos://connect?...> [name]"))
	}
	name := ""
	if len(args) >= 2 {
		name = args[1]
	}
	c := load(path)
	if err := c.Add(name, args[0]); err != nil {
		fatal(err)
	}
	if err := c.Save(path); err != nil {
		fatal(err)
	}
	fmt.Println("saved. active server:", c.Active)
}

func cmdRemove(path string, args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: rm <name>"))
	}
	c := load(path)
	c.Remove(args[0])
	if err := c.Save(path); err != nil {
		fatal(err)
	}
	fmt.Println("removed", args[0])
}

func cmdUse(path string, args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: use <name>"))
	}
	c := load(path)
	if _, err := c.Profile(args[0]); err != nil {
		fatal(err)
	}
	c.Active = args[0]
	if err := c.Save(path); err != nil {
		fatal(err)
	}
	fmt.Println("active server:", c.Active)
}

func cmdConnect(path string, args []string) {
	fs := flag.NewFlagSet("connect", flag.ExitOnError)
	mode := fs.String("mode", "proxy", "proxy|tun")
	socks := fs.String("socks", "127.0.0.1:1080", "SOCKS5 listen address")
	httpAddr := fs.String("http", "127.0.0.1:8080", "HTTP proxy listen address")
	iface := fs.String("iface", "arnos0", "TUN adapter name (tun mode)")

	// Allow an optional server name before the flags.
	name := ""
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		name = args[0]
		args = args[1:]
	}
	_ = fs.Parse(args)

	c := load(path)
	profile, err := c.Profile(name)
	if err != nil {
		fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	fmt.Printf("connecting to %s:%d (%s mode)…\n", profile.Host, profile.Port, *mode)
	tunnel, err := client.Connect(ctx, profile)
	if err != nil {
		fatal(err)
	}
	defer tunnel.Close()
	fmt.Printf("connected — assigned %s/%d\n", tunnel.LocalIP, tunnel.Mask)

	switch *mode {
	case "proxy":
		fmt.Printf("set your system/browser proxy to SOCKS5 %s or HTTP %s\n", *socks, *httpAddr)
		err = client.RunProxy(ctx, tunnel, *socks, *httpAddr)
	case "tun":
		serverIP := resolve(profile.Host)
		err = client.RunTUN(ctx, tunnel, serverIP, *iface)
	default:
		fatal(fmt.Errorf("unknown mode %q (use proxy or tun)", *mode))
	}
	if err != nil {
		fatal(err)
	}
	fmt.Println("disconnected.")
}

func cmdGUI(path string, args []string) {
	fs := flag.NewFlagSet("gui", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:7654", "control-panel listen address")
	_ = fs.Parse(args)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	fmt.Println("opening the ArnosVPN control panel…")
	if err := client.RunGUI(ctx, path, *addr); err != nil {
		fatal(err)
	}
}

// resolve returns the first IP for host, or host itself if it's already an IP.
func resolve(host string) string {
	if net.ParseIP(host) != nil {
		return host
	}
	ips, err := net.LookupHost(host)
	if err != nil || len(ips) == 0 {
		fatal(fmt.Errorf("resolve %s: %v", host, err))
	}
	return ips[0]
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
