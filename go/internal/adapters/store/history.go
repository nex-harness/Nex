package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"strings"

	"github.com/tylinux/nex/go/internal/sessionstore"
)

const inputHistoryLimit = 100

// RecordInputHistory mirrors recordInputHistory: it trims the text, skips
// duplicates of the most recent entry, inserts, and keeps only the newest 100
// rows. A skipped duplicate returns ("", nil).
func (s *SessionStore) RecordInputHistory(input sessionstore.InputHistory) (string, error) {
	text := trimSpace(input.Text)
	if text == "" {
		return "", nil
	}
	latest, err := s.latestInputHistory(input.ProjectID)
	if err != nil {
		return "", err
	}
	if latest != nil && latest.text == text && latest.attachments == stringMatch(input.Attachments) {
		return "", nil
	}
	id := inputHistoryID(s.now())
	timeCreated := s.now()
	if input.TimeCreated != nil {
		timeCreated = *input.TimeCreated
	}
	tx, err := s.beginTx()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		insert into input_history (id, project_id, session_id, text, attachments, kind, time_created)
		values (?, ?, ?, ?, ?, ?, ?)`,
		id, input.ProjectID, nullString(input.SessionID), text, nullableBlob(input.Attachments),
		input.Kind, timeCreated,
	); err != nil {
		return "", err
	}
	if _, err := tx.Exec(`
		delete from input_history
		where id not in (
			select id from input_history order by time_created desc, id desc limit ?
		)`, inputHistoryLimit,
	); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return id, nil
}

type inputHistoryRow struct {
	text        string
	attachments string
}

func (s *SessionStore) latestInputHistory(projectID string) (*inputHistoryRow, error) {
	var row inputHistoryRow
	err := s.db.QueryRow(`
		select text, coalesce(attachments, '') from input_history
		where project_id = ?
		order by time_created desc, id desc
		limit 1`, projectID).Scan(&row.text, &row.attachments)
	if err != nil {
		if isNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

func stringMatch(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return string(b)
}

func inputHistoryID(now int64) string {
	return "input_" + base36(now) + "_" + randomUUID()
}

// UpdateTodos mirrors updateTodos: it replaces all rows for a session in one
// transaction and touches the session.
func (s *SessionStore) UpdateTodos(sessionID string, todos []sessionstore.Todo) error {
	now := s.now()
	tx, err := s.beginTx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("delete from todo where session_id = ?", sessionID); err != nil {
		return err
	}
	for position, todo := range todos {
		if _, err := tx.Exec(`
			insert into todo (session_id, content, status, priority, position, time_created, time_updated)
			values (?, ?, ?, ?, ?, ?, ?)`,
			sessionID, todo.Content, todo.Status, todo.Priority, position, now, now,
		); err != nil {
			return err
		}
	}
	if _, err := tx.Exec("update session set time_updated = max(time_updated, ?) where id = ?", now, sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

func randomUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	dst := make([]byte, 36)
	hex.Encode(dst[0:8], b[0:4])
	dst[8] = '-'
	hex.Encode(dst[9:13], b[4:6])
	dst[13] = '-'
	hex.Encode(dst[14:18], b[6:8])
	dst[18] = '-'
	hex.Encode(dst[19:23], b[8:10])
	dst[23] = '-'
	hex.Encode(dst[24:36], b[10:16])
	return string(dst)
}

func base36(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	var buf [13]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = digits[v%36]
		v /= 36
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func trimSpace(s string) string { return strings.TrimSpace(s) }

func isNoRows(err error) bool { return err == sql.ErrNoRows }
