package bots

// types_messages.go holds the message-shaped half of the @nex/shared contract
// (the inbound message, its actor and attachments, the outbound message, the
// selection prompt and the elicitation request). It is split from types.go only
// to keep both files under the 400-line rule; together they are one contract.
// SelectionOption is one row of SelectionPrompt.options.
type SelectionOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// SelectionPrompt is SelectionPrompt.
type SelectionPrompt struct {
	ID          string            `json:"id"`
	Token       string            `json:"token,omitempty"`
	Title       string            `json:"title"`
	CurrentID   string            `json:"currentId,omitempty"`
	CancelLabel string            `json:"cancelLabel,omitempty"`
	ShowCancel  *bool             `json:"showCancel,omitempty"`
	Action      string            `json:"action"`
	Options     []SelectionOption `json:"options"`
}

// OutboundElicitationRequest is BotOutboundElicitationRequest.
type OutboundElicitationRequest struct {
	RequestID       string              `json:"requestId"`
	TaskID          string              `json:"taskId"`
	RunID           string              `json:"runId"`
	CurrentQuestion int                 `json:"currentQuestionIndex"`
	Questions       []ElicitationOption `json:"questions"`
	Answers         map[string][]string `json:"answers,omitempty"`
	Status          string              `json:"status,omitempty"`
}

// ElicitationOption is one question of an elicitation request.
type ElicitationOption struct {
	Question    string   `json:"question"`
	Header      string   `json:"header"`
	Options     []string `json:"options,omitempty"`
	MultiSelect bool     `json:"multiSelect,omitempty"`
}

// OutboundMessage is BotOutboundMessage.
type OutboundMessage struct {
	BotID                string                      `json:"botId"`
	Provider             Provider                    `json:"provider"`
	ProviderUserID       string                      `json:"providerUserId"`
	Text                 string                      `json:"text"`
	Locale               string                      `json:"locale,omitempty"`
	Selection            *SelectionPrompt            `json:"selection,omitempty"`
	Elicitation          *OutboundElicitationRequest `json:"elicitation,omitempty"`
	ProviderContextToken string                      `json:"providerContextToken,omitempty"`
}

// RuntimeInfo is BotRuntimeInfo.
type RuntimeInfo struct {
	BotID         string   `json:"botId"`
	Provider      Provider `json:"provider"`
	Status        string   `json:"status"`
	MessageID     string   `json:"messageId,omitempty"`
	Message       string   `json:"message,omitempty"`
	LastUpdateAt  *float64 `json:"lastUpdateAt,omitempty"`
	Offset        *float64 `json:"offset,omitempty"`
	DeliveryError string   `json:"deliveryError,omitempty"`
}

// ServiceStatus is BotServiceStatus.
type ServiceStatus struct {
	BotsCount        int           `json:"botsCount"`
	EnabledBotsCount int           `json:"enabledBotsCount"`
	ContextsCount    int           `json:"contextsCount"`
	BotRuntime       []RuntimeInfo `json:"botRuntime"`
}

// CallbackResult is BotProviderCallbackResult — the HTTP status contract of
// POST /api/bots/:provider. Status nil means "200"; ResponseBody overrides the
// default `{ok, replies}` body.
type CallbackResult struct {
	OK           bool              `json:"ok"`
	Replies      []OutboundMessage `json:"replies"`
	ResponseBody any               `json:"responseBody,omitempty"`
	Status       int               `json:"status,omitempty"`
}

// TestResult is BotTestResult.
type TestResult struct {
	OK       bool     `json:"ok"`
	Message  string   `json:"message"`
	Name     string   `json:"name,omitempty"`
	Provider Provider `json:"provider,omitempty"`
}

// BindCodeResult is BotBindCodeResult.
type BindCodeResult struct {
	Code      string  `json:"code"`
	ExpiresAt float64 `json:"expiresAt"`
}

// Task broadcast channel names.
const (
	TaskBroadcastChannel       = "bots:task"
	TaskStreamBroadcastChannel = "bots:task-stream"
)

func boolPtr(value bool) *bool { return &value }

// BoolPtr exposes the optional-bool constructor for the config normalizers and
// their table tests.
func BoolPtr(value bool) *bool { return &value }
