package runtime

import _ "embed"

// contractsJSON holds the 20 default tool contracts (ModelToolContract fields)
// that buildContextUsageSnapshot stringifies for the `system_tool_schemas`
// breakdown category. It is exported from the real Node tool registry by the
// emitter vectors (scripts/go-rewrite/gen-emitter-vectors.mts); the values are
// environment-independent and produce the recorded 81936 chars.
//
//go:embed contracts.json
var contractsJSON []byte
