//go:build !pam
// +build !pam

package auth

import "context"

type unavailablePAM struct{}

func newPAMAuthenticator(string) Authenticator { return unavailablePAM{} }

func (unavailablePAM) Authenticate(context.Context, string, string) error {
	return ErrUnavailable
}
