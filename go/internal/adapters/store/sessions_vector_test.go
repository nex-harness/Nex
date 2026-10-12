package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/tylinux/nex/go/internal/adapters/store"
	"github.com/tylinux/nex/go/internal/sessionstore"
)

// fixedClock returns a constant so Date.now()-derived columns are reproducible.
type fixedClock struct{ now int64 }

func (c fixedClock) Now() int64 { return c.now }

const vectorPath = "../../../testdata/vectors/store-scenarios.json"

type vectors struct {
	ExcludedColumns []string `json:"excludedColumns"`
	Scenarios       []struct {
		Name   string                      `json:"name"`
		Tables map[string][]map[string]any `json:"tables"`
	} `json:"scenarios"`
}

const (
	vectorNow = 2_000_000_000_500
	vecDir    = "/work/proj"
	vecProj   = "proj_work-proj"
	vecSess   = "sess_00000000-0000-4000-8000-000000000001"
	vecTurn   = "turn_00000000-0000-4000-8000-000000000002"
	vecTrace  = "trace-0000-0000-0000-000000000000"
	vecSpan   = "span-0000-0000-00"
	vecUser   = "msg_test_user_1"
)

var vectorTime = struct {
	sessionCreate, inputAdmit, userMessage, modelStart, firstToken, assistantComplete, turnComplete, title, toolStart, toolFirstOutput, toolComplete int64
}{
	sessionCreate:     vectorNow - 20_000,
	inputAdmit:        vectorNow - 19_000,
	userMessage:       vectorNow - 18_000,
	modelStart:        vectorNow - 17_000,
	firstToken:        vectorNow - 16_000,
	assistantComplete: vectorNow - 15_000,
	turnComplete:      vectorNow - 14_000,
	title:             vectorNow - 13_000,
	toolStart:         vectorNow - 12_000,
	toolFirstOutput:   vectorNow - 11_000,
	toolComplete:      vectorNow - 10_000,
}

func loadVectors(t *testing.T) vectors {
	t.Helper()
	raw, err := os.ReadFile(vectorPath)
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var v vectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("parse vectors: %v", err)
	}
	return v
}

func openStore(t *testing.T) *store.SessionStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "db.sqlite")
	db, err := store.Open(context.Background(), path, nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return store.NewSessionStore(db, store.SessionStoreOptions{Clock: fixedClock{vectorNow}})
}

// dumpTable reads every row of a table with columns sorted, mirroring the
// generator's stableRow so comparisons are column-order independent.
func dumpTable(t *testing.T, s *store.SessionStore, table string) []map[string]any {
	t.Helper()
	db := s.DB()
	rows, err := db.Query("select * from " + table)
	if err != nil {
		t.Fatalf("select %s: %v", table, err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out []map[string]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("scan %s: %v", table, err)
		}
		row := map[string]any{}
		for i, c := range cols {
			row[c] = normalize(vals[i])
		}
		out = append(out, row)
	}
	return out
}

// normalize converts driver values to the JSON shapes the generator produced:
// integers as float64 (JSON number), blobs as string.
func normalize(v any) any {
	switch x := v.(type) {
	case []byte:
		return string(x)
	case int64:
		return float64(x)
	case bool:
		if x {
			return float64(1)
		}
		return float64(0)
	default:
		return v
	}
}

func sortRows(rows []map[string]any) {
	sort.SliceStable(rows, func(i, j int) bool {
		return rowKey(rows[i]) < rowKey(rows[j])
	})
}

// stripColumn removes a column from every row. Used for the one excluded column
// (input_history.id, which Node generates with crypto.randomUUID and cannot be
// injected); its shape is asserted separately by TestInputHistoryIDShape.
func stripColumn(rows []map[string]any, column string) {
	for _, row := range rows {
		delete(row, column)
	}
}

func rowKey(row map[string]any) string {
	b, _ := json.Marshal(row)
	return string(b)
}

var _ = sql.ErrNoRows
var _ = sessionstore.SessionRow{}
