//go:build ios || android

package app

import (
	"context"
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
		if sid := parseNotificationSessionID(id); sid != "" {
			_ = EventOpenSession.Broadcast(sid)
		}
	})

	mygo.Power.OnResume(func() {
		go func() { _, _ = svc.ReconnectRemote(context.Background()) }()
		_ = EventAppResume.Broadcast(time.Now().UnixMilli())
	})

	mygo.App.OnDidBecomeActive(func() {
		mygo.ClearNotifications()
		go func() { _, _ = svc.ReconnectRemote(context.Background()) }()
		_ = EventAppResume.Broadcast(time.Now().UnixMilli())
	})

	mygo.App.OnMemoryWarning(func() {
		// No heavy in-memory caches in the thin mobile shell.
	})

	mygo.App.WhenReady(func() {
		svc.setAppReady(true)
		mygo.SystemBars.SetStyle(mygo.BarStyleAuto)
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:           "Nex",
			BackgroundColor: "light-dark(#f8f8f8, #161616)",
			URL:             "/",
		})
		configureWindowPermissions(win)
		go svc.ensureStartupConnected(context.Background())
	})

	return mygo.App.Run()
}
