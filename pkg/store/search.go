package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// SessionSearchHit represents a matched session from full-text search across titles and message history.
type SessionSearchHit struct {
	Session    SessionRecord `json:"session"`
	MatchType  string        `json:"match_type"` // "title" | "workspace" | "message"
	Snippet    string        `json:"snippet"`
	MatchedSeq uint64        `json:"matched_seq,omitempty"`
}

// SearchSessions performs case-insensitive search across top-level session titles,
// workspace paths, and recorded conversation message text.
func (s *Store) SearchSessions(ctx context.Context, query string, limit int) ([]SessionSearchHit, error) {
	q := strings.TrimSpace(query)
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	sessions, err := s.ListSessions(ctx, "", false)
	if err != nil {
		return nil, err
	}
	if q == "" {
		hits := make([]SessionSearchHit, 0, min(len(sessions), limit))
		for _, sess := range sessions {
			if sess.Depth > 0 {
				continue
			}
			hits = append(hits, SessionSearchHit{
				Session:   sess,
				MatchType: "title",
				Snippet:   sess.WorkspacePath,
			})
			if len(hits) >= limit {
				break
			}
		}
		return hits, nil
	}

	qLower := strings.ToLower(q)
	byID := make(map[string]SessionRecord, len(sessions))
	hits := make([]SessionSearchHit, 0, limit)
	matched := make(map[string]bool)

	for _, sess := range sessions {
		if sess.Depth > 0 {
			continue
		}
		byID[sess.ID] = sess
		if strings.Contains(strings.ToLower(sess.Title), qLower) {
			hits = append(hits, SessionSearchHit{
				Session:   sess,
				MatchType: "title",
				Snippet:   sess.WorkspacePath,
			})
			matched[sess.ID] = true
			if len(hits) >= limit {
				return hits, nil
			}
		} else if strings.Contains(strings.ToLower(sess.WorkspacePath), qLower) {
			hits = append(hits, SessionSearchHit{
				Session:   sess,
				MatchType: "workspace",
				Snippet:   sess.WorkspacePath,
			})
			matched[sess.ID] = true
			if len(hits) >= limit {
				return hits, nil
			}
		}
	}

	// Full-text scan over message_end payloads in SQLite
	likeArg := "%" + strings.ReplaceAll(strings.ReplaceAll(qLower, "%", "\\%"), "_", "\\_") + "%"
	rows, err := s.mainDB.QueryContext(ctx, `
		SELECT e.session_id, e.seq, e.payload_json
		FROM session_events e
		INNER JOIN sessions s ON s.id = e.session_id
		WHERE s.depth = 0
		  AND e.event_type = 'message_end'
		  AND LOWER(e.payload_json) LIKE ? ESCAPE '\'
		ORDER BY s.last_active_at DESC, e.seq DESC
		LIMIT 200
	`, likeArg)
	if err != nil {
		return hits, nil
	}
	defer rows.Close()

	for rows.Next() {
		var sessID string
		var seq int64
		var payloadJSON string
		if err := rows.Scan(&sessID, &seq, &payloadJSON); err != nil {
			continue
		}
		if matched[sessID] {
			continue
		}
		sess, ok := byID[sessID]
		if !ok {
			if fetched, gErr := s.GetSession(ctx, sessID); gErr == nil && fetched != nil && fetched.Depth == 0 {
				sess = *fetched
			} else {
				continue
			}
		}
		plain := extractMessagePlainText(payloadJSON)
		if plain == "" || !strings.Contains(strings.ToLower(plain), qLower) {
			continue
		}
		hits = append(hits, SessionSearchHit{
			Session:    sess,
			MatchType:  "message",
			Snippet:    extractMatchSnippet(plain, q, 140),
			MatchedSeq: uint64(max(0, seq)),
		})
		matched[sessID] = true
		if len(hits) >= limit {
			break
		}
	}

	return hits, nil
}

func extractMessagePlainText(payloadJSON string) string {
	var parsed struct {
		Message *struct {
			Role    string `json:"role"`
			Content []struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				Thinking string `json:"thinking"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(payloadJSON), &parsed); err != nil || parsed.Message == nil {
		return ""
	}
	var parts []string
	for _, b := range parsed.Message.Content {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			parts = append(parts, strings.TrimSpace(b.Text))
		}
	}
	return strings.Join(parts, "\n")
}

func extractMatchSnippet(text, query string, maxLen int) string {
	clean := strings.Join(strings.Fields(text), " ")
	runes := []rune(clean)
	if len(runes) <= maxLen {
		return clean
	}
	lowerClean := strings.ToLower(clean)
	idx := strings.Index(lowerClean, strings.ToLower(query))
	if idx < 0 {
		return string(runes[:maxLen]) + "…"
	}
	runeIdx := len([]rune(clean[:idx]))
	start := max(0, runeIdx-maxLen/3)
	end := min(len(runes), start+maxLen)
	prefix := ""
	suffix := ""
	if start > 0 {
		prefix = "…"
	}
	if end < len(runes) {
		suffix = "…"
	}
	return prefix + string(runes[start:end]) + suffix
}

// ExportSessionMarkdown renders a session and all of its recorded conversation turns as clean Markdown.
func (s *Store) ExportSessionMarkdown(ctx context.Context, sessionID string) (string, string, error) {
	sess, err := s.GetSession(ctx, sessionID)
	if err != nil {
		return "", "", err
	}
	events, err := s.ListSessionEventsAfter(ctx, sessionID, 0)
	if err != nil {
		return "", "", err
	}

	title := strings.TrimSpace(sess.Title)
	if title == "" {
		title = "Nex Conversation"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", title)
	fmt.Fprintf(&b, "- **Session ID**: `%s`\n", sess.ID)
	if sess.ModelID != "" {
		fmt.Fprintf(&b, "- **Model**: `%s`", sess.ModelID)
		if sess.ProviderID != "" {
			fmt.Fprintf(&b, " (%s)", sess.ProviderID)
		}
		b.WriteString("\n")
	}
	if sess.WorkspacePath != "" {
		fmt.Fprintf(&b, "- **Workspace**: `%s`\n", sess.WorkspacePath)
	}
	if sess.CreatedAt > 0 {
		fmt.Fprintf(&b, "- **Created**: %s\n", time.UnixMilli(sess.CreatedAt).Format(time.RFC3339))
	}
	b.WriteString("\n---\n\n")

	for _, ev := range events {
		switch ev.EventType {
		case "message_end":
			var parsed struct {
				Message *struct {
					Role    string `json:"role"`
					Content []struct {
						Type      string          `json:"type"`
						Text      string          `json:"text"`
						Thinking  string          `json:"thinking"`
						ToolName  string          `json:"name"`
						Arguments json.RawMessage `json:"arguments"`
					} `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal(ev.Payload, &parsed) != nil || parsed.Message == nil {
				continue
			}
			role := strings.ToLower(parsed.Message.Role)
			if role == "tool" {
				continue
			}
			if role == "user" {
				b.WriteString("## User\n\n")
			} else {
				b.WriteString("## Assistant\n\n")
			}
			for _, blk := range parsed.Message.Content {
				switch blk.Type {
				case "thinking":
					if strings.TrimSpace(blk.Thinking) != "" {
						b.WriteString("<details>\n<summary>Thinking</summary>\n\n")
						b.WriteString(strings.TrimSpace(blk.Thinking))
						b.WriteString("\n\n</details>\n\n")
					}
				case "text":
					if strings.TrimSpace(blk.Text) != "" {
						b.WriteString(strings.TrimSpace(blk.Text))
						b.WriteString("\n\n")
					}
				case "tool_call":
					if blk.ToolName != "" {
						fmt.Fprintf(&b, "> **Tool call**: `%s`", blk.ToolName)
						if len(blk.Arguments) > 0 {
							argStr := strings.TrimSpace(string(blk.Arguments))
							if len(argStr) > 240 {
								argStr = argStr[:240] + "…"
							}
							fmt.Fprintf(&b, " `%s`", argStr)
						}
						b.WriteString("\n\n")
					}
				}
			}
		case "tool_execution_end":
			var parsed struct {
				ToolName   string `json:"tool_name"`
				ToolResult *struct {
					Output  string `json:"output"`
					IsError bool   `json:"is_error"`
				} `json:"tool_result"`
			}
			if json.Unmarshal(ev.Payload, &parsed) == nil && parsed.ToolName != "" && parsed.ToolResult != nil {
				out := strings.TrimSpace(parsed.ToolResult.Output)
				if len(out) > 1200 {
					out = out[:1200] + "\n… (truncated)"
				}
				status := "ok"
				if parsed.ToolResult.IsError {
					status = "error"
				}
				fmt.Fprintf(&b, "<details>\n<summary>Tool result: <code>%s</code> (%s)</summary>\n\n```text\n%s\n```\n\n</details>\n\n", parsed.ToolName, status, out)
			}
		}
	}

	safeName := sanitizeFilename(title)
	if safeName == "" {
		safeName = sess.ID
	}
	return b.String(), safeName + ".md", nil
}

func sanitizeFilename(s string) string {
	var out strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|':
			out.WriteByte('-')
		case r == '\n' || r == '\r' || r == '\t':
			out.WriteByte(' ')
		default:
			out.WriteRune(r)
		}
	}
	res := strings.Trim(strings.Join(strings.Fields(out.String()), "-"), "-.")
	if len([]rune(res)) > 64 {
		res = string([]rune(res)[:64])
	}
	return res
}
