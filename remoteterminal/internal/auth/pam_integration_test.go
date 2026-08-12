//go:build linux && cgo && pam
// +build linux,cgo,pam

package auth

import (
	"context"
	"os"
	"testing"
)

// TestPAMConfiguredAccount is the deployment feasibility checkpoint. It is
// intentionally opt-in because automated tests must not contain a real system
// password. Run it as the final systemd account with the two environment
// variables set on a disposable target.
func TestPAMConfiguredAccount(t *testing.T) {
	username := os.Getenv("REMOTE_TERMINAL_PAM_TEST_USERNAME")
	password := os.Getenv("REMOTE_TERMINAL_PAM_TEST_PASSWORD")
	if username == "" || password == "" {
		t.Skip("PAM integration credentials are not configured")
	}
	if err := NewPAMAuthenticator("remoteterminal").Authenticate(context.Background(), username, password); err != nil {
		t.Fatalf("PAM authentication/account check failed: %v", err)
	}
}
