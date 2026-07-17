package server

import (
	"strconv"
	"sync"
	"time"

	"arnosvpn/internal/protocol"
)

// replayGuard remembers recently accepted handshakes so a captured hello cannot
// be replayed while it is still inside the ±AuthWindow timestamp tolerance.
//
// A legitimate client picks a fresh random salt per connection, so a repeated
// (salt, ts) pair is a replay. Entries live for a little over twice the auth
// window — long enough to cover the full span in which a stale hello would still
// pass the timestamp check — and are swept lazily on each observe call.
type replayGuard struct {
	mu   sync.Mutex
	seen map[string]int64 // key -> unix expiry
	ttl  time.Duration
}

func newReplayGuard() *replayGuard {
	return &replayGuard{
		seen: make(map[string]int64),
		ttl:  time.Duration(2*protocol.AuthWindow+5) * time.Second,
	}
}

// observe records a handshake and reports whether it is fresh. It returns false
// if this (salt, ts) pair has already been seen inside the window.
func (g *replayGuard) observe(salt string, ts int64) bool {
	key := salt + "|" + strconv.FormatInt(ts, 10)
	now := time.Now()
	exp := now.Add(g.ttl).Unix()

	g.mu.Lock()
	defer g.mu.Unlock()

	// Opportunistic sweep of expired entries.
	nowUnix := now.Unix()
	for k, e := range g.seen {
		if e <= nowUnix {
			delete(g.seen, k)
		}
	}

	if _, dup := g.seen[key]; dup {
		return false
	}
	g.seen[key] = exp
	return true
}
