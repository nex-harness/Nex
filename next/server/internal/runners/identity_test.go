package runners_test

import (
	"testing"

	"github.com/nex-harness/nex/server/internal/runners"
)

func TestBuildIdentity(t *testing.T) {
	cases := []struct {
		runnerID string
		path     string
		want     string
	}{
		{"runner_0123456789abcdef", "/home/me/app", "runner:runner_0123456789abcdef:/home/me/app"},
		{"alpha", `C:\Users\me\app`, `runner:alpha:C:\Users\me\app`},
		{"beta", `/home/me/a:b`, `runner:beta:/home/me/a:b`},
	}
	for _, testCase := range cases {
		if got := runners.BuildIdentity(testCase.runnerID, testCase.path); got != testCase.want {
			t.Fatalf("BuildIdentity(%q, %q) = %q, want %q", testCase.runnerID, testCase.path, got, testCase.want)
		}
	}
}

func TestParseIdentity(t *testing.T) {
	cases := []struct {
		name     string
		identity string
		runnerID string
		path     string
		ok       bool
	}{
		{
			name:     "posix",
			identity: "runner:runner_0123456789abcdef:/home/me/app",
			runnerID: "runner_0123456789abcdef",
			path:     "/home/me/app",
			ok:       true,
		},
		{
			name:     "windows drive letter",
			identity: `runner:alpha:C:\Users\me\app`,
			runnerID: "alpha",
			path:     `C:\Users\me\app`,
			ok:       true,
		},
		{
			name:     "unc path",
			identity: `runner:alpha:\\server\share\app`,
			runnerID: "alpha",
			path:     `\\server\share\app`,
			ok:       true,
		},
		{
			name:     "posix path with colon",
			identity: "runner:alpha:/home/me/a:b:c",
			runnerID: "alpha",
			path:     "/home/me/a:b:c",
			ok:       true,
		},
		{
			name:     "surrounding whitespace",
			identity: "  runner:alpha:/w  ",
			runnerID: "alpha",
			path:     "/w",
			ok:       true,
		},
		{
			// 机器浏览 scope（docs/next/runner-folders.md 决策 2）：`~` 不是绝对
			// 路径，但 server 必须能把它解析成 runner 身份才能路由。
			name:     "machine scope",
			identity: "runner:alpha:~",
			runnerID: "alpha",
			path:     runners.MachineScopePath,
			ok:       true,
		},
		{name: "empty", identity: "", ok: false},
		{name: "wrong prefix", identity: "remote:ssh:host:22:user:/w", ok: false},
		{name: "missing path", identity: "runner:alpha", ok: false},
		{name: "empty path", identity: "runner:alpha:", ok: false},
		{name: "empty runner id", identity: "runner::/w", ok: false},
		{name: "whitespace runner id", identity: "runner:a b:/w", ok: false},
		{name: "tab in runner id", identity: "runner:a\tb:/w", ok: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			runnerID, path, ok := runners.ParseIdentity(testCase.identity)
			if ok != testCase.ok {
				t.Fatalf("ParseIdentity(%q) ok = %v, want %v", testCase.identity, ok, testCase.ok)
			}
			if !testCase.ok {
				return
			}
			if runnerID != testCase.runnerID || path != testCase.path {
				t.Fatalf("ParseIdentity(%q) = (%q, %q), want (%q, %q)",
					testCase.identity, runnerID, path, testCase.runnerID, testCase.path)
			}
		})
	}
}

func TestValidRunnerID(t *testing.T) {
	for _, id := range []string{"runner_0123456789abcdef", "alpha", "a.b-c_d"} {
		if !runners.ValidRunnerID(id) {
			t.Fatalf("ValidRunnerID(%q) = false, want true", id)
		}
	}
	for _, id := range []string{"", "a:b", "a b", "a\nb", "a\tb"} {
		if runners.ValidRunnerID(id) {
			t.Fatalf("ValidRunnerID(%q) = true, want false", id)
		}
	}
}
