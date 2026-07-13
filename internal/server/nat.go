package server

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
)

// netConfig programs the host so tunnelled traffic leaves with the server's
// own public IP. This is what makes a client's apparent IP become the
// server's: packets from the 10.66.0.0/24 tunnel are source-NATed
// (MASQUERADE) onto the WAN interface.
//
// All steps are idempotent and reversible; teardown() undoes them. Errors are
// wrapped with the exact command so failures are diagnosable in logs.
type netConfig struct {
	tunName   string
	tunAddr   string // e.g. 10.66.0.1
	tunCIDR   string // e.g. 10.66.0.0/24
	prefixLen int
	wanIface  string
	applied   []func() error
}

func configureNetwork(tunName, gateway, tunCIDR string, prefixLen int, wanIface string) (*netConfig, error) {
	if wanIface == "" {
		iface, err := defaultRouteInterface()
		if err != nil {
			return nil, fmt.Errorf("detect WAN interface: %w", err)
		}
		wanIface = iface
	}
	n := &netConfig{
		tunName:   tunName,
		tunAddr:   gateway,
		tunCIDR:   tunCIDR,
		prefixLen: prefixLen,
		wanIface:  wanIface,
	}
	if err := n.apply(); err != nil {
		n.Teardown()
		return nil, err
	}
	return n, nil
}

func (n *netConfig) apply() error {
	// 1. Bring the TUN interface up with the gateway address.
	if err := run("ip", "addr", "add", fmt.Sprintf("%s/%d", n.tunAddr, n.prefixLen), "dev", n.tunName); err != nil {
		return err
	}
	if err := run("ip", "link", "set", "dev", n.tunName, "up"); err != nil {
		return err
	}

	// 2. Enable IPv4 forwarding.
	if err := writeSysctl("/proc/sys/net/ipv4/ip_forward", "1"); err != nil {
		return err
	}

	// 3. Masquerade tunnel traffic onto the WAN interface, and allow forwarding
	//    both directions. -C tests for an existing rule to stay idempotent.
	if err := ensureRule("nat", "POSTROUTING",
		"-s", n.tunCIDR, "-o", n.wanIface, "-j", "MASQUERADE"); err != nil {
		return err
	}
	n.applied = append(n.applied, func() error {
		return run("iptables", "-t", "nat", "-D", "POSTROUTING",
			"-s", n.tunCIDR, "-o", n.wanIface, "-j", "MASQUERADE")
	})

	if err := ensureRule("filter", "FORWARD",
		"-i", n.tunName, "-o", n.wanIface, "-j", "ACCEPT"); err != nil {
		return err
	}
	n.applied = append(n.applied, func() error {
		return run("iptables", "-D", "FORWARD",
			"-i", n.tunName, "-o", n.wanIface, "-j", "ACCEPT")
	})

	if err := ensureRule("filter", "FORWARD",
		"-i", n.wanIface, "-o", n.tunName,
		"-m", "state", "--state", "RELATED,ESTABLISHED", "-j", "ACCEPT"); err != nil {
		return err
	}
	n.applied = append(n.applied, func() error {
		return run("iptables", "-D", "FORWARD",
			"-i", n.wanIface, "-o", n.tunName,
			"-m", "state", "--state", "RELATED,ESTABLISHED", "-j", "ACCEPT")
	})

	// 4. Clamp TCP MSS to the tunnel MTU so large flows don't black-hole on
	//    paths that drop the needed ICMP "fragmentation needed".
	if err := ensureRule("mangle", "FORWARD",
		"-o", n.tunName, "-p", "tcp", "--tcp-flags", "SYN,RST", "SYN",
		"-j", "TCPMSS", "--clamp-mss-to-pmtu"); err != nil {
		return err
	}
	n.applied = append(n.applied, func() error {
		return run("iptables", "-t", "mangle", "-D", "FORWARD",
			"-o", n.tunName, "-p", "tcp", "--tcp-flags", "SYN,RST", "SYN",
			"-j", "TCPMSS", "--clamp-mss-to-pmtu")
	})

	return nil
}

// Teardown reverses everything apply() added, best-effort.
func (n *netConfig) Teardown() {
	for i := len(n.applied) - 1; i >= 0; i-- {
		_ = n.applied[i]()
	}
	n.applied = nil
}

// WANInterface reports the interface outbound traffic is masqueraded onto.
func (n *netConfig) WANInterface() string { return n.wanIface }

// ensureRule appends an iptables rule only if an identical one is absent.
func ensureRule(table, chain string, spec ...string) error {
	check := append([]string{"-t", table, "-C", chain}, spec...)
	if run("iptables", check...) == nil {
		return nil // already present
	}
	add := append([]string{"-t", table, "-A", chain}, spec...)
	return run("iptables", add...)
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func writeSysctl(path, value string) error {
	if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// defaultRouteInterface parses /proc/net/route for the interface owning the
// default route (destination 00000000).
func defaultRouteInterface() (string, error) {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return "", err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Scan() // header
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[1] == "00000000" {
			return fields[0], nil
		}
	}
	// Fall back to the first non-loopback interface with an address.
	ifaces, _ := net.Interfaces()
	for _, i := range ifaces {
		if i.Flags&net.FlagLoopback != 0 || i.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, _ := i.Addrs()
		if len(addrs) > 0 {
			return i.Name, nil
		}
	}
	return "", fmt.Errorf("no default route interface found")
}
