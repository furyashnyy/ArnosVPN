//go:build windows

package client

import (
	"fmt"
	"os/exec"
	"strings"
)

// configureTUN programs a wintun adapter on Windows via netsh/route. Best
// effort: run the client as Administrator. If routing misbehaves on your setup,
// prefer `--mode proxy`, which needs no adapter or routes.
func configureTUN(name, localIP string, mask int, dns []string, serverIP string) (func(), error) {
	origGW, err := defaultGateway()
	if err != nil {
		return nil, err
	}
	netmask := maskToDotted(mask)

	steps := [][]string{
		{"netsh", "interface", "ip", "set", "address", "name=" + name, "static", localIP, netmask},
		// Pin the carrier to the real gateway so it doesn't loop through the VPN.
		{"route", "add", serverIP, "mask", "255.255.255.255", origGW, "metric", "1"},
		// Default via the tunnel adapter (two /1 routes override the default).
		{"netsh", "interface", "ipv4", "add", "route", "0.0.0.0/1", name},
		{"netsh", "interface", "ipv4", "add", "route", "128.0.0.0/1", name},
	}
	var undo [][]string
	for _, s := range steps {
		if err := run(s...); err != nil {
			winCleanup(undo)
			return nil, err
		}
		undo = append(undo, winUndo(s, serverIP))
	}

	for _, d := range dns {
		_ = run("netsh", "interface", "ip", "add", "dns", "name="+name, d, "validate=no")
	}

	return func() { winCleanup(undo) }, nil
}

func winUndo(add []string, serverIP string) []string {
	switch add[0] {
	case "route":
		return []string{"route", "delete", serverIP}
	case "netsh":
		out := append([]string{}, add...)
		for i, a := range out {
			if a == "add" {
				out[i] = "delete"
			}
		}
		return out
	}
	return nil
}

func winCleanup(cmds [][]string) {
	for i := len(cmds) - 1; i >= 0; i-- {
		if cmds[i] != nil {
			_ = run(cmds[i]...)
		}
	}
}

func run(args ...string) error {
	out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// defaultGateway returns the current IPv4 default-route next hop.
func defaultGateway() (string, error) {
	out, err := exec.Command("powershell", "-NoProfile", "-Command",
		"(Get-NetRoute -DestinationPrefix 0.0.0.0/0 | Sort-Object RouteMetric | Select-Object -First 1).NextHop").Output()
	if err != nil {
		return "", fmt.Errorf("find default gateway: %w", err)
	}
	gw := strings.TrimSpace(string(out))
	if gw == "" {
		return "", fmt.Errorf("no default gateway found")
	}
	return gw, nil
}

func maskToDotted(prefix int) string {
	m := uint32(0xffffffff) << (32 - prefix)
	return fmt.Sprintf("%d.%d.%d.%d", byte(m>>24), byte(m>>16), byte(m>>8), byte(m))
}
