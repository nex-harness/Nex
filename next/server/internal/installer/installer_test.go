package installer

import (
	"strings"
	"sync"
	"testing"
)

func TestValidateTarget(t *testing.T) {
	cases := []struct {
		name    string
		target  SSHTarget
		wantErr string
	}{
		{"ok", SSHTarget{Host: "h", Username: "u", Password: "p"}, ""},
		{"no host", SSHTarget{Username: "u", Password: "p"}, "host is required"},
		{"no username", SSHTarget{Host: "h", Password: "p"}, "username is required"},
		{"no password (privateKeyPath unsupported this release)", SSHTarget{Host: "h", Username: "u"}, "password authentication is required"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := testCase.target.Validate()
			if testCase.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("error = %v, want %q", err, testCase.wantErr)
			}
		})
	}
}

func TestDefaultPort(t *testing.T) {
	if got := (SSHTarget{Port: 0}).defaultPort(); got != 22 {
		t.Fatalf("default port = %d", got)
	}
	if got := (SSHTarget{Port: 2222}).defaultPort(); got != 2222 {
		t.Fatalf("explicit port = %d", got)
	}
	if got := (SSHTarget{Port: 99999}).defaultPort(); got != 22 {
		t.Fatalf("invalid port should fall back, got %d", got)
	}
}

func TestParsePlatform(t *testing.T) {
	cases := []struct {
		uname       string
		goos, arch  string
		bundle      string
	}{
		{"Linux arm64 aarch64\n", "linux", "arm64", "linux-arm64"},
		{"Darwin arm64\n", "darwin", "arm64", "darwin-arm64"},
		{"Darwin x86_64\n", "darwin", "x64", "darwin-x64"},
		{"Linux x86_64\n", "linux", "x64", "linux-x64"},
		{"MINGW64_NT x86_64\n", "win32", "x64", "win32-x64"},
	}
	for _, testCase := range cases {
		platform := parsePlatform(testCase.uname)
		if platform.GOOS != testCase.goos || platform.GOArch != testCase.arch {
			t.Fatalf("parsePlatform(%q) = %s/%s, want %s/%s", testCase.uname, platform.GOOS, platform.GOArch, testCase.goos, testCase.arch)
		}
		if platform.bundlePlatform() != testCase.bundle {
			t.Fatalf("bundle(%q) = %s, want %s", testCase.uname, platform.bundlePlatform(), testCase.bundle)
		}
	}
}

func TestNodeMajorAtLeast24(t *testing.T) {
	cases := map[string]bool{
		"v24.0.0":  true,
		"v26.12.1": true,
		"v18.0.0":  false,
		"v4.9.1":   false,
		"missing":  false,
		"":         false,
	}
	for version, want := range cases {
		if got := nodeMajorAtLeast24(version); got != want {
			t.Fatalf("nodeMajorAtLeast24(%q) = %v, want %v", version, got, want)
		}
	}
}

func TestLogRingBufferBounds(t *testing.T) {
	session := &Session{}
	for i := 0; i < maxLogLines+50; i++ {
		session.Log(LogInfo, "line %d", i)
	}
	logs := session.Snapshot()
	if len(logs) != maxLogLines {
		t.Fatalf("logs = %d, want capped at %d", len(logs), maxLogLines)
	}
	// The newest lines survive; the oldest are dropped.
	if !strings.Contains(logs[len(logs)-1].Message, "line 249") {
		t.Fatalf("newest line missing: %q", logs[len(logs)-1].Message)
	}
	if !strings.Contains(logs[0].Message, "line 50") {
		t.Fatalf("expected oldest kept to be line 50, got %q", logs[0].Message)
	}
}

func TestFinishIsOnceOnly(t *testing.T) {
	session := &Session{Status: StatusRunning}
	session.Finish(StatusFailed, "boom")
	session.Finish(StatusSucceeded, "")
	if session.Status != StatusFailed || session.Error != "boom" {
		t.Fatalf("finish overwrote terminal state: %+v", session)
	}
}

// TestManagerDedupsConcurrentTargets locks the one-install-per-target rule
// (spec decision 8).
func TestManagerDedupsConcurrentTargets(t *testing.T) {
	manager := NewManager(nil)
	manager.LookupRunner = func(string) bool { return false }
	target := SSHTarget{Host: "h", Username: "u", Password: "p"}

	var first, second *Session
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(2)
	// Both goroutines race Start for the same target.
	for i := 0; i < 2; i++ {
		go func(index int) {
			defer wg.Done()
			session, _ := manager.Start(target)
			mu.Lock()
			defer mu.Unlock()
			if index == 0 {
				first = session
			} else {
				second = session
			}
		}(i)
	}
	wg.Wait()
	if first == nil || second == nil {
		t.Fatalf("sessions not returned")
	}
	if first.ID != second.ID {
		t.Fatalf("concurrent installs for one target should join one session: %s vs %s", first.ID, second.ID)
	}
	// The joined session eventually fails (lookup always false) and reports.
	first.Finish(StatusFailed, "test")
}

func TestWriteRemoteFileCommandPOSIX(t *testing.T) {
	command := writeRemoteFileCommand("/home/u/.nex-next-runner/env", "A=1\nB=2\n", targetPlatform{GOOS: "linux"})
	if !strings.HasPrefix(command, "printf ") || !strings.Contains(command, "A=1\\nB=2\\n") {
		t.Fatalf("unexpected posix write command: %s", command)
	}
}

func TestStartCommandIncludesLingerFallback(t *testing.T) {
	command := buildStartCommand(targetPlatform{GOOS: "linux", GOArch: "x64", NodeCmd: "node"}, installRequest{Target: SSHTarget{Username: "u", Host: "h", Password: "p"}}, "/home/u")
	for _, fragment := range []string{
		"systemctl --user restart nex-runner.service",
		"enable-linger 'u'",
		// Container fallback: nohup (not setsid) with a runner.log tail.
		"nohup", "setsid",
		"/home/u/.nex-next-runner/runner.log",
		// Absolute paths only (SFTP/units cannot use ~).
		"cd /home/u/.nex-next-runner",
	} {
		if !strings.Contains(command, fragment) {
			t.Fatalf("linux start command missing %q: %s", fragment, command)
		}
	}
}

func TestStartCommandDarwinUsesLaunchAgent(t *testing.T) {
	command := buildStartCommand(targetPlatform{GOOS: "darwin", GOArch: "arm64"}, installRequest{Target: SSHTarget{Username: "u", Host: "h", Password: "p"}}, "/Users/u")
	if !strings.Contains(command, "/Users/u/Library/LaunchAgents/cn.nex.runner.plist") || !strings.Contains(command, "launchctl load") {
		t.Fatalf("darwin start command should write + load the LaunchAgent: %s", command)
	}
}

func TestStartCommandWindowsUsesScheduledTask(t *testing.T) {
	command := buildStartCommand(targetPlatform{GOOS: "win32", GOArch: "x64"}, installRequest{Target: SSHTarget{Username: "u", Host: "h", Password: "p"}}, "C:/Users/u")
	if !strings.Contains(command, "schtasks") || !strings.Contains(command, "onlogon") || !strings.Contains(command, "C:/Users/u/.nex-next-runner") {
		t.Fatalf("windows start command should use schtasks onlogon: %s", command)
	}
}

func TestGenerateRunnerIDShape(t *testing.T) {
	id := generateRunnerID()
	if !strings.HasPrefix(id, "runner_") || len(id) != len("runner_")+16 {
		t.Fatalf("runner id shape unexpected: %q", id)
	}
	if generateRunnerID() == id {
		t.Fatalf("runner ids should be unique")
	}
}
