package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func open(t *testing.T, path string, rec *[]Progress) *sql.DB {
	t.Helper()
	db, err := Open(context.Background(), path, func(p Progress) error {
		if rec != nil {
			*rec = append(*rec, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestEmbeddedMigrationsMatchManifestAndAreVerbatim(t *testing.T) {
	list, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 22 {
		t.Fatalf("want 22 migrations, got %d", len(list))
	}
	for i := 1; i < len(list); i++ {
		if list[i-1].ID >= list[i].ID {
			t.Fatalf("migrations out of order: %s then %s", list[i-1].ID, list[i].ID)
		}
	}
	for _, m := range list {
		sum := sha256.Sum256([]byte(strings.TrimSpace(m.sql)))
		if hex.EncodeToString(sum[:]) != m.Checksum {
			t.Errorf("%s: checksum drift", m.ID)
		}
	}
}

func TestFreshDatabaseProgressSequenceMatchesNode(t *testing.T) {
	var got []Progress
	open(t, filepath.Join(t.TempDir(), "db.sqlite"), &got)
	// checking, checking(facts), 22 x migrating, committing, ready
	if len(got) != 26 {
		t.Fatalf("want 26 states, got %d", len(got))
	}
	if got[0].Phase != PhaseChecking || got[0].Migration != nil {
		t.Errorf("first state: %+v", got[0])
	}
	if got[1].Phase != PhaseChecking || got[1].Migration == nil || got[1].Migration.Kind != "initialize" {
		t.Errorf("second state: %+v", got[1])
	}
	for i := 0; i < 22; i++ {
		p := got[2+i]
		if p.Phase != PhaseMigrating || *p.Completed != i || *p.Total != 22 || p.Migration.ExecutedCount != i {
			t.Errorf("state %d: %+v completed=%v", 2+i, p, p.Completed)
		}
		if p.Migration.LastAppliedMigrationID != nil {
			t.Errorf("fresh database must report lastApplied null")
		}
	}
	last := got[25]
	if last.Phase != PhaseReady || last.Migration.ExecutedCount != 22 || last.Migration.CommittedCount != 22 {
		t.Errorf("ready state: %+v", last.Migration)
	}
}

func TestReopenIsANoOpAndReportsKindNone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.sqlite")
	open(t, path, nil).Close()
	var got []Progress
	open(t, path, &got)
	for _, p := range got {
		if p.Phase == PhaseMigrating {
			t.Fatalf("reopen must not migrate: %+v", p)
		}
	}
	final := got[len(got)-1]
	if final.Phase != PhaseReady || final.Migration.Kind != "none" || final.Migration.ExecutedCount != 0 {
		t.Errorf("final: %+v", final.Migration)
	}
	if final.Migration.LastAppliedMigrationID == nil || *final.Migration.LastAppliedMigrationID != "0022_backfilled_session_reasoning" {
		t.Errorf("lastApplied: %v", final.Migration.LastAppliedMigrationID)
	}
}

func TestPartiallyMigratedDatabaseUpgrades(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.sqlite")
	list, _ := Migrations()
	// Build a database as an older agent would have left it: first 5 migrations applied.
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	raw.Exec(`create table schema_migration (id text primary key, checksum text not null, app_version text, time_applied integer not null)`)
	for _, m := range list[:5] {
		if _, err := raw.Exec(m.sql); err != nil {
			t.Fatal(m.ID, err)
		}
		raw.Exec("insert into schema_migration values (?,?,?,?)", m.ID, m.Checksum, m.AppVersion, 1)
	}
	raw.Close()
	var got []Progress
	open(t, path, &got)
	migrating := 0
	for _, p := range got {
		if p.Phase == PhaseMigrating {
			migrating++
		}
	}
	if migrating != 17 {
		t.Errorf("want 17 pending migrations run, got %d", migrating)
	}
	if k := got[1].Migration.Kind; k != "upgrade" {
		t.Errorf("kind: %s", k)
	}
}

func TestTamperedHistoricalMigrationIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.sqlite")
	db := open(t, path, nil)
	if _, err := db.Exec("update schema_migration set checksum='deadbeef' where id='0001_base_session_store'"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	var got []Progress
	_, err := Open(context.Background(), path, func(p Progress) error { got = append(got, p); return nil })
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("want checksum mismatch, got %v", err)
	}
	if got[len(got)-1].Phase != PhaseFailed || got[len(got)-1].ErrorCode != "checksum_mismatch" {
		t.Errorf("failure must be reported with code: %+v", got[len(got)-1])
	}
}

func TestDatabaseFromNewerAgentIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.sqlite")
	db := open(t, path, nil)
	db.Exec("insert into schema_migration values ('0099_from_the_future','x','9.9.9',1)")
	db.Close()
	if _, err := Open(context.Background(), path, nil); err == nil || !strings.Contains(err.Error(), "newer agent") {
		t.Fatalf("want ErrDatabaseNewer, got %v", err)
	}
}

func TestFailedMigrationRollsBackEverything(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.sqlite")
	list, _ := Migrations()
	raw, _ := sql.Open("sqlite", "file:"+path)
	raw.Exec("create table session (id text)") // collides with 0001's `create table session`
	raw.Close()
	if _, err := Open(context.Background(), path, nil); err == nil {
		t.Fatalf("expected the colliding migration to fail (%s)", list[0].ID)
	}
	raw, _ = sql.Open("sqlite", "file:"+path)
	defer raw.Close()
	var n int
	raw.QueryRow("select count(*) from schema_migration").Scan(&n)
	if n != 0 {
		t.Errorf("a failed startup must leave no ledger rows, found %d", n)
	}
}

func TestFileSurvivesAndIsWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.sqlite")
	db := open(t, path, nil)
	var mode string
	db.QueryRow("pragma journal_mode").Scan(&mode)
	if mode != "wal" {
		t.Errorf("journal mode %q", mode)
	}
	var fk int
	db.QueryRow("pragma foreign_keys").Scan(&fk)
	if fk != 1 {
		t.Errorf("foreign_keys must be on, as in the Node runner")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

// The host distinguishes "key absent" (ledger not read yet) from null (fresh
// database) from an id. Pin all three, because encoding/json would collapse them.
func TestFactsJSONKeepsAbsentNullAndValueApart(t *testing.T) {
	enc := func(f MigrationFacts) string {
		b, err := f.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if s := enc(MigrationFacts{Kind: "initialize"}); strings.Contains(s, "lastAppliedMigrationId") {
		t.Errorf("before the ledger is read the key must be absent: %s", s)
	}
	if s := enc(MigrationFacts{Kind: "initialize", hasLast: true}); !strings.Contains(s, `"lastAppliedMigrationId":null`) {
		t.Errorf("fresh database must report null: %s", s)
	}
	id := "0022_x"
	if s := enc(MigrationFacts{Kind: "none", hasLast: true, LastAppliedMigrationID: &id}); !strings.Contains(s, `"lastAppliedMigrationId":"0022_x"`) {
		t.Errorf("applied database must report the id: %s", s)
	}
}

func TestProgressJSONOmitsUnsetFields(t *testing.T) {
	b, _ := Progress{Phase: PhaseChecking, ElapsedMs: 3}.MarshalJSON()
	if string(b) != `{"elapsedMs":3,"phase":"checking"}` {
		t.Errorf("got %s", b)
	}
	c, tot := 0, 22
	b, _ = Progress{Phase: PhaseMigrating, MigrationID: "0001_x", Completed: &c, Total: &tot}.MarshalJSON()
	if !strings.Contains(string(b), `"completed":0`) || !strings.Contains(string(b), `"total":22`) {
		t.Errorf("completed:0 must be kept (a zero is meaningful): %s", b)
	}
}
