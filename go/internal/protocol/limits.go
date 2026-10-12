package protocol

// V4WireProtocolVersion is V4_WIRE_PROTOCOL_VERSION (shared/nex-protocol-v4/core.ts).
const V4WireProtocolVersion = 3

// Limits copied from PROTOCOL_V4_LIMITS in shared/nex-protocol-v4/core.ts.
// wire_test.go compares them with the values the TypeScript module exports, so a
// change on either side fails a test instead of drifting silently.
const (
	MaxFrameBytes                    = 1024 * 1024
	LogicalFrameAssemblyMaxBytes     = 16 * 1024 * 1024
	LogicalFrameAssemblyMaxFragments = 1024
	TransportEnvelopeIDMaxChars      = 256
)

// MaxSafeInteger is JS Number.MAX_SAFE_INTEGER, used by the worst-case size model.
const MaxSafeInteger = 1<<53 - 1
