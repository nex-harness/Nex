//go:build ios || android

package app

import (
	"os"
	"path/filepath"
	"time"

	"github.com/egoist/mygo"
)

// RunMobile launches the mygo iOS / Android container shell.
func RunMobile() error {
	mygo.App.SetName("Nex")

	dataDir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil || dataDir == "" {
		home, _ := os.UserHomeDir()
		dataDir = filepath.Join(home, ".nex-mobile")
	}

	svc := NewHostService(HostServiceOptions{
		ConfigDir: dataDir,
	})
	mygo.BindAs("NexHost", svc)

	mygo.App.OnOpenURL(func(rawURL string) {
		svc.handleDeepLink(rawURL)
	})

	mygo.App.OnNotificationClick(func(id string) {
		if id != "" {
			_ = EventOpenSession.Broadcast(id)
		}
	})

	mygo.Power.OnResume(func() {
		_ = EventAppResume.Broadcast(time.Now().UnixMilli())
	})

	mygo.App.OnDidBecomeActive(func() {
		mygo.ClearNotifications()
		_ = EventAppResume.Broadcast(time.Now().UnixMilli())
	})

	mygo.App.OnMemoryWarning(func() {
		// No heavy in-memory caches in the thin mobile shell.
	})

	mygo.App.WhenReady(func() {
		svc.setAppReady(true)
		mygo.SystemBars.SetStyle(mygo.BarStyleAuto)
		mygo.NewWindow(mygo.WindowOptions{
			Title:           "Nex",
			BackgroundColor: "light-dark(#f8f8f8, #161616)",
			URL:             "/",
		})
	})

	return mygo.App.Run()
}
