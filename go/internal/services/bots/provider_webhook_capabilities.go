package bots

import "context"

// The webhook provider implements ONLY the capability the HTTP callback route
// needs — test, send, parseCallback. Everything else returns
// ErrUnsupportedCapability so a caller that probes an optional method gets a
// definite answer instead of a silent no-op.
// The optional-capability methods: the webhook provider implements none of them.
func (p *WebhookProvider) ResolveName(context.Context, Config) (string, error) {
	return "", ErrUnsupportedCapability
}

func (p *WebhookProvider) SyncCommands(context.Context, Config) error {
	return ErrUnsupportedCapability
}

func (p *WebhookProvider) SendTyping(context.Context, Config, TypingTarget) error {
	return ErrUnsupportedCapability
}

func (p *WebhookProvider) StopTyping(context.Context, Config, TypingTarget) error {
	return ErrUnsupportedCapability
}

func (p *WebhookProvider) AcknowledgeCallback(context.Context, Config, map[string]any, string, *OutboundMessage) (AcknowledgeResult, error) {
	return AcknowledgeResult{}, ErrUnsupportedCapability
}

func (p *WebhookProvider) PrepareCallbackPayload(_ context.Context, _ Config, payload map[string]any) (map[string]any, error) {
	return payload, nil
}

func (p *WebhookProvider) HandleCallbackResponse(context.Context, Config, map[string]any) (*CallbackResponse, error) {
	return nil, nil
}

func (p *WebhookProvider) DownloadAttachment(context.Context, Config, Attachment, *Actor) (*DownloadedAttachment, error) {
	return nil, nil
}

func (p *WebhookProvider) CreateStreamingReplyCard(context.Context, Config, StreamingCardState) (*CardHandle, error) {
	return nil, nil
}

func (p *WebhookProvider) UpdateStreamingReplyCard(context.Context, Config, CardHandle, StreamingCardState) error {
	return nil
}
