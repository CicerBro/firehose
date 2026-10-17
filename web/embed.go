// Package web holds the embedded, pre-built frontend (web/dist).
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS is the built UI, rooted at dist/.
var FS fs.FS = mustSub(dist, "dist")

func mustSub(f fs.FS, dir string) fs.FS {
	s, err := fs.Sub(f, dir)
	if err != nil {
		panic(err)
	}
	return s
}
