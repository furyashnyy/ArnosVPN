//go:build !windows

package client

import "context"

// openWindow on non-Windows platforms opens the control panel in the default
// browser (a native WebView window is Windows-only for now) and blocks until
// the context is cancelled.
func openWindow(ctx context.Context, url, _ string) {
	openBrowser(url)
	<-ctx.Done()
}
