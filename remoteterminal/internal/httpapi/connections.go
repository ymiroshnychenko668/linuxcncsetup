package httpapi

import (
	"bufio"
	"crypto/sha256"
	"errors"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// connectionTracker owns downstream terminal connections after net/http has
// hijacked them for a WebSocket upgrade. Connections are grouped by a hash of
// the opaque authentication token, so logout can revoke every terminal opened
// by that browser session without retaining the token itself.
type connectionTracker struct {
	mu      sync.Mutex
	byToken map[[sha256.Size]byte]map[*trackedConn]struct{}
	closed  bool // guarded by mu; once set, every future Track is closed
}

type trackedConn struct {
	net.Conn
	tracker    *connectionTracker
	key        [sha256.Size]byte
	closed     uint32
	timer      *time.Timer // guarded by tracker.mu
	generation uint64      // guarded by tracker.mu
	validUntil func() (time.Time, bool)
}

func newConnectionTracker() *connectionTracker {
	return &connectionTracker{byToken: make(map[[sha256.Size]byte]map[*trackedConn]struct{})}
}

func (t *connectionTracker) Track(token string, connection net.Conn, deadline time.Time,
	validUntil func() (time.Time, bool)) net.Conn {
	key := sha256.Sum256([]byte(token))
	tracked := &trackedConn{
		Conn: connection, tracker: t, key: key,
		validUntil: validUntil,
	}

	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		tracked.closeUnderlying()
		return tracked
	}
	connections := t.byToken[key]
	if connections == nil {
		connections = make(map[*trackedConn]struct{})
		t.byToken[key] = connections
	}
	connections[tracked] = struct{}{}
	t.resetTimerLocked(tracked, deadline)
	t.mu.Unlock()

	// Delete-before-close ordering in logout plus this validation closes both
	// sides of the race where logout occurs immediately around Hijack.
	if validUntil != nil {
		if currentDeadline, valid := validUntil(); !valid {
			t.CloseToken(token)
		} else if !currentDeadline.Equal(deadline) {
			t.Refresh(token, currentDeadline)
		}
	}
	return tracked
}

func (t *connectionTracker) Refresh(token string, deadline time.Time) {
	t.refreshKey(sha256.Sum256([]byte(token)), deadline)
}

func (t *connectionTracker) refreshKey(key [sha256.Size]byte, deadline time.Time) {
	t.mu.Lock()
	for connection := range t.byToken[key] {
		t.resetTimerLocked(connection, deadline)
	}
	t.mu.Unlock()
}

func (t *connectionTracker) resetTimerLocked(connection *trackedConn, deadline time.Time) {
	connection.generation++
	generation := connection.generation
	if connection.timer != nil {
		connection.timer.Stop()
	}
	delay := time.Until(deadline)
	if delay < 0 {
		delay = 0
	}
	connection.timer = time.AfterFunc(delay, func() {
		t.expire(connection.key, connection, generation)
	})
}

func (t *connectionTracker) expire(key [sha256.Size]byte, trigger *trackedConn, generation uint64) {
	if trigger.validUntil != nil {
		if deadline, valid := trigger.validUntil(); valid {
			t.mu.Lock()
			connections := t.byToken[key]
			if _, present := connections[trigger]; present && trigger.generation == generation {
				for connection := range connections {
					t.resetTimerLocked(connection, deadline)
				}
			}
			t.mu.Unlock()
			return
		}
	}
	t.mu.Lock()
	connections := t.byToken[key]
	if _, present := connections[trigger]; !present || trigger.generation != generation {
		t.mu.Unlock()
		return
	}
	delete(t.byToken, key)
	toClose := make([]*trackedConn, 0, len(connections))
	for connection := range connections {
		if connection.timer != nil {
			connection.timer.Stop()
		}
		toClose = append(toClose, connection)
	}
	t.mu.Unlock()
	for _, connection := range toClose {
		connection.closeUnderlying()
	}
}

func (t *connectionTracker) CloseToken(token string) {
	t.closeKey(sha256.Sum256([]byte(token)))
}

func (t *connectionTracker) closeKey(key [sha256.Size]byte) {
	t.mu.Lock()
	connections := t.byToken[key]
	delete(t.byToken, key)
	toClose := make([]*trackedConn, 0, len(connections))
	for connection := range connections {
		if connection.timer != nil {
			connection.timer.Stop()
		}
		toClose = append(toClose, connection)
	}
	t.mu.Unlock()
	for _, connection := range toClose {
		connection.closeUnderlying()
	}
}

func (t *connectionTracker) CloseAll() {
	t.mu.Lock()
	t.closed = true
	toClose := make([]*trackedConn, 0)
	for key, connections := range t.byToken {
		delete(t.byToken, key)
		for connection := range connections {
			if connection.timer != nil {
				connection.timer.Stop()
			}
			toClose = append(toClose, connection)
		}
	}
	t.mu.Unlock()
	for _, connection := range toClose {
		connection.closeUnderlying()
	}
}

func (t *connectionTracker) remove(connection *trackedConn) {
	t.mu.Lock()
	if connections := t.byToken[connection.key]; connections != nil {
		delete(connections, connection)
		if len(connections) == 0 {
			delete(t.byToken, connection.key)
		}
	}
	if connection.timer != nil {
		connection.timer.Stop()
	}
	t.mu.Unlock()
}

func (c *trackedConn) Close() error {
	if !atomic.CompareAndSwapUint32(&c.closed, 0, 1) {
		return nil
	}
	err := c.Conn.Close()
	c.tracker.remove(c)
	return err
}

func (c *trackedConn) closeUnderlying() {
	if atomic.CompareAndSwapUint32(&c.closed, 0, 1) {
		_ = c.Conn.Close()
	}
}

type trackedResponseWriter struct {
	http.ResponseWriter
	tracker    *connectionTracker
	token      string
	deadline   time.Time
	validUntil func() (time.Time, bool)
}

func (w *trackedResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("response writer does not support hijacking")
	}
	connection, readWriter, err := hijacker.Hijack()
	if err != nil {
		return nil, nil, err
	}
	// The HTTP ReadTimeout is intentionally cleared when ownership transfers
	// away from net/http. The authentication deadline below replaces it.
	_ = connection.SetDeadline(time.Time{})
	tracked := w.tracker.Track(w.token, connection, w.deadline, w.validUntil)
	return tracked, readWriter, nil
}

func (w *trackedResponseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *trackedResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
