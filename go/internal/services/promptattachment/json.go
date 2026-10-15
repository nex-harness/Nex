// json.go: the tiny json seam (kept separate so the service file has no
// encoding/json import noise).
package promptattachment

import "encoding/json"

func jsonMarshal(value any) ([]byte, error)  { return json.Marshal(value) }
func jsonUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }
