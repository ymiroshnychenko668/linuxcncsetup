// Package libraryidentity defines the portable binding between a managed
// library marker and its SQLite catalog. It intentionally excludes host path,
// device and inode so a matched cold backup can be restored to new storage.
package libraryidentity

import (
	"crypto/sha256"
	"encoding/hex"
)

// Fingerprint returns a versioned one-way derivation of the persisted random
// library marker. The marker itself remains the stable library ID.
func Fingerprint(libraryID string) string {
	digest := sha256.Sum256([]byte("web-setup-manager/library-fingerprint/v2\x00" + libraryID))
	return hex.EncodeToString(digest[:])
}
