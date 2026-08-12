// Package auth implements PAM authentication, opaque browser sessions and
// login throttling. HTTP concerns (cookies and status codes) intentionally live
// in httpapi so this package remains straightforward to test.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"sort"
	"sync"
	"time"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrUnavailable        = errors.New("authentication unavailable")
	ErrRateLimited        = errors.New("too many authentication attempts")
	ErrSessionCapacity    = errors.New("authentication session capacity reached")
)

// Authenticator abstracts PAM so HTTP behavior can be tested without real
// passwords or a privileged test fixture.
type Authenticator interface {
	Authenticate(context.Context, string, string) error
}

// Session is the authenticated state associated with an opaque cookie token.
type Session struct {
	Username      string
	CSRFToken     string
	CreatedAt     time.Time
	LastSeen      time.Time
	ExpiresAt     time.Time
	IdleExpiresAt time.Time
}

// Store keeps authentication sessions only in process memory. Cookie values
// are hashed before being used as map keys, reducing the impact of accidental
// state inspection.
type Store struct {
	mu       sync.Mutex
	sessions map[[sha256.Size]byte]Session
	idle     time.Duration
	absolute time.Duration
	capacity int
	now      func() time.Time
	random   io.Reader
}

func NewStore(idle, absolute time.Duration, capacity int) *Store {
	return &Store{
		sessions: make(map[[sha256.Size]byte]Session),
		idle:     idle,
		absolute: absolute,
		capacity: capacity,
		now:      time.Now,
		random:   rand.Reader,
	}
}

// Create creates an opaque cookie token and an independent CSRF token.
func (s *Store) Create(username string) (string, Session, error) {
	now := s.now()
	token, err := randomToken(s.random)
	if err != nil {
		return "", Session{}, err
	}
	csrf, err := randomToken(s.random)
	if err != nil {
		return "", Session{}, err
	}
	key := sha256.Sum256([]byte(token))

	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeExpiredLocked(now)
	if len(s.sessions) >= s.capacity {
		return "", Session{}, ErrSessionCapacity
	}
	record := Session{
		Username:      username,
		CSRFToken:     csrf,
		CreatedAt:     now,
		LastSeen:      now,
		ExpiresAt:     now.Add(s.absolute),
		IdleExpiresAt: now.Add(s.idle),
	}
	if record.IdleExpiresAt.After(record.ExpiresAt) {
		record.IdleExpiresAt = record.ExpiresAt
	}
	s.sessions[key] = record
	return token, record, nil
}

// Get validates and refreshes the server-side idle deadline. The absolute
// deadline never moves.
func (s *Store) Get(token string) (Session, bool) {
	if token == "" {
		return Session{}, false
	}
	key := sha256.Sum256([]byte(token))
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.sessions[key]
	if !ok {
		return Session{}, false
	}
	if !now.Before(record.ExpiresAt) || !now.Before(record.IdleExpiresAt) {
		delete(s.sessions, key)
		return Session{}, false
	}
	record.LastSeen = now
	record.IdleExpiresAt = now.Add(s.idle)
	if record.IdleExpiresAt.After(record.ExpiresAt) {
		record.IdleExpiresAt = record.ExpiresAt
	}
	s.sessions[key] = record
	return record, true
}

// Valid checks whether a token is still usable without refreshing its idle
// deadline. The HTTP hijack path uses it to close an upgrade that raced with
// logout or expiry.
func (s *Store) Valid(token string) bool {
	_, ok := s.DeadlineFor(token)
	return ok
}

// DeadlineFor returns the current server-side deadline without refreshing it.
// It is used by upgraded connection timers to resolve races with concurrent
// API requests that extend the idle deadline.
func (s *Store) DeadlineFor(token string) (time.Time, bool) {
	if token == "" {
		return time.Time{}, false
	}
	key := sha256.Sum256([]byte(token))
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.sessions[key]
	if !ok {
		return time.Time{}, false
	}
	if !now.Before(record.ExpiresAt) || !now.Before(record.IdleExpiresAt) {
		delete(s.sessions, key)
		return time.Time{}, false
	}
	return record.Deadline(), true
}

// Deadline is the earliest point at which this authenticated session expires.
func (s Session) Deadline() time.Time {
	if s.IdleExpiresAt.Before(s.ExpiresAt) {
		return s.IdleExpiresAt
	}
	return s.ExpiresAt
}

func (s *Store) Delete(token string) {
	key := sha256.Sum256([]byte(token))
	s.mu.Lock()
	delete(s.sessions, key)
	s.mu.Unlock()
}

func (s *Store) removeExpiredLocked(now time.Time) {
	for key, record := range s.sessions {
		if !now.Before(record.ExpiresAt) || !now.Before(record.IdleExpiresAt) {
			delete(s.sessions, key)
		}
	}
}

func randomToken(reader io.Reader) (string, error) {
	var value [32]byte
	if _, err := io.ReadFull(reader, value[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value[:]), nil
}

// EqualCSRF performs a constant-time CSRF token comparison.
func EqualCSRF(expected, supplied string) bool {
	if len(expected) == 0 || len(expected) != len(supplied) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(supplied)) == 1
}

type attempt struct {
	times []time.Time
}

// Throttler is a bounded, in-memory sliding-window login limiter. Keys should
// combine the peer address with the submitted username.
type Throttler struct {
	mu       sync.Mutex
	attempts map[string]attempt
	limit    int
	window   time.Duration
	now      func() time.Time
}

func NewThrottler(limit int, window time.Duration) *Throttler {
	return &Throttler{attempts: make(map[string]attempt), limit: limit, window: window, now: time.Now}
}

func (t *Throttler) Allow(key string) bool {
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	a := t.pruneLocked(key, now)
	return len(a.times) < t.limit
}

func (t *Throttler) Failure(key string) {
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	a := t.pruneLocked(key, now)
	a.times = append(a.times, now)
	t.attempts[key] = a
	// Bound attacker-controlled keys. Evict entries with the oldest last
	// attempt when the map grows unexpectedly large.
	if len(t.attempts) > 4096 {
		t.evictLocked(3072)
	}
}

func (t *Throttler) Success(key string) {
	t.mu.Lock()
	delete(t.attempts, key)
	t.mu.Unlock()
}

func (t *Throttler) pruneLocked(key string, now time.Time) attempt {
	a := t.attempts[key]
	cutoff := now.Add(-t.window)
	first := 0
	for first < len(a.times) && !a.times[first].After(cutoff) {
		first++
	}
	a.times = append([]time.Time(nil), a.times[first:]...)
	if len(a.times) == 0 {
		delete(t.attempts, key)
	} else {
		t.attempts[key] = a
	}
	return a
}

func (t *Throttler) evictLocked(keep int) {
	type keyedTime struct {
		key  string
		last time.Time
	}
	entries := make([]keyedTime, 0, len(t.attempts))
	for key, value := range t.attempts {
		last := time.Time{}
		if len(value.times) != 0 {
			last = value.times[len(value.times)-1]
		}
		entries = append(entries, keyedTime{key: key, last: last})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].last.After(entries[j].last) })
	for _, entry := range entries[keep:] {
		delete(t.attempts, entry.key)
	}
}
