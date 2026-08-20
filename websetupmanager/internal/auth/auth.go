// Package auth implements PAM authentication, opaque browser sessions and
// bounded login throttling. HTTP concerns such as cookies and status codes live
// in httpapi so this package can be tested without a browser or real password.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
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
	ErrInvalidLifetime    = errors.New("invalid authentication session lifetime")
)

const (
	maxPersistentSessions      = 10000
	persistentOperationTimeout = 5 * time.Second
)

// Authenticator abstracts PAM so HTTP behavior can be tested without real
// passwords or a privileged fixture.
type Authenticator interface {
	Authenticate(context.Context, string, string) error
}

// Session is the authenticated state associated with an opaque cookie token.
// The raw cookie token is deliberately absent.
type Session struct {
	Username      string
	CSRFToken     string
	CreatedAt     time.Time
	LastSeen      time.Time
	ExpiresAt     time.Time
	IdleExpiresAt time.Time
	Remembered    bool
	idleTimeout   time.Duration
	persistScope  string
}

// Store keeps ordinary sessions only in memory. When constructed with
// NewPersistentStore it also restores and durably records remembered sessions
// in SQLite. Both the in-memory and durable keys are SHA-256 hashes; neither a
// raw browser token nor a password is stored.
//
// Store does not own the supplied *sql.DB. Close invalidates its in-memory
// sessions but deliberately leaves the shared database open.
type Store struct {
	mu       sync.Mutex
	sessions map[[sha256.Size]byte]Session
	idle     time.Duration
	absolute time.Duration
	capacity int
	now      func() time.Time
	random   io.Reader
	db       *sql.DB
	user     string
	scope    string
	closed   bool
}

// NewStore creates an in-memory session store. Production callers that offer
// Remember Me should use NewPersistentStore instead.
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

// NewPersistentStore restores remembered sessions from SQLite. Only sessions
// for the current configured user and deployment scope survive startup; a user
// or scope change durably invalidates prior remembered sessions.
func NewPersistentStore(db *sql.DB, idle, absolute time.Duration, capacity int, user, scope string) (*Store, error) {
	if db == nil || idle <= 0 || absolute <= 0 || capacity <= 0 || !validBoundedString(user, 256) || !validBoundedString(scope, 512) {
		return nil, errors.New("invalid persistent authentication store configuration")
	}
	store := NewStore(idle, absolute, capacity)
	store.db = db
	store.user = user
	store.scope = scope
	if err := store.loadPersistent(); err != nil {
		return nil, err
	}
	return store, nil
}

// Create creates a browser-session token and an independent CSRF token using
// the store's normal idle and absolute limits.
func (s *Store) Create(username string) (string, Session, error) {
	return s.create(username, s.idle, s.absolute, false)
}

// CreateRemembered creates a remembered session with one fixed deadline. The
// password is never retained. A persistent store durably inserts the token hash
// before the token can be returned to the caller.
func (s *Store) CreateRemembered(username string, lifetime time.Duration) (string, Session, error) {
	if lifetime <= 0 {
		return "", Session{}, ErrInvalidLifetime
	}
	return s.create(username, lifetime, lifetime, true)
}

func (s *Store) create(username string, idle, absolute time.Duration, remembered bool) (string, Session, error) {
	if idle <= 0 || absolute <= 0 {
		return "", Session{}, ErrInvalidLifetime
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", Session{}, ErrUnavailable
	}
	now := s.now()
	if err := s.removeExpiredLocked(now); err != nil {
		return "", Session{}, err
	}
	if len(s.sessions) >= s.capacity {
		return "", Session{}, ErrSessionCapacity
	}

	for range 4 {
		token, err := randomToken(s.random)
		if err != nil {
			return "", Session{}, fmt.Errorf("%w: generate session token", ErrUnavailable)
		}
		csrf, err := randomToken(s.random)
		if err != nil {
			return "", Session{}, fmt.Errorf("%w: generate CSRF token", ErrUnavailable)
		}
		key := sha256.Sum256([]byte(token))
		if _, duplicate := s.sessions[key]; duplicate {
			continue
		}
		record := Session{
			Username:      username,
			CSRFToken:     csrf,
			CreatedAt:     now,
			LastSeen:      now,
			ExpiresAt:     now.Add(absolute),
			IdleExpiresAt: now.Add(idle),
			Remembered:    remembered,
			idleTimeout:   idle,
			persistScope:  s.scope,
		}
		if record.IdleExpiresAt.After(record.ExpiresAt) {
			record.IdleExpiresAt = record.ExpiresAt
		}
		if remembered && s.db != nil {
			if err := s.insertPersistentLocked(key, record); err != nil {
				return "", Session{}, err
			}
		}
		s.sessions[key] = record
		return token, record, nil
	}
	return "", Session{}, fmt.Errorf("%w: repeated random token collision", ErrUnavailable)
}

// Get validates and refreshes the server-side idle deadline. The absolute
// deadline never moves. Remembered sessions have a fixed idle deadline equal to
// their absolute deadline.
func (s *Store) Get(token string) (Session, bool) {
	if token == "" {
		return Session{}, false
	}
	key := sha256.Sum256([]byte(token))
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Session{}, false
	}
	record, ok := s.sessions[key]
	if !ok {
		return Session{}, false
	}
	if !now.Before(record.ExpiresAt) || !now.Before(record.IdleExpiresAt) {
		delete(s.sessions, key)
		if record.Remembered && s.db != nil {
			_ = s.deletePersistentLocked(key)
		}
		return Session{}, false
	}
	record.LastSeen = now
	idleTimeout := record.idleTimeout
	if idleTimeout <= 0 {
		idleTimeout = s.idle
	}
	record.IdleExpiresAt = now.Add(idleTimeout)
	if record.IdleExpiresAt.After(record.ExpiresAt) {
		record.IdleExpiresAt = record.ExpiresAt
	}
	s.sessions[key] = record
	return record, true
}

// Valid checks whether a token is usable without refreshing its idle deadline.
func (s *Store) Valid(token string) bool {
	_, ok := s.DeadlineFor(token)
	return ok
}

// DeadlineFor returns the current deadline without refreshing it.
func (s *Store) DeadlineFor(token string) (time.Time, bool) {
	if token == "" {
		return time.Time{}, false
	}
	key := sha256.Sum256([]byte(token))
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return time.Time{}, false
	}
	record, ok := s.sessions[key]
	if !ok {
		return time.Time{}, false
	}
	if !now.Before(record.ExpiresAt) || !now.Before(record.IdleExpiresAt) {
		delete(s.sessions, key)
		if record.Remembered && s.db != nil {
			_ = s.deletePersistentLocked(key)
		}
		return time.Time{}, false
	}
	return record.Deadline(), true
}

// Deadline is the earliest point at which this session expires.
func (s Session) Deadline() time.Time {
	if s.IdleExpiresAt.Before(s.ExpiresAt) {
		return s.IdleExpiresAt
	}
	return s.ExpiresAt
}

// Delete invalidates a session. A remembered session is removed from SQLite
// before success is reported; if durable deletion fails, the in-memory record
// is restored so logout cannot falsely claim revocation.
func (s *Store) Delete(token string) error {
	key := sha256.Sum256([]byte(token))
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrUnavailable
	}
	record, ok := s.sessions[key]
	if !ok {
		return nil
	}
	delete(s.sessions, key)
	if record.Remembered && s.db != nil {
		if err := s.deletePersistentLocked(key); err != nil {
			s.sessions[key] = record
			return err
		}
	}
	return nil
}

// Close invalidates all in-memory sessions. It does not close the shared DB and
// does not delete remembered rows, which are intended to survive restart.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	clear(s.sessions)
	return nil
}

func (s *Store) loadPersistent() (finalErr error) {
	ctx, cancel := context.WithTimeout(context.Background(), persistentOperationTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("%w: begin remembered-session restore", ErrUnavailable)
	}
	defer func() {
		if finalErr != nil {
			_ = tx.Rollback()
		}
	}()

	rows, err := tx.QueryContext(ctx, `
		SELECT token_hash, username, csrf_token, created_at, expires_at, scope
		  FROM auth_sessions
		 ORDER BY token_hash`)
	if err != nil {
		return fmt.Errorf("%w: read remembered sessions", ErrUnavailable)
	}
	now := s.now()
	stale := make([]string, 0)
	seen := 0
	for rows.Next() {
		seen++
		if seen > maxPersistentSessions {
			_ = rows.Close()
			return errors.New("remembered-session table contains too many records")
		}
		var tokenHash, username, csrf, createdRaw, expiresRaw, scope string
		if err := rows.Scan(&tokenHash, &username, &csrf, &createdRaw, &expiresRaw, &scope); err != nil {
			_ = rows.Close()
			return fmt.Errorf("%w: decode remembered session", ErrUnavailable)
		}
		key, record, err := decodePersistentSession(tokenHash, username, csrf, createdRaw, expiresRaw, scope)
		if err != nil {
			_ = rows.Close()
			return err
		}
		if username != s.user || scope != s.scope || !now.Before(record.ExpiresAt) {
			stale = append(stale, tokenHash)
			continue
		}
		if _, duplicate := s.sessions[key]; duplicate {
			_ = rows.Close()
			return errors.New("remembered-session table contains a duplicate token hash")
		}
		if len(s.sessions) >= s.capacity {
			_ = rows.Close()
			return ErrSessionCapacity
		}
		s.sessions[key] = record
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("%w: close remembered-session query", ErrUnavailable)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("%w: read remembered sessions", ErrUnavailable)
	}
	for _, tokenHash := range stale {
		if _, err := tx.ExecContext(ctx, `DELETE FROM auth_sessions WHERE token_hash = ?`, tokenHash); err != nil {
			return fmt.Errorf("%w: prune remembered session", ErrUnavailable)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%w: commit remembered-session restore", ErrUnavailable)
	}
	return nil
}

func decodePersistentSession(tokenHash, username, csrf, createdRaw, expiresRaw, scope string) ([sha256.Size]byte, Session, error) {
	var key [sha256.Size]byte
	hash, err := hex.DecodeString(tokenHash)
	if err != nil || len(hash) != sha256.Size || hex.EncodeToString(hash) != tokenHash {
		return key, Session{}, errors.New("remembered-session table contains an invalid token hash")
	}
	copy(key[:], hash)
	csrfBytes, err := base64.RawURLEncoding.DecodeString(csrf)
	if err != nil || len(csrfBytes) != 32 || base64.RawURLEncoding.EncodeToString(csrfBytes) != csrf {
		return key, Session{}, errors.New("remembered-session table contains an invalid CSRF token")
	}
	created, err := time.Parse(time.RFC3339Nano, createdRaw)
	if err != nil {
		return key, Session{}, errors.New("remembered-session table contains an invalid creation time")
	}
	expires, err := time.Parse(time.RFC3339Nano, expiresRaw)
	if err != nil || !created.Before(expires) {
		return key, Session{}, errors.New("remembered-session table contains an invalid expiry time")
	}
	if !validBoundedString(username, 256) || !validBoundedString(scope, 512) {
		return key, Session{}, errors.New("remembered-session table contains an invalid session record")
	}
	lifetime := expires.Sub(created)
	return key, Session{
		Username:      username,
		CSRFToken:     csrf,
		CreatedAt:     created,
		LastSeen:      created,
		ExpiresAt:     expires,
		IdleExpiresAt: expires,
		Remembered:    true,
		idleTimeout:   lifetime,
		persistScope:  scope,
	}, nil
}

func (s *Store) insertPersistentLocked(key [sha256.Size]byte, record Session) error {
	ctx, cancel := context.WithTimeout(context.Background(), persistentOperationTimeout)
	defer cancel()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO auth_sessions(token_hash, username, csrf_token, created_at, expires_at, scope)
		VALUES (?, ?, ?, ?, ?, ?)`,
		hex.EncodeToString(key[:]), record.Username, record.CSRFToken,
		formatPersistentTime(record.CreatedAt), formatPersistentTime(record.ExpiresAt), record.persistScope)
	if err != nil {
		return fmt.Errorf("%w: persist remembered session", ErrUnavailable)
	}
	return nil
}

func (s *Store) deletePersistentLocked(key [sha256.Size]byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), persistentOperationTimeout)
	defer cancel()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM auth_sessions WHERE token_hash = ?`, hex.EncodeToString(key[:])); err != nil {
		return fmt.Errorf("%w: delete remembered session", ErrUnavailable)
	}
	return nil
}

func (s *Store) removeExpiredLocked(now time.Time) error {
	// Delete by the canonical fixed-width timestamp as well as pruning the map.
	// This also removes an expired durable row whose best-effort deletion from a
	// previous Get/DeadlineFor failed before the process recovered.
	if s.db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), persistentOperationTimeout)
		defer cancel()
		if _, err := s.db.ExecContext(ctx, `DELETE FROM auth_sessions WHERE expires_at <= ?`, formatPersistentTime(now)); err != nil {
			return fmt.Errorf("%w: prune expired remembered sessions", ErrUnavailable)
		}
	}
	for key, record := range s.sessions {
		if !now.Before(record.ExpiresAt) || !now.Before(record.IdleExpiresAt) {
			delete(s.sessions, key)
		}
	}
	return nil
}

func formatPersistentTime(value time.Time) string {
	// Fixed-width UTC timestamps preserve chronological order under SQLite's
	// bytewise TEXT comparison used by the bounded expiry prune above.
	return value.UTC().Format("2006-01-02T15:04:05.000000000Z")
}

func validBoundedString(value string, maximum int) bool {
	if value == "" || len(value) > maximum {
		return false
	}
	for _, character := range value {
		if character == '\x00' || character == '\r' || character == '\n' {
			return false
		}
	}
	return true
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

// Throttler is a bounded, in-memory sliding-window login limiter. Callers
// should use independent keys for the peer address and submitted username.
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
	return t.limit > 0 && t.window > 0 && len(a.times) < t.limit
}

func (t *Throttler) Failure(key string) {
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	a := t.pruneLocked(key, now)
	a.times = append(a.times, now)
	t.attempts[key] = a
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
