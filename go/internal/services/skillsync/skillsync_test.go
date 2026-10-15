// skillsync_test.go: the ISkillSyncService behavior suite — candidates,
// remote statuses (path re-validation + name dedupe), export/import
// round-trip with skips, and the offline write-access preflight.
package skillsync

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func writeSkill(t *testing.T, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	os.MkdirAll(dir, 0o755)
	path := filepath.Join(dir, "SKILL.md")
	os.WriteFile(path, []byte("---\nname: "+name+"\ndescription: d\n---\nbody of "+name), 0o644)
	return path
}

func newService(t *testing.T) (*Service, string) {
	t.Helper()
	home := t.TempDir()
	return New(Options{HomeDir: home}), home
}

// TestListLocalUserSkillCandidates: .nex first, uncovered .agents after,
// sorted by directory name.
func TestListLocalUserSkillCandidates(t *testing.T) {
	service, home := newService(t)
	writeSkill(t, filepath.Join(home, ".nex", "skills"), "alpha")
	writeSkill(t, filepath.Join(home, ".agents", "skills"), "beta")
	writeSkill(t, filepath.Join(home, ".agents", "skills"), "alpha") // covered by name

	result, err := service.ListLocalUserSkillCandidates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 2 {
		t.Fatalf("candidates = %+v", result.Candidates)
	}
	if result.Candidates[0].Name != "alpha" || result.Candidates[1].Name != "beta" {
		t.Fatalf("order = %+v", result.Candidates)
	}
	if result.Candidates[0].SizeBytes == 0 {
		t.Fatal("size not computed")
	}
	if result.MaxArchiveBytes != DefaultMaxArchiveBytes {
		t.Fatalf("maxArchiveBytes = %d", result.MaxArchiveBytes)
	}
}

// TestListRemoteUserSkillStatuses: unsafe directory names are rejected
// server-side; existing dirs and same-name skills report exists.
func TestListRemoteUserSkillStatuses(t *testing.T) {
	service, home := newService(t)
	writeSkill(t, filepath.Join(home, ".nex", "skills"), "alpha")

	statuses, err := service.ListRemoteUserSkillStatuses(context.Background(),
		[]string{"alpha", "missing"},
		[]struct {
			DirectoryName string
			Name          string
		}{{"other-dir", "alpha"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses.Statuses) != 2 {
		t.Fatalf("statuses = %+v", statuses.Statuses)
	}
	if !statuses.Statuses[0].Exists || statuses.Statuses[0].Path == "" {
		t.Fatalf("existing dir: %+v", statuses.Statuses[0])
	}
	if statuses.Statuses[1].Exists {
		t.Fatal("missing dir must not exist")
	}

	// Path traversal in directoryName is refused (server-side re-validation).
	if _, err := service.ListRemoteUserSkillStatuses(context.Background(), []string{"../escape"}, nil); err == nil {
		t.Fatal("unsafe directoryName must be rejected")
	}
}

// TestExportImportRoundTrip: export selects by candidate id; import into a
// fresh home syncs, skips existing dirs, and skips same-name skills.
func TestExportImportRoundTrip(t *testing.T) {
	exportService, home := newService(t)
	writeSkill(t, filepath.Join(home, ".nex", "skills"), "alpha")
	result, err := exportService.ListLocalUserSkillCandidates(context.Background())
	if err != nil || len(result.Candidates) != 1 {
		t.Fatalf("candidates = %+v, %v", result.Candidates, err)
	}

	exported, err := exportService.ExportSkillsArchive(context.Background(), []string{result.Candidates[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(exported.Archive) == 0 || exported.ArchiveBytes != len(exported.Archive) {
		t.Fatalf("export = %+v", exported)
	}

	// Import into a fresh remote home.
	importService, remoteHome := newService(t)
	imported, err := importService.ImportSkillsArchive(context.Background(), exported.Archive, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(imported.Results) != 1 || imported.Results[0].Status != "synced" {
		t.Fatalf("import = %+v", imported.Results)
	}
	if _, err := os.Stat(filepath.Join(remoteHome, ".nex", "skills", "alpha", "SKILL.md")); err != nil {
		t.Fatal("synced skill missing on disk")
	}

	// Re-import: skipped (target exists).
	imported, err = importService.ImportSkillsArchive(context.Background(), exported.Archive, false)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Results[0].Status != "skipped" {
		t.Fatalf("re-import = %+v", imported.Results)
	}

	// Same-name skill in the .agents compatibility root also skips.
	os.MkdirAll(filepath.Join(remoteHome, ".agents", "skills", "other"), 0o755)
	os.WriteFile(filepath.Join(remoteHome, ".agents", "skills", "other", "SKILL.md"),
		[]byte("---\nname: alpha\n---\nbody"), 0o644)
	os.RemoveAll(filepath.Join(remoteHome, ".nex", "skills", "alpha"))
	imported, err = importService.ImportSkillsArchive(context.Background(), exported.Archive, false)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Results[0].Status != "skipped" {
		t.Fatalf("compat-root name dedupe = %+v", imported.Results)
	}
}

// TestImportOverwriteRefused and unknown candidate errors.
func TestImportOverwriteRefused(t *testing.T) {
	service, _ := newService(t)
	if _, err := service.ImportSkillsArchive(context.Background(), []byte("x"), true); err == nil || err.Error() != "skill sync overwrite is not supported" {
		t.Fatalf("err = %v", err)
	}
	if _, err := service.ExportSkillsArchive(context.Background(), []string{"missing"}); err == nil || err.Error() != "skill sync candidate not found: missing" {
		t.Fatalf("err = %v", err)
	}
}

// TestCheckRemoteUserSkillWriteAccess: ok on a writable root.
func TestCheckRemoteUserSkillWriteAccess(t *testing.T) {
	service, home := newService(t)
	result, err := service.CheckRemoteUserSkillWriteAccess(context.Background())
	if err != nil || !result.OK {
		t.Fatalf("result = %+v, %v", result, err)
	}
	if result.Path != filepath.Join(home, ".nex", "skills") {
		t.Fatalf("path = %s", result.Path)
	}
}
