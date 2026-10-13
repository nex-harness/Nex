//go:build windows

package paths

import (
	"path/filepath"
	"testing"
)

// TestPathResolutionOnWindowsGOOS runs the same resolution table under a real
// Windows build, where filepath.Join uses `\` and the separators in the wants
// must be Windows ones. The pure variant in paths_test.go already proves the
// logic; this test proves the package also produces the right bytes when the
// platform is actually Windows (the drive/UNC strings below are what a real
// Windows process would capture as HOME / NEX_DATA_BASE_DIR).
func TestPathResolutionOnWindowsGOOS(t *testing.T) {
	cases := []struct {
		name        string
		envBaseDir  string
		homeDir     string
		setExplicit string
		wantV2      string
	}{
		{"explicit wins", `D:\env`, `C:\Users\u`, `E:\explicit`, `E:\explicit\.nex\v2`},
		{"env beats home", `D:\env`, `C:\Users\u`, "", `D:\env\.nex\v2`},
		{"home fallback", "", `C:\Users\u`, "", `C:\Users\u\.nex\v2`},
		{"unc env dir", `\\server\share\nex`, `C:\Users\u`, "", `\\server\share\nex\.nex\v2`},
		{"drive-relative home", "", `C:`, "", filepath.Join(`C:`, ".nex", "v2")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := New(tc.envBaseDir, tc.homeDir)
			if tc.setExplicit != "" {
				p.SetDataBaseDir(tc.setExplicit)
			}
			if got := p.AppConfigDir(); got != tc.wantV2 {
				t.Errorf("AppConfigDir() = %q, want %q", got, tc.wantV2)
			}
			if got := p.TasksIndexDatabasePath(); got != tc.wantV2+`\tasks-index.sqlite` && got != filepath.Join(tc.wantV2, "tasks-index.sqlite") {
				t.Errorf("TasksIndexDatabasePath() = %q, want %q", got, tc.wantV2+`\tasks-index.sqlite`)
			}
		})
	}
}

// TestWindowsComparablePathOnWindowsGOOS pins the comparison helpers on a real
// Windows filesystem layout: same expectations as the pure table, proving the
// port does not accidentally depend on POSIX separators at runtime.
func TestWindowsComparablePathOnWindowsGOOS(t *testing.T) {
	if got := NormalizeWindowsComparablePath(`C:\Program Files\Nex`); got != `c:\program files\nex` {
		t.Errorf("comparable = %q, want c:\\program files\\nex", got)
	}
	res := ValidateDataBaseDirTarget(PlatformWin32, `C:\Program Files\Nex\data`, map[string]string{
		"ProgramFiles": `C:\Program Files`,
	}, "")
	if res.OK {
		t.Errorf("data dir inside Program Files\\Nex must be rejected: %+v", res)
	}
}
