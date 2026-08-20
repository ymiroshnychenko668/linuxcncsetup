package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	appdb "github.com/ymiroshnychenko668/linuxcncsetup/websetupmanager/internal/database"
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
	key := sha256.Sum256([]byte(token))
	if _, ok := store.sessions[key]; !ok {
		t.Fatal("session map is not keyed by the token hash")
	}
	if !store.Valid(token) || !session.Deadline().Equal(session.IdleExpiresAt) {
		t.Fatal("new session validity/deadline is incorrect")
	}
	now = now.Add(30 * time.Second)
	if refreshed, ok := store.Get(token); !ok || !refreshed.LastSeen.Equal(now) {
		t.Fatalf("idle session was not refreshed: %+v, %v", refreshed, ok)
	}
	now = now.Add(time.Minute + time.Nanosecond)
	if _, ok := store.Get(token); ok || store.Valid(token) {
		t.Fatal("idle-expired session remained valid")
	}
}

func TestStoreAbsoluteExpiryCapacityDeleteAndClose(t *testing.T) {
	now := time.Unix(2000, 0)
	store := NewStore(3*time.Hour, 2*time.Hour, 1)
	store.now = func() time.Time { return now }
	store.random = bytes.NewReader(bytes.Repeat([]byte{0x21}, 256))
	token, _, err := store.Create("operator")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Create("operator"); !errors.Is(err, ErrSessionCapacity) {
		t.Fatalf("second Create error = %v, want ErrSessionCapacity", err)
	}
	now = now.Add(90 * time.Minute)
	if _, ok := store.Get(token); !ok {
		t.Fatal("session expired before its absolute deadline")
	}
	if err := store.Delete(token); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get(token); ok {
		t.Fatal("deleted session remained valid")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("second Close = %v", err)
	}
	if _, _, err := store.Create("operator"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Create after Close error = %v", err)
	}
}

func TestStoreCreatesRememberedSessionWithFixedDeadline(t *testing.T) {
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

func TestRememberedSessionPersistsOnlyHashAcrossRestartAndLogout(t *testing.T) {
	db := openAuthTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	store, err := NewPersistentStore(db.SQL(), time.Minute, time.Hour, 8, "operator", "https")
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

	var tokenHash, username, csrf string
	if err := db.SQL().QueryRow(`SELECT token_hash, username, csrf_token FROM auth_sessions`).Scan(&tokenHash, &username, &csrf); err != nil {
		t.Fatal(err)
	}
	wantHash := sha256.Sum256([]byte(token))
	if tokenHash != hex.EncodeToString(wantHash[:]) || username != "operator" || csrf != created.CSRFToken {
		t.Fatalf("durable session = hash %q user %q csrf %q", tokenHash, username, csrf)
	}
	for _, raw := range []string{token, ordinaryToken} {
		if tokenHash == raw || csrf == raw {
			t.Fatal("persistent authentication state contains a raw browser token")
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	restored, err := NewPersistentStore(db.SQL(), time.Minute, time.Hour, 8, "operator", "https")
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
	var count int
	if err := db.SQL().QueryRow(`SELECT count(*) FROM auth_sessions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("remembered rows after logout = %d, err=%v", count, err)
	}

	afterLogout, err := NewPersistentStore(db.SQL(), time.Minute, time.Hour, 8, "operator", "https")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := afterLogout.Get(token); ok {
		t.Fatal("logged-out remembered session returned after restart")
	}
}

func TestPersistentStoreInvalidatesSessionsWhenUserOrScopeChanges(t *testing.T) {
	for _, test := range []struct {
		name  string
		user  string
		scope string
	}{
		{name: "user", user: "different", scope: "https"},
		{name: "scope", user: "operator", scope: "https-via-proxy"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := openAuthTestDB(t)
			store, err := NewPersistentStore(db.SQL(), time.Minute, time.Hour, 8, "operator", "https")
			if err != nil {
				t.Fatal(err)
			}
			token, _, err := store.CreateRemembered("operator", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			changed, err := NewPersistentStore(db.SQL(), time.Minute, time.Hour, 8, test.user, test.scope)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := changed.Get(token); ok {
				t.Fatal("remembered session crossed its configured user/scope")
			}
			var count int
			if err := db.SQL().QueryRow(`SELECT count(*) FROM auth_sessions`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("invalidated rows = %d, err=%v", count, err)
			}
		})
	}
}

func TestPersistentStoreRejectsMalformedDurableRecord(t *testing.T) {
	db := openAuthTestDB(t)
	csrf := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x72}, 32))
	if _, err := db.SQL().Exec(`
		INSERT INTO auth_sessions(token_hash, username, csrf_token, created_at, expires_at, scope)
		VALUES (?, 'operator', ?, '2026', '2027', 'https')`, strings.Repeat("a", 64), csrf); err != nil {
		t.Fatal(err)
	}
	if _, err := NewPersistentStore(db.SQL(), time.Minute, time.Hour, 8, "operator", "https"); err == nil {
		t.Fatal("persistent store accepted malformed timestamps")
	}
}

func TestCreatePrunesExpiredDurableRows(t *testing.T) {
	db := openAuthTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	store, err := NewPersistentStore(db.SQL(), time.Minute, time.Hour, 8, "operator", "https")
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return now }
	store.random = bytes.NewReader(bytes.Repeat([]byte{0x31}, 128))
	if _, _, err := store.CreateRemembered("operator", time.Hour); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	if _, _, err := store.Create("operator"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.SQL().QueryRow(`SELECT count(*) FROM auth_sessions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("expired durable rows = %d, err=%v", count, err)
	}
}

func TestPersistentDeleteFailureKeepsRememberedSessionValid(t *testing.T) {
	ctx := context.Background()
	db, err := appdb.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPersistentStore(db.SQL(), time.Minute, time.Hour, 8, "operator", "https")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := store.CreateRemembered("operator", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(token); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Delete error = %v, want ErrUnavailable", err)
	}
	if !store.Valid(token) {
		t.Fatal("failed durable logout falsely invalidated the in-memory session")
	}
}

func TestStoreConcurrentAccess(t *testing.T) {
	store := NewStore(time.Hour, 2*time.Hour, 128)
	tokens := make([]string, 32)
	for index := range tokens {
		token, _, err := store.Create("operator")
		if err != nil {
			t.Fatal(err)
		}
		tokens[index] = token
	}
	var workers sync.WaitGroup
	for _, token := range tokens {
		token := token
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 50 {
				_, _ = store.Get(token)
				_ = store.Valid(token)
			}
			if err := store.Delete(token); err != nil {
				t.Errorf("Delete: %v", err)
			}
		}()
	}
	workers.Wait()
}

func TestThrottlerSlidingWindowResetAndBound(t *testing.T) {
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
	for index := range 4200 {
		limiter.Failure(string(rune(index + 1)))
	}
	if len(limiter.attempts) > 4096 {
		t.Fatalf("attacker-controlled throttle keys grew to %d", len(limiter.attempts))
	}
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

func openAuthTestDB(t *testing.T) *appdb.DB {
	t.Helper()
	db, err := appdb.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close DB: %v", err)
		}
	})
	return db
}
