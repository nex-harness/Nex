// Package tooldefs holds the tool definitions the model sees. They are data, not
// code: tools.json is exported from a request the real Node agent sent
// (scripts/go-rewrite/gen-request-vectors.mjs), so descriptions and schemas are
// byte-identical and in the same key order. A test pins the exported file to the
// request body, and a second one pins the registry to the same list.
package tooldefs

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed tools.json
var raw []byte

// Def is one entry of the OpenAI `tools` array.
type Def struct {
	Name string
	// JSON is the entry exactly as sent ({"type":"function","function":{...}}).
	JSON json.RawMessage
}

// Default returns the tool list a plain session (no MCP, plugins or skills) sends,
// in the order the Node agent sends it.
func Default() ([]Def, error) {
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	defs := make([]Def, len(entries))
	for i, e := range entries {
		var head struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}
		if err := json.Unmarshal(e, &head); err != nil || head.Function.Name == "" {
			return nil, fmt.Errorf("tool %d has no function.name", i)
		}
		// Compact so the bytes on the wire match JSON.stringify of the same value.
		var buf bytes.Buffer
		if err := json.Compact(&buf, e); err != nil {
			return nil, err
		}
		defs[i] = Def{Name: head.Function.Name, JSON: json.RawMessage(buf.Bytes())}
	}
	return defs, nil
}
