package client

import (
	"strings"
	"sync"
	"time"
)

// logRing is an in-memory ring buffer of recent log lines, exposed by the GUI's
// Logs page. It implements io.Writer so it can be attached to the standard
// logger alongside stderr.
type logRing struct {
	mu    sync.Mutex
	lines []string
	max   int
}

var guiLog = &logRing{max: 500}

func (r *logRing) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if line == "" {
			continue
		}
		r.lines = append(r.lines, line)
	}
	if len(r.lines) > r.max {
		r.lines = r.lines[len(r.lines)-r.max:]
	}
	return len(p), nil
}

// add appends an application-level line (with a timestamp) directly.
func (r *logRing) add(line string) {
	_, _ = r.Write([]byte(time.Now().Format("15:04:05") + " " + line + "\n"))
}

// Lines returns a copy of the buffered lines, oldest first.
func (r *logRing) Lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.lines))
	copy(out, r.lines)
	return out
}

// clear empties the buffer.
func (r *logRing) clear() {
	r.mu.Lock()
	r.lines = nil
	r.mu.Unlock()
}
