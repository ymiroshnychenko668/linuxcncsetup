//go:build !production

// Package webassets exposes the frontend bundled into the Web Setup Manager
// process. Development Go tests intentionally use a tiny fallback so a clean
// checkout does not require Node.js merely to compile backend packages.
package webassets

import (
	"embed"
	"io/fs"
)

//go:embed web/fallback
var embedded embed.FS

// FS returns the development fallback filesystem.
func FS() (fs.FS, error) {
	return fs.Sub(embedded, "web/fallback")
}
