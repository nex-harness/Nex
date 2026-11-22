//go:build !ios && !android

package app

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
)

// SleepInhibitor keeps macOS from idle-sleeping while local server + remote sessions are active.
type SleepInhibitor struct {
	mu  sync.Mutex
	cmd *exec.Cmd
}

// Start begins a lightweight idle-sleep assertion on supported desktop hosts (no-op elsewhere).
func (s *SleepInhibitor) Start() {
	if runtime.GOOS != "darwin" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != nil {
		return
	}
	// Ties assertion lifetime to current process PID via `-w` so OS cleans it up automatically even on crash.
	cmd := exec.Command("caffeinate", "-i", "-w", strconv.Itoa(os.Getpid()))
	if err := cmd.Start(); err == nil {
		s.cmd = cmd
	}
}

// Stop releases the idle-sleep assertion cleanly.
func (s *SleepInhibitor) Stop() {
	s.mu.Lock()
	cmd := s.cmd
	s.cmd = nil
	s.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
}
