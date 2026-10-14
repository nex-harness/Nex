package file

// codec_test.go: byte-exact vectors for the workspaceFileEntriesCodec port,
// including the escapes, the Windows separator handling, and the
// round-trip through the captured golden files.
//
// Vectors here are cross-checked against the TS implementation via the
// captured golden files (go/testdata/services/file/*listWorkspaceFilesRange*)
// in the golden replay test in service_test.go.

import (
	"strings"
	"testing"
)

func TestPackWorkspaceFileEntriesVectors(t *testing.T) {
	cases := []struct {
		name    string
		entries []workspaceEntry
		want    string
	}{
		{"empty", nil, ""},
		{
			"single file",
			[]workspaceEntry{{Name: "a.ts", Path: "/w/a.ts", RelativePath: "a.ts", Type: "file"}},
			"file\ta.ts",
		},
		{
			"dirs first then files",
			[]workspaceEntry{
				{RelativePath: "src", Type: "directory"},
				{RelativePath: "src/a.ts", Type: "file"},
			},
			"directory\tsrc\nfile\tsrc/a.ts",
		},
		{
			// Unix 文件名可含 \t、\n、\\：转义必须先转反斜杠
			"escapes: tab newline backslash",
			[]workspaceEntry{
				{RelativePath: "a\tb", Type: "file"},
				{RelativePath: "c\nd", Type: "file"},
				{RelativePath: "e\\f", Type: "file"},
				{RelativePath: "g\\th", Type: "file"}, // literal backslash-t
			},
			"file\ta\\tb\nfile\tc\\nd\nfile\te\\\\f\nfile\tg\\\\th",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := packWorkspaceFileEntries(tc.entries)
			if got != tc.want {
				t.Fatalf("pack mismatch:\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}

func TestUnpackWorkspaceFileEntriesVectors(t *testing.T) {
	t.Run("posix root", func(t *testing.T) {
		got := unpackWorkspaceFileEntries("file\ta/b/c.ts\ndirectory\td", "/w")
		if len(got) != 2 {
			t.Fatalf("want 2 entries, got %d", len(got))
		}
		if got[0].Path != "/w/a/b/c.ts" || got[0].Name != "c.ts" || got[0].Type != "file" {
			t.Errorf("entry0 = %+v", got[0])
		}
		if got[1].Path != "/w/d" || got[1].Type != "directory" {
			t.Errorf("entry1 = %+v", got[1])
		}
	})
	t.Run("windows root uses backslash separators", func(t *testing.T) {
		got := unpackWorkspaceFileEntries("file\ta/b/c.ts", `C:\w`)
		if len(got) != 1 || got[0].Path != `C:\w\a\b\c.ts` {
			t.Fatalf("got %+v", got)
		}
		if got[0].RelativePath != "a/b/c.ts" {
			t.Errorf("relativePath must stay posix: %+v", got[0])
		}
	})
	t.Run("root already ends with separator", func(t *testing.T) {
		got := unpackWorkspaceFileEntries("file\ta.ts", "/w/")
		if len(got) != 1 || got[0].Path != "/w/a.ts" {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("escapes round-trip", func(t *testing.T) {
		entries := []workspaceEntry{
			{RelativePath: "a\tb", Type: "file"},
			{RelativePath: "c\nd", Type: "file"},
			{RelativePath: "e\\f", Type: "file"},
			{RelativePath: "g\\th", Type: "file"},
			{RelativePath: "h\\ni\tj", Type: "directory"},
		}
		packed := packWorkspaceFileEntries(entries)
		got := unpackWorkspaceFileEntries(packed, "/w")
		if len(got) != len(entries) {
			t.Fatalf("round-trip lost entries: %d -> %d", len(entries), len(got))
		}
		for i := range entries {
			if got[i].RelativePath != entries[i].RelativePath || got[i].Type != entries[i].Type {
				t.Errorf("entry %d: got %+v want %+v", i, got[i], entries[i])
			}
		}
	})
	t.Run("malformed lines are skipped like the TS", func(t *testing.T) {
		got := unpackWorkspaceFileEntries("notab\n\nfile\ta\n", "/w")
		if len(got) != 1 || got[0].RelativePath != "a" {
			t.Fatalf("got %+v", got)
		}
	})
}

func TestChunkSizeConstantParity(t *testing.T) {
	// WORKSPACE_FILE_ENTRIES_CHUNK_SIZE = 4_000_000
	if WorkspaceFileEntriesChunkSize != 4_000_000 {
		t.Fatalf("chunk size drifted: %d", WorkspaceFileEntriesChunkSize)
	}
}

// TestCodecAgainstCapturedGolden replays the listWorkspaceFilesRange capture
// (Node output) through unpack and verifies the entries are exactly the
// workspace fixture tree — the byte-exactness proof across engines.
func TestCodecAgainstCapturedGolden(t *testing.T) {
	if testing.Short() {
		t.Skip("golden file test")
	}
	golden := readGolden(t, "25-listWorkspaceFilesRange")
	if !golden.OK {
		t.Fatalf("golden says the call failed: %+v", golden.Error)
	}
	packed, ok := golden.Result.(string)
	if !ok {
		t.Fatalf("golden result is not a string: %T", golden.Result)
	}
	wantPacked := "directory\tdocs\ndirectory\tmedia\ndirectory\tsrc\ndirectory\tsrc/binary\ndirectory\tsrc/text\ndirectory\tsrc/util\nfile\t.gitignore\nfile\t.hidden/nested/file.txt\nfile\t.hidden/root-hidden.txt\nfile\tdocs/note.md\nfile\tmedia/sample.txt\nfile\tREADME.md\nfile\tsrc/binary/blob.bin\nfile\tsrc/main.ts\nfile\tsrc/text/big.txt\nfile\tsrc/util/helper.ts"
	if packed != wantPacked {
		t.Fatalf("golden packed mismatch:\n got: %q\nwant: %q", packed, wantPacked)
	}
	entries := unpackWorkspaceFileEntries(packed, "/w")
	if len(entries) != 16 {
		t.Fatalf("want 16 entries, got %d", len(entries))
	}
	if !strings.HasPrefix(entries[0].Path, "/w/") {
		t.Errorf("path prefix missing: %+v", entries[0])
	}
}
