//go:build !pam

package auth

import "context"

type unavailablePAM struct{}

func newPAMAuthenticator(string) Authenticator { return unavailablePAM{} }

func PAMAvailable() bool { return false }

func (unavailablePAM) Authenticate(context.Context, string, string) error {
	return ErrUnavailable
}
