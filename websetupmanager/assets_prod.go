//go:build production

// Package webassets exposes the production Vite output bundled into the Web
// Setup Manager process. The build pipeline must run npm build first.
package webassets

import (
	"embed"
	"io/fs"
)

//go:embed web/dist
var embedded embed.FS

// FS returns the production frontend filesystem.
func FS() (fs.FS, error) {
	return fs.Sub(embedded, "web/dist")
}
