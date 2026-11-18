//go:build windows

package pty

import (
	"os"
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsConPTY struct {
	hpc       windows.Handle
	inWrite   *os.File
	outRead   *os.File
	proc      windows.Handle
	thread    windows.Handle
	closeOnce sync.Once
}

func startPlatformPTY(cwd, shell string, cols, rows uint16) (platformPTY, string, error) {
	if shell == "" {
		if p, err := exec.LookPath("pwsh.exe"); err == nil && p != "" {
			shell = p
		} else if p, err := exec.LookPath("powershell.exe"); err == nil && p != "" {
			shell = p
		} else {
			shell = "cmd.exe"
		}
	}

	var ptyInRead, hostInWrite windows.Handle
	if err := windows.CreatePipe(&ptyInRead, &hostInWrite, nil, 0); err != nil {
		return nil, "", err
	}
	var hostOutRead, ptyOutWrite windows.Handle
	if err := windows.CreatePipe(&hostOutRead, &ptyOutWrite, nil, 0); err != nil {
		_ = windows.CloseHandle(ptyInRead)
		_ = windows.CloseHandle(hostInWrite)
		return nil, "", err
	}

	size := windows.Coord{X: int16(cols), Y: int16(rows)}
	var hpc windows.Handle
	if err := windows.CreatePseudoConsole(size, ptyInRead, ptyOutWrite, 0, &hpc); err != nil {
		_ = windows.CloseHandle(ptyInRead)
		_ = windows.CloseHandle(hostInWrite)
		_ = windows.CloseHandle(hostOutRead)
		_ = windows.CloseHandle(ptyOutWrite)
		return nil, "", err
	}
	// PseudoConsole duplicates its own pipe ends; close child-side handles in parent
	_ = windows.CloseHandle(ptyInRead)
	_ = windows.CloseHandle(ptyOutWrite)

	attrList, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		windows.ClosePseudoConsole(hpc)
		_ = windows.CloseHandle(hostInWrite)
		_ = windows.CloseHandle(hostOutRead)
		return nil, "", err
	}
	defer attrList.Delete()

	if err := attrList.Update(
		windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE,
		unsafe.Pointer(hpc),
		unsafe.Sizeof(hpc),
	); err != nil {
		windows.ClosePseudoConsole(hpc)
		_ = windows.CloseHandle(hostInWrite)
		_ = windows.CloseHandle(hostOutRead)
		return nil, "", err
	}

	var siEx windows.StartupInfoEx
	siEx.StartupInfo.Cb = uint32(unsafe.Sizeof(siEx))
	siEx.ProcThreadAttributeList = attrList.List()

	cmdLine, err := windows.UTF16PtrFromString(shell)
	if err != nil {
		windows.ClosePseudoConsole(hpc)
		return nil, "", err
	}
	var cwdPtr *uint16
	if cwd != "" {
		cwdPtr, _ = windows.UTF16PtrFromString(cwd)
	}

	var pi windows.ProcessInformation
	err = windows.CreateProcess(
		nil,
		cmdLine,
		nil,
		nil,
		false,
		windows.EXTENDED_STARTUPINFO_PRESENT,
		nil,
		cwdPtr,
		&siEx.StartupInfo,
		&pi,
	)
	if err != nil {
		windows.ClosePseudoConsole(hpc)
		_ = windows.CloseHandle(hostInWrite)
		_ = windows.CloseHandle(hostOutRead)
		return nil, "", err
	}

	return &windowsConPTY{
		hpc:     hpc,
		inWrite: os.NewFile(uintptr(hostInWrite), "conpty-in"),
		outRead: os.NewFile(uintptr(hostOutRead), "conpty-out"),
		proc:    pi.Process,
		thread:  pi.Thread,
	}, shell, nil
}

func (w *windowsConPTY) Read(p []byte) (int, error)  { return w.outRead.Read(p) }
func (w *windowsConPTY) Write(p []byte) (int, error) { return w.inWrite.Write(p) }

func (w *windowsConPTY) Resize(cols, rows uint16) error {
	return windows.ResizePseudoConsole(w.hpc, windows.Coord{X: int16(cols), Y: int16(rows)})
}

func (w *windowsConPTY) Close() error {
	w.closeOnce.Do(func() {
		windows.ClosePseudoConsole(w.hpc)
		_ = windows.TerminateProcess(w.proc, 0)
		_ = w.inWrite.Close()
		_ = w.outRead.Close()
		_ = windows.CloseHandle(w.thread)
		_ = windows.CloseHandle(w.proc)
	})
	return nil
}

func (w *windowsConPTY) Wait() error {
	_, err := windows.WaitForSingleObject(w.proc, windows.INFINITE)
	return err
}
