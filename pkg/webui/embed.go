package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var rawDist embed.FS

// DistFS returns the embedded production React + libghostty Web UI filesystem.
func DistFS() fs.FS {
	sub, err := fs.Sub(rawDist, "dist")
	if err != nil {
		return rawDist
	}
	return sub
}
