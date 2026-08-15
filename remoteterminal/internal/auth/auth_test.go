package auth

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreCreatesOpaqueTokensAndExpiresIdleSession(t *testing.T) {
	now := time.Unix(1000, 0)
	store := NewStore(time.Minute, time.Hour, 2)
	store.now = func() time.Time { return now }
	store.random = bytes.NewReader(append(bytes.Repeat([]byte{0x42}, 32), bytes.Repeat([]byte{0x43}, 32)...))
	token, session, err := store.Create("operator")
	if err != nil {
		t.Fatal(err)
	}
	if token == "" || token == session.CSRFToken || session.Username != "operator" {
		t.Fatalf("tokens/session were not created safely: token=%q session=%+v", token, session)
	}
	if !store.Valid(token) || !session.Deadline().Equal(session.IdleExpiresAt) {
		t.Fatal("new session validity/deadline is incorrect")
	}
	if _, ok := store.Get(token); !ok {
		t.Fatal("new session was not found")
	}
	now = now.Add(30 * time.Second)
	if refreshed, ok := store.Get(token); !ok || !refreshed.LastSeen.Equal(now) {
		t.Fatalf("idle session was not refreshed: %+v, %v", refreshed, ok)
	}
	now = now.Add(time.Minute + time.Nanosecond)
	if _, ok := store.Get(token); ok {
		t.Fatal("idle-expired session remained valid")
	}
	if store.Valid(token) {
		t.Fatal("Valid accepted an expired token")
	}
}

func TestStoreAbsoluteExpiryAndDelete(t *testing.T) {
	now := time.Unix(2000, 0)
	store := NewStore(3*time.Hour, 2*time.Hour, 2)
	store.now = func() time.Time { return now }
	store.random = bytes.NewReader(bytes.Repeat([]byte{0x21}, 128))
	token, _, err := store.Create("operator")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(90 * time.Minute)
	if _, ok := store.Get(token); !ok {
		t.Fatal("session expired before its absolute deadline")
	}
	now = time.Unix(2000, 0).Add(2 * time.Hour)
	if _, ok := store.Get(token); ok {
		t.Fatal("session remained valid at its absolute deadline")
	}

	store.random = bytes.NewReader(bytes.Repeat([]byte{0x33}, 64))
	now = time.Unix(5000, 0)
	token, _, err = store.Create("operator")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(token); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get(token); ok {
		t.Fatal("deleted session remained valid")
	}
}

func TestStoreCapacity(t *testing.T) {
	store := NewStore(time.Hour, time.Hour, 1)
	store.random = bytes.NewReader(bytes.Repeat([]byte{0x11}, 128))
	if _, _, err := store.Create("operator"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Create("operator"); !errors.Is(err, ErrSessionCapacity) {
		t.Fatalf("second Create() error = %v, want ErrSessionCapacity", err)
	}
}

func TestStoreCreatesRememberedSessionWithFixedLongDeadline(t *testing.T) {
	now := time.Unix(7000, 0)
	store := NewStore(time.Minute, time.Hour, 2)
	store.now = func() time.Time { return now }
	store.random = bytes.NewReader(bytes.Repeat([]byte{0x52}, 64))

	token, session, err := store.CreateRemembered("operator", 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !session.Remembered || !session.ExpiresAt.Equal(now.Add(30*24*time.Hour)) ||
		!session.IdleExpiresAt.Equal(session.ExpiresAt) {
		t.Fatalf("remembered session = %+v", session)
	}

	now = now.Add(15 * 24 * time.Hour)
	refreshed, ok := store.Get(token)
	if !ok || !refreshed.IdleExpiresAt.Equal(session.ExpiresAt) {
		t.Fatalf("remembered session deadline moved or expired: %+v, %v", refreshed, ok)
	}
	now = session.ExpiresAt
	if _, ok := store.Get(token); ok {
		t.Fatal("remembered session survived its fixed deadline")
	}
	if _, _, err := store.CreateRemembered("operator", 0); !errors.Is(err, ErrInvalidLifetime) {
		t.Fatalf("zero remembered lifetime error = %v", err)
	}
}

func TestRememberedSessionPersistsAsAHashAcrossRestartAndLogout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth", "remembered-sessions.json")
	now := time.Now().UTC().Truncate(time.Second)
	store, err := NewPersistentStore(time.Minute, time.Hour, 8, path, "operator", "https")
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return now }
	store.random = bytes.NewReader(bytes.Join([][]byte{
		bytes.Repeat([]byte{0x61}, 32),
		bytes.Repeat([]byte{0x62}, 32),
		bytes.Repeat([]byte{0x63}, 32),
		bytes.Repeat([]byte{0x64}, 32),
	}, nil))
	token, created, err := store.CreateRemembered("operator", 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ordinaryToken, _, err := store.Create("operator")
	if err != nil {
		t.Fatal(err)
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), token) || strings.Contains(string(contents), ordinaryToken) {
		t.Fatal("persistent authentication state contains a raw browser token")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("remembered-session file mode = %v", info.Mode().Perm())
	}
	directoryInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if directoryInfo.Mode().Perm() != 0700 {
		t.Fatalf("remembered-session directory mode = %v", directoryInfo.Mode().Perm())
	}

	restored, err := NewPersistentStore(time.Minute, time.Hour, 8, path, "operator", "https")
	if err != nil {
		t.Fatal(err)
	}
	restoredSession, ok := restored.Get(token)
	if !ok || restoredSession.CSRFToken != created.CSRFToken || !restoredSession.Remembered {
		t.Fatalf("restored remembered session = %+v, %v", restoredSession, ok)
	}
	if _, ok := restored.Get(ordinaryToken); ok {
		t.Fatal("ordinary browser session was persisted")
	}
	if err := restored.Delete(token); err != nil {
		t.Fatal(err)
	}

	afterLogout, err := NewPersistentStore(time.Minute, time.Hour, 8, path, "operator", "https")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := afterLogout.Get(token); ok {
		t.Fatal("logged-out remembered session returned after restart")
	}
}

func TestPersistentStoreInvalidatesRememberedSessionsWhenScopeChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth", "remembered-sessions.json")
	store, err := NewPersistentStore(time.Minute, time.Hour, 8, path, "operator", "http")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := store.CreateRemembered("operator", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	changed, err := NewPersistentStore(time.Minute, time.Hour, 8, path, "operator", "https")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := changed.Get(token); ok {
		t.Fatal("remembered session crossed authentication cookie modes")
	}
	restoredOriginalScope, err := NewPersistentStore(time.Minute, time.Hour, 8, path, "operator", "http")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := restoredOriginalScope.Get(token); ok {
		t.Fatal("invalidated old-scope session remained on disk")
	}
}

func TestPersistentStoreRejectsSymbolicLinkStateFile(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "auth")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	canary := filepath.Join(root, "canary")
	if err := os.WriteFile(canary, []byte("do not replace"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "remembered-sessions.json")
	if err := os.Symlink(canary, path); err != nil {
		t.Fatal(err)
	}
	if _, err := NewPersistentStore(time.Minute, time.Hour, 8, path, "operator", "https"); err == nil {
		t.Fatal("persistent store accepted a symbolic-link state file")
	}
	contents, err := os.ReadFile(canary)
	if err != nil || string(contents) != "do not replace" {
		t.Fatalf("state-file canary = %q err=%v", contents, err)
	}
}

func TestThrottlerSlidingWindowAndReset(t *testing.T) {
	now := time.Unix(1000, 0)
	limiter := NewThrottler(2, time.Minute)
	limiter.now = func() time.Time { return now }
	if !limiter.Allow("peer") {
		t.Fatal("fresh key rejected")
	}
	limiter.Failure("peer")
	limiter.Failure("peer")
	if limiter.Allow("peer") {
		t.Fatal("limited key allowed")
	}
	limiter.Success("peer")
	if !limiter.Allow("peer") {
		t.Fatal("successful login did not clear failures")
	}
	limiter.Failure("peer")
	now = now.Add(time.Minute + time.Nanosecond)
	if !limiter.Allow("peer") {
		t.Fatal("old failure was not pruned")
	}
}

func TestEqualCSRF(t *testing.T) {
	if !EqualCSRF("known-token", "known-token") {
		t.Fatal("equal tokens rejected")
	}
	for _, value := range []string{"", "known-tokeN", "short"} {
		if EqualCSRF("known-token", value) {
			t.Fatalf("unequal token %q accepted", value)
		}
	}
}
