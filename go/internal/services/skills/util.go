// util.go: shared decode helpers for the knowledge-extension service packages.
package skills

import "encoding/json"

func unmarshalJSONObject(data []byte, target *map[string]any) error {
	if err := json.Unmarshal(data, target); err != nil {
		return err
	}
	if *target == nil {
		*target = map[string]any{}
	}
	return nil
}
