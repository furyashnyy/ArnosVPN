package client

import (
	"context"
	"fmt"
	"log"
	"time"

	"golang.zx2c4.com/wireguard/tun"
)

// RunTUN brings the tunnel up as a system-wide VPN: it creates a real TUN
// adapter (wintun on Windows, /dev/net/tun on Linux), points the default route
// at it, and pins a host route to the server through the original gateway so
// the carrier connection doesn't loop. Requires administrator/root.
//
// serverIP is the server's resolved address (so we can except it from the
// default route). ifName is a suggested adapter name.
func RunTUN(ctx context.Context, t *Tunnel, serverIP, ifName string) error {
	dev, err := tun.CreateTUN(ifName, t.MTU)
	if err != nil {
		// Won't recover on retry: needs elevation and wintun.dll (Windows).
		return &setupError{fmt.Errorf("TUN mode needs Administrator and wintun.dll — "+
			"run the app as administrator with wintun.dll next to it, or switch to Proxy mode: %w", err)}
	}
	defer dev.Close()

	name, _ := dev.Name()
	cleanup, err := configureTUN(name, t.LocalIP, t.Mask, t.DNS, serverIP)
	if err != nil {
		return &setupError{fmt.Errorf("configure %s: %w", name, err)}
	}
	defer cleanup()
	log.Printf("TUN up: dev=%s ip=%s/%d — all traffic exits via the server", name, t.LocalIP, t.Mask)

	done := make(chan struct{})
	go t.KeepAlive(20*time.Second, done)
	defer close(done)

	go func() {
		<-ctx.Done()
		_ = dev.Close()
	}()
	return pump(dev, t)
}
