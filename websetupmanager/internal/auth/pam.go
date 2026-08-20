package auth

import (
	"context"
	"strings"
)

// NewPAMAuthenticator returns the platform PAM adapter. Production Linux
// builds must use `-tags pam` with cgo and libpam development headers present.
func NewPAMAuthenticator(service string) Authenticator {
	return validatedPAM{service: service, delegate: newPAMAuthenticator(service)}
}

type validatedPAM struct {
	service  string
	delegate Authenticator
}

func (p validatedPAM) Authenticate(ctx context.Context, username, password string) error {
	if ctx.Err() != nil {
		return ErrUnavailable
	}
	if p.service == "" || strings.ContainsRune(p.service, '\x00') {
		return ErrUnavailable
	}
	// C.CString truncates at NUL. Reject it before crossing the cgo boundary so
	// neither the configured account nor password can be interpreted differently
	// by Go validation and PAM.
	if username == "" || password == "" || strings.ContainsRune(username, '\x00') || strings.ContainsRune(password, '\x00') {
		return ErrInvalidCredentials
	}
	return p.delegate.Authenticate(ctx, username, password)
}
