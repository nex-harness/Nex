package tools_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	tools2 "github.com/tylinux/nex/go/internal/tools"
)

// validationVector mirrors one entry of tools-validation.json.
type validationVector struct {
	Tool                    string          `json:"tool"`
	Name                    string          `json:"name"`
	Input                   json.RawMessage `json:"input"`
	NormalizedInput         json.RawMessage `json:"normalizedInput"`
	RuntimeValidationIssues json.RawMessage `json:"runtimeValidationIssues"`
	Valid                   bool            `json:"valid"`
	ErrorMessage            string          `json:"errorMessage"`
	ModelContent            *string         `json:"modelContent"`
}

func decodeJSON(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return v
}

// toolInputSchemas reads the JSON input schema of Read/Write/Edit from the tool
// definitions the model sees (go/internal/tooldefs/tools.json).
func toolInputSchemas(t *testing.T) map[string]map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "tooldefs", "tools.json"))
	if err != nil {
		t.Fatalf("read tools.json: %v", err)
	}
	var defs []struct {
		Function struct {
			Name       string         `json:"name"`
			Parameters map[string]any `json:"parameters"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &defs); err != nil {
		t.Fatalf("parse tools.json: %v", err)
	}
	out := map[string]map[string]any{}
	for _, d := range defs {
		out[d.Function.Name] = d.Function.Parameters
	}
	return out
}

// TestValidationVectors replays the model-visible InputValidationError text the
// executor produces for invalid Read/Write/Edit input.
func TestValidationVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "vectors", "tools-validation.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vecs []validationVector
	if err := json.Unmarshal(raw, &vecs); err != nil {
		t.Fatal(err)
	}
	if len(vecs) < 20 {
		t.Fatalf("only %d validation vectors", len(vecs))
	}
	schemas := toolInputSchemas(t)

	for _, v := range vecs {
		v := v
		t.Run(v.Name, func(t *testing.T) {
			schema := schemas[v.Tool]
			if schema == nil {
				t.Fatalf("no schema for %s", v.Tool)
			}
			prepared := tools2.PrepareInitialInput(v.Tool, decodeJSON(t, v.Input))

			// The runtime normalization must match the vector exactly.
			wantNormalized := decodeJSON(t, v.NormalizedInput)
			if !reflect.DeepEqual(prepared.Input, wantNormalized) {
				got, _ := json.Marshal(prepared.Input)
				t.Fatalf("normalizedInput mismatch\n got: %s\nwant: %s", got, v.NormalizedInput)
			}
			wantRuntime := decodeJSON(t, v.RuntimeValidationIssues)
			if wantRuntime != nil || len(prepared.RuntimeValidationIssues) > 0 {
				got, _ := json.Marshal(prepared.RuntimeValidationIssues)
				if !jsonEqualRaw(got, v.RuntimeValidationIssues) {
					t.Fatalf("runtimeValidationIssues mismatch\n got: %s\nwant: %s", got, v.RuntimeValidationIssues)
				}
			}

			validation := tools2.ValidateJSONSchemaValue(prepared.Input, schema)
			if validation.Valid != v.Valid {
				t.Fatalf("valid mismatch: got %v want %v (errors %v)", validation.Valid, v.Valid, validation.Errors)
			}
			if !v.Valid {
				model := tools2.CreateInitialInputValidationModelContent(v.Tool, schema, validation.Issues, prepared.RuntimeValidationIssues)
				if v.ModelContent == nil || model != *v.ModelContent {
					want := ""
					if v.ModelContent != nil {
						want = *v.ModelContent
					}
					t.Fatalf("model content mismatch\n got: %q\nwant: %q", model, want)
				}
			}
		})
	}
}

func jsonEqualRaw(a, b []byte) bool {
	var av, bv any
	_ = json.Unmarshal(a, &av)
	if len(b) == 0 || string(b) == "null" {
		return av == nil
	}
	_ = json.Unmarshal(b, &bv)
	return reflect.DeepEqual(av, bv)
}
