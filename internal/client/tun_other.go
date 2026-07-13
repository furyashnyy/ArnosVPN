//go:build !linux && !windows

package client

import "fmt"

// configureTUN is unsupported on this OS. Use proxy mode (`--mode proxy`), which
// needs no TUN adapter or routing changes.
func configureTUN(name, localIP string, mask int, dns []string, serverIP string) (func(), error) {
	return nil, fmt.Errorf("TUN mode is not supported on this OS; use --mode proxy")
}
