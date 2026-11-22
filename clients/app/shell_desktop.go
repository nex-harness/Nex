//go:build !ios && !android

package app

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/egoist/mygo"
)

// DesktopRunOptions configures the native mygo Desktop shell.
type DesktopRunOptions struct {
	DataDir      string
	WorkspaceDir string
	LocalAddr    string
	ServerURL    string
	ForceLocal   bool
}

// RunDesktop launches the cross-platform mygo Desktop application (macOS, Linux, Windows).
func RunDesktop(opts DesktopRunOptions) error {
	mygo.App.SetName("Nex")

	generating := os.Getenv("MYGO_GENERATE") != ""
	var (
		mu      sync.Mutex
		mainWin *mygo.Window
	)

	getOrCreateWindow := func() *mygo.Window {
		mu.Lock()
		defer mu.Unlock()
		if mainWin == nil || mainWin.IsDestroyed() {
			winOpts := mygo.WindowOptions{
				Title:           "Nex",
				Width:           1320,
				Height:          860,
				MinWidth:        380,
				MinHeight:       520,
				BackgroundColor: "light-dark(#f8f8f8, #161616)",
				URL:             "/",
			}
			if runtime.GOOS == "darwin" {
				winOpts.TitleBarStyle = mygo.TitleBarHidden
				winOpts.TitleBarHeight = 40
			}
			mainWin = mygo.NewWindow(winOpts)
		}
		mainWin.Restore()
		mainWin.Show()
		mainWin.Focus()
		return mainWin
	}

	if !generating {
		if !mygo.App.RequestSingleInstanceLock() {
			return nil
		}
		mygo.App.OnSecondInstance(func(_ []string, _ string) {
			getOrCreateWindow()
		})
	}

	dataDir := opts.DataDir
	if dataDir == "" {
		if p, err := mygo.App.Path(mygo.PathUserData); err == nil && p != "" {
			dataDir = p
		} else {
			home, _ := os.UserHomeDir()
			dataDir = filepath.Join(home, ".nex-desktop")
		}
	}

	localAddr := opts.LocalAddr
	if localAddr == "" {
		localAddr = "127.0.0.1:0"
	}

	svc := NewHostService(HostServiceOptions{
		ConfigDir:    dataDir,
		WorkspaceDir: opts.WorkspaceDir,
		LocalAddr:    localAddr,
	})

	if !generating {
		if opts.ServerURL != "" {
			if _, err := svc.SetServerURL(opts.ServerURL); err != nil {
				log.Printf("nex-app invalid -server URL %q: %v", opts.ServerURL, err)
			}
		} else if opts.ForceLocal {
			svc.mu.Lock()
			svc.cfg.ConnectionMode = "local"
			_ = svc.saveConfigLocked()
			svc.mu.Unlock()
		}
	}

	mygo.BindAs("NexHost", svc)

	mygo.App.OnOpenURL(func(rawURL string) {
		getOrCreateWindow()
		svc.handleDeepLink(rawURL)
	})

	mygo.App.OnNotificationClick(func(id string) {
		getOrCreateWindow()
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

	// Keep alive in Dock / system tray when last window closes.
	mygo.App.OnWindowAllClosed(func() {})

	mygo.App.OnActivate(func(hasVisibleWindows bool) {
		if !hasVisibleWindows {
			getOrCreateWindow()
		}
	})

	mygo.App.OnQuit(func() {
		svc.closeHost()
	})

	mygo.App.WhenReady(func() {
		svc.setAppReady(true)

		if svc.startupMode() == "local" {
			if _, err := svc.ensureLocalStarted(context.Background()); err != nil {
				log.Printf("nex-app embedded local mode failed: %v", err)
			}
		}

		mygo.App.SetMenu(mygo.NewMenu([]*mygo.MenuItem{
			{Role: mygo.RoleAppMenu},
			{Label: "File", Submenu: []*mygo.MenuItem{
				{Label: "New Task", Accelerator: "CmdOrCtrl+N", Click: func(*mygo.MenuItem, *mygo.Window) {
					getOrCreateWindow()
					_ = EventNewTask.Broadcast(true)
				}},
				{Label: "Settings…", Accelerator: "CmdOrCtrl+,", Click: func(*mygo.MenuItem, *mygo.Window) {
					getOrCreateWindow()
					_ = EventOpenSettings.Broadcast("general")
				}},
				mygo.Separator(),
				{Role: mygo.RoleClose},
			}},
			{Role: mygo.RoleEditMenu},
			{Role: mygo.RoleViewMenu},
			{Role: mygo.RoleWindowMenu},
		}))

		if _, err := mygo.NewTray(mygo.TrayOptions{
			Icon:           desktopTrayIcon(),
			IconIsTemplate: true,
			ToolTip:        "Nex",
			Menu: mygo.NewMenu([]*mygo.MenuItem{
				{Label: "Show Nex", Click: func(*mygo.MenuItem, *mygo.Window) { getOrCreateWindow() }},
				{Label: "New Task", Click: func(*mygo.MenuItem, *mygo.Window) {
					getOrCreateWindow()
					_ = EventNewTask.Broadcast(true)
				}},
				{Label: "Settings…", Click: func(*mygo.MenuItem, *mygo.Window) {
					getOrCreateWindow()
					_ = EventOpenSettings.Broadcast("general")
				}},
				mygo.Separator(),
				{Role: mygo.RoleQuit},
			}),
		}); err != nil {
			log.Printf("nex-app tray init skipped: %v", err)
		}

		if err := mygo.GlobalShortcut.Register("CmdOrCtrl+Shift+Space", func() {
			getOrCreateWindow()
		}); err != nil {
			log.Printf("nex-app global shortcut skipped: %v", err)
		}

		getOrCreateWindow()
	})

	return mygo.App.Run()
}

func desktopTrayIcon() []byte {
	img := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	// Crisp monochrome "N" glyph for macOS template / tray icon.
	for y := 6; y <= 25; y++ {
		for x := 7; x <= 24; x++ {
			left := x >= 7 && x <= 10
			right := x >= 21 && x <= 24
			diagY := 6 + (x-8)*(19)/14
			diag := x >= 8 && x <= 23 && y >= diagY-2 && y <= diagY+2
			if left || right || diag {
				img.Set(x, y, color.Black)
			}
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}
