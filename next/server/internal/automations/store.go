package automations

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
)

// storeFileName is the persisted document inside Options.Dir.
const storeFileName = "automations.json"

// store is the JSON document plus its atomic writer. All methods are called
// with the owning Service's mutex held; the file is the only durable state.
type store struct {
	path string
	log  *slog.Logger

	doc      document
	loaded   bool
	mutation func()
}

func newStore(dir string, logger *slog.Logger) *store {
	return &store{path: filepath.Join(dir, storeFileName), log: logger}
}

// load reads the document from disk once. A missing file is an empty schema-1
// document. Unknown schemas fail loudly: silently rewriting a newer document
// would drop data.
func (s *store) load() error {
	if s.loaded {
		return nil
	}
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.doc = document{Schema: StoreSchema}
		s.loaded = true
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading %s: %w", s.path, err)
	}
	if len(raw) == 0 {
		s.doc = document{Schema: StoreSchema}
		s.loaded = true
		return nil
	}
	var parsed document
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return fmt.Errorf("decoding %s: %w", s.path, err)
	}
	if parsed.Schema != StoreSchema {
		return fmt.Errorf("%s: unsupported schema %d (want %d)", s.path, parsed.Schema, StoreSchema)
	}
	s.doc = parsed
	s.loaded = true
	return nil
}

// persist writes the document with tmp+rename so a crash never leaves a torn
// file. It always writes before the caller dispatches, which is what makes a
// crash unable to re-fire a run.
func (s *store) persist() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("creating automations directory: %w", err)
	}
	s.doc.Schema = StoreSchema
	encoded, err := json.MarshalIndent(s.doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding automations: %w", err)
	}
	encoded = append(encoded, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".automations-*.tmp")
	if err != nil {
		return fmt.Errorf("creating automations temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("setting automations file mode: %w", err)
	}
	if _, err := tmp.Write(encoded); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing automations: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("syncing automations: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing automations temp file: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("replacing automations: %w", err)
	}
	return nil
}

// commit persists and, on success, notifies the owner (timer re-arm and the
// Change hook). Callers mutate s.doc first; a failed write leaves memory and
// disk divergent, so the error is returned for the caller to surface.
func (s *store) commit() error {
	if err := s.persist(); err != nil {
		return err
	}
	if s.mutation != nil {
		s.mutation()
	}
	return nil
}

// find returns the index of an automation by id, or -1.
func (s *store) find(automationID string) int {
	for index := range s.doc.Automations {
		if s.doc.Automations[index].AutomationID == automationID {
			return index
		}
	}
	return -1
}

// runsOf returns the indices of an automation's runs, oldest first
// (createdAt, then insertion order for ties).
func (s *store) runsOf(automationID string) []int {
	indices := make([]int, 0, 8)
	for index := range s.doc.Runs {
		if s.doc.Runs[index].AutomationID == automationID {
			indices = append(indices, index)
		}
	}
	sort.SliceStable(indices, func(left, right int) bool {
		return s.doc.Runs[indices[left]].CreatedAt < s.doc.Runs[indices[right]].CreatedAt
	})
	return indices
}

// findRun returns the index of a run by id, or -1.
func (s *store) findRun(runID string) int {
	for index := range s.doc.Runs {
		if s.doc.Runs[index].RunID == runID {
			return index
		}
	}
	return -1
}

// upsertRun inserts or replaces a run and prunes the oldest history of that
// automation beyond maxRunsPerAutomation.
func (s *store) upsertRun(run Run) {
	if index := s.findRun(run.RunID); index >= 0 {
		s.doc.Runs[index] = run
		return
	}
	s.doc.Runs = append(s.doc.Runs, run)
	indices := s.runsOf(run.AutomationID)
	if overflow := len(indices) - maxRunsPerAutomation; overflow > 0 {
		drop := make(map[int]struct{}, overflow)
		for _, index := range indices[:overflow] {
			drop[index] = struct{}{}
		}
		kept := s.doc.Runs[:0]
		for index, existing := range s.doc.Runs {
			if _, dropped := drop[index]; dropped {
				continue
			}
			kept = append(kept, existing)
		}
		s.doc.Runs = kept
	}
}

// removeRunsOf drops every run of an automation (delete).
func (s *store) removeRunsOf(automationID string) {
	kept := s.doc.Runs[:0]
	for _, run := range s.doc.Runs {
		if run.AutomationID == automationID {
			continue
		}
		kept = append(kept, run)
	}
	s.doc.Runs = kept
}
