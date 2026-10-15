// ticketstore.go holds the local ticket store: the on-disk file format, the
// migration of stored tickets and the list/upsert operations. Split out of
// feedback.go; symbols are unchanged.
package feedback

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

var ticketStatuses = map[string]bool{
	"已提交": true, "信息不足": true, "已采纳": true, "答复关闭": true,
	"已归档": true, "已拒绝": true, "开发中": true, "已解决": true, "已上线": true,
}

type localTicketStore struct {
	rootDir string
}

// filePath resolves tickets.json; empty root disables persistence (tests
// that only exercise create/list flows against fakes).
func (s *localTicketStore) filePath() (string, bool) {
	if s.rootDir == "" {
		return "", false
	}
	return filepath.Join(s.rootDir, "tickets.json"), true
}

func (s *localTicketStore) read() ticketStoreFile {
	path, ok := s.filePath()
	if !ok {
		return ticketStoreFile{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ticketStoreFile{}
	}
	var parsed map[string]any
	if json.Unmarshal(data, &parsed) != nil {
		return ticketStoreFile{}
	}
	rawTickets, ok := parsed["tickets"].([]any)
	if !ok {
		return ticketStoreFile{}
	}
	file := ticketStoreFile{}
	for _, raw := range rawTickets {
		record, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		stored := storedTicket{}
		if str, ok := record["deviceMid"].(string); ok {
			stored.DeviceMid = str
		} else {
			continue
		}
		if str, ok := record["id"].(string); ok {
			stored.ID = str
		} else {
			continue
		}
		if str, ok := record["title"].(string); ok {
			stored.Title = str
		} else {
			continue
		}
		if str, ok := record["type"].(string); ok {
			stored.Type = str
		} else {
			continue
		}
		if str, ok := record["status"].(string); ok {
			stored.Status = str
		} else {
			continue
		}
		if str, ok := record["created_at"].(string); ok {
			stored.CreatedAt = str
		} else {
			continue
		}
		if str, ok := record["updated_at"].(string); ok {
			stored.UpdatedAt = str
		} else {
			continue
		}
		file.Tickets = append(file.Tickets, migrateStoredTicket(stored))
	}
	return file
}

// migrateStoredTicket ports migrateStoredFeedbackTicket: 3.3.5 renamed
// 「待评估」 to 「已提交」; unknown statuses crash the UI's status metadata
// lookup, so they normalize on read.
func migrateStoredTicket(ticket storedTicket) storedTicket {
	if ticket.Status == "待评估" || !ticketStatuses[ticket.Status] {
		ticket.Status = "已提交"
	}
	return ticket
}

func (s *localTicketStore) write(file ticketStoreFile) error {
	path, ok := s.filePath()
	if !ok {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// List ports FeedbackLocalTicketStore.list.
func (s *localTicketStore) List(deviceMid string, query ListQuery) ([]TicketSummary, error) {
	file := s.read()
	var tickets []TicketSummary
	for _, stored := range file.Tickets {
		if stored.DeviceMid != deviceMid {
			continue
		}
		if query.Status != "" && stored.Status != query.Status {
			continue
		}
		if query.Type != "" && stored.Type != query.Type {
			continue
		}
		tickets = append(tickets, stored.TicketSummary)
	}
	sort.SliceStable(tickets, func(i, j int) bool {
		return tickets[i].UpdatedAt > tickets[j].UpdatedAt
	})
	offset := query.Offset
	if offset < 0 {
		offset = 0
	}
	limit := len(tickets)
	if query.Limit >= 0 && query.Limit < limit {
		limit = query.Limit
	}
	if offset > len(tickets) {
		return []TicketSummary{}, nil
	}
	end := offset + limit
	if end > len(tickets) {
		end = len(tickets)
	}
	return tickets[offset:end], nil
}

// Upsert ports FeedbackLocalTicketStore.upsert (cap 200, newest first).
func (s *localTicketStore) Upsert(deviceMid string, ticket TicketSummary) error {
	file := s.read()
	next := storedTicket{TicketSummary: ticket, DeviceMid: deviceMid}
	var rest []storedTicket
	for _, current := range file.Tickets {
		if current.DeviceMid != deviceMid || current.ID != ticket.ID {
			rest = append(rest, current)
		}
	}
	all := append([]storedTicket{next}, rest...)
	if len(all) > 200 {
		all = all[:200]
	}
	return s.write(ticketStoreFile{Tickets: all})
}

// ── compact log archive ───────────────────────────────────────────────────

const (
	archiveMaxFileBytes  = 8 * 1024 * 1024
	archiveMaxTotalBytes = 32 * 1024 * 1024
	compactMaxTotalBytes = 2 * 1024 * 1024
	archiveMaxDepth      = 4
	archiveMaxVisited    = 2000
)

var diagnosticFileRe = regexp.MustCompile(`(?i)(?:\.log(?:\.\d+)?|\.jsonl|\.ndjson)$`)

type logArchive struct {
	sources        []ArchiveSource
	outputRoot     string
	appVersion     string
	commit         string
	runtimeVersion string
}
