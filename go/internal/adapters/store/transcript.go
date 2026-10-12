package store

import (
	"database/sql"

	"github.com/tylinux/nex/go/internal/sessionstore"
)

// SaveMessage mirrors saveMessage: sequence is allocated per session as
// max(sequence)+1, an existing row in the same session keeps its sequence, and
// the session is touched. The legacy `model` snapshot is re-derived for user
// messages (Node always writes it).
func (s *SessionStore) SaveMessage(message sessionstore.Message) error {
	return saveMessageTx(s.db, s, message)
}

// SavePart mirrors savePart. time_created is derived by PartCreatedAt and
// time_updated is the current clock; the session is touched.
func (s *SessionStore) SavePart(part sessionstore.Part) error {
	return savePartTx(s.db, s, part)
}

// execer is satisfied by *sql.DB and *sql.Tx.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
}

func saveMessageTx(db execer, s *SessionStore, message sessionstore.Message) error {
	data, err := sessionstore.EncodeMessageData(message)
	if err != nil {
		return err
	}
	timeUpdated := message.TimeCreated
	if message.Role == "assistant" && message.TimeCompleted != nil {
		timeUpdated = *message.TimeCompleted
	}
	_, err = db.Exec(`
		insert into message (id, session_id, time_created, time_updated, data, sequence)
		values (
			?, ?, ?, ?, ?,
			(select coalesce(max(sequence), -1) + 1 from message where session_id = ?)
		)
		on conflict(id) do update set
			session_id = excluded.session_id,
			time_updated = excluded.time_updated,
			data = case when message.session_id = excluded.session_id then excluded.data else excluded.data end,
			sequence = case
				when message.session_id = excluded.session_id then message.sequence
				else excluded.sequence
			end`,
		message.ID, message.SessionID, message.TimeCreated, timeUpdated, string(data), message.SessionID,
	)
	if err != nil {
		return err
	}
	return s.touchSessionExec(db, message.SessionID, timeUpdated)
}

func savePartTx(db execer, s *SessionStore, part sessionstore.Part) error {
	data, err := sessionstore.EncodePartData(part)
	if err != nil {
		return err
	}
	if data == nil {
		data = part.RawData // caller-supplied for part types the encoder does not model
	}
	timeCreated := sessionstore.PartCreatedAt(part, s.now())
	if part.TimeCreated != nil {
		timeCreated = *part.TimeCreated
	}
	timeUpdated := s.now()
	sequence := part.Sequence
	if sequence != nil {
		_, err = db.Exec(`
			insert into part (id, message_id, session_id, time_created, time_updated, data, sequence)
			values (?, ?, ?, ?, ?, ?, ?)
			on conflict(id) do update set
				message_id = excluded.message_id,
				session_id = excluded.session_id,
				time_updated = excluded.time_updated,
				data = excluded.data,
				sequence = case
					when part.message_id = excluded.message_id and part.session_id = excluded.session_id
						then part.sequence
					else excluded.sequence
				end`,
			part.ID, part.MessageID, part.SessionID, timeCreated, timeUpdated, string(data), *sequence,
		)
	} else {
		_, err = db.Exec(`
			insert into part (id, message_id, session_id, time_created, time_updated, data, sequence)
			values (?, ?, ?, ?, ?, ?,
				(select coalesce(max(sequence), -1) + 1 from part where message_id = ?)
			)
			on conflict(id) do update set
				message_id = excluded.message_id,
				session_id = excluded.session_id,
				time_updated = excluded.time_updated,
				data = excluded.data,
				sequence = case
					when part.message_id = excluded.message_id and part.session_id = excluded.session_id
						then part.sequence
					else excluded.sequence
				end`,
			part.ID, part.MessageID, part.SessionID, timeCreated, timeUpdated, string(data), part.MessageID,
		)
	}
	if err != nil {
		return err
	}
	return s.touchSessionExec(db, part.SessionID, timeUpdated)
}

func (s *SessionStore) touchSessionExec(db execer, id string, timeUpdated int64) error {
	_, err := db.Exec("update session set time_updated = max(time_updated, ?) where id = ?", timeUpdated, id)
	return err
}

// ListMessages returns the transcript ordered exactly as messages.ts does, with
// parts grouped per message. Used for resume and cross-reads.
func (s *SessionStore) ListMessages(sessionID string) ([]sessionstore.MessageWithParts, error) {
	rows, err := s.db.Query(`
		select id, session_id, sequence, time_created, time_updated, data from message
		where session_id = ?
		order by sequence is null, sequence, time_created, rowid`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []sessionstore.MessageWithParts
	for rows.Next() {
		var m sessionstore.MessageWithParts
		if err := rows.Scan(&m.MessageID, &m.SessionID, &m.Sequence, &m.TimeCreated, &m.TimeUpdated, &m.Data); err != nil {
			return nil, err
		}
		messages = append(messages, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	partRows, err := s.db.Query(`
		select id, message_id, session_id, sequence, time_created, time_updated, data from part
		where session_id = ?
		order by message_id, sequence is null, sequence, time_created, id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer partRows.Close()
	byMessage := map[string][]sessionstore.StoredPart{}
	for partRows.Next() {
		var p sessionstore.StoredPart
		if err := partRows.Scan(&p.ID, &p.MessageID, &p.SessionID, &p.Sequence, &p.TimeCreated, &p.TimeUpdated, &p.Data); err != nil {
			return nil, err
		}
		byMessage[p.MessageID] = append(byMessage[p.MessageID], p)
	}
	if err := partRows.Err(); err != nil {
		return nil, err
	}
	for i := range messages {
		messages[i].Parts = byMessage[messages[i].MessageID]
	}
	return messages, nil
}
