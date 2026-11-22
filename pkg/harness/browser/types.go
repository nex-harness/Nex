package browser

import (
	"fmt"
	"time"
)

const (
	DefaultViewportWidth  = 1280
	DefaultViewportHeight = 720
	MinViewportWidth      = 320
	MaxViewportWidth      = 3840
	MinViewportHeight     = 320
	MaxViewportHeight     = 2160
	DefaultActionTimeout  = 5 * time.Second
	DefaultIdleTimeout    = 10 * time.Minute
	MaxConsoleEntries     = 100
	MaxNetworkEntries     = 100
)

type Mode string

const (
	ModeManagedCDP Mode = "managed_cdp"
	ModeAttachCDP  Mode = "attach_cdp"
)

type ProfileMode string

const (
	ProfilePersistent ProfileMode = "persistent"
	ProfileEphemeral  ProfileMode = "ephemeral"
)

type ErrorCode string

const (
	ErrBackendUnavailable    ErrorCode = "backend_unavailable"
	ErrCapabilityUnsupported ErrorCode = "capability_unsupported"
	ErrRefNotFound           ErrorCode = "ref_not_found"
	ErrNavigationBlocked     ErrorCode = "navigation_blocked"
	ErrTimeout               ErrorCode = "timeout"
	ErrPermissionDenied      ErrorCode = "permission_denied"
	ErrHandoffCancelled      ErrorCode = "handoff_cancelled"
	ErrExecutionError        ErrorCode = "execution_error"
)

// BrowserError carries structured error semantics so the LLM knows whether
// a side effect may have occurred and which recovery action to take.
type BrowserError struct {
	Code       ErrorCode `json:"code"`
	Message    string    `json:"message"`
	SideEffect string    `json:"side_effect,omitempty"` // "none" | "uncertain"
}

func (e *BrowserError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

func newError(code ErrorCode, sideEffect, msg string) *BrowserError {
	if sideEffect == "" {
		sideEffect = "none"
	}
	return &BrowserError{Code: code, Message: msg, SideEffect: sideEffect}
}

// Config configures the Runner-side browser runtime.
type Config struct {
	Enabled        bool        `json:"enabled"`
	Mode           Mode        `json:"mode"`
	Headless       bool        `json:"headless"`
	ProfileMode    ProfileMode `json:"profile_mode"`
	AttachURL      string      `json:"attach_url,omitempty"`
	ExecutablePath string      `json:"executable_path,omitempty"`
	DownloadMirror string      `json:"download_mirror,omitempty"`
	AllowedDomains []string    `json:"allowed_domains,omitempty"`
	BlockedDomains []string    `json:"blocked_domains,omitempty"`
	IdleTimeout    time.Duration `json:"-"`
}

func DefaultConfig() Config {
	return Config{
		Enabled:     true,
		Mode:        ModeManagedCDP,
		Headless:    true,
		ProfileMode: ProfilePersistent,
		IdleTimeout: DefaultIdleTimeout,
	}
}

type Viewport struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type Rect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type TabInfo struct {
	TabID        string   `json:"tab_id"`
	URL          string   `json:"url"`
	Title        string   `json:"title"`
	Viewport     Viewport `json:"viewport"`
	Active       bool     `json:"active"`
	CanGoBack    bool     `json:"can_go_back"`
	CanGoForward bool     `json:"can_go_forward"`
	HumanControl bool     `json:"human_control,omitempty"`
}

type PageState struct {
	TabID        string   `json:"tab_id"`
	URL          string   `json:"url"`
	Title        string   `json:"title"`
	CanGoBack    bool     `json:"can_go_back"`
	CanGoForward bool     `json:"can_go_forward"`
	ScrollX      int      `json:"scroll_x"`
	ScrollY      int      `json:"scroll_y"`
	Viewport     Viewport `json:"viewport"`
}

// SnapshotDOMNode represents a visible semantic node for reading the page.
type SnapshotDOMNode struct {
	Tag        string            `json:"tag"`
	Depth      int               `json:"depth"`
	InViewport bool              `json:"in_viewport"`
	Ref        string            `json:"ref,omitempty"`
	Role       string            `json:"role,omitempty"`
	Name       string            `json:"name,omitempty"`
	Text       string            `json:"text,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// SnapshotElement represents an actionable element keyed by stable ref e1..eN.
type SnapshotElement struct {
	Ref        string            `json:"ref"`
	Tag        string            `json:"tag"`
	Role       string            `json:"role,omitempty"`
	Name       string            `json:"name,omitempty"`
	Text       string            `json:"text,omitempty"`
	Value      string            `json:"value,omitempty"`
	Disabled   bool              `json:"disabled,omitempty"`
	Checked    *bool             `json:"checked,omitempty"`
	Selector   string            `json:"selector"`
	Rect       Rect              `json:"rect"`
	InViewport bool              `json:"in_viewport"`
	ParentRef  string            `json:"parent_ref,omitempty"`
	FramePath  string            `json:"frame_path,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// Snapshot puts semantic DOM nodes before actionable elements so large truncated
// outputs still preserve page meaning before losing coordinate/selector details.
type Snapshot struct {
	URL                string            `json:"url"`
	Title              string            `json:"title"`
	Viewport           Viewport          `json:"viewport"`
	ConsoleErrorsCount int               `json:"console_errors_count"`
	DOM                []SnapshotDOMNode `json:"dom"`
	DOMTruncated       bool              `json:"dom_truncated"`
	Elements           []SnapshotElement `json:"elements"`
	Truncated          bool              `json:"truncated"`
}

type ConsoleEntry struct {
	Level     string `json:"level"`
	Text      string `json:"text"`
	URL       string `json:"url,omitempty"`
	Timestamp int64  `json:"ts"`
}

type NetworkEntry struct {
	URL        string `json:"url"`
	Method     string `json:"method"`
	Status     int    `json:"status,omitempty"`
	ErrorText  string `json:"error_text,omitempty"`
	Type       string `json:"resource_type,omitempty"`
	Timestamp  int64  `json:"ts"`
}

type DialogInfo struct {
	Type          string `json:"type"`
	Message       string `json:"message"`
	DefaultPrompt string `json:"default_prompt,omitempty"`
}

type HandoffState struct {
	SessionID string `json:"session_id"`
	TabID     string `json:"tab_id"`
	State     string `json:"state"` // "idle" | "awaiting_human" | "resumed" | "cancelled"
	Reason    string `json:"reason,omitempty"`
	Message   string `json:"message,omitempty"`
}

type SessionStatePayload struct {
	SessionID    string        `json:"session_id"`
	RunnerID     string        `json:"runner_id,omitempty"`
	Tabs         []TabInfo     `json:"tabs"`
	ActiveTabID  string        `json:"active_tab_id,omitempty"`
	HumanControl bool          `json:"human_control,omitempty"`
	Handoff      *HandoffState `json:"handoff,omitempty"`
}

type ProbeResult struct {
	Status           string   `json:"status"` // "ready" | "missing_binary" | "missing_deps" | "unsupported_arch" | "error"
	BrowserName      string   `json:"browser_name,omitempty"`
	Version          string   `json:"version,omitempty"`
	ExecutablePath   string   `json:"executable_path,omitempty"`
	Mode             Mode     `json:"mode"`
	OS               string   `json:"os"`
	Arch             string   `json:"arch"`
	MissingDeps      []string `json:"missing_deps,omitempty"`
	SuggestedCommand string   `json:"suggested_command,omitempty"`
	Error            string   `json:"error,omitempty"`
}

type InstallProgress struct {
	Phase   string  `json:"phase"` // "resolving" | "downloading" | "extracting" | "verifying" | "done" | "error"
	Percent float64 `json:"percent"`
	Message string  `json:"message,omitempty"`
}

type InputEvent struct {
	SessionID string `json:"session_id"`
	TabID     string `json:"tab_id"`
	Kind      string `json:"kind"` // "mouse" | "wheel" | "key"
	// Mouse / Wheel fields in normalized CSS viewport coordinates
	Type       string  `json:"type,omitempty"` // mousePressed|mouseReleased|mouseMoved|keyDown|keyUp|char
	X          float64 `json:"x,omitempty"`
	Y          float64 `json:"y,omitempty"`
	Button     string  `json:"button,omitempty"` // left|right|middle|none
	ClickCount int     `json:"click_count,omitempty"`
	DeltaX     float64 `json:"delta_x,omitempty"`
	DeltaY     float64 `json:"delta_y,omitempty"`
	// Keyboard fields
	Key       string `json:"key,omitempty"`
	Code      string `json:"code,omitempty"`
	Text      string `json:"text,omitempty"`
	Modifiers int    `json:"modifiers,omitempty"`
}

type ControlRequest struct {
	SessionID string `json:"session_id"`
	TabID     string `json:"tab_id,omitempty"`
	Action    string `json:"action"` // navigate|back|forward|reload|switch_tab|new_tab|close_tab|set_viewport|stream_start|stream_stop|takeover|handback
	URL       string `json:"url,omitempty"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
	Quality   int    `json:"quality,omitempty"`
	MaxWidth  int    `json:"max_width,omitempty"`
	MaxHeight int    `json:"max_height,omitempty"`
}
