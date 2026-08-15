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
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrUnavailable        = errors.New("authentication unavailable")
	ErrRateLimited        = errors.New("too many authentication attempts")
	ErrSessionCapacity    = errors.New("authentication session capacity reached")
	ErrInvalidLifetime    = errors.New("invalid authentication session lifetime")
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
	Remembered    bool
	idleTimeout   time.Duration
	persistScope  string
}

// Store keeps ordinary authentication sessions only in process memory. Its
// optional durable mode writes remembered-session metadata and token hashes,
// never raw cookie values. All in-memory map keys are hashed as well.
type Store struct {
	mu       sync.Mutex
	sessions map[[sha256.Size]byte]Session
	idle     time.Duration
	absolute time.Duration
	capacity int
	now      func() time.Time
	random   io.Reader
	persist  string
	user     string
	scope    string
}

const (
	persistentSessionVersion = 1
	maxPersistentSessionFile = 8 << 20
)

type persistentSessionFile struct {
	Version  int                       `json:"version"`
	Sessions []persistentSessionRecord `json:"sessions"`
}

type persistentSessionRecord struct {
	TokenHash string    `json:"tokenHash"`
	Username  string    `json:"username"`
	CSRFToken string    `json:"csrfToken"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	Scope     string    `json:"scope"`
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

// NewPersistentStore restores remembered sessions from an application-private
// state file. Only token hashes are durable; browser tokens and passwords are
// never written to disk. User and scope bind sessions to the current account
// and transport cookie mode.
func NewPersistentStore(idle, absolute time.Duration, capacity int, path, user, scope string) (*Store, error) {
	if !filepath.IsAbs(path) || user == "" || scope == "" {
		return nil, errors.New("invalid persistent authentication store configuration")
	}
	store := NewStore(idle, absolute, capacity)
	store.persist = filepath.Clean(path)
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

// CreateRemembered creates a persistent browser-session token. A remembered
// session has one fixed deadline and deliberately does not store the password.
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
		ExpiresAt:     now.Add(absolute),
		IdleExpiresAt: now.Add(idle),
		Remembered:    remembered,
		idleTimeout:   idle,
		persistScope:  s.scope,
	}
	if record.IdleExpiresAt.After(record.ExpiresAt) {
		record.IdleExpiresAt = record.ExpiresAt
	}
	s.sessions[key] = record
	if remembered && s.persist != "" {
		if err := s.persistLocked(); err != nil {
			delete(s.sessions, key)
			return "", Session{}, fmt.Errorf("persist remembered session: %w", err)
		}
	}
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

func (s *Store) Delete(token string) error {
	key := sha256.Sum256([]byte(token))
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.sessions[key]
	if !ok {
		return nil
	}
	delete(s.sessions, key)
	if record.Remembered && s.persist != "" {
		if err := s.persistLocked(); err != nil {
			s.sessions[key] = record
			return fmt.Errorf("persist remembered-session deletion: %w", err)
		}
	}
	return nil
}

func (s *Store) loadPersistent() error {
	if s.idle <= 0 || s.absolute <= 0 || s.capacity <= 0 {
		return ErrInvalidLifetime
	}
	directory := filepath.Dir(s.persist)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return fmt.Errorf("create persistent authentication directory: %w", err)
	}
	directoryInfo, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("inspect persistent authentication directory: %w", err)
	}
	if err := validatePrivateDirectory(directory, directoryInfo); err != nil {
		return err
	}
	if err := os.Chmod(directory, 0700); err != nil {
		return fmt.Errorf("secure persistent authentication directory: %w", err)
	}

	fileInfo, err := os.Lstat(s.persist)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect remembered-session file: %w", err)
	}
	if err := validatePrivateFile(s.persist, fileInfo); err != nil {
		return err
	}
	if err := os.Chmod(s.persist, 0600); err != nil {
		return fmt.Errorf("secure remembered-session file: %w", err)
	}
	if fileInfo.Size() > maxPersistentSessionFile {
		return errors.New("remembered-session file is unexpectedly large")
	}
	contents, err := os.ReadFile(s.persist)
	if err != nil {
		return fmt.Errorf("read remembered-session file: %w", err)
	}
	var persisted persistentSessionFile
	if err := json.Unmarshal(contents, &persisted); err != nil {
		return fmt.Errorf("decode remembered-session file: %w", err)
	}
	if persisted.Version != persistentSessionVersion {
		return fmt.Errorf("unsupported remembered-session file version %d", persisted.Version)
	}
	if len(persisted.Sessions) > 10000 {
		return errors.New("remembered-session file contains too many records")
	}

	now := s.now()
	dirty := false
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, candidate := range persisted.Sessions {
		key, record, err := decodePersistentSession(candidate)
		if err != nil {
			return err
		}
		if record.Username != s.user || record.persistScope != s.scope ||
			!now.Before(record.ExpiresAt) {
			dirty = true
			continue
		}
		if _, duplicate := s.sessions[key]; duplicate {
			return errors.New("remembered-session file contains a duplicate token hash")
		}
		if len(s.sessions) >= s.capacity {
			return ErrSessionCapacity
		}
		s.sessions[key] = record
	}
	if dirty {
		if err := s.persistLocked(); err != nil {
			return fmt.Errorf("prune remembered-session file: %w", err)
		}
	}
	return nil
}

func decodePersistentSession(candidate persistentSessionRecord) ([sha256.Size]byte, Session, error) {
	var key [sha256.Size]byte
	hash, err := hex.DecodeString(candidate.TokenHash)
	if err != nil || len(hash) != sha256.Size {
		return key, Session{}, errors.New("remembered-session file contains an invalid token hash")
	}
	copy(key[:], hash)
	csrf, err := base64.RawURLEncoding.DecodeString(candidate.CSRFToken)
	if err != nil || len(csrf) != 32 {
		return key, Session{}, errors.New("remembered-session file contains an invalid CSRF token")
	}
	if candidate.Username == "" || len(candidate.Username) > 256 ||
		candidate.Scope == "" || stringsContainNUL(candidate.Username) || stringsContainNUL(candidate.Scope) ||
		candidate.CreatedAt.IsZero() || candidate.ExpiresAt.IsZero() ||
		!candidate.CreatedAt.Before(candidate.ExpiresAt) {
		return key, Session{}, errors.New("remembered-session file contains an invalid session record")
	}
	lifetime := candidate.ExpiresAt.Sub(candidate.CreatedAt)
	return key, Session{
		Username:      candidate.Username,
		CSRFToken:     candidate.CSRFToken,
		CreatedAt:     candidate.CreatedAt,
		LastSeen:      candidate.CreatedAt,
		ExpiresAt:     candidate.ExpiresAt,
		IdleExpiresAt: candidate.ExpiresAt,
		Remembered:    true,
		idleTimeout:   lifetime,
		persistScope:  candidate.Scope,
	}, nil
}

func stringsContainNUL(value string) bool {
	for _, character := range value {
		if character == '\x00' {
			return true
		}
	}
	return false
}

func (s *Store) persistLocked() error {
	if s.persist == "" {
		return nil
	}
	records := make([]persistentSessionRecord, 0, len(s.sessions))
	for key, session := range s.sessions {
		if !session.Remembered || session.Username != s.user || session.persistScope != s.scope {
			continue
		}
		records = append(records, persistentSessionRecord{
			TokenHash: hex.EncodeToString(key[:]),
			Username:  session.Username,
			CSRFToken: session.CSRFToken,
			CreatedAt: session.CreatedAt,
			ExpiresAt: session.ExpiresAt,
			Scope:     session.persistScope,
		})
	}
	sort.Slice(records, func(left, right int) bool {
		return records[left].TokenHash < records[right].TokenHash
	})
	contents, err := json.MarshalIndent(persistentSessionFile{
		Version: persistentSessionVersion, Sessions: records,
	}, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')

	directory := filepath.Dir(s.persist)
	temporary, err := os.CreateTemp(directory, ".remembered-sessions-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0600); err != nil {
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, s.persist); err != nil {
		return err
	}
	directoryFile, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer directoryFile.Close()
	return directoryFile.Sync()
}

func validatePrivateDirectory(path string, info os.FileInfo) error {
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is not a private directory", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("%s is not owned by the service account", path)
	}
	return nil
}

func validatePrivateFile(path string, info os.FileInfo) error {
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is not a private regular file", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return fmt.Errorf("%s has unsafe ownership or link count", path)
	}
	return nil
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
