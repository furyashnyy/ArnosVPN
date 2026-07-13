package client

import (
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"time"
)

func newHTTPGet(rawURL string) (*http.Request, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ArnosVPN/desktop")
	return req, nil
}

func fetchBody(req *http.Request) ([]byte, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20)) // 4 MiB cap
}

// maybeBase64 decodes a whole-body base64 blob (common for subscription feeds).
func maybeBase64(s string) (string, bool) {
	t := strings.TrimSpace(s)
	if strings.Contains(t, "arnos://") {
		return "", false // already plain text
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding} {
		if b, err := enc.DecodeString(t); err == nil && strings.Contains(string(b), "arnos://") {
			return string(b), true
		}
	}
	return "", false
}
