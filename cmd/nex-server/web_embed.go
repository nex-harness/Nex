package main

import (
	"io/fs"

	"github.com/nex-harness/nex/pkg/webui"
)

func embeddedWebFS() fs.FS {
	return webui.DistFS()
}
