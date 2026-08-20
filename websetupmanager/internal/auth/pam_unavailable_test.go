//go:build !pam

package auth

import (
	"context"
	"errors"
	"testing"
)

func TestPAMUnavailableWithoutProductionBuildTag(t *testing.T) {
	if PAMAvailable() {
		t.Fatal("PAMAvailable returned true without the pam build tag")
	}
	if err := NewPAMAuthenticator("websetupmanager").Authenticate(context.Background(), "operator", "secret"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Authenticate error = %v, want ErrUnavailable", err)
	}
}

func TestPAMAdapterRejectsNULBeforePlatformBoundary(t *testing.T) {
	authenticator := NewPAMAuthenticator("websetupmanager")
	for _, credentials := range []struct {
		username string
		password string
	}{
		{username: "operator\x00other", password: "secret"},
		{username: "operator", password: "secret\x00other"},
	} {
		if err := authenticator.Authenticate(context.Background(), credentials.username, credentials.password); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("Authenticate(%q) error = %v, want ErrInvalidCredentials", credentials.username, err)
		}
	}
	if err := NewPAMAuthenticator("bad\x00service").Authenticate(context.Background(), "operator", "secret"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("invalid PAM service error = %v, want ErrUnavailable", err)
	}
}
