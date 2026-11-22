package protocol

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/nex-harness/nex/pkg/ai"
	"github.com/nex-harness/nex/pkg/store"
)

const ProtocolVersion = 1

// Frame Types exchanged over /ws/runner and /ws/client.
const (
	// Handshake & Sync
	TypeRunnerHello   = "runner.hello"
	TypeRunnerWelcome = "runner.welcome"
	TypeConfigSync    = "config.sync"
	TypeOutboxAck     = "outbox.ack"
	TypeRunnerStatus  = "runner.status"

	// Client Subscription & Session Control
	TypeClientSubscribe = "client.subscribe"
	TypeClientNotify    = "client.notify"
	TypeSessionPrompt   = "session.prompt"
	TypeSessionSteer    = "session.steer"
	TypeSessionFollowUp = "session.follow_up"
	TypeSessionAbort    = "session.abort"
	TypeSessionEvent    = "session.event"
	TypeSessionForkSync = "session.fork_sync"
	TypeSessionRewind   = "session.rewind"
	TypeMemorySync      = "memory.sync"

	// Remote Approval & Remote Plugin UI (ctx.ui)
	TypeApprovalResolve = "approval.resolve"
	TypeUIResolve       = "ui.resolve"

	// On-demand CAS Trace Blob Pull & Remote Server-Hosted MCP Call
	TypeBlobFetch       = "blob.fetch"
	TypeBlobData        = "blob.data"
	TypeMCPRemoteCall   = "mcp.remote_call"
	TypeMCPRemoteResult = "mcp.remote_result"
	TypeSkillsDiscover  = "skills.discover"
	TypeMCPProbe        = "mcp.probe"

	// Remote Workspace (Files Tree, Preview, Git Diff) & Remote PTY Control
	TypeWorkspaceReq  = "workspace.req"
	TypeWorkspaceResp = "workspace.resp"
	TypePTYOpen       = "pty.open"
	TypePTYOpened     = "pty.opened"
	TypePTYResize     = "pty.resize"
	TypePTYClose      = "pty.close"
	TypePTYClosed     = "pty.closed"
	TypePTYList       = "pty.list"
	TypePTYListResp   = "pty.list_resp"

	// Remote Browser Use Control, Screencast & Provisioning
	TypeBrowserState     = "browser.state"
	TypeBrowserControl   = "browser.control"
	TypeBrowserInput     = "browser.input"
	TypeBrowserHandoff   = "browser.handoff"
	TypeBrowserProbe     = "browser.probe"
	TypeBrowserInstall   = "browser.install"
	TypeBrowserClearData = "browser.clear_data"

	TypeError = "error"
)

// Envelope is the self-describing JSON WebSocket message frame.
type Envelope struct {
	V         int             `json:"v"`
	ID        string          `json:"id"`
	ReplyTo   string          `json:"reply_to,omitempty"`
	Type      string          `json:"type"`
	TS        int64           `json:"ts"`
	SessionID string          `json:"session_id,omitempty"`
	RunnerID  string          `json:"runner_id,omitempty"`
	Seq       uint64          `json:"seq,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// NewEnvelope creates a versioned Envelope marshaling payload as JSON.
func NewEnvelope(frameType string, payload any) (Envelope, error) {
	var raw json.RawMessage
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return Envelope{}, err
		}
		raw = b
	}
	return Envelope{
		V:       ProtocolVersion,
		ID:      "msg_" + uuid.NewString(),
		Type:    frameType,
		TS:      time.Now().UnixMilli(),
		Payload: raw,
	}, nil
}

// RunnerHelloPayload is sent by nex-runner immediately after opening /ws/runner.
type RunnerHelloPayload struct {
	RunnerID         string   `json:"runner_id"`
	Name             string   `json:"name"`
	AuthToken        string   `json:"auth_token"`
	Hostname         string   `json:"hostname"`
	OS               string   `json:"os"`
	Arch             string   `json:"arch"`
	Shell            string   `json:"shell"`
	Version          string   `json:"version"`
	Tags             []string `json:"tags,omitempty"`
	WorkspaceDefault string   `json:"workspace_default"`
	SyncedConfigRev  int64    `json:"synced_config_rev"`
}

// MemoryConfigSync distributes memory automation preferences from Server to Runners.
type MemoryConfigSync struct {
	AutoExtract bool   `json:"auto_extract"`
	ProviderID  string `json:"provider_id,omitempty"`
	ModelID     string `json:"model_id,omitempty"`
}

// BrowserConfigSync distributes Browser Use preferences from Server to Runners.
type BrowserConfigSync struct {
	Enabled        bool   `json:"enabled"`
	Mode           string `json:"mode,omitempty"` // "managed_cdp" | "attach_cdp"
	Headless       bool   `json:"headless"`
	ProfileMode    string `json:"profile_mode,omitempty"` // "persistent" | "ephemeral"
	AttachURL      string `json:"attach_url,omitempty"`
	ExecutablePath string `json:"executable_path,omitempty"`
	DownloadMirror string `json:"download_mirror,omitempty"`
}

// MemorySyncPayload synchronizes a Runner-created/deleted memory back to Server (and live Web clients).
type MemorySyncPayload struct {
	Action   string              `json:"action"` // "upsert" | "delete" | "refresh"
	Record   *store.MemoryRecord `json:"record,omitempty"`
	MemoryID string              `json:"memory_id,omitempty"`
}

// ConfigSnapshot carries centralized configuration down to an autonomous Runner.
type ConfigSnapshot struct {
	ConfigRev            int64                                `json:"config_rev"`
	ServerBaseURL        string                               `json:"server_base_url,omitempty"`
	Providers            []store.ProviderRecord               `json:"providers,omitempty"`
	Models               []ai.ModelMeta                       `json:"models,omitempty"`
	AgentProfiles        []store.AgentProfile                 `json:"agent_profiles,omitempty"`
	Skills               []store.SkillRecord                  `json:"skills,omitempty"`
	SkillVisibilityRules map[string]store.SkillVisibilityRule `json:"skill_visibility_rules,omitempty"`
	Plugins              []store.PluginRecord                 `json:"plugins,omitempty"`
	MCPServers           []store.MCPServerRecord              `json:"mcp_servers,omitempty"`
	CronJobs             []store.CronJobRecord                `json:"cron_jobs,omitempty"`
	Memories             []store.MemoryRecord                 `json:"memories,omitempty"`
	MemoriesSynced       bool                                 `json:"memories_synced,omitempty"`
	MemorySettings       *MemoryConfigSync                    `json:"memory_settings,omitempty"`
	BrowserSettings      *BrowserConfigSync                   `json:"browser_settings,omitempty"`
}

// RunnerWelcomePayload acknowledges Runner registration and optionally pushes updated config.
type RunnerWelcomePayload struct {
	RunnerID string          `json:"runner_id"`
	Snapshot *ConfigSnapshot `json:"snapshot,omitempty"`
}

// ClientSubscribePayload subscribes a UI client to session events starting after LastSeenSeq.
type ClientSubscribePayload struct {
	SessionID   string `json:"session_id"`
	LastSeenSeq uint64 `json:"last_seen_seq"`
}

// PromptPayload starts or continues a conversation on a target Runner.
type PromptPayload struct {
	SessionID        string            `json:"session_id"`
	RunnerID         string            `json:"runner_id"`
	WorkspacePath    string            `json:"workspace_path,omitempty"`
	AgentProfileSlug string            `json:"agent_profile_slug,omitempty"`
	ProviderID       string            `json:"provider_id,omitempty"`
	ModelID          string            `json:"model_id,omitempty"`
	ThinkingEffort   ai.ThinkingEffort `json:"thinking_effort,omitempty"`
	PermissionMode   string            `json:"permission_mode,omitempty"`
	Text             string            `json:"text"`
	Images           []ai.ContentBlock `json:"images,omitempty"`
}

// SessionForkSyncPayload mirrors a server-forked session and its event snapshot into a Runner's local SQLite.
type SessionForkSyncPayload struct {
	Session store.SessionRecord  `json:"session"`
	Events  []store.SessionEvent `json:"events"`
}

// ApprovalResolvePayload resolves a pending interactive tool approval request.
type ApprovalResolvePayload struct {
	ApprovalID string `json:"approval_id"`
	Approved   bool   `json:"approved"`
	Reason     string `json:"reason,omitempty"`
}

// UIPromptRequestPayload represents a remote declarative ctx.ui prompt from a headless Runner plugin.
type UIPromptRequestPayload struct {
	RequestID    string   `json:"request_id"`
	Kind         string   `json:"kind"` // "select" | "confirm" | "input"
	Title        string   `json:"title"`
	Message      string   `json:"message,omitempty"`
	Placeholder  string   `json:"placeholder,omitempty"`
	Options      []string `json:"options,omitempty"`
	Password     bool     `json:"password,omitempty"`
	DefaultValue string   `json:"default_value,omitempty"`
}

// UIResolvePayload returns the user's response to a remote ctx.ui prompt.
type UIResolvePayload struct {
	RequestID string `json:"request_id"`
	Canceled  bool   `json:"canceled,omitempty"`
	Confirmed bool   `json:"confirmed,omitempty"`
	Value     string `json:"value,omitempty"`
}

// ClientNotificationPayload broadcasts user-facing alert triggers (task completion,
// questions, approvals, plan review, browser handoff) to all connected Web/Desktop/Mobile clients.
type ClientNotificationPayload struct {
	SessionID    string `json:"session_id"`
	RunnerID     string `json:"runner_id,omitempty"`
	Kind         string `json:"kind"` // "task_completed" | "question" | "approval" | "plan_approval" | "browser_handoff"
	SessionTitle string `json:"session_title,omitempty"`
	Title        string `json:"title,omitempty"`
	Body         string `json:"body,omitempty"`
	ToolName     string `json:"tool_name,omitempty"`
	RequestID    string `json:"request_id,omitempty"`
}

// BlobFetchPayload asks a Runner to upload a CAS Trace/Prompt blob on demand.
type BlobFetchPayload struct {
	Hash string `json:"hash"`
}

// BlobDataPayload carries uncompressed CAS blob bytes + MIME from Runner to Server.
type BlobDataPayload struct {
	Hash  string `json:"hash"`
	Mime  string `json:"mime"`
	Data  []byte `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
}

// MCPRemoteCallPayload routes a server-hosted MCP tool call from Runner's goja Code Mode to Server.
type MCPRemoteCallPayload struct {
	Namespace string          `json:"namespace"`
	ToolName  string          `json:"tool_name"`
	Arguments json.RawMessage `json:"arguments"`
}

// MCPRemoteResultPayload returns the result of a server-hosted MCP call back to the Runner.
type MCPRemoteResultPayload struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// SkillsDiscoverPayload asks a Runner to scan its user & workspace skill directories.
type SkillsDiscoverPayload struct {
	WorkspacePath string `json:"workspace_path,omitempty"`
}

// MCPProbePayload asks a Runner to test-connect a runner_local MCP server and return discovered tools.
type MCPProbePayload struct {
	Server store.MCPServerRecord `json:"server"`
}

// OutboxAckPayload confirms persistence of Runner outbox event IDs / sequences on Server.
type OutboxAckPayload struct {
	SessionID string `json:"session_id"`
	AckSeq    uint64 `json:"ack_seq"`
}

// SessionRewindPayload asks a Runner to list file checkpoints or rewind modified files prior to BeforeSeq.
type SessionRewindPayload struct {
	Action    string `json:"action"` // "list" | "rewind"
	SessionID string `json:"session_id"`
	BeforeSeq uint64 `json:"before_seq,omitempty"`
}

// --- Binary Fast-Path Frame for libghostty Remote PTY ---
// Wire format: [1 byte OpCode][16 bytes ASCII/UUID Prefix or padded PTY ID (36 bytes fixed)][Raw Terminal Bytes]

const (
	PTYBinaryStdin          byte = 0x01
	PTYBinaryStdout         byte = 0x02
	BrowserBinaryScreencast byte = 0x10
	PTYIDHeaderLen               = 36
)

// EncodePTYBinaryFrame packs a zero-base64 binary WebSocket frame for low-latency terminal I/O.
func EncodePTYBinaryFrame(op byte, ptyID string, data []byte) []byte {
	buf := make([]byte, 1+PTYIDHeaderLen+len(data))
	buf[0] = op
	copy(buf[1:1+PTYIDHeaderLen], ptyID)
	copy(buf[1+PTYIDHeaderLen:], data)
	return buf
}

// EncodeBrowserBinaryFrame packs a zero-base64 binary Screencast JPEG frame using the shared fast-path framing.
func EncodeBrowserBinaryFrame(tabID string, frameData []byte) []byte {
	return EncodePTYBinaryFrame(BrowserBinaryScreencast, tabID, frameData)
}

// DecodePTYBinaryFrame unpacks op, ptyID, and raw terminal bytes from a binary WebSocket frame.
func DecodePTYBinaryFrame(frame []byte) (op byte, ptyID string, data []byte, err error) {
	if len(frame) < 1+PTYIDHeaderLen {
		return 0, "", nil, errors.New("protocol: binary PTY frame too short")
	}
	op = frame[0]
	idSlice := frame[1 : 1+PTYIDHeaderLen]
	end := len(idSlice)
	for end > 0 && idSlice[end-1] == 0 {
		end--
	}
	ptyID = string(idSlice[:end])
	data = frame[1+PTYIDHeaderLen:]
	return op, ptyID, data, nil
}
