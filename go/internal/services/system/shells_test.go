package system

// shells_test.go: integratedTerminalShells.ts — pure functions on
// (env, isExecutable, platform) so every branch runs on Linux.

import (
	"strings"
	"testing"
)

func TestShellsNonWindowsReturnsEmpty(t *testing.T) {
	got := listIntegratedTerminalShellOptions(map[string]string{}, func(string) bool { return true }, "linux")
	if len(got) != 0 {
		t.Fatalf("non-win32: %+v", got)
	}
}

func TestShellsWindowsComSpecDefault(t *testing.T) {
	got := listIntegratedTerminalShellOptions(map[string]string{"ComSpec": `C:\Windows\system32\cmd.exe`}, func(string) bool { return false }, "win32")
	if len(got) != 1 {
		t.Fatalf("cmd only: %+v", got)
	}
	if got[0].Dialect != "cmd" || got[0].ID != `cmd:C:\Windows\system32\cmd.exe` || got[0].Source != "system" {
		t.Fatalf("cmd option: %+v", got[0])
	}
	// ComSpec 缺省兜底 cmd.exe
	got = listIntegratedTerminalShellOptions(map[string]string{}, func(string) bool { return false }, "win32")
	if got[0].Path != "cmd.exe" {
		t.Fatalf("fallback: %+v", got[0])
	}
}

func TestShellsWindowsGitBashFromSystemPath(t *testing.T) {
	// well-known install path exists
	exe := map[string]bool{`C:\Program Files\Git\bin\bash.exe`: true}
	got := listIntegratedTerminalShellOptions(map[string]string{}, func(p string) bool { return exe[p] }, "win32")
	if len(got) != 2 || got[1].Dialect != "git-bash" || got[1].Source != "system" {
		t.Fatalf("git bash: %+v", got)
	}
	if got[1].ID != `git-bash:C:\Program Files\Git\bin\bash.exe` {
		t.Fatalf("id: %+v", got[1])
	}
}

func TestShellsWindowsGitBashFromPATHInference(t *testing.T) {
	// git on PATH -> infer ..\bin\bash.exe
	exe := map[string]bool{`C:\tools\git\cmd\git.exe`: true, `C:\tools\git\bin\bash.exe`: true}
	env := map[string]string{
		"PATH":    `C:\tools\git\cmd;C:\Windows`,
		"PATHEXT": ".COM;.EXE;.BAT;.CMD",
	}
	got := listIntegratedTerminalShellOptions(env, func(p string) bool { return exe[p] }, "win32")
	if len(got) != 2 {
		t.Fatalf("inferred: %+v", got)
	}
	if got[1].Source != "path" || !strings.HasSuffix(got[1].Path, "bash.exe") {
		t.Fatalf("inferred option: %+v", got[1])
	}
}

func TestShellsWindowsExecutableExtensions(t *testing.T) {
	// PATHEXT without .exe gets it prepended; case-insensitive env lookup
	env := map[string]string{"path": `C:\x`, "pathext": ".COM;.BAT"}
	exts := windowsExecutableExtensions(env)
	if exts[0] != ".exe" {
		t.Fatalf("exe first: %v", exts)
	}
	// env lookup is case-insensitive (ComSpec/comspec)
	if v := getEnvValue(map[string]string{"comspec": "X"}, "ComSpec"); v != "X" {
		t.Fatalf("case-insensitive env: %q", v)
	}
}

func TestShellsCommandPromptIsAlwaysFirst(t *testing.T) {
	exe := map[string]bool{`C:\Program Files\Git\bin\bash.exe`: true}
	got := listIntegratedTerminalShellOptions(map[string]string{}, func(p string) bool { return exe[p] }, "win32")
	if got[0].Dialect != "cmd" {
		t.Fatalf("cmd first: %+v", got)
	}
}
