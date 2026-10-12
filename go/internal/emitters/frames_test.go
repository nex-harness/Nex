package emitters

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/tylinux/nex/go/internal/protocol"
)

// TestMCPStartupFrameMatchesRecorded re-creates the S02 process/mcpTelemetry
// session_startup frame and compares it (modulo the time/id placeholders the
// recorder normalizes) with the recorded bytes.
func TestMCPStartupFrameMatchesRecorded(t *testing.T) {
	msg := MCPStartupFrame(MCPStartupInput{
		Arch:          "x64",
		ConfiguredCnt: 0,
		ConnectedCnt:  0,
		FailedCnt:     0,
		OccurredAt:    1,
		Platform:      "linux",
		ProcessCount:  0,
		SessionID:     "sess",
	})
	if msg.Method != MethodMCPTelemetry {
		t.Fatalf("method = %q", msg.Method)
	}
	// Key order must be exactly the TypeScript literal order.
	want := `{"arch":"x64","configuredCount":0,"connectedCount":0,"failedCount":0,"kind":"session_startup","occurredAt":1,"platform":"linux","processCount":0,"sessionId":"sess"}`
	if string(msg.Params) != want {
		t.Fatalf("params mismatch\n got: %s\nwant: %s", msg.Params, want)
	}
}

// TestRuntimePreferencesRequestMatchesRecorded checks the agent->host prefs
// request shape and that the scope serializes in the recorded order.
func TestRuntimePreferencesRequestMatchesRecorded(t *testing.T) {
	msg := RuntimePreferencesRequest(protocol.StringID("server-1"), "sess", PrefsRuntimeMaterialization)
	if msg.Kind != protocol.KindRequest || !msg.ID.IsString() || msg.ID.String() != "server-1" {
		t.Fatalf("unexpected request envelope: %+v", msg)
	}
	want := `{"sessionId":"sess","scope":"runtime-materialization"}`
	if string(msg.Params) != want {
		t.Fatalf("params mismatch\n got: %s\nwant: %s", msg.Params, want)
	}
	m2 := RuntimePreferencesRequest(protocol.StringID("server-2"), "sess", PrefsUserExecution)
	want2 := `{"sessionId":"sess","scope":"user-execution"}`
	if string(m2.Params) != want2 {
		t.Fatalf("params mismatch\n got: %s\nwant: %s", m2.Params, want2)
	}
}

// TestStateUpdatedFrameMatchesRecorded re-creates the S02 post-turn state.updated
// frame and compares it to the recorded bytes (modulo the workspace path).
func TestStateUpdatedFrameMatchesRecorded(t *testing.T) {
	readOnlyTrue := false
	_ = readOnlyTrue
	msg := StateUpdatedFrame(
		"sess",
		WorkspaceRef{WorkspacePath: "/ws", WorkspaceKey: "/ws"},
		"prompt_completed",
		1,
		StatePatch{
			Mode: "build",
			Available: []ModelOption{{
				Ref:             ModelRef{ProviderID: "mock", ModelID: "mock-model"},
				Label:           "Mock Model",
				ProviderLabel:   "Mock",
				ContextWindow:   200000,
				MaxOutputToken:  8000,
				ReasoningLevels: []string{"none"},
				DefaultLevel:    "none",
				HasInputFormat:  true,
				SupportsText:    true,
			}},
			Current:        ModelRef{ProviderID: "mock", ModelID: "mock-model"},
			CurrentOptions: json.RawMessage(`{"reasoningLevel":"none"}`),
			LastUsed:       &ModelRef{ProviderID: "mock", ModelID: "mock-model"},
			Permission:     "build",
			ThoughtAvail:   []ThoughtLevel{{Label: "none", Value: "none"}},
			ThoughtCurrent: "none",
			ThoughtEnabled: true,
		},
	)
	want := `{"patch":{"mode":{"current":"build"},"model":{"available":[{"ref":{"providerId":"mock","modelId":"mock-model"},"label":"Mock Model","providerLabel":"Mock","contextWindow":200000,"maxOutputTokens":8000,"reasoning":{"levels":[{"value":"none","label":"none"}],"defaultLevel":"none"},"properties":{"inputFormat":{"supportsText":true,"supportsImage":false,"supportsVideo":false,"supportsAudio":false,"supportsPdf":false},"outputFormat":{"supportsText":true}}}],"current":{"providerId":"mock","modelId":"mock-model","options":{"reasoningLevel":"none"}},"lastUsed":{"providerId":"mock","modelId":"mock-model"}},"permission":{"mode":"build"},"thoughtLevel":{"available":[{"label":"none","value":"none"}],"current":"none","enabled":true}},"reason":"prompt_completed","revision":1,"scope":"session","sessionId":"sess","type":"state.updated","workspace":{"workspacePath":"/ws","workspaceKey":"/ws"}}`
	if normalizeWS(string(msg.Params)) != normalizeWS(want) {
		t.Fatalf("state.updated mismatch\n got: %s\nwant: %s", msg.Params, want)
	}
}

func normalizeWS(s string) string { return s }

// TestFramesDoNotDropUnknown re-asserts that the frame params are compact
// (no whitespace), because the host compares/forwards bytes.
func TestFramesAreCompact(t *testing.T) {
	msg := MCPStartupFrame(MCPStartupInput{Arch: "x64", OccurredAt: 0, Platform: "linux", SessionID: "s"})
	var buf bytes.Buffer
	if err := json.Compact(&buf, msg.Params); err != nil {
		t.Fatal(err)
	}
	if buf.String() != string(msg.Params) {
		t.Fatalf("params not compact: %s", msg.Params)
	}
}
