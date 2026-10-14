package file

// search_test.go: the fuzzy-search scoring and top-K behaviors from
// shared workspaceFileSearch.ts + file/workspaceFileSearch.ts.

import (
	"context"
	"testing"
)

func TestScoreNormalizedFuzzyTiers(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		query string
		want  int
		ok    bool
	}{
		{"empty text never matches", "", "a", 0, false},
		{"empty query scores 0 (TS short-circuit)", "abc", "", 0, true},
		{"prefix score is length difference", "readme", "read", 2, true},
		{"substring scores 100+index", "aareadme", "read", 102, true},
		{"subsequence adds 200 plus gaps", "readme", "rm", 207, true},
		{"missing char fails", "readme", "z", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := scoreNormalized(tc.text, tc.query)
			if ok != tc.ok || (ok && got != tc.want) {
				t.Fatalf("got (%d,%v) want (%d,%v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestCandidateScoreTiers(t *testing.T) {
	c := searchCandidate{
		name: "readme", path: "/w/readme", relativePath: "readme",
		entryType:     "file",
		lowercaseName: "readme", lowercaseRelative: "readme", lowercasePath: "/w/readme",
	}
	// name tier beats relativePath (+25) beats keyword (+300)
	score, ok := candidateScore(c, "read")
	if !ok || score != 2 {
		t.Fatalf("name tier: %d %v", score, ok)
	}
	// a query matching only the path hits the keyword tier
	c2 := searchCandidate{
		name: "x", path: "/w/deep/readme", relativePath: "deep/readme",
		entryType:         "file",
		lowercaseName:     "x",
		lowercaseRelative: "deep/readme",
		lowercasePath:     "/w/deep/readme",
	}
	score, ok = candidateScore(c2, "read")
	if !ok {
		t.Fatalf("keyword tier must match")
	}
	// relativePath tier (+25) beats the keyword tier (+300): 105+25 = 130
	if score != 130 {
		t.Fatalf("relativePath tier score: %d", score)
	}
}

func TestFilterCandidatesTopKOrderAndLimit(t *testing.T) {
	var candidates []searchCandidate
	for i := 0; i < 100; i++ {
		name := "file" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		candidates = append(candidates, searchCandidate{
			name: name, relativePath: name, path: "/w/" + name, entryType: "file",
			lowercaseName: name, lowercaseRelative: name, lowercasePath: "/w/" + name,
		})
	}
	got := filterCandidates(candidates, "file", 5)
	if len(got) != 5 {
		t.Fatalf("limit: %d", len(got))
	}
	// best first (prefix scores ascending)
	for i := 1; i < len(got); i++ {
		prev, _ := scoreNormalized(got[i-1].lowercaseName, "file")
		cur, _ := scoreNormalized(got[i].lowercaseName, "file")
		if prev > cur {
			t.Fatalf("order: %d before %d", prev, cur)
		}
	}
}

func TestFilterCandidatesEmptyQuerySortsDirectoriesLast(t *testing.T) {
	// TS 默认排序：directory priority 1 排后，稳定按原序
	candidates := []searchCandidate{
		{name: "zdir", entryType: "directory"},
		{name: "afile", entryType: "file"},
		{name: "adir", entryType: "directory"},
		{name: "bfile", entryType: "file"},
	}
	got := filterCandidates(candidates, "", 10)
	if len(got) != 4 {
		t.Fatalf("len: %d", len(got))
	}
	want := []string{"afile", "bfile", "zdir", "adir"}
	for i, w := range want {
		if got[i].name != w {
			t.Fatalf("order[%d]: got %s want %s", i, got[i].name, w)
		}
	}
}

func TestSearchWorkspaceFilesEndToEnd(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir+"/README.md", "x")
	mustWrite(t, dir+"/src/util/helper.ts", "x")
	mustWrite(t, dir+"/src/main.ts", "x")
	s := newTestService(t)
	ctx := context.Background()

	// exact-ish name match ranks first
	res, err := s.SearchWorkspaceFiles(ctx, dir, "", "readme", ptrInt64(10), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) == 0 || res[0].Name != "README.md" {
		t.Fatalf("readme first: %+v", res)
	}
	// case-insensitive (the TS matcher lowercases)
	res, err = s.SearchWorkspaceFiles(ctx, dir, "", "HELPER", ptrInt64(10), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Name != "helper.ts" {
		t.Fatalf("HELPER: %+v", res)
	}
	// limit 0 returns empty
	res, err = s.SearchWorkspaceFiles(ctx, dir, "", "a", ptrInt64(0), false)
	if err != nil || len(res) != 0 {
		t.Fatalf("limit 0: %+v %v", res, err)
	}
	// no match returns empty
	res, err = s.SearchWorkspaceFiles(ctx, dir, "", "zzznope", ptrInt64(10), false)
	if err != nil || len(res) != 0 {
		t.Fatalf("no match: %+v %v", res, err)
	}
}

func TestBuildHostCandidatesBatchedDecodeMatchesWholeDecode(t *testing.T) {
	// 分批解码与整表解码必须一致（batch 边界在行中间时按行尾推进）
	entries := make([]workspaceEntry, 500)
	for i := range entries {
		entries[i] = workspaceEntry{RelativePath: "dir/file" + string(rune('a'+i%26)) + string(rune('0'+i%10)), Type: "file"}
	}
	packed := packWorkspaceFileEntries(entries)
	batched, err := buildHostCandidates(context.Background(), packed, "/w")
	if err != nil {
		t.Fatal(err)
	}
	if len(batched) != len(entries) {
		t.Fatalf("batched decode lost entries: %d != %d", len(batched), len(entries))
	}
	for i := range entries {
		if batched[i].relativePath != entries[i].RelativePath {
			t.Fatalf("entry %d: %s != %s", i, batched[i].relativePath, entries[i].RelativePath)
		}
	}
}

func TestListWorkspaceFilesRangeSlicing(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir+"/a.txt", "x")
	mustWrite(t, dir+"/b.txt", "x")
	s := newTestService(t)
	ctx := context.Background()

	length, err := s.ListWorkspaceFilesLength(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	// window inside the packed string
	mid, err := s.ListWorkspaceFilesRange(ctx, dir, int64(length/2), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(mid) > 5 {
		t.Fatalf("window length: %d", len(mid))
	}
	// offset beyond the end → empty string
	end, err := s.ListWorkspaceFilesRange(ctx, dir, int64(length+100), 5)
	if err != nil || end != "" {
		t.Fatalf("past-end: %q %v", end, err)
	}
	// negative length clamps to 0
	none, err := s.ListWorkspaceFilesRange(ctx, dir, 0, -3)
	if err != nil || none != "" {
		t.Fatalf("negative length: %q %v", none, err)
	}
}
