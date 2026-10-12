package emitters

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// contextVectors mirrors go/testdata/vectors/emitters-context.json.
type contextVectors struct {
	GeneratedBy   string            `json:"generatedBy"`
	ToolContracts []json.RawMessage `json:"toolContracts"`
	Cases         []contextCase     `json:"cases"`
}

type contextCase struct {
	ScenarioID    string             `json:"scenarioId"`
	Breakdown     []map[string]any   `json:"breakdown"`
	SectionInputs []contextSectionIn `json:"sectionInputs"`
	MessageInputs []contextMessageIn `json:"messageInputs"`
}

type contextSectionIn struct {
	Name            string `json:"name"`
	Source          string `json:"source"`
	InjectionTarget string `json:"injectionTarget"`
	CacheHint       string `json:"cacheHint"`
	Chars           int64  `json:"chars"`
	Tokens          int64  `json:"tokens"`
}

type contextMessageIn struct {
	Role    string          `json:"role"`
	Content string          `json:"content"`
	Raw     json.RawMessage `json:"raw"`
}

func loadContextVectors(t *testing.T) contextVectors {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "vectors", "emitters-context.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var v contextVectors
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("parse vectors: %v", err)
	}
	if len(v.Cases) == 0 || len(v.ToolContracts) == 0 {
		t.Fatal("empty context vectors")
	}
	return v
}

// toolContractsFromVectors decodes the data vectors into ToolContract values.
func toolContractsFromVectors(t *testing.T, raws []json.RawMessage) []ToolContract {
	t.Helper()
	out := make([]ToolContract, 0, len(raws))
	for _, raw := range raws {
		var w struct {
			Name            string          `json:"name"`
			Description     string          `json:"description"`
			Capability      string          `json:"capability"`
			InputSchema     json.RawMessage `json:"inputSchema"`
			OutputSchema    json.RawMessage `json:"outputSchema"`
			ReadOnly        *bool           `json:"readOnly"`
			Destructive     *bool           `json:"destructive"`
			SideEffectScope string          `json:"sideEffectScope"`
			Permission      json.RawMessage `json:"permission"`
			ResultBudget    json.RawMessage `json:"resultBudget"`
		}
		if err := json.Unmarshal(raw, &w); err != nil {
			t.Fatalf("decode tool contract: %v", err)
		}
		out = append(out, ToolContract{
			Name:            w.Name,
			Description:     w.Description,
			Capability:      w.Capability,
			InputSchema:     w.InputSchema,
			OutputSchema:    w.OutputSchema,
			ReadOnly:        w.ReadOnly,
			Destructive:     w.Destructive,
			SideEffectScope: w.SideEffectScope,
			Permission:      w.Permission,
			ResultBudget:    w.ResultBudget,
		})
	}
	return out
}

// TestContextUsageMatchesTypeScript reproduces the recorded contextUsageBreakdown
// from Go inputs (promptctx sections + tool contracts + message list). It also
// pins the two trace-anchored numbers: system_tool_schemas 81936 and messages
// 37/42/57 for S02/S03/S04.
func TestContextUsageMatchesTypeScript(t *testing.T) {
	v := loadContextVectors(t)
	tools := toolContractsFromVectors(t, v.ToolContracts)

	// The trace records these message char counts for the first model step.
	wantMessages := map[string]int64{"S02": 37, "S03": 42, "S04": 57}

	for _, c := range v.Cases {
		c := c
		t.Run(c.ScenarioID, func(t *testing.T) {
			sections := make([]ContextUsageSection, 0, len(c.SectionInputs))
			for _, s := range c.SectionInputs {
				sections = append(sections, ContextUsageSection{
					Name:            s.Name,
					Source:          s.Source,
					InjectionTarget: s.InjectionTarget,
					CacheHint:       s.CacheHint,
					Chars:           s.Chars,
					Tokens:          s.Tokens,
				})
			}
			var messages []ContextUsageMessage
			for _, m := range c.MessageInputs {
				cm, err := contextMessageFromRaw(m.Raw)
				if err != nil {
					t.Fatalf("message: %v", err)
				}
				messages = append(messages, cm)
			}
			got := ContextUsage(sections, tools, messages)

			// Compare against the expected breakdown as ordered {source, chars}.
			var want []string
			for _, item := range c.Breakdown {
				src, _ := item["source"].(string)
				chars, _ := item["chars"].(float64)
				want = append(want, src)
				_ = chars
			}
			if len(got) != len(c.Breakdown) {
				t.Fatalf("breakdown length = %d, want %d (got %v)", len(got), len(c.Breakdown), got)
			}
			for i := range got {
				src, _ := c.Breakdown[i]["source"].(string)
				chars, _ := c.Breakdown[i]["chars"].(float64)
				if got[i].Source != src {
					t.Errorf("item %d source = %q, want %q", i, got[i].Source, src)
				}
				if got[i].Chars != int64(chars) {
					t.Errorf("item %d (%s) chars = %d, want %d", i, src, got[i].Chars, int64(chars))
				}
				if src == "system_tool_schemas" && got[i].Chars != 81936 {
					t.Errorf("system_tool_schemas chars = %d, want 81936", got[i].Chars)
				}
				if src == "messages" {
					if w, ok := wantMessages[c.ScenarioID]; ok && got[i].Chars != w {
						t.Errorf("messages chars = %d, want trace value %d", got[i].Chars, w)
					}
				}
			}
		})
	}
}

// contextMessageFromRaw builds a ContextUsageMessage from the vector's raw
// message object.
func contextMessageFromRaw(raw json.RawMessage) (ContextUsageMessage, error) {
	var m struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		ToolCalls  json.RawMessage `json:"toolCalls"`
		ToolCallID string          `json:"toolCallId"`
		ToolName   string          `json:"toolName"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return ContextUsageMessage{}, err
	}
	return ContextUsageMessage{
		Role:       m.Role,
		Content:    m.Content,
		ToolCalls:  m.ToolCalls,
		ToolCallID: m.ToolCallID,
		ToolName:   m.ToolName,
	}, nil
}
