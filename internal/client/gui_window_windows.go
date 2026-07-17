package client

import (
	"context"
	"runtime"

	webview2 "github.com/jchv/go-webview2"
)

// openWindow shows the control panel in a native application window using the
// Edge WebView2 runtime that ships with Windows 10/11 — a real app window, no
// console and no browser tab. It reuses the exact same local UI. It blocks until
// the window is closed (or ctx is cancelled). If the WebView2 runtime is absent
// it falls back to opening the default browser.
func openWindow(ctx context.Context, url, title string) {
	// The Win32 message loop must stay on one OS thread.
	runtime.LockOSThread()

	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     false,
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title:  title,
			Width:  1024,
			Height: 720,
			Center: true,
		},
	})
	if w == nil {
		// WebView2 runtime not installed — degrade to the browser.
		openBrowser(url)
		<-ctx.Done()
		return
	}
	defer w.Destroy()

	// Cancelling ctx (Ctrl-C / signal) closes the window too.
	go func() {
		<-ctx.Done()
		w.Terminate()
	}()

	w.Navigate(url)
	w.Run() // returns when the window is closed
}
