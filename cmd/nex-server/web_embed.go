package main

import (
	"io/fs"
	"testing/fstest"
)

// embeddedWebFS returns the embedded Web UI filesystem (wired to real clients/web/dist once built).
func embeddedWebFS() fs.FS {
	return fstest.MapFS{
		"index.html": &fstest.MapFile{
			Data: []byte("<!doctype html><html><head><title>Nex-v3</title></head><body><div id=\"root\">Nex-v3 Server Ready</div></body></html>"),
		},
	}
}
