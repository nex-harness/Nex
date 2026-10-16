package agentscope

import (
	"context"
	"encoding/json"
)

// methods.go are the forward-only members of the facade: everything that adds a
// readiness gate and a trusted identity but owns no subscription of its own.
//
// Three of them carry a rule that is a bug fix, not plumbing:
//
//   - SendCommand rejects a terminal command whose envelope clientId is not the one
//     the handshake bound. The older facade did not cover the command entry point at
//     all, so an unhandshaked call AND a forged clientId both reached the CLI. It is
//     not silently rewritten either: rewriting would break the command's idempotency
//     ownership, so the mismatch is refused explicitly.
//   - QueryCommands pins the workspace a command query may bind to. A pure clock
//     probe is exempt, because it queries no command and must not claim (or change)
//     the business query's workspace binding.
//   - AttachmentBegin registers a flow route ONLY for a staging result, and a begin
//     ACK that arrives after the port closed is ABORTED under the same trusted
//     identity before it is refused. Registering the route anyway would register it
//     after close had already passed, leaving the CLI with a half-finished upload
//     that can only wait for its TTL.

// SendCommand forwards a turn's command envelope under this connection's trusted
// identity.
func (s *Scope) SendCommand(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	if err := s.handshake.assertOpen(s.isDisposed()); err != nil {
		return nil, err
	}
	incoming, err := decodeParams(params)
	if err != nil {
		return nil, err
	}
	if s.cfg.Role == RoleTerminalClient {
		if err := s.handshake.assertReady(s.isDisposed()); err != nil {
			return nil, err
		}
		if incoming.envelope().ClientID != s.handshake.boundClient() {
			// 旧 facade 未覆盖 command 入口，未握手调用与伪造 clientId 都会直达
			// CLI；静默覆盖又会破坏 command 幂等归属，因此明确拒绝不一致。
			return nil, &Fault{Code: FaultClientMismatch}
		}
	}
	// command 过去只校验 envelope.clientId，却没有像订阅、附件一样注入 host 真值，
	// 导致 mobile 可伪造顶层 clientMode，且 relay 下游身份在 command 链路丢失。
	// envelope 仍原样透传；可信连接上下文只通过 host 内部 carrier 传给 base service。
	return s.base.SendCommand(ctx, s.call(incoming), incoming.envelope())
}

// QueryCommands answers a command-key query. The first non-probe query pins the
// workspace; a later query naming a different one is refused, because the CLI's
// command state is per-workspace and silently rebinding would hand one workspace's
// command set to another.
func (s *Scope) QueryCommands(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	if err := s.handshake.assertReady(s.isDisposed()); err != nil {
		return nil, err
	}
	incoming, err := decodeParams(params)
	if err != nil {
		return nil, err
	}
	forwarded := s.call(incoming)
	// 纯时钟探测不查询任何 command，不能抢占/改变业务查询的 workspace 绑定。
	if incoming.boolField("clock") {
		return s.base.QueryCommands(ctx, forwarded, incoming.rawField("commands"))
	}
	requested := incoming.target().WorkspaceKey()
	s.queryMu.Lock()
	if s.commandQueryWorkspaceKey != "" && s.commandQueryWorkspaceKey != requested {
		s.queryMu.Unlock()
		return nil, &Fault{Code: FaultForeignWorkspace}
	}
	s.commandQueryWorkspaceKey = requested
	s.queryMu.Unlock()
	return s.base.QueryCommands(ctx, forwarded, incoming.rawField("commands"))
}

// AttachmentBegin opens an upload. A staging result registers the flow route; a late
// staging ACK is aborted under the same identity rather than registered after close.
func (s *Scope) AttachmentBegin(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	if err := s.handshake.assertReady(s.isDisposed()); err != nil {
		return nil, err
	}
	incoming, err := decodeParams(params)
	if err != nil {
		return nil, err
	}
	forwarded := s.call(incoming)
	result, err := s.base.AttachmentBegin(ctx, forwarded, marshalFields(incoming.StripForgeable()))
	if err != nil {
		return nil, err
	}
	if result.State != AttachmentStagingState {
		return json.Marshal(result)
	}
	if s.isDisposed() || s.flow.isClosed() {
		// begin ACK 可能晚于 port close。若此时再登记 route，closed 已经错过它，
		// CLI 会留下只能等 TTL 的半上传；先用同 trusted identity abort，再拒绝迟到结果。
		s.abortQuietly(ctx, forwarded)
		return nil, &Fault{Code: FaultClosed}
	}
	route := flowRoute{
		key:     flowRouteKey{workspace: incoming.target().WorkspaceKey(), connectionID: forwarded.Trusted.ConnectionID},
		target:  incoming.target(),
		trusted: *forwarded.Trusted,
	}
	s.flow.registerAttachment(route)
	if s.flow.currentState() == FlowSaturated {
		if err := s.flow.forward(route, FlowSaturated); err != nil {
			s.flow.unregisterAttachment(route)
			s.abortQuietly(ctx, forwarded)
			return nil, err
		}
	}
	return json.Marshal(result)
}

// AttachmentAbort abandons an upload. It is the reverse teardown of a staging begin
// and uses the same trusted identity, which is what lets the CLI match it.
func (s *Scope) AttachmentAbort(ctx context.Context, params json.RawMessage) error {
	if err := s.handshake.assertReady(s.isDisposed()); err != nil {
		return err
	}
	incoming, err := decodeParams(params)
	if err != nil {
		return err
	}
	forwarded := s.call(incoming)
	return s.base.AttachmentAbort(ctx, forwarded, marshalFields(incoming.StripForgeable()))
}

// abortQuietly is the abort the late-ACK path uses: the subscribe/begin already
// failed, and the abort's own failure must not replace the honest first error.
func (s *Scope) abortQuietly(ctx context.Context, forwarded Forwarded) {
	_ = s.base.AttachmentAbort(ctx, forwarded, marshalFields(forwarded.Fields))
}

// AttachmentStagingState is the only AttachmentBegin state that creates a route.
const AttachmentStagingState = "staging"

// PassThroughMethod is one forward-only INexAgentService method. The facade adds
// exactly two things to each: the readiness gate and the trusted identity. Every
// one of them is a read or a self-contained operation, so none needs its own
// ownership bookkeeping — which is why they share ONE code path instead of the
// forty the TS writes out by hand.
const (
	MethodBackgroundBashOutput       = "backgroundBashOutputV4"
	MethodConversationRowsRange      = "conversationRowsRangeV4"
	MethodAttachmentRead             = "attachmentReadV4"
	MethodConversationAttachmentRead = "conversationAttachmentReadV4"
	MethodConversationAttachmentStat = "conversationAttachmentStatV4"
	MethodAttachmentPreviewSource    = "attachmentPreviewSourceV4"
	MethodAttachmentChunk            = "attachmentChunkV4"
	MethodAttachmentCommit           = "attachmentCommitV4"
)

// PassThroughMethods is the closed set Call accepts. A name outside it is a
// programming error at the call site and is refused rather than forwarded.
var PassThroughMethods = map[string]bool{
	MethodBackgroundBashOutput:       true,
	MethodConversationRowsRange:      true,
	MethodAttachmentRead:             true,
	MethodConversationAttachmentRead: true,
	MethodConversationAttachmentStat: true,
	MethodAttachmentPreviewSource:    true,
	MethodAttachmentChunk:            true,
	MethodAttachmentCommit:           true,
}

// PassThrough forwards one of the read-only / self-contained methods under this
// connection's trusted identity.
func (s *Scope) PassThrough(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	if err := s.handshake.assertReady(s.isDisposed()); err != nil {
		return nil, err
	}
	if !PassThroughMethods[method] {
		return nil, &Fault{Code: "fault.agent.unknownMethod"}
	}
	incoming, err := decodeParams(params)
	if err != nil {
		return nil, err
	}
	return s.base.PassThrough(ctx, method, s.call(incoming), marshalFields(incoming.StripForgeable()))
}

// SetConnectionFlowState is the TRUSTED relay's control-plane edge. Three rules from
// the TS are load-bearing here:
//
//   - a terminal client may not call it at all (fault.connection.flowControlForbidden):
//     the sideband belongs to the host transport, not to a UI;
//   - it must carry a trusted downstream connection (fault.connection.flowControlUntrusted):
//     an edge with no downstream identity has nothing to attribute to;
//   - the downstream must actually own a route here
//     (fault.subscription.notOwned), otherwise any relay could pause another
//     connection's CLI stream.
func (s *Scope) SetConnectionFlowState(ctx context.Context, params json.RawMessage) error {
	if err := s.handshake.assertOpen(s.isDisposed()); err != nil {
		return err
	}
	if s.cfg.Role != RoleTrustedHostRelay {
		return &Fault{Code: FaultFlowControlForbidden}
	}
	incoming, err := decodeParams(params)
	if err != nil {
		return err
	}
	if ReadTrustedConnection(incoming.carrierValue()) == nil {
		return &Fault{Code: FaultFlowControlUntrusted}
	}
	forwarded := s.call(incoming)
	key := flowRouteKey{workspace: incoming.target().WorkspaceKey(), connectionID: forwarded.Trusted.ConnectionID}
	owns := s.owned.ownsRoute(key.workspace, key.connectionID)
	if !owns {
		s.flow.mu.Lock()
		_, owns = s.flow.attachmentRoutes[key]
		s.flow.mu.Unlock()
	}
	if !owns {
		return &Fault{Code: FaultNotOwned}
	}
	return s.base.SetConnectionFlowState(ctx, forwarded, FlowState(paramsState(incoming)))
}

// paramsState reads the flow state member. An absent or unknown value reads as
// drained, which is the neutral edge: it never pauses anything.
func paramsState(params incomingParams) string {
	if state := params.stringField("state"); state != "" {
		return state
	}
	return string(FlowDrained)
}

// RegisterInteractionHandler installs this scope's answers for the agent's
// interaction/* requests on the protocol client that owns its agent leg. Each of
// them is an agent→host REQUEST needing a host answer (a permission prompt, user
// input, browser control, provider runtime headers, official MCP auth headers), and
// each belongs to the window/connection that can actually show it.
//
// The handler is released on dispose: a window that is gone must stop being a
// candidate for the next prompt, and leaving the closure installed would keep the
// whole scope (and the workspace state it captured) reachable.
func (s *Scope) RegisterInteractionHandler() {
	if s.cfg.Interaction == nil {
		return
	}
	for _, method := range interactionMethods {
		s.cfg.Interaction.RegisterInteractionHandler(method, s.interactionHandler(method))
	}
}

// ReleaseInteractionHandlers restores the method-not-found default. It runs on
// dispose, before the owned subscriptions are torn down, so an agent request that
// races the close gets the honest "this host cannot ask" answer rather than one
// served by a half-disposed scope.
func (s *Scope) ReleaseInteractionHandlers() {
	if s.interactionDirectory != nil && s.interactionResolver != nil {
		s.interactionDirectory.Unregister(s.cfg.ConnectionID)
		s.interactionResolver = nil
	}
	if s.cfg.Interaction == nil {
		return
	}
	for _, method := range interactionMethods {
		s.cfg.Interaction.RegisterInteractionHandler(method, nil)
	}
}

// LiveInteractions reports how many connections currently have a resolver in the
// shared directory, so a leak (a scope that never released its entry) is visible
// rather than silent.
func (s *Scope) LiveInteractions() int {
	if s.interactionDirectory == nil {
		return 0
	}
	return s.interactionDirectory.Live()
}
