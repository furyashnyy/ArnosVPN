package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Version is the running client's version. It defaults to the value in
// /version.json and is overridden at release time via the linker:
//
//	go build -ldflags "-X arnosvpn/internal/client.Version=1.2.0"
//
// so a published binary always reports the version it was cut from.
var Version = "1.4.0"

// The update feed is the project's public GitHub Releases. Because the repo is
// public, no token is needed — the client just reads the latest release and
// downloads the asset for its own OS/arch.
const (
	updateOwner = "furyashnyy"
	updateRepo  = "ArnosVPN"
)

// UpdateInfo is what the UI needs to show the update state.
type UpdateInfo struct {
	Current   string `json:"current"`   // running version
	Latest    string `json:"latest"`    // newest published version
	HasUpdate bool   `json:"hasUpdate"` // latest is strictly newer than current
	Notes     string `json:"notes"`     // release notes (may be long)
	Asset     string `json:"asset"`     // download URL for this platform ("" if none)
	AssetName string `json:"assetName"` // asset file name
}

type ghRelease struct {
	TagName string `json:"tag_name"`
	Name    string `json:"name"`
	Body    string `json:"body"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// desktopAssetName is the release asset produced for this OS/arch by the
// release workflow (see .github/workflows/release.yml).
func desktopAssetName() string {
	switch runtime.GOOS {
	case "windows":
		return "arnosvpn-client-windows-amd64.exe"
	case "linux":
		return "arnosvpn-client-linux-amd64"
	default:
		return "" // unsupported target — self-update disabled
	}
}

// CheckUpdate queries the latest public release and reports whether a newer
// version than the running one is available for this platform.
func CheckUpdate(ctx context.Context) (*UpdateInfo, error) {
	rel, err := latestRelease(ctx)
	if err != nil {
		return nil, err
	}
	latest := strings.TrimPrefix(rel.TagName, "v")
	info := &UpdateInfo{
		Current:   Version,
		Latest:    latest,
		HasUpdate: isNewer(latest, Version),
		Notes:     rel.Body,
	}
	want := desktopAssetName()
	for _, a := range rel.Assets {
		if a.Name == want {
			info.Asset = a.URL
			info.AssetName = a.Name
			break
		}
	}
	return info, nil
}

func latestRelease(ctx context.Context) (*ghRelease, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", updateOwner, updateRepo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "ArnosVPN-updater")
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("no releases published yet")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("update check failed: %s", resp.Status)
	}
	var rel ghRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

// ApplyUpdate downloads the newest desktop binary and replaces the running
// executable in place. It returns nil on success; the caller should then
// prompt the user to restart. A running binary can't overwrite itself on
// Windows, so there the current file is moved aside first.
func ApplyUpdate(ctx context.Context) error {
	info, err := CheckUpdate(ctx)
	if err != nil {
		return err
	}
	if !info.HasUpdate {
		return fmt.Errorf("already up to date (%s)", info.Current)
	}
	if info.Asset == "" {
		return fmt.Errorf("no download available for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)

	// Download into the target directory so the final rename is atomic (same
	// filesystem) rather than a cross-device copy.
	tmp, err := os.CreateTemp(dir, ".arnos-update-*")
	if err != nil {
		return fmt.Errorf("cannot write to %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once renamed away

	if err := download(ctx, info.Asset, tmp); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return err
	}

	if runtime.GOOS == "windows" {
		// The running .exe is locked; move it aside, then put the new one in
		// place. The stale copy is cleaned up on next launch.
		_ = os.Remove(exe + ".old")
		if err := os.Rename(exe, exe+".old"); err != nil {
			return fmt.Errorf("replace failed: %w", err)
		}
		if err := os.Rename(tmpPath, exe); err != nil {
			_ = os.Rename(exe+".old", exe) // roll back
			return fmt.Errorf("replace failed: %w", err)
		}
		return nil
	}
	if err := os.Rename(tmpPath, exe); err != nil {
		return fmt.Errorf("replace failed: %w", err)
	}
	return nil
}

// CleanupOldBinary removes the leftover ".old" copy from a previous Windows
// self-update. Best effort; safe to call on every launch.
func CleanupOldBinary() {
	if runtime.GOOS != "windows" {
		return
	}
	if exe, err := os.Executable(); err == nil {
		_ = os.Remove(exe + ".old")
	}
}

func download(ctx context.Context, url string, dst io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "ArnosVPN-updater")
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed: %s", resp.Status)
	}
	// Cap at 200 MiB to bound a malicious/broken feed.
	if _, err := io.Copy(dst, io.LimitReader(resp.Body, 200<<20)); err != nil {
		return err
	}
	return nil
}

// isNewer reports whether version a is strictly newer than b using a numeric,
// dot-separated comparison (1.10.0 > 1.9.0). Non-numeric or unparsable inputs
// fall back to a simple inequality so an update is still offered.
func isNewer(a, b string) bool {
	pa, oka := parseVersion(a)
	pb, okb := parseVersion(b)
	if !oka || !okb {
		return a != "" && a != b
	}
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			return x > y
		}
	}
	return false
}

func parseVersion(v string) ([]int, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if v == "" {
		return nil, false
	}
	parts := strings.Split(v, ".")
	nums := make([]int, 0, len(parts))
	for _, p := range parts {
		// Tolerate a pre-release suffix like "1.2.0-rc1" by cutting at '-'.
		if i := strings.IndexAny(p, "-+"); i >= 0 {
			p = p[:i]
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, false
		}
		nums = append(nums, n)
	}
	return nums, true
}
