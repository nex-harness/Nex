package proto

// wiretypes.go holds the assembler's vocabulary: the fault reason codes the host
// and UI read, the event/fault shapes a consumer routes on, the configuration
// limits, and the in-progress state. They are data rather than transition rules,
// so they live apart from the state machine that moves them (wireassembler.go) and
// from the lifecycle/bookkeeping surface (wirelifecycle.go).

import "github.com/tylinux/nex/go/internal/protocol"

// Fault reason codes, byte-identical to the strings the host and UI read. A
// rename here is a wire change, not a refactor.
const (
	ReasonAssemblyMetadataMismatch = "proto.frameAssemblyMetadataMismatch"
	ReasonAssemblyOrdinalConflict  = "proto.frameAssemblyOrdinalConflict"
	ReasonAssemblySuperseded       = "proto.frameAssemblySuperseded"
	ReasonAssemblyEnvelopeTooLarge = "proto.frameEnvelopeTooLarge"
	ReasonAssemblyTooLarge         = "proto.frameAssemblyTooLarge"
	ReasonFragmentCountExceeded    = "proto.frameFragmentCountExceeded"
	ReasonAssemblyInvalidBase64    = "proto.frameAssemblyInvalidBase64"
	ReasonAssemblyConcurrentLimit  = "proto.frameAssemblyConcurrentLimit"
	ReasonAssemblyBudgetExceeded   = "proto.frameAssemblyBudgetExceeded"
	ReasonAssemblyFragmentConflict = "proto.frameAssemblyFragmentConflict"
	ReasonAssemblyLengthMismatch   = "proto.frameAssemblyLengthMismatch"
	ReasonAssemblyChecksumMismatch = "proto.frameAssemblyChecksumMismatch"
	ReasonAssemblyInvalidUtf8      = "proto.frameAssemblyInvalidUtf8"
	ReasonAssemblyInvalidJson      = "proto.frameAssemblyInvalidJson"
	ReasonAssemblyInvalidPayload   = "proto.frameAssemblyInvalidPayload"
	ReasonAssemblyTimedOut         = "proto.frameAssemblyTimedOut"
)

// Fault is the typed failure a consumer routes on. A missing or garbled
// deliveryKind is reported as absent rather than guessed: the fault must not claim
// a purpose the frame never had.
type Fault struct {
	HasDeliveryKind     bool
	DeliveryKind        protocol.DeliveryKind
	ReasonCode          string
	LogicalFrameID      string
	LogicalFrameOrdinal int64
	Topic               string
	SubscriptionID      string
}

// Event is what Accept returns: one logical frame, or one fault — never both. A
// frame that fails validation produces a fault and no frame.
type Event struct {
	IsFault      bool
	Frame        []byte // the logical frame JSON
	DeliveryKind protocol.DeliveryKind
	Fault        Fault
}

// FrameSchema validates the reassembled logical frame payload and returns its
// normalised bytes. The TS takes a zod schema per topic family; here the caller
// supplies the check, because each family validates a different shape and the
// assembler only needs to know whether it passed.
type FrameSchema func(frame []byte) ([]byte, bool)

// AssemblerOptions mirror TopicWireFrameAssemblerOptions. Zero means the protocol
// maximum, matching the TS `undefined`.
type AssemblerOptions struct {
	MaxAssemblyBytes        int
	MaxFragments            int
	MaxConcurrentAssemblies int
	MaxStagedDecodedBytes   int
	TimeoutMs               int64
	MaxPhysicalFrameBytes   int
}

// Defaults for the limits PROTOCOL_V4_LIMITS pins.
const (
	defaultMaxConcurrent = 32
	defaultMaxStaged     = 32 * 1024 * 1024
	defaultTimeoutMs     = 30_000
)

// fragmentAssembly is one in-progress logical frame.
type fragmentAssembly struct {
	deliveryKind        protocol.DeliveryKind
	logicalFrameID      string
	logicalFrameOrdinal int64
	topic               string
	subscriptionID      string
	fragmentCount       int
	logicalBytes        int
	checksumValue       string
	fragments           [][]byte
	receivedCount       int
	decodedBytes        int
	firstSeenAt         int64
}

// settled is a per-route ordinal tombstone. It is monotonic and never
// bounded-evicted: forgetting a settled ordinal would let a replayed old frame
// resurrect a frame the consumer already failed closed on.
type settled struct {
	logicalFrameID      string
	logicalFrameOrdinal int64
}

// Assembler reassembles physical frames into logical ones. It is NOT safe for
// concurrent use, exactly like the TS: its consumer is one connection's frame
// pump.
type Assembler struct {
	schema               FrameSchema
	assemblies           map[string]*fragmentAssembly
	settledByRoute       map[string]settled
	stagedDecodedBytes   int
	maxAssemblyBytes     int
	maxFragments         int
	maxConcurrent        int
	maxStagedDecodedByte int
	timeoutMs            int64
	maxPhysicalBytes     int
}
