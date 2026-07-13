package server

import (
	"fmt"

	"github.com/songgao/water"
)

// tunDevice wraps a Linux TUN interface. The server reads IP packets destined
// for tunnel clients here and writes packets received from clients back in.
type tunDevice struct {
	iface *water.Interface
	name  string
	mtu   int
}

func openTUN(name string, mtu int) (*tunDevice, error) {
	cfg := water.Config{DeviceType: water.TUN}
	if name != "" {
		cfg.Name = name
	}
	iface, err := water.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("open tun: %w", err)
	}
	return &tunDevice{iface: iface, name: iface.Name(), mtu: mtu}, nil
}

func (t *tunDevice) Name() string { return t.name }

func (t *tunDevice) Read(p []byte) (int, error)  { return t.iface.Read(p) }
func (t *tunDevice) Write(p []byte) (int, error) { return t.iface.Write(p) }
func (t *tunDevice) Close() error                { return t.iface.Close() }
