//go:build !windows

package client

// setSystemProxy / clearSystemProxy are Windows-only. On other platforms the
// system-proxy toggle is a no-op (the desktop GUI hides it off Windows).
func setSystemProxy(string) error { return nil }
func clearSystemProxy() error     { return nil }
