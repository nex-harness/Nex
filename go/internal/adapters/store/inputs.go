package store

import (
	"bytes"
	"encoding/json"

	"github.com/tylinux/nex/go/internal/sessionstore"
)

// AdmitInput inserts an admission row, matching saveSessionInput. A re-admission
// with the same id keeps its original sequence and status (idempotent), but
// updates kind/delivery/payload.
func (s *SessionStore) AdmitInput(input sessionstore.SessionInput) error {
	now := s.now()
	if input.TimeNow != nil {
		now = *input.TimeNow
	}
	payload := input.Payload
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	_, err := s.db.Exec(`
		insert into session_input (
			id, session_id, kind, delivery, payload,
			admitted_sequence, status, time_created, time_updated
		)
		values (
			?, ?, ?, ?, ?,
			(select coalesce(max(admitted_sequence), -1) + 1 from session_input where session_id = ?),
			'admitted', ?, ?
		)
		on conflict(id) do update set
			kind = excluded.kind,
			delivery = excluded.delivery,
			payload = excluded.payload,
			time_updated = excluded.time_updated`,
		input.ID, input.SessionID, input.Kind, input.Delivery, string(payload), input.SessionID, now, now,
	)
	return err
}

// UpdateInputs rewrites the durable intent for queued inputs (queue edit/reorder).
func (s *SessionStore) UpdateInputs(sessionID string, updates []sessionstore.SessionInputUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	tx, err := s.beginTx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// 读也要走同一个事务：连接池只有一条连接（MaxOpenConns=1），事务持有连接时
	// 用 s.db 再读会自锁。
	now := s.now()
	for _, update := range updates {
		var delivery, payloadRaw string
		if err := tx.QueryRow(
			"select delivery, payload from session_input where id = ? and session_id = ? and status = 'admitted'",
			update.ID, sessionID).Scan(&delivery, &payloadRaw); err != nil {
			continue // no admitted row: skip, matching Node
		}
		payload := decodePayload(payloadRaw)
		if update.Text != nil {
			payload.set("text", *update.Text)
		}
		if ci, ok := payload.get("conversationInputIntent"); ok {
			payload.setRaw("conversationInputIntent", patchConversationInputIntent(ci, update))
		}
		if len(update.Intent) > 0 {
			payload.setRaw("intent", update.Intent)
		} else if _, ok := payload.get("intent"); ok && update.QueuePosition != nil {
			intent := decodeOrdered(string(payload.values["intent"]))
			intent.set("queuePosition", *update.QueuePosition)
			payload.setRaw("intent", json.RawMessage(intent.encode()))
		}
		if _, err := tx.Exec(
			"update session_input set delivery = ?, payload = ?, time_updated = ? where id = ? and session_id = ? and status = 'admitted'",
			delivery, payload.encode(), now, update.ID, sessionID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SettleInput closes an admitted input as cancelled/discarded/failed.
func (s *SessionStore) SettleInput(id, sessionID, status, reason string) error {
	var reasonArg any
	if reason != "" {
		reasonArg = reason
	}
	_, err := s.db.Exec(`
		update session_input
		set status = ?, status_reason = ?, time_updated = ?
		where id = ? and session_id = ? and status = 'admitted'`,
		status, reasonArg, s.now(), id, sessionID,
	)
	return err
}

// MarkInputPromoted flips an admitted input to promoted without writing a message.
func (s *SessionStore) MarkInputPromoted(id, sessionID, promotedMessageID string) error {
	_, err := s.db.Exec(`
		update session_input
		set status = 'promoted',
			promoted_message_id = ?,
			promoted_sequence = (
				select coalesce(max(promoted_sequence), -1) + 1
				from session_input
				where session_id = ?
			),
			time_updated = ?
		where id = ? and session_id = ? and status = 'admitted'`,
		promotedMessageID, sessionID, s.now(), id, sessionID,
	)
	return err
}

// PromoteInput writes the user message and parts and marks the input promoted in
// one transaction. This atomicity is required: it prevents "queue consumed but
// transcript has no user message".
func (s *SessionStore) PromoteInput(id, sessionID string, message sessionstore.Message, parts []sessionstore.Part) error {
	tx, err := s.beginTx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := saveMessageTx(tx, s, message); err != nil {
		return err
	}
	for _, part := range parts {
		if err := savePartTx(tx, s, part); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`
		update session_input
		set status = 'promoted',
			promoted_message_id = ?,
			promoted_sequence = (
				select coalesce(max(promoted_sequence), -1) + 1
				from session_input
				where session_id = ?
			),
			time_updated = ?
		where id = ? and session_id = ?`,
		message.ID, sessionID, s.now(), id, sessionID,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// orderedObject keeps JSON object members in their original order so that a
// rewrite of session_input.payload stays byte-comparable with Node's
// JSON.stringify output (Go maps would sort keys).
type orderedObject struct {
	keys   []string
	values map[string]json.RawMessage
}

func decodeOrdered(raw string) *orderedObject {
	o := &orderedObject{values: map[string]json.RawMessage{}}
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return o
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return o
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			break
		}
		key, _ := keyTok.(string)
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			break
		}
		if _, seen := o.values[key]; !seen {
			o.keys = append(o.keys, key)
		}
		o.values[key] = val
	}
	return o
}

func (o *orderedObject) set(key string, value any) {
	raw, err := json.Marshal(value)
	if err != nil {
		return
	}
	o.setRaw(key, raw)
}

func (o *orderedObject) setRaw(key string, value json.RawMessage) {
	if _, seen := o.values[key]; !seen {
		o.keys = append(o.keys, key)
	}
	o.values[key] = value
}

func (o *orderedObject) get(key string) (json.RawMessage, bool) {
	v, ok := o.values[key]
	return v, ok
}

func (o *orderedObject) encode() string {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(o.values[k])
	}
	buf.WriteByte('}')
	return buf.String()
}

func decodePayload(raw string) *orderedObject {
	o := decodeOrdered(raw)
	if _, ok := o.get("text"); !ok {
		o.set("text", "")
	}
	return o
}

func patchConversationInputIntent(raw json.RawMessage, patch sessionstore.SessionInputUpdate) json.RawMessage {
	obj := decodeOrdered(string(raw))
	if obj == nil {
		return raw
	}
	if patch.Text != nil {
		obj.set("text", *patch.Text)
	}
	var intentObj *orderedObject
	if len(patch.Intent) > 0 {
		intentObj = decodeOrdered(string(patch.Intent))
	}
	var queuePosition *int
	if patch.QueuePosition != nil {
		queuePosition = patch.QueuePosition
	} else if intentObj != nil {
		if qp, ok := intentObj.get("queuePosition"); ok {
			var n int
			if json.Unmarshal(qp, &n) == nil {
				queuePosition = &n
			}
		}
	}
	if queuePosition != nil {
		if orderRaw, ok := obj.get("order"); ok {
			order := decodeOrdered(string(orderRaw))
			order.set("queuePosition", *queuePosition)
			obj.setRaw("order", json.RawMessage(order.encode()))
		} else {
			obj.set("order", map[string]any{"queuePosition": *queuePosition})
		}
	}
	if intentObj != nil {
		delivery := &orderedObject{values: map[string]json.RawMessage{}}
		delivery.setRaw("requested", intentObj.values["requestedDelivery"])
		delivery.setRaw("admitted", intentObj.values["admittedDelivery"])
		if fr, ok := intentObj.get("fallbackReasonCode"); ok && string(fr) != "null" {
			delivery.setRaw("fallbackReasonCode", fr)
			steer := &orderedObject{values: map[string]json.RawMessage{}}
			steer.set("state", "fellBack")
			steer.setRaw("reasonCode", fr)
			obj.setRaw("steer", json.RawMessage(steer.encode()))
		}
		obj.setRaw("delivery", json.RawMessage(delivery.encode()))
	}
	return json.RawMessage(obj.encode())
}
