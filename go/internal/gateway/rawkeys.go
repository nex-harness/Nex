package gateway

// rawkeys.go is the JSON scanner behind checkDuplicateKeys.
//
// encoding/json maps a repeated object key onto one map entry and keeps the LAST value, silently.
// That is the wrong behavior for a configuration file: an operator who writes the same channel
// twice — once with a typo'd target, once corrected — gets no error and no warning, and cannot
// tell which of the two the server actually applied. Detecting it means looking at the raw object.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// rawObjectEntries returns the keys of a JSON object IN SOURCE ORDER, including repeats. An
// absent field yields no entries and no error: "channels" is optional in the file.
//
// The scan is hand-written rather than a streaming decoder because encoding/json's token stream
// resolves duplicate object keys before the caller can see them, and Decoder.Token is the only
// public API that exposes the raw sequence.
func rawObjectEntries(raw json.RawMessage) ([]string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	// Opening token must be '{'.
	token, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("http prefix table: %w", err)
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return nil, errors.New("expected an object")
	}
	var keys []string
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("object key is not a string")
		}
		keys = append(keys, key)
		// Skip the value: Decode into RawMessage consumes exactly one value, however nested.
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
	}
	// Closing token. Anything after the OBJECT is not this function's business: the caller's own
	// json.Unmarshal of the enclosing document already rejects a document with trailing content,
	// and duplicating that check here would only mean maintaining two opinions about what a valid
	// document is.
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	return keys, nil
}
