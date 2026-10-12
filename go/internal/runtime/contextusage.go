package runtime

import (
	"encoding/json"
	"time"

	"github.com/tylinux/nex/go/internal/emitters"
	"github.com/tylinux/nex/go/internal/modelreq"
	"github.com/tylinux/nex/go/internal/promptctx"
)

// contextUsageMessage maps one runtime message to the breakdown input. The
// toolCalls shape is the runtime ModelToolCall ({id,name,input}), not the
// provider's wire shape.
func contextUsageMessage(m modelreq.Message) emitters.ContextUsageMessage {
	out := emitters.ContextUsageMessage{Role: m.Role, ToolCallID: m.ToolCallID, ToolName: m.ToolName}
	if len(m.ToolCalls) > 0 {
		out.ToolCalls = runtimeToolCalls(m.ToolCalls)
	}
	out.Content = jsStringify(m.Content)
	return out
}

// runtimeToolCalls converts the provider tool_calls array to the runtime shape
// (ModelToolCall: {id, name, input}), which is what the context-usage snapshot
// stringifies for a message.
func runtimeToolCalls(providerCalls json.RawMessage) json.RawMessage {
	var calls []struct {
		ID       string `json:"id"`
		Function struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"function"`
	}
	if err := json.Unmarshal(providerCalls, &calls); err != nil {
		return providerCalls
	}
	out := make([]map[string]any, 0, len(calls))
	for _, c := range calls {
		// Arguments is a JSON-encoded string; unwrap it to the object.
		var argStr string
		var input any
		if err := json.Unmarshal(c.Function.Arguments, &argStr); err == nil {
			_ = json.Unmarshal([]byte(argStr), &input)
		} else {
			_ = json.Unmarshal(c.Function.Arguments, &input)
		}
		entry := map[string]any{"id": c.ID, "name": c.Function.Name}
		if input != nil {
			entry["input"] = input
		}
		out = append(out, entry)
	}
	return jsStringify(out)
}

// contract mirrors one entry of the embedded contracts.json (the ModelToolContract
// fields buildContextUsageSnapshot stringifies).
type contract struct {
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

func (c contract) toEmitter() emitters.ToolContract {
	return emitters.ToolContract{
		Name: c.Name, Description: c.Description, Capability: c.Capability,
		InputSchema: c.InputSchema, OutputSchema: c.OutputSchema,
		ReadOnly: c.ReadOnly, Destructive: c.Destructive, SideEffectScope: c.SideEffectScope,
		Permission: c.Permission, ResultBudget: c.ResultBudget,
	}
}

// contractsOnce caches the decoded tool contracts (static data).
func loadContracts() []contract {
	var cs []contract
	_ = json.Unmarshal(contractsJSON, &cs)
	return cs
}

// contextBreakdown computes the contextUsageBreakdown the usage patch carries:
// the prompt sections, the 20 default tool contracts and the request messages.
func contextBreakdown(s *session, history []modelreq.Message) []emitters.BreakdownItem {
	built := promptctx.Build(s.promptConfig())
	sections := make([]emitters.ContextUsageSection, 0, len(built.Sections))
	for _, sec := range built.Sections {
		sections = append(sections, emitters.ContextUsageSection{
			Name: sec.Name, Source: sec.Source, InjectionTarget: string(sec.Target),
			CacheHint: string(sec.Hint), Chars: int64(sec.Chars()), Tokens: int64(sec.Tokens()),
		})
	}
	cs := loadContracts()
	tools := make([]emitters.ToolContract, 0, len(cs))
	for _, c := range cs {
		tools = append(tools, c.toEmitter())
	}
	var messages []emitters.ContextUsageMessage
	for _, a := range built.MetaUser {
		content, _ := json.Marshal("<system-reminder>\n" + a.Content + "\n</system-reminder>\n")
		messages = append(messages, emitters.ContextUsageMessage{Role: "user", Content: content, MetaUserContext: true})
	}
	for _, hm := range history {
		cm := contextUsageMessage(hm)
		messages = append(messages, cm)
	}
	return emitters.ContextUsage(sections, tools, messages)
}

// promptConfig is the prompt context config for the session's turns.
func (s *session) promptConfig() promptctx.Config {
	return promptctx.Config{
		Env: promptctx.EnvInfo{
			Cwd: s.ws.Path, Platform: s.rt.deps.Platform, Shell: s.rt.deps.Shell,
			OSVersion: s.rt.deps.OSVersion, IsGitRepository: boolPtr(false), GitStatus: "not_repo",
		},
		Model: &promptctx.Model{
			ProviderID: s.rt.deps.Provider.ProviderID, ModelID: s.rt.deps.Provider.ModelID,
		},
		CurrentDate: s.rt.currentDate(),
	}
}

// currentDate returns the local ISO date (formatLocalIsoDate).
func (r *Runtime) currentDate() string {
	return time.UnixMilli(r.deps.Clock.NowMs()).Format("2006-01-02")
}

var _ = time.Now
