package client

import (
	"strings"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

// inetSettings is the per-user WinINET configuration browsers (Edge/Chrome/
// Yandex) and most Windows apps read for their proxy.
const inetSettings = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

// setSystemProxy points the Windows system (WinINET) proxy at addr, so proxy
// mode routes browser/app traffic through the tunnel without per-app setup.
// addr is host:port of the local HTTP proxy (handles CONNECT for HTTPS too).
func setSystemProxy(addr string) error {
	addr = loopbackAddr(addr)
	k, err := registry.OpenKey(registry.CURRENT_USER, inetSettings, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.SetStringValue("ProxyServer", addr); err != nil {
		return err
	}
	// Keep localhost and private ranges direct so the tunnel handshake and LAN
	// stay reachable.
	_ = k.SetStringValue("ProxyOverride", "localhost;127.*;10.*;172.16.*;172.17.*;172.18.*;172.19.*;172.20.*;172.21.*;172.22.*;172.23.*;172.24.*;172.25.*;172.26.*;172.27.*;172.28.*;172.29.*;172.30.*;172.31.*;192.168.*;<local>")
	if err := k.SetDWordValue("ProxyEnable", 1); err != nil {
		return err
	}
	refreshWinINET()
	return nil
}

// clearSystemProxy disables the WinINET proxy again.
func clearSystemProxy() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, inetSettings, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	err = k.SetDWordValue("ProxyEnable", 0)
	refreshWinINET()
	return err
}

// refreshWinINET notifies WinINET that the proxy settings changed so running
// browsers pick them up without a restart. Best-effort.
func refreshWinINET() {
	const (
		internetOptionSettingsChanged = 39
		internetOptionRefresh         = 37
	)
	proc := syscall.NewLazyDLL("wininet.dll").NewProc("InternetSetOptionW")
	_, _, _ = proc.Call(0, internetOptionSettingsChanged, 0, 0)
	_, _, _ = proc.Call(0, internetOptionRefresh, 0, 0)
}

// loopbackAddr rewrites a wildcard bind (0.0.0.0) to 127.0.0.1 for the system
// proxy value, since the proxy always answers on loopback.
func loopbackAddr(addr string) string {
	if strings.HasPrefix(addr, "0.0.0.0:") {
		return "127.0.0.1:" + strings.TrimPrefix(addr, "0.0.0.0:")
	}
	return addr
}
