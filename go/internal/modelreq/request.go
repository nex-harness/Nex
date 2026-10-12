// Package modelreq builds the OpenAI chat-completions request body the agent
// sends. Field order is the order the Node agent serialises them in, because the
// body is compared byte-for-byte in tests and some gateways are order-sensitive
// when caching prompts.
package modelreq

import (
	"bytes"
	"encoding/json"

	"github.com/tylinux/nex/go/internal/promptctx"
	"github.com/tylinux/nex/go/internal/tooldefs"
)

const (
	systemReminderOpen  = "<system-reminder>\n"
	systemReminderClose = "\n</system-reminder>\n"
)

// Message is one chat message. Content is the text content. ToolCalls carries
// the assistant message's tool_calls array (raw JSON); Content is then encoded
// as null. ToolCallID marks a tool-result message (role "tool").
type Message struct {
	Role       string
	Content    string
	ToolCalls  json.RawMessage
	ToolCallID string
	ToolName   string
}

// Input is everything the first request of a turn depends on.
type Input struct {
	ModelID         string
	Context         promptctx.Config
	History         []Message // messages after the leading system and meta-user ones
	MaxOutputTokens int
	Tools           []tooldefs.Def
}

// Build returns the request body as compact JSON.
func Build(in Input) ([]byte, error) {
	built := promptctx.Build(in.Context)
	msgs := []Message{{Role: "system", Content: built.MergedSystem}}
	for _, a := range built.MetaUser {
		msgs = append(msgs, Message{Role: "user", Content: systemReminderOpen + a.Content + systemReminderClose})
	}
	msgs = append(msgs, in.History...)

	var b bytes.Buffer
	b.WriteString(`{"model":`)
	writeString(&b, in.ModelID)
	b.WriteString(`,"messages":[`)
	for i, m := range msgs {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"role":`)
		writeString(&b, m.Role)
		if m.ToolCallID != "" {
			b.WriteString(`,"tool_call_id":`)
			writeString(&b, m.ToolCallID)
			b.WriteString(`,"content":`)
			writeString(&b, m.Content)
			b.WriteByte('}')
			continue
		}
		if len(m.ToolCalls) > 0 {
			b.WriteString(`,"content":null,"tool_calls":`)
			b.Write(m.ToolCalls)
			b.WriteByte('}')
			continue
		}
		b.WriteString(`,"content":`)
		writeString(&b, m.Content)
		b.WriteByte('}')
	}
	b.WriteString(`],"tools":[`)
	for i, t := range in.Tools {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(t.JSON)
	}
	b.WriteString(`],"tool_choice":"auto","stream":true,"stream_options":{"include_usage":true},"max_completion_tokens":`)
	n, _ := json.Marshal(in.MaxOutputTokens)
	b.Write(n)
	b.WriteByte('}')
	return b.Bytes(), nil
}

// writeString writes s as JSON.stringify does: no HTML escaping, U+2028/U+2029 raw.
func writeString(b *bytes.Buffer, s string) {
	enc := json.NewEncoder(b)
	enc.SetEscapeHTML(false)
	var tmp bytes.Buffer
	e := json.NewEncoder(&tmp)
	e.SetEscapeHTML(false)
	_ = enc
	_ = e.Encode(s)
	out := bytes.TrimSuffix(tmp.Bytes(), []byte("\n"))
	// encoding/json escapes U+2028/U+2029 even with SetEscapeHTML(false); JSON.stringify does not.
	out = bytes.ReplaceAll(out, []byte(`\u2028`), []byte("\u2028"))
	out = bytes.ReplaceAll(out, []byte(`\u2029`), []byte("\u2029"))
	b.Write(out)
}
