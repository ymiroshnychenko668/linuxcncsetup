package auth

import (
	"bytes"
	"errors"
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
	store.Delete(token)
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
