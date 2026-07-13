package client

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"time"
)

//go:embed webui/index.html
var webFS embed.FS

// RunGUI starts a local web control panel for ArnosVPN and opens it in the
// default browser. This is the desktop "app": a graphical panel to manage
// servers and connect/disconnect, served on 127.0.0.1 only. It needs no GUI
// toolkit, so it builds and runs identically on Windows and Linux.
func RunGUI(ctx context.Context, cfgPath, addr string) error {
	ctrl, err := NewController(cfgPath)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		page, _ := webFS.ReadFile("webui/index.html")
		_, _ = w.Write(page)
	})
	mux.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, ctrl.State())
	})
	mux.HandleFunc("/api/add", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ URI string }
		if err := decode(r, &body); err != nil || ctrl.AddServer(body.URI) != nil {
			httpErr(w, "invalid profile URI")
			return
		}
		writeJSON(w, ctrl.State())
	})
	mux.HandleFunc("/api/active", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Name string }
		if err := decode(r, &body); err != nil || ctrl.SetActive(body.Name) != nil {
			httpErr(w, "no such server")
			return
		}
		writeJSON(w, ctrl.State())
	})
	mux.HandleFunc("/api/remove", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Name string }
		_ = decode(r, &body)
		_ = ctrl.RemoveServer(body.Name)
		writeJSON(w, ctrl.State())
	})
	mux.HandleFunc("/api/connect", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Mode string }
		_ = decode(r, &body)
		if err := ctrl.Connect(body.Mode); err != nil {
			httpErr(w, err.Error())
			return
		}
		writeJSON(w, ctrl.State())
	})
	mux.HandleFunc("/api/disconnect", func(w http.ResponseWriter, r *http.Request) {
		ctrl.Disconnect()
		writeJSON(w, ctrl.State())
	})

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	url := "http://" + ln.Addr().String()
	log.Printf("ArnosVPN control panel: %s", url)

	srv := &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		ctrl.Disconnect()
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	go openBrowser(url)
	err = srv.Serve(ln)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, msg string) {
	http.Error(w, msg, http.StatusBadRequest)
}

func decode(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

// openBrowser opens url in the user's default browser, per OS.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
