//go:build ios || android

package main

import (
	"log"

	"github.com/egoist/mygo"
	"github.com/nex-harness/nex/clients/app"
	"github.com/nex-harness/nex/pkg/webui"
)

func main() {
	mygo.SetFrontend(webui.DistFS())
	if err := app.RunMobile(); err != nil {
		log.Fatalf("nex-app mobile exited with error: %v", err)
	}
}
