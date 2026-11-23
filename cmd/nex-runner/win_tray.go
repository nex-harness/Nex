//go:build windows

package main

import (
	"context"
	_ "embed"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"github.com/nex-harness/nex/pkg/runner"
	"golang.org/x/sys/windows"
)

//go:embed tray_icon.ico
var trayIconBytes []byte

var (
	modUser32  = windows.NewLazySystemDLL("user32.dll")
	modShell32 = windows.NewLazySystemDLL("shell32.dll")

	procRegisterClassExW  = modUser32.NewProc("RegisterClassExW")
	procCreateWindowExW   = modUser32.NewProc("CreateWindowExW")
	procDefWindowProcW    = modUser32.NewProc("DefWindowProcW")
	procDestroyWindow     = modUser32.NewProc("DestroyWindow")
	procPostMessageW      = modUser32.NewProc("PostMessageW")
	procPostQuitMessage   = modUser32.NewProc("PostQuitMessage")
	procGetMessageW       = modUser32.NewProc("GetMessageW")
	procTranslateMessage  = modUser32.NewProc("TranslateMessage")
	procDispatchMessageW  = modUser32.NewProc("DispatchMessageW")
	procCreatePopupMenu   = modUser32.NewProc("CreatePopupMenu")
	procDestroyMenu       = modUser32.NewProc("DestroyMenu")
	procAppendMenuW       = modUser32.NewProc("AppendMenuW")
	procTrackPopupMenu    = modUser32.NewProc("TrackPopupMenu")
	procGetCursorPos      = modUser32.NewProc("GetCursorPos")
	procSetForegroundWin  = modUser32.NewProc("SetForegroundWindow")
	procLoadImageW        = modUser32.NewProc("LoadImageW")
	procLoadIconW         = modUser32.NewProc("LoadIconW")
	procDestroyIcon       = modUser32.NewProc("DestroyIcon")
	procShellNotifyIconW  = modShell32.NewProc("Shell_NotifyIconW")
)

const (
	wmDestroy     = 0x0002
	wmClose       = 0x0010
	wmCommand     = 0x0111
	wmApp         = 0x8000
	wmTrayEvent   = wmApp + 1
	wmLButtonDbl  = 0x0203
	wmRButtonUp   = 0x0205
	wmContextMenu = 0x007B

	nimAdd    = 0x00000000
	nimModify = 0x00000001
	nimDelete = 0x00000002

	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004

	mfString  = 0x00000000
	mfGrayed  = 0x00000001
	mfChecked = 0x00000008
	mfSep     = 0x00000800

	tpmRightAlign  = 0x0008
	tpmBottomAlign = 0x0020
	tpmNonotify    = 0x0080
	tpmReturnCmd   = 0x0100

	imageIcon       = 1
	lrLoadFromFile  = 0x00000010
	lrDefaultSize   = 0x00000040
	idiApplication  = 32512
	hwndMessage     = ^uintptr(2) // HWND_MESSAGE = (HWND)-3

	menuIDStatus       = 1001
	menuIDUpdate       = 1002
	menuIDElevate      = 1003
	menuIDAutostart    = 1004
	menuIDOpenDataDir  = 1005
	menuIDExit         = 1006
)

type winPoint struct {
	x, y int32
}

type winMsg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      winPoint
}

type wndClassEx struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

type notifyIconData struct {
	cbSize           uint32
	hWnd             uintptr
	uID              uint32
	uFlags           uint32
	uCallbackMessage uint32
	hIcon            uintptr
	szTip            [128]uint16
	dwState          uint32
	dwStateMask      uint32
	szInfo           [256]uint16
	uVersion         uint32
	szInfoTitle      [64]uint16
	dwInfoFlags      uint32
	guidItem         [16]byte
	hBalloonIcon     uintptr
}

type windowsTrayHost struct {
	ctx     context.Context
	cancel  context.CancelFunc
	node    *runner.Node
	rawArgs []string

	mu      sync.Mutex
	hwnd    uintptr
	hIcon   uintptr
	nid     notifyIconData
	iconTmp string
	closed  bool
}

func startWindowsTray(ctx context.Context, cancel context.CancelFunc, node *runner.Node, rawArgs []string, trayEnabled bool) func() {
	if !trayEnabled {
		return func() {}
	}
	host := &windowsTrayHost{
		ctx:     ctx,
		cancel:  cancel,
		node:    node,
		rawArgs: append([]string(nil), rawArgs...),
	}
	ready := make(chan struct{})
	go host.runLoop(ready)
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
	}
	return host.Cleanup
}

func (h *windowsTrayHost) runLoop(ready chan<- struct{}) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	var hMod windows.Handle
	_ = windows.GetModuleHandleEx(0, nil, &hMod)

	className, _ := windows.UTF16PtrFromString("NexRunnerTrayClass")
	winTitle, _ := windows.UTF16PtrFromString("NexRunnerTray")

	wndProc := windows.NewCallback(func(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
		switch msg {
		case wmTrayEvent:
			ev := uint32(lParam & 0xFFFF)
			if ev == wmRButtonUp || ev == wmContextMenu || ev == wmLButtonDbl {
				h.showContextMenu(hwnd)
			}
			return 0
		case wmClose:
			procDestroyWindow.Call(hwnd)
			return 0
		case wmDestroy:
			procPostQuitMessage.Call(0)
			return 0
		default:
			ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
			return ret
		}
	})

	wc := wndClassEx{
		cbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		lpfnWndProc:   wndProc,
		hInstance:     uintptr(hMod),
		lpszClassName: className,
	}
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	hwnd, _, err := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(winTitle)),
		0,
		0, 0, 0, 0,
		hwndMessage,
		0,
		uintptr(hMod),
		0,
	)
	if hwnd == 0 {
		log.Printf("nex-runner: failed to create tray message window: %v", err)
		close(ready)
		return
	}

	iconHandle, iconPath := loadTrayIcon(h.node.DataDir())
	h.mu.Lock()
	h.hwnd = hwnd
	h.hIcon = iconHandle
	h.iconTmp = iconPath
	h.nid = notifyIconData{
		cbSize:           uint32(unsafe.Sizeof(notifyIconData{})),
		hWnd:             hwnd,
		uID:              1,
		uFlags:           nifMessage | nifIcon | nifTip,
		uCallbackMessage: wmTrayEvent,
		hIcon:            iconHandle,
	}
	copyUTF16Tip(&h.nid.szTip, h.buildTooltip())
	nidCopy := h.nid
	h.mu.Unlock()

	procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nidCopy)))
	close(ready)

	go func() {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-h.ctx.Done():
				h.Cleanup()
				return
			case <-ticker.C:
				h.updateTooltip()
			}
		}
	}()

	var m winMsg
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func (h *windowsTrayHost) buildStatusSummary() string {
	conn := "Offline"
	if h.node.IsOnline() {
		conn = "Online"
	}
	priv := "Standard"
	if h.node.Elevated() {
		priv = "Admin"
	}
	devTag := ""
	if h.node.IsDev() {
		devTag = " (dev)"
	}
	return fmt.Sprintf("Nex Runner %s%s - %s [%s]", h.node.Version(), devTag, conn, priv)
}

func (h *windowsTrayHost) buildTooltip() string {
	return h.buildStatusSummary()
}

func (h *windowsTrayHost) updateTooltip() {
	h.mu.Lock()
	if h.closed || h.hwnd == 0 {
		h.mu.Unlock()
		return
	}
	h.nid.uFlags = nifTip
	copyUTF16Tip(&h.nid.szTip, h.buildTooltip())
	nidCopy := h.nid
	h.mu.Unlock()

	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&nidCopy)))
}

func (h *windowsTrayHost) showContextMenu(hwnd uintptr) {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)

	appendMenuText(menu, mfGrayed, menuIDStatus, h.buildStatusSummary())
	procAppendMenuW.Call(menu, mfSep, 0, 0)
	appendMenuText(menu, mfString, menuIDUpdate, "Check & Apply Update")

	elevFlags := uintptr(mfString)
	elevText := "Restart as Administrator"
	if h.node.Elevated() {
		elevFlags = mfGrayed
		elevText = "Running as Administrator"
	}
	appendMenuText(menu, elevFlags, menuIDElevate, elevText)

	installed, _, _ := autostartStatus()
	autoFlags := uintptr(mfString)
	if installed {
		autoFlags |= mfChecked
	}
	appendMenuText(menu, autoFlags, menuIDAutostart, "Autostart on Logon (Admin)")
	appendMenuText(menu, mfString, menuIDOpenDataDir, "Open Data Folder")
	procAppendMenuW.Call(menu, mfSep, 0, 0)
	appendMenuText(menu, mfString, menuIDExit, "Exit")

	var pt winPoint
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundWin.Call(hwnd)
	cmd, _, _ := procTrackPopupMenu.Call(
		menu,
		tpmRightAlign|tpmBottomAlign|tpmNonotify|tpmReturnCmd,
		uintptr(pt.x),
		uintptr(pt.y),
		0,
		hwnd,
		0,
	)
	procPostMessageW.Call(hwnd, 0, 0, 0)

	switch uint32(cmd) {
	case menuIDUpdate:
		if h.node.Updater() != nil && !h.node.Updater().TriggerManualFromLastWelcome(h.ctx) {
			log.Printf("nex-runner (%s): no pending update available right now", h.node.RunnerID())
		}
	case menuIDElevate:
		if !h.node.Elevated() {
			if err := relaunchAsAdministrator(h.rawArgs); err != nil {
				log.Printf("nex-runner: UAC elevation cancelled or failed: %v", err)
			} else {
				h.Cleanup()
				_ = h.node.Close()
				if h.cancel != nil {
					h.cancel()
				}
			}
		}
	case menuIDAutostart:
		if installed {
			if err := uninstallAutostart(); err != nil {
				log.Printf("nex-runner: uninstall autostart failed: %v", err)
			}
		} else {
			if err := installAutostart(h.rawArgs); err != nil {
				log.Printf("nex-runner: install autostart failed: %v", err)
			}
		}
	case menuIDOpenDataDir:
		_ = exec.Command("explorer.exe", h.node.DataDir()).Start()
	case menuIDExit:
		h.Cleanup()
		if h.cancel != nil {
			h.cancel()
		}
	}
}

func (h *windowsTrayHost) Cleanup() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	hwnd := h.hwnd
	hIcon := h.hIcon
	iconTmp := h.iconTmp
	nidCopy := h.nid
	h.hwnd = 0
	h.hIcon = 0
	h.mu.Unlock()

	if hwnd != 0 {
		procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nidCopy)))
		procPostMessageW.Call(hwnd, wmClose, 0, 0)
	}
	if hIcon != 0 {
		procDestroyIcon.Call(hIcon)
	}
	if iconTmp != "" {
		_ = os.Remove(iconTmp)
	}
}

func appendMenuText(menu, flags uintptr, id uintptr, text string) {
	ptr, err := windows.UTF16PtrFromString(text)
	if err != nil {
		return
	}
	procAppendMenuW.Call(menu, flags, id, uintptr(unsafe.Pointer(ptr)))
}

func copyUTF16Tip(dst *[128]uint16, s string) {
	for i := range dst {
		dst[i] = 0
	}
	u16, err := windows.UTF16FromString(s)
	if err != nil {
		return
	}
	if len(u16) > len(dst) {
		u16 = u16[:len(dst)-1]
		u16 = append(u16, 0)
	}
	copy(dst[:], u16)
}

func loadTrayIcon(dataDir string) (uintptr, string) {
	tmpDir := dataDir
	if tmpDir == "" {
		tmpDir = os.TempDir()
	}
	_ = os.MkdirAll(tmpDir, 0o755)
	icoPath := filepath.Join(tmpDir, fmt.Sprintf("nex-tray-%d.ico", os.Getpid()))
	if err := os.WriteFile(icoPath, trayIconBytes, 0o600); err == nil {
		ptr, _ := windows.UTF16PtrFromString(icoPath)
		hIcon, _, _ := procLoadImageW.Call(
			0,
			uintptr(unsafe.Pointer(ptr)),
			imageIcon,
			16, 16,
			lrLoadFromFile|lrDefaultSize,
		)
		if hIcon != 0 {
			return hIcon, icoPath
		}
		_ = os.Remove(icoPath)
	}
	fallback, _, _ := procLoadIconW.Call(0, idiApplication)
	return fallback, ""
}
