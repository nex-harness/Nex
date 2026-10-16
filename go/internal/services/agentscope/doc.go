// Package agentscope is the Go port of nexAgentConnectionScope.ts (step 3.10.3):
// the V4 connection-scoped service facade.
//
// One Scope is built per accepted WebSocket / MessagePort attachment. It owns the
// subscriptions THAT connection created, and nothing else: the shared base agent
// service keeps owning the CLI fact, and neither host/main nor the relay copies
// business state into it. The rules the port preserves, each with the TS comment
// that documents it:
//
//   - The connection identity is a TRUSTED carrier field. A renderer or a phone
//     must not be able to choose its own connectionId / clientMode /
//     deliveryProfile / subscriberScope / workflowRunDeltas, so WithTrustedConnection
//     deletes every forgeable key before writing the host truth.
//   - Role gates exist per method, not per connection: a terminal-client must
//     complete hello → clientHello, a trusted-host-relay is already authenticated by
//     the outer transport and only forwards a downstream connection.
//   - workflowRunDeltas is a CONNECTION fact written by the handshake, never a
//     per-subscription preference.
//   - Frame routing is by (connectionId, topic). The publisher fans out per
//     workspace; the facade filters by ownership so two connections never
//     cross-deliver.
//   - A CLI notification can arrive BEFORE the subscribe ACK returns. Those frames
//     are staged against a pending ownership and released after the ACK, in arrival
//     order; a stage overflow fails the subscribe loudly rather than dropping a
//     logical frame mid-sequence.
//   - Runtime restart invalidates local ownership by GENERATION. A late ACK from
//     the dead runtime must never re-register an owner, and must never unsubscribe
//     a same-named subscription of the NEW runtime.
//   - Flow control is per route and ordered: close is enqueued AFTER every already
//     accepted SAT/DRN, and the last subscription closes its route BEFORE it
//     unsubscribes, so an upstream that forgets the owner cannot reject the close
//     and strand a paused connection.
//
// The frame/notification wire vocabulary (topics, hello/clientHello, flow states,
// fault strings) lives in internal/agentproc/proto, which step 3.10.2 built; this
// package depends on it rather than re-declaring the wire.
package agentscope
