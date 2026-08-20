//go:build linux && cgo && pam

package auth

import (
	"context"
	"os"
	"testing"
)

// TestPAMConfiguredAccount is an opt-in deployment checkpoint. Run it only as
// the final service account on a disposable target; automated tests must never
// contain or inject a real system password by default.
func TestPAMConfiguredAccount(t *testing.T) {
	username := os.Getenv("WEB_SETUP_MANAGER_PAM_TEST_USERNAME")
	password := os.Getenv("WEB_SETUP_MANAGER_PAM_TEST_PASSWORD")
	if username == "" || password == "" {
		t.Skip("PAM integration credentials are not configured")
	}
	if !PAMAvailable() {
		t.Fatal("PAM production build reports unavailable")
	}
	if err := NewPAMAuthenticator("websetupmanager").Authenticate(context.Background(), username, password); err != nil {
		t.Fatalf("PAM authentication/account check failed: %v", err)
	}
}
