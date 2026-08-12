package auth

// NewPAMAuthenticator returns the platform PAM adapter. Production Linux
// builds must use `-tags pam` with cgo and libpam development headers present.
func NewPAMAuthenticator(service string) Authenticator {
	return newPAMAuthenticator(service)
}
