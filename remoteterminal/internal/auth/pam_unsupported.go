//go:build pam && (!linux || !cgo)
// +build pam
// +build !linux !cgo

package auth

import "context"

type unsupportedPAM struct{}

func newPAMAuthenticator(string) Authenticator { return unsupportedPAM{} }

func (unsupportedPAM) Authenticate(context.Context, string, string) error {
	return ErrUnavailable
}
