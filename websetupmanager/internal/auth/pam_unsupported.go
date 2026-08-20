//go:build pam && (!linux || !cgo)

package auth

import "context"

type unsupportedPAM struct{}

func newPAMAuthenticator(string) Authenticator { return unsupportedPAM{} }

func PAMAvailable() bool { return false }

func (unsupportedPAM) Authenticate(context.Context, string, string) error {
	return ErrUnavailable
}
