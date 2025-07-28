package transparent

import (
	"net"
	"net/http"
	"sync"

	"github.com/stacklok/toolhive/pkg/logger"
)

// ConnTracker tracks active connections for the transparent proxy
type ConnTracker struct {
	mu    sync.Mutex
	conns map[net.Conn]struct{}
}

// NewConnTracker creates a new ConnTracker instance
func NewConnTracker() *ConnTracker {
	return &ConnTracker{conns: make(map[net.Conn]struct{})}
}

// ConnState updates the connection state in the tracker
func (t *ConnTracker) ConnState(c net.Conn, state http.ConnState) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch state {
	case http.StateNew, http.StateActive, http.StateIdle:
		t.conns[c] = struct{}{}
	case http.StateHijacked, http.StateClosed:
		// Remove terminal states from the tracker
		delete(t.conns, c)
	default:
		delete(t.conns, c)
	}
}

// CloseAll closes all tracked connections
func (t *ConnTracker) CloseAll() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for c := range t.conns {
		if err := c.Close(); err != nil {
			logger.Warnf("Failed to close connection: %v", err)
		}
	}
	t.conns = make(map[net.Conn]struct{})
}
