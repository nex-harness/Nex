package runtime

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/tylinux/nex/go/internal/sessionstore"
)

// persistSession writes the session row and its two session_entry rows. Mirrors
// events.ts ensureSessionPersisted + turn-model.ts + execution-state.ts.
func persistSession(s *session) error {
	now := s.nowMs()
	title := titleFromInput(s.firstInputText())
	projectID := projectIDForDirectory(s.ws.Path)
	dir := s.ws.Path
	row, err := s.rt.deps.Store.CreateSession(sessionstore.CreateSessionInput{
		ID:          s.id,
		ProjectID:   projectID,
		Directory:   dir,
		Path:        &dir,
		Title:       title,
		TitleSource: "first_input",
		Version:     s.rt.version,
		Permission:  json.RawMessage(`{"mode":"build"}`),
		TimeCreated: &now,
		TimeUpdated: &now,
	})
	if err != nil {
		return err
	}
	_ = row
	if err := s.rt.deps.Store.UpsertSessionEntry(sessionstore.SessionEntry{
		ID: s.id + ":runtime-model-selection", SessionID: s.id, Type: "runtime/model_selection",
		TimeCreated: now, TimeUpdated: now,
		Data: mustJSON(map[string]any{"modelSelection": json.RawMessage(s.modelSelection)}),
	}); err != nil {
		return err
	}
	return s.rt.deps.Store.UpsertSessionEntry(sessionstore.SessionEntry{
		ID: s.id + ":runtime-execution-state", SessionID: s.id, Type: "runtime/execution_state",
		TimeCreated: now, TimeUpdated: now,
		Data: json.RawMessage(`{"mode":"build","planEnabled":false}`),
	})
}

func (s *session) firstInputText() string { return s.firstText }

// projectIDForDirectory mirrors projectIdFromDirectory: "proj_" + slugify(dir).
func projectIDForDirectory(dir string) string {
	slug := slugify(dir)
	if len(slug) > 80 {
		slug = slug[:80]
	}
	if slug == "" {
		slug = "default"
	}
	return "proj_" + slug
}

var nonSlug = regexp.MustCompile(`[^a-z0-9._-]+`)

func slugify(value string) string {
	s := strings.ToLower(value)
	s = nonSlug.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "session"
	}
	return s
}

// titleFromInput mirrors titleFromInput.
func titleFromInput(input string) string {
	compact := strings.Join(strings.Fields(input), " ")
	if compact == "" {
		return "Untitled session"
	}
	r := []rune(compact)
	if len(r) <= 60 {
		return compact
	}
	return string(r[:57]) + "..."
}
