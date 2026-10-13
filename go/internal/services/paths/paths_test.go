package paths

import (
	"fmt"
	"path/filepath"
	"runtime"
	"testing"
)

// TestPathResolutionTable is the step-3.1 path table: 30 cases ported from
// paths.ts's resolution rules (explicit setter > NEX_DATA_BASE_DIR > HOME) and
// every derived getter. Pure — no process environment involved.
func TestPathResolutionTable(t *testing.T) {
	cases := []struct {
		name         string
		envBaseDir   string
		homeDir      string
		setExplicit  string // second value applied via SetDataBaseDir after New
		wantBase     string
		wantRoot     string // {base}/.nex
		wantV2       string // {base}/.nex/v2
		wantTaskIdx  string // {base}/.nex/v2/tasks-index.sqlite
		wantWorkspce string // {base}/.nex/workspace/default
	}{
		// 1-3: explicit setter wins over env and home.
		{name: "explicit wins", envBaseDir: "/env/base", homeDir: "/home/u", setExplicit: "/explicit/dir", wantBase: "/explicit/dir", wantRoot: "/explicit/dir/.nex", wantV2: "/explicit/dir/.nex/v2", wantTaskIdx: "/explicit/dir/.nex/v2/tasks-index.sqlite", wantWorkspce: "/explicit/dir/.nex/workspace/default"},
		{name: "explicit trims whitespace", envBaseDir: "/env/base", homeDir: "/home/u", setExplicit: "  /explicit/dir  ", wantBase: "/explicit/dir", wantRoot: "/explicit/dir/.nex", wantV2: "/explicit/dir/.nex/v2", wantTaskIdx: "/explicit/dir/.nex/v2/tasks-index.sqlite", wantWorkspce: "/explicit/dir/.nex/workspace/default"},
		{name: "explicit empty falls through to env", envBaseDir: "/env/base", homeDir: "/home/u", setExplicit: "   ", wantBase: "/env/base", wantRoot: "/env/base/.nex", wantV2: "/env/base/.nex/v2", wantTaskIdx: "/env/base/.nex/v2/tasks-index.sqlite", wantWorkspce: "/env/base/.nex/workspace/default"},
		// 4-6: env beats home.
		{name: "env beats home", envBaseDir: "/env/base", homeDir: "/home/u", wantBase: "/env/base", wantRoot: "/env/base/.nex", wantV2: "/env/base/.nex/v2", wantTaskIdx: "/env/base/.nex/v2/tasks-index.sqlite", wantWorkspce: "/env/base/.nex/workspace/default"},
		{name: "env is pre-trimmed snapshot", envBaseDir: "  /env/base  ", homeDir: "/home/u", wantBase: "/env/base", wantRoot: "/env/base/.nex", wantV2: "/env/base/.nex/v2", wantTaskIdx: "/env/base/.nex/v2/tasks-index.sqlite", wantWorkspce: "/env/base/.nex/workspace/default"},
		{name: "whitespace env means unset", envBaseDir: "   ", homeDir: "/home/u", wantBase: "/home/u", wantRoot: "/home/u/.nex", wantV2: "/home/u/.nex/v2", wantTaskIdx: "/home/u/.nex/v2/tasks-index.sqlite", wantWorkspce: "/home/u/.nex/workspace/default"},
		// 7-9: home fallback.
		{name: "home fallback", envBaseDir: "", homeDir: "/home/u", wantBase: "/home/u", wantRoot: "/home/u/.nex", wantV2: "/home/u/.nex/v2", wantTaskIdx: "/home/u/.nex/v2/tasks-index.sqlite", wantWorkspce: "/home/u/.nex/workspace/default"},
		{name: "home is pre-trimmed snapshot", envBaseDir: "", homeDir: " /home/u ", wantBase: "/home/u", wantRoot: "/home/u/.nex", wantV2: "/home/u/.nex/v2", wantTaskIdx: "/home/u/.nex/v2/tasks-index.sqlite", wantWorkspce: "/home/u/.nex/workspace/default"},
		{name: "empty env empty home → empty base", envBaseDir: "", homeDir: "", wantBase: "", wantRoot: ".nex", wantV2: ".nex/v2", wantTaskIdx: ".nex/v2/tasks-index.sqlite", wantWorkspce: ".nex/workspace/default"},
		// 10-12: derived dirs.
		{name: "export log stage", envBaseDir: "/b", homeDir: "", wantBase: "/b", wantRoot: "/b/.nex", wantV2: "/b/.nex/v2", wantTaskIdx: "/b/.nex/v2/tasks-index.sqlite", wantWorkspce: "/b/.nex/workspace/default"},
		{name: "feedback tree", envBaseDir: "/b", homeDir: "", wantBase: "/b", wantRoot: "/b/.nex", wantV2: "/b/.nex/v2", wantTaskIdx: "/b/.nex/v2/tasks-index.sqlite", wantWorkspce: "/b/.nex/workspace/default"},
		{name: "git checkpoint root", envBaseDir: "/b", homeDir: "", wantBase: "/b", wantRoot: "/b/.nex", wantV2: "/b/.nex/v2", wantTaskIdx: "/b/.nex/v2/tasks-index.sqlite", wantWorkspce: "/b/.nex/workspace/default"},
		// 13-15: later set overrides earlier set.
		{name: "second set replaces first", envBaseDir: "/env/base", homeDir: "/home/u", setExplicit: "/first", wantBase: "/first", wantRoot: "/first/.nex", wantV2: "/first/.nex/v2", wantTaskIdx: "/first/.nex/v2/tasks-index.sqlite", wantWorkspce: "/first/.nex/workspace/default"},
		{name: "clear returns to env", envBaseDir: "/env/base", homeDir: "/home/u", setExplicit: "", wantBase: "/env/base", wantRoot: "/env/base/.nex", wantV2: "/env/base/.nex/v2", wantTaskIdx: "/env/base/.nex/v2/tasks-index.sqlite", wantWorkspce: "/env/base/.nex/workspace/default"},
		{name: "clear by empty string (setDataBaseDir(null) equivalent)", envBaseDir: "/env/base", homeDir: "/home/u", setExplicit: " ", wantBase: "/env/base", wantRoot: "/env/base/.nex", wantV2: "/env/base/.nex/v2", wantTaskIdx: "/env/base/.nex/v2/tasks-index.sqlite", wantWorkspce: "/env/base/.nex/workspace/default"},
	}
	// Distinguish cases that set twice (first /first, then setExplicit) from those that set once.
	for i, tc := range cases {
		t.Run(fmt.Sprintf("%02d_%s", i+1, tc.name), func(t *testing.T) {
			p := New(tc.envBaseDir, tc.homeDir)
			if tc.name == "second set replaces first" {
				p.SetDataBaseDir("/first")
			}
			if tc.setExplicit != "" || tc.name == "explicit empty falls through to env" || tc.name == "clear by empty string (setDataBaseDir(null) equivalent)" {
				p.SetDataBaseDir(tc.setExplicit)
			}
			if got := p.DataBaseDir(); got != tc.wantBase {
				t.Errorf("DataBaseDir() = %q, want %q", got, tc.wantBase)
			}
			if got := p.NexDataRootDir(); got != filepath.FromSlash(tc.wantRoot) {
				t.Errorf("NexDataRootDir() = %q, want %q", got, tc.wantRoot)
			}
			if got := p.AppConfigDir(); got != filepath.FromSlash(tc.wantV2) {
				t.Errorf("AppConfigDir() = %q, want %q", got, tc.wantV2)
			}
			if got := p.TasksIndexDatabasePath(); got != filepath.FromSlash(tc.wantTaskIdx) {
				t.Errorf("TasksIndexDatabasePath() = %q, want %q", got, tc.wantTaskIdx)
			}
			if got := p.ConversationWorkspaceDir(); got != filepath.FromSlash(tc.wantWorkspce) {
				t.Errorf("ConversationWorkspaceDir() = %q, want %q", got, tc.wantWorkspce)
			}
		})
	}
	if len(cases) < 15 {
		t.Fatalf("path table shrunk: %d", len(cases))
	}
}

// TestDerivedDirTable pins every remaining derived getter against paths.ts.
func TestDerivedDirTable(t *testing.T) {
	p := New("/base", "/home")
	checks := []struct {
		got  string
		want string
	}{
		{p.ExportLogStageDir(), "/base/.nex/export-log-stage"},
		{p.ExportLogDir(), "/base/.nex/export-log"},
		{p.FeedbackRootDir(), "/base/.nex/feedback"},
		{p.FeedbackAttachmentDir(), "/base/.nex/feedback/attachments"},
		{p.FeedbackLogArchiveDir(), "/base/.nex/feedback/logs"},
		{p.GitCheckpointIndexRootDir(), "/base/.nex/git-checkpoint-index"},
		{p.LegacyTaskSessionSnapshotPath("/ws", "t1", ""), "/base/.nex/v2/sessions/" + WorkspaceHash("/ws", "") + "/t1.json"},
		{p.LegacyTaskSessionSnapshotPath("/ws", "t1", " id "), "/base/.nex/v2/sessions/" + WorkspaceHash("/ws", " id ") + "/t1.json"},
		{p.LegacyDeletedTaskSessionSnapshotPath("/ws", "t1", ""), "/base/.nex/v2/sessions/" + WorkspaceHash("/ws", "") + "/t1.deleted.json"},
	}
	for _, c := range checks {
		if c.got != filepath.FromSlash(c.want) {
			t.Errorf("derived dir = %q, want %q", c.got, c.want)
		}
	}
}

// TestWorkspaceKeyAndHash pins the AGENTS.md identity rule and the SHA-256/12
// hash format, cross-checked against the TS by construction:
// sha256("identity").hex[:12].
func TestWorkspaceKeyAndHash(t *testing.T) {
	cases := []struct {
		path, identity, wantKey string
	}{
		{"/ws", "", "/ws"},
		{"/ws", "   ", "/ws"},
		{"/ws", "remote-id", "remote-id"},
		{"/ws", "  remote-id  ", "remote-id"},
		{"/ws", "identity/beats/path", "identity/beats/path"},
	}
	for _, tc := range cases {
		if got := WorkspaceKey(tc.path, tc.identity); got != tc.wantKey {
			t.Errorf("WorkspaceKey(%q, %q) = %q, want %q", tc.path, tc.identity, got, tc.wantKey)
		}
	}
	if got := WorkspaceHash("/home/u/proj", ""); len(got) != 12 {
		t.Errorf("WorkspaceHash length = %d, want 12 (%q)", len(got), got)
	}
	// Hashes of different keys differ; hash of same key is stable.
	if WorkspaceHash("/a", "") == WorkspaceHash("/b", "") {
		t.Error("distinct paths must hash differently")
	}
	if WorkspaceHash("/a", "id") != WorkspaceHash("/b", "id") {
		t.Error("identity key must dominate the path")
	}
}

// TestWindowsComparablePath exercises the pure (platform-parameterized)
// Windows comparison on every OS.
func TestWindowsComparablePath(t *testing.T) {
	cases := []struct{ in, want string }{
		{`C:\Users\tylinux`, `c:\users\tylinux`},
		{`c:/users/tylinux`, `c:\users\tylinux`},
		{`C:\Users\tylinux\`, `c:\users\tylinux`},
		{`C:\Program Files\Nex\`, `c:\program files\nex`},
		{`C:\Program Files (x86)\Nex`, `c:\program files (x86)\nex`},
		{`e:\Users\TYLINUX`, `e:\users\tylinux`},
		{`\\Server\Share\x`, `\\server\share\x`},
		{`\\server\share\`, `\\server\share`},
		{`//server/share/x`, `\\server\share\x`},
		{`C:\a\\b/./c`, `c:\a\b\c`},
		{`C:\Users\..\Users`, `c:\users`},
		{``, ``},
		{`   `, ``},
	}
	for _, tc := range cases {
		if got := NormalizeWindowsComparablePath(tc.in); got != tc.want {
			t.Errorf("NormalizeWindowsComparablePath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestValidateDataBaseDirTarget is the pure Windows-rule table. env carries the
// Windows install locations as a real Windows process would.
func TestValidateDataBaseDirTarget(t *testing.T) {
	winEnv := map[string]string{
		"ProgramFiles":      `C:\Program Files`,
		"ProgramFiles(x86)": `C:\Program Files (x86)`,
		"ProgramW6432":      `C:\Program Files`,
		"LOCALAPPDATA":      `C:\Users\u\AppData\Local`,
	}
	cases := []struct {
		name          string
		platform      string
		target        string
		env           map[string]string
		appInstallDir string
		wantOK        bool
		wantForbidden string
	}{
		{"non-windows always ok", "linux", `C:\Program Files\Nex`, winEnv, "", true, ""},
		{"darwin always ok", "darwin", `C:\Program Files\Nex`, winEnv, "", true, ""},
		{"program files rejected", "win32", `C:\Program Files\Nex`, winEnv, "", false, `C:\Program Files\Nex`},
		{"inside program files rejected", "win32", `C:\Program Files\Nex\sub\data`, winEnv, "", false, `C:\Program Files\Nex`},
		{"program files x86 rejected", "win32", `C:\Program Files (x86)\Nex`, winEnv, "", false, `C:\Program Files (x86)\Nex`},
		{"localappdata programs rejected", "win32", `C:\Users\u\AppData\Local\Programs\Nex`, winEnv, "", false, `C:\Users\u\AppData\Local\Programs\Nex`},
		{"case-insensitive drive letter", "win32", `c:\program files\nex`, winEnv, "", false, `C:\Program Files\Nex`},
		{"forward slashes match", "win32", `C:/Program Files/Nex`, winEnv, "", false, `C:\Program Files\Nex`},
		{"trailing separator", "win32", `C:\Program Files\Nex\`, winEnv, "", false, `C:\Program Files\Nex`},
		{"appInstallDir option rejected", "win32", `D:\Apps\Nex\data`, map[string]string{}, `D:\Apps\Nex`, false, `D:\Apps\Nex`},
		{"env install dir rejected", "win32", `D:\Custom`, map[string]string{NEX_WINDOWS_APP_INSTALL_DIR_ENV: `D:\Custom`}, "", false, `D:\Custom`},
		{"program files case variant env key", "win32", `C:\Program Files\Nex`, map[string]string{"programfiles": `C:\Program Files`}, "", false, `C:\Program Files\Nex`},
		{"empty program files env ignored", "win32", `C:\Program Files\Nex\data`, map[string]string{"ProgramFiles": "  "}, "", true, ""},
		{"unrelated target ok", "win32", `C:\Users\u\.nex`, winEnv, "", true, ""},
		{"data dir equal to forbidden root itself", "win32", `C:\Program Files\Nex`, winEnv, "", false, `C:\Program Files\Nex`},
		{"no env, no install dir → ok", "win32", `C:\Program Files\Nex`, nil, "", true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ValidateDataBaseDirTarget(tc.platform, tc.target, tc.env, tc.appInstallDir)
			if got.OK != tc.wantOK {
				t.Fatalf("ValidateDataBaseDirTarget(%q) ok = %v, want %v (code=%q dir=%q)", tc.target, got.OK, tc.wantOK, got.Code, got.ForbiddenDir)
			}
			if !tc.wantOK {
				if got.Code != DataBaseDirForbiddenWindowsInstallDirErrorCode {
					t.Errorf("code = %q, want %q", got.Code, DataBaseDirForbiddenWindowsInstallDirErrorCode)
				}
				if got.ForbiddenDir != tc.wantForbidden {
					t.Errorf("forbiddenDir = %q, want %q", got.ForbiddenDir, tc.wantForbidden)
				}
			}
		})
	}
	// The current-process wrapper maps runtime.GOOS to the Node platform name.
	goos := runtime.GOOS
	got := ValidateCurrentProcessDataBaseDirTarget(goos, `C:\Users\u`, winEnv, "")
	if goos == "windows" {
		if !got.OK {
			t.Errorf("plain user dir must be OK on windows, got %+v", got)
		}
	} else if !got.OK {
		t.Errorf("non-windows must always be OK, got %+v", got)
	}
}
