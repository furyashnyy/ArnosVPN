//go:build linux

package client

import (
	"fmt"
	"os/exec"
	"strings"
)

// configureTUN programs addressing and routing on Linux via the `ip` tool.
func configureTUN(name, localIP string, mask int, dns []string, serverIP string) (func(), error) {
	// Discover the current default route so we can (a) pin a host route to the
	// server through it and (b) restore sane behaviour on teardown.
	gw, dev, err := defaultRoute()
	if err != nil {
		return nil, err
	}

	steps := [][]string{
		{"ip", "addr", "add", fmt.Sprintf("%s/%d", localIP, mask), "dev", name},
		{"ip", "link", "set", "dev", name, "up"},
		// Keep the carrier connection off the tunnel.
		{"ip", "route", "add", serverIP + "/32", "via", gw, "dev", dev},
		// Default via the tunnel (two /1 routes beat the existing default).
		{"ip", "route", "add", "0.0.0.0/1", "dev", name},
		{"ip", "route", "add", "128.0.0.0/1", "dev", name},
	}
	var undo [][]string
	for _, s := range steps {
		if err := run(s...); err != nil {
			cleanup(undo)
			return nil, err
		}
		undo = append(undo, delRoute(s))
	}
	return func() { cleanup(undo) }, nil
}

func delRoute(add []string) []string {
	// Turn an "add" command into its "del" counterpart for teardown.
	out := make([]string, len(add))
	copy(out, add)
	for i, a := range out {
		if a == "add" {
			out[i] = "del"
		}
	}
	return out
}

func cleanup(cmds [][]string) {
	for i := len(cmds) - 1; i >= 0; i-- {
		_ = run(cmds[i]...)
	}
}

func run(args ...string) error {
	out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// defaultRoute returns the gateway IP and interface of the current default route.
func defaultRoute() (gw, dev string, err error) {
	out, err := exec.Command("ip", "route", "show", "default").Output()
	if err != nil {
		return "", "", err
	}
	// e.g. "default via 192.168.1.1 dev eth0 proto dhcp metric 100"
	fields := strings.Fields(string(out))
	for i := 0; i < len(fields)-1; i++ {
		switch fields[i] {
		case "via":
			gw = fields[i+1]
		case "dev":
			dev = fields[i+1]
		}
	}
	if gw == "" || dev == "" {
		return "", "", fmt.Errorf("could not parse default route: %q", string(out))
	}
	return gw, dev, nil
}
